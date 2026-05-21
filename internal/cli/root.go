package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/checkpoint"
	"github.com/anthonylu23/switchboard-cli/internal/config"
	"github.com/anthonylu23/switchboard-cli/internal/credentials"
	"github.com/anthonylu23/switchboard-cli/internal/data"
	"github.com/anthonylu23/switchboard-cli/internal/event"
	"github.com/anthonylu23/switchboard-cli/internal/home"
	"github.com/anthonylu23/switchboard-cli/internal/packaging"
	"github.com/anthonylu23/switchboard-cli/internal/provider"
	chinacloudprovider "github.com/anthonylu23/switchboard-cli/internal/provider/chinacloud"
	gcpprovider "github.com/anthonylu23/switchboard-cli/internal/provider/gcp"
	lambdaprovider "github.com/anthonylu23/switchboard-cli/internal/provider/lambda"
	localprovider "github.com/anthonylu23/switchboard-cli/internal/provider/local"
	mockprovider "github.com/anthonylu23/switchboard-cli/internal/provider/mock"
	"github.com/anthonylu23/switchboard-cli/internal/redact"
	"github.com/anthonylu23/switchboard-cli/internal/routing"
	"github.com/anthonylu23/switchboard-cli/internal/runtimeprep"
	"github.com/anthonylu23/switchboard-cli/internal/state"
	"github.com/anthonylu23/switchboard-cli/internal/summary"
	"github.com/spf13/cobra"
)

type Options struct {
	Stdout                io.Writer
	Stderr                io.Writer
	GCPProviderFactory    func(config.GCPConfig, io.Writer, io.Writer) app.ProviderAdapter
	LambdaProviderFactory func(config.LambdaConfig, io.Writer, io.Writer) app.ProviderAdapter
	ImageBuilderFactory   func(io.Writer, io.Writer) ImageBuilder
}

type ImageBuilder interface {
	BuildAndPush(context.Context, packaging.BuildRequest) (packaging.BuildResult, error)
}

const (
	exitCodeInternal      = 1
	exitCodeInvalidSpec   = 10
	exitCodeRouting       = 30
	exitCodeMissingResume = 40
	exitCodeCanceled      = 130
)

func NewRootCommand(opts Options) *cobra.Command {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	var home string
	root := &cobra.Command{
		Use:           "switchboard-cli",
		Short:         "Local-first ML job orchestration",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&home, "home", "", "Switchboard home directory")
	root.AddCommand(newTrainCommand(opts, &home))
	root.AddCommand(newStatusCommand(opts, &home))
	root.AddCommand(newLogsCommand(opts, &home))
	root.AddCommand(newCancelCommand(opts, &home))
	root.AddCommand(newProvidersCommand(opts))
	root.AddCommand(newCredentialsCommand(opts, &home))
	return root
}

func Execute() {
	cmd := NewRootCommand(Options{})
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var exitErr exitCodeError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.code)
		}
		os.Exit(1)
	}
}

func newTrainCommand(opts Options, home *string) *cobra.Command {
	var flags config.TrainFlags
	cmd := &cobra.Command{
		Use:   "train",
		Short: "Run a training script",
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.SwitchboardHome = *home
			resolved, err := config.LoadTrain(flags)
			if err != nil {
				return err
			}
			code, err := runTrain(cmd.Context(), opts, resolved)
			if err != nil {
				return err
			}
			if code != 0 {
				return exitCodeError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&flags.ConfigPath, "config", "", "Path to switchboard-cli YAML config")
	cmd.Flags().StringVar(&flags.Provider, "provider", "local", "Provider to use")
	cmd.Flags().StringVar(&flags.Script, "script", "", "Training script path")
	cmd.Flags().StringArrayVar(&flags.Args, "arg", nil, "Argument to pass to the script; repeat for multiple args")
	cmd.Flags().BoolVar(&flags.AllowLargeDataBundle, "allow-large-data-bundle", false, "Allow local data bundles above configured limit")
	return cmd
}

func runTrain(ctx context.Context, opts Options, resolved config.ResolvedTrainConfig) (int, error) {
	if err := artifact.EnsureHome(resolved.SwitchboardHome); err != nil {
		return exitCodeInternal, err
	}

	manifest, err := data.Prepare(resolved.Job, data.PreflightOptions{
		BundleSizeLimitBytes: resolved.BundleMaxSizeBytes,
		RequireOverride:      resolved.RequireOverrideAboveLimit,
		AllowLargeBundle:     resolved.AllowLargeDataBundle,
	})
	if err != nil {
		return exitCodeInvalidSpec, err
	}
	job := resolved.Job
	job.Data = append([]app.DataInput(nil), manifest.Inputs...)
	runRedactor := redact.FromEnvironment(job.Env)

	now := time.Now().UTC()
	runID := app.NewRunID()
	originalScript := job.Script
	if shouldPackageForProvider(resolved, job) {
		packaged, err := packageJob(ctx, opts, resolved, job, runID)
		if err != nil {
			return exitCodeInvalidSpec, err
		}
		job = packaged
	}
	paths := artifact.ForRun(resolved.SwitchboardHome, runID)
	if err := artifact.EnsureRun(paths); err != nil {
		return exitCodeInternal, err
	}
	store, err := state.Open(paths.DB)
	if err != nil {
		return exitCodeInternal, err
	}
	defer store.Close()

	run := app.Run{ID: runID, JobName: resolved.Job.Name, Script: originalScript, Image: job.Image, Provider: resolved.Provider, State: app.RunStateRunning, StartedAt: now}
	if err := store.CreateRun(ctx, run); err != nil {
		return exitCodeInternal, err
	}

	credentialResolver, err := credentialResolverForTrain(opts, resolved)
	if err != nil {
		return exitCodeInvalidSpec, err
	}
	registry := buildTrainProviderRegistry(opts, resolved, credentialResolver)
	maxAttempts := resolved.Routing.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	excluded := map[string]bool{}
	var resumeFrom *app.CheckpointRef
	for attemptNumber := 1; attemptNumber <= maxAttempts; attemptNumber++ {
		selectedProvider := resolved.Provider
		var selectedHardware *app.HardwareSelection
		if selectedProvider == string(app.ProviderAuto) {
			decision, err := routing.Select(ctx, registry, job, routingOptions(resolved, excluded, resumeFrom))
			decision.RunID = runID
			if decision.RunID != "" {
				_ = store.SaveRoutingDecision(ctx, decision)
			}
			if err != nil {
				if resumeFrom != nil {
					message := fmt.Sprintf("retryable provider failure but checkpoint %q is not reachable by any remaining provider", resumeFrom.URI)
					finishRunOnly(ctx, store, runID, exitCodeMissingResume, runRedactor.String(message))
					_ = writeSummary(ctx, store, paths, runID)
					return exitCodeMissingResume, fmt.Errorf("%s", message)
				}
				finishRunOnly(ctx, store, runID, exitCodeRouting, runRedactor.String(err.Error()))
				_ = writeSummary(ctx, store, paths, runID)
				return exitCodeRouting, err
			}
			selectedProvider = decision.SelectedProvider
			selectedHardware = decision.SelectedHardware
			fmt.Fprintf(opts.Stdout, "Selected %s: %s\n", selectedProvider, decision.SelectionReason)
		}
		code, retryable, err := runAttempt(ctx, opts, store, registry, paths, runID, selectedProvider, job, manifest, resumeFrom, selectedHardware)
		if err == nil {
			return code, nil
		}
		if !retryable || resolved.Provider != string(app.ProviderAuto) || attemptNumber == maxAttempts {
			if retryable {
				finishRunOnly(ctx, store, runID, code, runRedactor.String(err.Error()))
			}
			_ = writeSummary(ctx, store, paths, runID)
			return code, err
		}
		excluded[selectedProvider] = true
		checkpointRef, checkpointErr := (checkpoint.Resolver{Home: resolved.SwitchboardHome}).Latest(ctx, runID)
		if checkpointErr != nil || checkpointRef == nil {
			message := "retryable provider failure but no checkpoint was found"
			finishRunOnly(ctx, store, runID, exitCodeMissingResume, message)
			_ = writeSummary(ctx, store, paths, runID)
			return exitCodeMissingResume, fmt.Errorf("%s", message)
		}
		resumeFrom = checkpointRef
		fmt.Fprintf(opts.Stdout, "Found checkpoint: step %d\n", checkpointRef.Step)
	}
	return exitCodeInternal, fmt.Errorf("run did not complete")
}

func routingOptions(resolved config.ResolvedTrainConfig, excluded map[string]bool, resumeFrom *app.CheckpointRef) routing.Options {
	return routing.Options{
		Mode:                resolved.Routing.Mode,
		Objective:           resolved.Routing.Objective,
		Exclude:             excluded,
		BudgetMaxRunCostUSD: resolved.Routing.Budget.MaxRunCostUSD,
		Sizing: routing.SizingHints{
			ModelParametersB:          resolved.Sizing.Hints.ModelParametersB,
			ModelArtifactGB:           resolved.Sizing.Hints.ModelArtifactGB,
			BatchSize:                 resolved.Sizing.Hints.BatchSize,
			GradientAccumulationSteps: resolved.Sizing.Hints.GradientAccumulationSteps,
			Precision:                 resolved.Sizing.Hints.Precision,
			Optimizer:                 resolved.Sizing.Hints.Optimizer,
			ExpectedSteps:             resolved.Sizing.Hints.ExpectedSteps,
		},
		Hardware: routing.HardwareConstraints{
			MaxGPUs:            resolved.Hardware.Constraints.MaxGPUs,
			AllowedGPUFamilies: resolved.Hardware.Constraints.AllowedGPUFamilies,
			MinVRAMGBPerGPU:    resolved.Hardware.Constraints.MinVRAMGBPerGPU,
			Regions:            resolved.Hardware.Constraints.Regions,
			AllowSpot:          resolved.Hardware.Constraints.AllowSpot,
			RequireOnDemand:    resolved.Hardware.Constraints.RequireOnDemand,
		},
		ManualHardware: routing.ManualHardware{
			Provider:    resolved.Hardware.Manual.Provider,
			ShapeID:     resolved.Hardware.Manual.ShapeID,
			MachineType: resolved.Hardware.Manual.MachineType,
			Region:      resolved.Hardware.Manual.Region,
		},
		ResumeFrom: resumeFrom,
	}
}

func shouldPackageForProvider(resolved config.ResolvedTrainConfig, job app.JobSpec) bool {
	if job.Image != "" || job.Script == "" {
		return false
	}
	if resolved.Provider != gcpprovider.ProviderName && resolved.Provider != string(app.ProviderAuto) {
		return false
	}
	return resolved.Packaging.Image != "" || resolved.Packaging.Dockerfile != "" || resolved.GCP.ArtifactRegistryRepository != ""
}

func packageJob(ctx context.Context, opts Options, resolved config.ResolvedTrainConfig, job app.JobSpec, runID string) (app.JobSpec, error) {
	image := resolved.Packaging.Image
	var err error
	if image == "" {
		image, err = packaging.ArtifactRegistryImage(resolved.GCP.Location, resolved.GCP.ProjectID, resolved.GCP.ArtifactRegistryRepository, runID)
		if err != nil {
			return app.JobSpec{}, err
		}
	}
	builder := imageBuilder(opts)
	fmt.Fprintf(opts.Stdout, "Packaging %s as %s\n", job.Script, image)
	result, err := builder.BuildAndPush(ctx, packaging.BuildRequest{
		Config: packaging.Config{
			Dockerfile: resolved.Packaging.Dockerfile,
			ContextDir: resolved.Packaging.Context,
			Image:      image,
			Platform:   resolved.Packaging.Platform,
		},
		RunID: runID,
	})
	if err != nil {
		return app.JobSpec{}, err
	}
	packaged := job
	packaged.Image = result.Image
	if len(packaged.Command) == 0 {
		scriptPath, err := containerScriptPath(job.Script, resolved.Packaging.Context)
		if err != nil {
			return app.JobSpec{}, err
		}
		packaged.Command = []string{"python3", scriptPath}
	}
	packaged.Script = ""
	return packaged, nil
}

func imageBuilder(opts Options) ImageBuilder {
	if opts.ImageBuilderFactory != nil {
		return opts.ImageBuilderFactory(opts.Stdout, opts.Stderr)
	}
	return packaging.DockerBuilder{Stdout: opts.Stdout, Stderr: opts.Stderr}
}

func containerScriptPath(script string, contextDir string) (string, error) {
	if contextDir == "" {
		contextDir = "."
	}
	if filepath.IsAbs(script) {
		absContext, err := filepath.Abs(contextDir)
		if err != nil {
			return "", fmt.Errorf("resolve packaging context: %w", err)
		}
		rel, err := filepath.Rel(absContext, script)
		if err != nil {
			return "", fmt.Errorf("resolve script relative to packaging context: %w", err)
		}
		if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return "", fmt.Errorf("script %q must be inside packaging context %q", script, contextDir)
		}
		return filepath.ToSlash(rel), nil
	}
	return filepath.ToSlash(script), nil
}

func finishFailed(ctx context.Context, store *state.Store, runID string, attemptID string, code int, reason string, providerRef string) {
	endedAt := time.Now().UTC()
	_ = store.FinishAttempt(ctx, attemptID, app.AttemptStateFailed, code, reason, providerRef, endedAt)
	_ = store.FinishRun(ctx, runID, app.RunStateFailed, code, reason, endedAt)
}

func finishRunOnly(ctx context.Context, store *state.Store, runID string, code int, reason string) {
	_ = store.FinishRun(ctx, runID, app.RunStateFailed, code, reason, time.Now().UTC())
}

func runAttempt(ctx context.Context, opts Options, store *state.Store, registry *provider.Registry, paths artifact.Paths, runID string, selectedProvider string, job app.JobSpec, manifest app.DataManifest, resumeFrom *app.CheckpointRef, selectedHardware *app.HardwareSelection) (int, bool, error) {
	baseRedactor := redact.FromEnvironment(job.Env)
	attemptID := app.NewAttemptID()
	attempt := app.Attempt{ID: attemptID, RunID: runID, Provider: selectedProvider, State: app.AttemptStateRunning, StartedAt: time.Now().UTC()}
	if resumeFrom != nil {
		attempt.ResumeFromURI = resumeFrom.URI
		step := resumeFrom.Step
		attempt.ResumeFromStep = &step
	}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		return exitCodeInternal, false, err
	}
	adapter, err := registry.Get(selectedProvider)
	if err != nil {
		finishFailed(ctx, store, runID, attemptID, exitCodeInternal, baseRedactor.String(err.Error()), "")
		return exitCodeInternal, false, err
	}
	attemptJob := job
	if selectedProvider == string(app.ProviderLocal) {
		prepared, err := runtimeprep.PrepareLocal(job, manifest, paths.Workspace)
		if err != nil {
			finishFailed(ctx, store, runID, attemptID, exitCodeInvalidSpec, baseRedactor.String(err.Error()), "")
			_ = writeSummary(ctx, store, paths, runID)
			return exitCodeInvalidSpec, false, err
		}
		attemptJob = prepared.Job
	}
	if report := adapter.ValidateJob(ctx, attemptJob); !report.Supported {
		reason := "job is not supported"
		if len(report.Reasons) > 0 {
			reason = report.Reasons[0]
		}
		finishFailed(ctx, store, runID, attemptID, exitCodeInvalidSpec, baseRedactor.String(reason), "")
		_ = writeSummary(ctx, store, paths, runID)
		return exitCodeInvalidSpec, false, fmt.Errorf("%s", reason)
	}
	resumeValue := ""
	if resumeFrom != nil {
		resumeValue = resumeFrom.URI
	}
	runtimeEnv := runtimeEnvForProvider(selectedProvider, runID, attemptID, resumeValue, paths)
	attemptRedactor := redact.FromEnvironment(attemptJob.Env, runtimeEnv)
	estimate := app.CostEstimate{Currency: "USD"}
	if selectedHardware != nil && selectedHardware.HourlyUSD > 0 {
		estimate.HourlyUSD = selectedHardware.HourlyUSD
	} else {
		var err error
		estimate, err = adapter.Estimate(ctx, attemptJob)
		if err != nil {
			reason := attemptRedactor.String(err.Error())
			finishFailed(ctx, store, runID, attemptID, exitCodeRouting, reason, "")
			_ = writeSummary(ctx, store, paths, runID)
			return exitCodeRouting, false, err
		}
	}
	if err := store.UpdateAttemptEstimate(ctx, attemptID, estimate); err != nil {
		return exitCodeInternal, false, err
	}
	result, err := adapter.Submit(ctx, app.SubmitRequest{
		JobSpec:          attemptJob,
		RunID:            runID,
		AttemptID:        attemptID,
		ResumeFrom:       resumeFrom,
		SelectedHardware: selectedHardware,
		RuntimeEnv:       runtimeEnv,
		RunDir:           paths.RunDir,
		OnStarted: func(ref app.ProviderJobRef) error {
			return store.UpdateAttemptProviderRef(ctx, attemptID, attemptRedactor.String(ref.ID))
		},
	})
	if err != nil {
		currentRun, runErr := store.GetRun(ctx, runID)
		if runErr != nil {
			return exitCodeInternal, false, runErr
		}
		if currentRun.State == app.RunStateCanceled {
			if err := writeSummary(ctx, store, paths, runID); err != nil {
				return exitCodeInternal, false, err
			}
			fmt.Fprintf(opts.Stdout, "Run %s %s\n", runID, app.RunStateCanceled)
			return exitCodeCanceled, false, nil
		}
		retryable := app.IsRetryableProviderError(err)
		endedAt := time.Now().UTC()
		reason := attemptRedactor.String(err.Error())
		if result.ExitReason != "" {
			reason = attemptRedactor.String(result.ExitReason)
		}
		providerRef := attemptRedactor.String(result.ProviderJobRef)
		if finishErr := store.FinishAttempt(ctx, attemptID, app.AttemptStateFailed, result.ExitCode, reason, providerRef, endedAt); finishErr != nil {
			return exitCodeInternal, false, finishErr
		}
		if !retryable {
			if finishErr := store.FinishRun(ctx, runID, app.RunStateFailed, result.ExitCode, reason, endedAt); finishErr != nil {
				return exitCodeInternal, false, finishErr
			}
		}
		return result.ExitCode, retryable, err
	}
	currentRun, err := store.GetRun(ctx, runID)
	if err != nil {
		return exitCodeInternal, false, err
	}
	if currentRun.State == app.RunStateCanceled {
		if err := writeSummary(ctx, store, paths, runID); err != nil {
			return exitCodeInternal, false, err
		}
		fmt.Fprintf(opts.Stdout, "Run %s %s\n", runID, app.RunStateCanceled)
		return exitCodeCanceled, false, nil
	}
	endedAt := time.Now().UTC()
	runState, attemptState := app.RunStateSucceeded, app.AttemptStateSucceeded
	if result.ExitCode != 0 {
		runState = app.RunStateFailed
		attemptState = app.AttemptStateFailed
	}
	exitReason := attemptRedactor.String(result.ExitReason)
	providerRef := attemptRedactor.String(result.ProviderJobRef)
	if err := store.FinishAttempt(ctx, attemptID, attemptState, result.ExitCode, exitReason, providerRef, endedAt); err != nil {
		return exitCodeInternal, false, err
	}
	if err := store.FinishRun(ctx, runID, runState, result.ExitCode, exitReason, endedAt); err != nil {
		return exitCodeInternal, false, err
	}
	if err := writeSummary(ctx, store, paths, runID); err != nil {
		return exitCodeInternal, false, err
	}
	fmt.Fprintf(opts.Stdout, "Run %s %s\n", runID, runState)
	return result.ExitCode, false, nil
}

func runtimeEnvForProvider(selectedProvider string, runID string, attemptID string, resumeValue string, paths artifact.Paths) map[string]string {
	checkpointDir := paths.Checkpoints
	eventsPath := paths.EventsJSONL
	if selectedProvider == gcpprovider.ProviderName || selectedProvider == lambdaprovider.ProviderName {
		checkpointDir = "/tmp/switchboard/checkpoints"
		eventsPath = "/tmp/switchboard/events.jsonl"
	}
	return map[string]string{
		"SWITCHBOARD_RUN_ID":          runID,
		"SWITCHBOARD_ATTEMPT_ID":      attemptID,
		"SWITCHBOARD_CHECKPOINT_DIR":  checkpointDir,
		"SWITCHBOARD_RESUME_FROM":     resumeValue,
		"SWITCHBOARD_EVENTS_PATH":     eventsPath,
		"ORCHESTRATOR_RUN_ID":         runID,
		"ORCHESTRATOR_ATTEMPT_ID":     attemptID,
		"ORCHESTRATOR_CHECKPOINT_DIR": checkpointDir,
		"ORCHESTRATOR_RESUME_FROM":    resumeValue,
		"ORCHESTRATOR_EVENTS_PATH":    eventsPath,
	}
}

func writeSummary(ctx context.Context, store *state.Store, paths artifact.Paths, runID string) error {
	run, err := store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	attempts, err := store.AttemptsByRun(ctx, runID)
	if err != nil {
		return err
	}
	events, err := event.ReadJSONL(paths.EventsJSONL)
	if err != nil {
		return err
	}
	if decision, err := store.GetRoutingDecision(ctx, runID); err == nil {
		built := summary.Build(run, attempts, events, decision)
		return artifact.WriteSummary(paths.Summary, redact.FromEnvironment().Summary(built))
	}
	built := summary.Build(run, attempts, events)
	return artifact.WriteSummary(paths.Summary, redact.FromEnvironment().Summary(built))
}

func newStatusCommand(opts Options, home *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status <run-id>",
		Short: "Show run status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedHome, err := resolveHome(*home)
			if err != nil {
				return err
			}
			store, err := state.Open(artifact.DBPath(resolvedHome))
			if err != nil {
				return err
			}
			defer store.Close()
			run, err := store.GetRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(opts.Stdout).Encode(run)
			}
			fmt.Fprintf(opts.Stdout, "%s\t%s\t%s\t%s\n", run.ID, run.State, run.Provider, runTarget(run))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON")
	return cmd
}

func runTarget(run app.Run) string {
	if run.Script != "" {
		return run.Script
	}
	return run.Image
}

func newLogsCommand(opts Options, home *string) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <run-id>",
		Short: "Show run logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedHome, err := resolveHome(*home)
			if err != nil {
				return err
			}
			path := artifact.ForRun(resolvedHome, args[0]).Logs
			if follow {
				return followLogs(cmd.Context(), opts.Stdout, resolvedHome, args[0], path)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, err = opts.Stdout.Write(content)
			return err
		},
	}
	cmd.Flags().BoolVar(&follow, "follow", false, "Follow logs")
	return cmd
}

func newCancelCommand(opts Options, home *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel a running local run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedHome, err := resolveHome(*home)
			if err != nil {
				return err
			}
			return cancelRun(cmd.Context(), opts, resolvedHome, args[0])
		},
	}
	return cmd
}

func cancelRun(ctx context.Context, opts Options, home string, runID string) error {
	paths := artifact.ForRun(home, runID)
	store, err := state.Open(paths.DB)
	if err != nil {
		return err
	}
	defer store.Close()

	run, err := store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.State != app.RunStateRunning {
		fmt.Fprintf(opts.Stdout, "Run %s already %s\n", runID, run.State)
		return nil
	}
	attempts, err := store.AttemptsByRun(ctx, runID)
	if err != nil {
		return err
	}
	var running *app.Attempt
	for i := range attempts {
		if attempts[i].State == app.AttemptStateRunning {
			running = &attempts[i]
		}
	}
	if running == nil {
		return fmt.Errorf("run %s has no running attempt", runID)
	}
	if running.ProviderRef == "" {
		return fmt.Errorf("run %s has no provider process reference yet", runID)
	}
	registry := buildProviderRegistry(opts, config.MockConfig{}, config.GCPConfig{})
	adapter, err := registry.Get(running.Provider)
	if err != nil {
		return err
	}
	if err := adapter.Cancel(ctx, app.ProviderJobRef{ID: running.ProviderRef}); err != nil {
		return err
	}
	endedAt := time.Now().UTC()
	if err := store.FinishAttempt(ctx, running.ID, app.AttemptStateCanceled, exitCodeCanceled, "canceled", running.ProviderRef, endedAt); err != nil {
		return err
	}
	if err := store.FinishRun(ctx, runID, app.RunStateCanceled, exitCodeCanceled, "canceled", endedAt); err != nil {
		return err
	}
	if err := writeSummary(ctx, store, paths, runID); err != nil {
		return err
	}
	fmt.Fprintf(opts.Stdout, "Run %s canceled\n", runID)
	return nil
}

func followLogs(ctx context.Context, w io.Writer, home string, runID string, path string) error {
	store, err := state.Open(artifact.DBPath(home))
	if err != nil {
		return err
	}
	defer store.Close()

	var offset int64
	for {
		var err error
		offset, err = copyLogFromOffset(w, path, offset)
		if err != nil {
			return err
		}

		run, err := store.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.State != app.RunStateRunning {
			_, err := copyLogFromOffset(w, path, offset)
			return err
		}

		select {
		case <-ctx.Done():
			if _, err := copyLogFromOffset(w, path, offset); err != nil {
				return err
			}
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func copyLogFromOffset(w io.Writer, path string, offset int64) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	written, err := io.Copy(w, file)
	if err != nil {
		return offset, err
	}
	return offset + written, nil
}

func newProvidersCommand(opts Options) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "Manage providers",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			registry := buildProviderRegistry(opts, config.MockConfig{}, config.GCPConfig{})
			names := registry.List()
			if asJSON {
				return json.NewEncoder(opts.Stdout).Encode(names)
			}
			for _, name := range names {
				fmt.Fprintln(opts.Stdout, name)
			}
			return nil
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "Print JSON")
	cmd.AddCommand(list)
	inspect := &cobra.Command{
		Use:   "inspect <provider>",
		Short: "Inspect provider capabilities without validating credentials",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry := buildProviderRegistry(opts, config.MockConfig{}, config.GCPConfig{})
			adapter, err := registry.Get(args[0])
			if err != nil {
				return err
			}
			capabilities, err := adapter.Capabilities(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(opts.Stdout).Encode(providerInspectReport{
					Name:         string(adapter.Name()),
					Capabilities: capabilities,
				})
			}
			printProviderCapabilities(opts.Stdout, string(adapter.Name()), capabilities)
			return nil
		},
	}
	inspect.Flags().BoolVar(&asJSON, "json", false, "Print JSON")
	check := &cobra.Command{
		Use:   "check <provider>",
		Short: "Validate provider credentials and endpoint readiness",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry := buildProviderRegistry(opts, config.MockConfig{}, config.GCPConfig{})
			adapter, err := registry.Get(args[0])
			if err != nil {
				return err
			}
			report := providerCheckReport{Name: string(adapter.Name()), Ready: true}
			capabilities, capErr := adapter.Capabilities(cmd.Context())
			if capErr != nil {
				report.Ready = false
				report.Error = capErr.Error()
			} else {
				report.Capabilities = capabilities
			}
			if authErr := adapter.ValidateAuth(cmd.Context()); authErr != nil {
				report.Ready = false
				report.Error = authErr.Error()
			}
			if asJSON {
				if err := json.NewEncoder(opts.Stdout).Encode(report); err != nil {
					return err
				}
			} else if report.Ready {
				fmt.Fprintf(opts.Stdout, "%s ready\n", report.Name)
			} else {
				fmt.Fprintf(opts.Stdout, "%s not ready: %s\n", report.Name, report.Error)
			}
			if !report.Ready {
				return exitCodeError{code: exitCodeInternal}
			}
			return nil
		},
	}
	check.Flags().BoolVar(&asJSON, "json", false, "Print JSON")
	cmd.AddCommand(inspect, check)
	return cmd
}

type providerInspectReport struct {
	Name         string                   `json:"name"`
	Capabilities app.ProviderCapabilities `json:"capabilities"`
}

type providerCheckReport struct {
	Name         string                   `json:"name"`
	Ready        bool                     `json:"ready"`
	Error        string                   `json:"error,omitempty"`
	Capabilities app.ProviderCapabilities `json:"capabilities,omitempty"`
}

func printProviderCapabilities(w io.Writer, name string, capabilities app.ProviderCapabilities) {
	fmt.Fprintf(w, "name\t%s\n", name)
	fmt.Fprintf(w, "regions\t%s\n", strings.Join(capabilities.Regions, ","))
	fmt.Fprintf(w, "gpu_families\t%s\n", strings.Join(capabilities.GPUFamilies, ","))
	fmt.Fprintf(w, "supports_docker_image\t%t\n", capabilities.SupportsDockerImage)
	fmt.Fprintf(w, "supports_local_script\t%t\n", capabilities.SupportsLocalScript)
	fmt.Fprintf(w, "supports_data_bundle\t%t\n", capabilities.SupportsDataBundle)
	fmt.Fprintf(w, "supports_object_store_pull\t%t\n", capabilities.SupportsObjectStorePull)
	fmt.Fprintf(w, "uri_schemes\t%s\n", strings.Join(capabilities.SupportedURISchemes, ","))
	fmt.Fprintf(w, "checkpoint_schemes\t%s\n", strings.Join(capabilities.SupportedCheckpointSchemes, ","))
}

func newCredentialsCommand(opts Options, homeFlag *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credentials",
		Short: "Manage encrypted provider credentials",
	}

	var force bool
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the encrypted local credentials store",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, path, err := openCredentialStore(opts, *homeFlag)
			if err != nil {
				return err
			}
			if err := store.Init(force); err != nil {
				return err
			}
			fmt.Fprintf(opts.Stdout, "Initialized credentials store: %s\n", path)
			return nil
		},
	}
	initCmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing credentials store")

	var fromEnv string
	var valueStdin bool
	setCmd := &cobra.Command{
		Use:   "set <provider> <name>",
		Short: "Set a credential value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := credentials.Key(args[0], args[1])
			if err != nil {
				return err
			}
			value, err := credentialValue(opts, key, fromEnv, valueStdin)
			if err != nil {
				return err
			}
			store, _, err := openCredentialStore(opts, *homeFlag)
			if err != nil {
				return err
			}
			if err := store.Set(key, value); err != nil {
				return err
			}
			fmt.Fprintf(opts.Stdout, "Stored credential: %s\n", key)
			return nil
		},
	}
	setCmd.Flags().StringVar(&fromEnv, "from-env", "", "Read the credential value from an environment variable")
	setCmd.Flags().BoolVar(&valueStdin, "value-stdin", false, "Read the credential value from stdin")

	var show bool
	getCmd := &cobra.Command{
		Use:   "get <provider> <name>",
		Short: "Check or reveal a credential value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := credentials.Key(args[0], args[1])
			if err != nil {
				return err
			}
			store, _, err := openCredentialStore(opts, *homeFlag)
			if err != nil {
				return err
			}
			secret, err := store.Get(key)
			if err != nil {
				return err
			}
			if show {
				fmt.Fprintln(opts.Stdout, secret.Value)
			} else {
				fmt.Fprintf(opts.Stdout, "Credential exists: %s\n", key)
			}
			return nil
		},
	}
	getCmd.Flags().BoolVar(&show, "show", false, "Print the credential value")

	deleteCmd := &cobra.Command{
		Use:   "delete <provider> <name>",
		Short: "Delete a credential",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := credentials.Key(args[0], args[1])
			if err != nil {
				return err
			}
			store, _, err := openCredentialStore(opts, *homeFlag)
			if err != nil {
				return err
			}
			if err := store.Delete(key); err != nil {
				return err
			}
			fmt.Fprintf(opts.Stdout, "Deleted credential: %s\n", key)
			return nil
		},
	}

	var listJSON bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List stored credential metadata",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, err := openCredentialStore(opts, *homeFlag)
			if err != nil {
				return err
			}
			items, err := store.List()
			if err != nil {
				return err
			}
			if listJSON {
				return json.NewEncoder(opts.Stdout).Encode(items)
			}
			for _, item := range items {
				fmt.Fprintf(opts.Stdout, "%s\tupdated=%s\n", item.Key, item.UpdatedAt.Format(time.RFC3339))
			}
			return nil
		},
	}
	listCmd.Flags().BoolVar(&listJSON, "json", false, "Print JSON")

	statusCmd := &cobra.Command{
		Use:   "status <provider>",
		Short: "Show stored credentials for one provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := strings.ReplaceAll(strings.ToLower(args[0]), "-", "_")
			store, _, err := openCredentialStore(opts, *homeFlag)
			if err != nil {
				return err
			}
			items, err := store.List()
			if err != nil {
				return err
			}
			found := false
			for _, item := range items {
				if item.Provider != provider {
					continue
				}
				found = true
				fmt.Fprintf(opts.Stdout, "%s\tpresent\tupdated=%s\n", item.Key, item.UpdatedAt.Format(time.RFC3339))
			}
			if !found {
				fmt.Fprintf(opts.Stdout, "No stored credentials for provider: %s\n", provider)
			}
			return nil
		},
	}

	cmd.AddCommand(initCmd, setCmd, getCmd, deleteCmd, listCmd, statusCmd)
	return cmd
}

func openCredentialStore(opts Options, homeFlag string) (*credentials.EncryptedFileStore, string, error) {
	resolvedHome, err := home.Resolve(homeFlag)
	if err != nil {
		return nil, "", err
	}
	passphrase, err := credentials.PassphraseFromEnvOrPrompt("Credentials passphrase: ", opts.Stderr)
	if err != nil {
		return nil, "", err
	}
	path := credentials.DefaultPath(resolvedHome)
	store, err := credentials.OpenEncryptedFile(path, passphrase)
	if err != nil {
		return nil, "", err
	}
	return store, path, nil
}

func openCredentialStoreAtHome(opts Options, resolvedHome string) (*credentials.EncryptedFileStore, string, error) {
	passphrase, err := credentials.PassphraseFromEnvOrPrompt("Credentials passphrase: ", opts.Stderr)
	if err != nil {
		return nil, "", err
	}
	path := credentials.DefaultPath(resolvedHome)
	store, err := credentials.OpenEncryptedFile(path, passphrase)
	if err != nil {
		return nil, "", err
	}
	return store, path, nil
}

func credentialValue(opts Options, key string, fromEnv string, valueStdin bool) (string, error) {
	if fromEnv != "" && valueStdin {
		return "", fmt.Errorf("--from-env and --value-stdin are mutually exclusive")
	}
	if fromEnv != "" {
		value := os.Getenv(fromEnv)
		if value == "" {
			return "", fmt.Errorf("environment variable %s is empty or unset", fromEnv)
		}
		return value, nil
	}
	if valueStdin {
		content, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		value := strings.TrimRight(string(content), "\r\n")
		if value == "" {
			return "", fmt.Errorf("stdin value for %s is empty", key)
		}
		return value, nil
	}
	return credentials.SecretFromPrompt(fmt.Sprintf("Credential value for %s: ", key), opts.Stderr)
}

func buildProviderRegistry(opts Options, mockConfig config.MockConfig, gcpConfig config.GCPConfig) *provider.Registry {
	return buildProviderRegistryWithOptions(opts, mockConfig, gcpConfig, config.LambdaConfig{}, credentials.Resolver{}, true, true)
}

func buildTrainProviderRegistry(opts Options, resolved config.ResolvedTrainConfig, credentialResolver credentials.Resolver) *provider.Registry {
	return buildProviderRegistryWithOptions(opts, resolved.Mock, resolved.GCP, resolved.Lambda, credentialResolver, shouldRegisterGCPForTrain(resolved), shouldRegisterLambdaForTrain(resolved))
}

func buildProviderRegistryWithOptions(opts Options, mockConfig config.MockConfig, gcpConfig config.GCPConfig, lambdaConfig config.LambdaConfig, credentialResolver credentials.Resolver, includeGCP bool, includeLambda bool) *provider.Registry {
	adapters := []app.ProviderAdapter{localprovider.New(opts.Stdout, opts.Stderr)}
	adapters = append(adapters, chinacloudprovider.NewProviders()...)
	for _, providerConfig := range mergedMockProviders(mockConfig) {
		adapters = append(adapters, mockprovider.New(mockprovider.Config{
			Name:           providerConfig.Name,
			HourlyCost:     providerConfig.HourlyCost,
			FailureMode:    providerConfig.FailureMode,
			HardwareShapes: append([]app.HardwareShape(nil), providerConfig.HardwareShapes...),
			Events:         mockEvents(providerConfig.Events),
		}, opts.Stdout, opts.Stderr))
	}
	if opts.GCPProviderFactory != nil && includeGCP {
		adapters = append(adapters, opts.GCPProviderFactory(gcpConfig, opts.Stdout, opts.Stderr))
	} else if includeGCP {
		adapters = append(adapters, gcpprovider.New(gcpConfigFromConfig(gcpConfig), opts.Stdout, opts.Stderr))
	}
	if opts.LambdaProviderFactory != nil && includeLambda {
		adapters = append(adapters, opts.LambdaProviderFactory(lambdaConfig, opts.Stdout, opts.Stderr))
	} else if includeLambda {
		adapters = append(adapters, lambdaprovider.New(lambdaConfigFromConfig(lambdaConfig, credentialResolver), opts.Stdout, opts.Stderr))
	}
	return provider.NewRegistry(adapters...)
}

func credentialResolverForTrain(opts Options, resolved config.ResolvedTrainConfig) (credentials.Resolver, error) {
	if opts.LambdaProviderFactory != nil || !shouldRegisterLambdaForTrain(resolved) {
		return credentials.Resolver{}, nil
	}
	store, _, err := openCredentialStoreAtHome(opts, resolved.SwitchboardHome)
	if err != nil {
		return credentials.Resolver{}, err
	}
	return credentials.Resolver{Store: store}, nil
}

func shouldRegisterGCPForTrain(resolved config.ResolvedTrainConfig) bool {
	if resolved.Provider == gcpprovider.ProviderName {
		return true
	}
	if resolved.Provider != string(app.ProviderAuto) {
		return false
	}
	return resolved.GCP.ProjectID != "" && resolved.GCP.OutputURIPrefix != ""
}

func shouldRegisterLambdaForTrain(resolved config.ResolvedTrainConfig) bool {
	if resolved.Provider == lambdaprovider.ProviderName {
		return true
	}
	if resolved.Provider != string(app.ProviderAuto) {
		return false
	}
	return resolved.Lambda.RegionName != "" && resolved.Lambda.InstanceTypeName != "" && resolved.Lambda.SSHKeyName != "" && resolved.Lambda.SSHPrivateKey != ""
}

func gcpConfigFromConfig(cfg config.GCPConfig) gcpprovider.Config {
	return gcpprovider.Config{
		ProjectID:                  cfg.ProjectID,
		Location:                   cfg.Location,
		OutputURIPrefix:            cfg.OutputURIPrefix,
		MachineType:                cfg.MachineType,
		AcceleratorType:            cfg.AcceleratorType,
		AcceleratorCount:           cfg.AcceleratorCount,
		BootDiskType:               cfg.BootDiskType,
		BootDiskSizeGB:             cfg.BootDiskSizeGB,
		ServiceAccount:             cfg.ServiceAccount,
		Network:                    cfg.Network,
		PollIntervalSeconds:        cfg.PollIntervalSeconds,
		EstimateHourlyUSD:          cfg.EstimateHourlyUSD,
		ArtifactRegistryRepository: cfg.ArtifactRegistryRepository,
	}
}

func lambdaConfigFromConfig(cfg config.LambdaConfig, credentialResolver credentials.Resolver) lambdaprovider.Config {
	terminateOnCompletion := true
	if cfg.TerminateOnCompletion != nil {
		terminateOnCompletion = *cfg.TerminateOnCompletion
	}
	return lambdaprovider.Config{
		RegionName:               cfg.RegionName,
		InstanceTypeName:         cfg.InstanceTypeName,
		SSHKeyName:               cfg.SSHKeyName,
		SSHPrivateKey:            cfg.SSHPrivateKey,
		ImageFamily:              cfg.ImageFamily,
		PollIntervalSeconds:      cfg.PollIntervalSeconds,
		TerminateOnCompletion:    terminateOnCompletion,
		TerminateOnCompletionSet: cfg.TerminateOnCompletion != nil,
		KeepInstanceOnFailure:    cfg.KeepInstanceOnFailure,
		APITimeoutSeconds:        cfg.APITimeoutSeconds,
		SSHConnectTimeoutSecs:    cfg.SSHConnectTimeoutSecs,
		SSHReadyTimeoutSeconds:   cfg.SSHReadyTimeoutSeconds,
		Credentials:              credentialResolver,
	}
}

func mergedMockProviders(mockConfig config.MockConfig) []config.MockProviderConfig {
	defaults := []config.MockProviderConfig{
		{Name: "mock-lambda", HourlyCost: 1.10, FailureMode: mockprovider.FailureCapacity},
		{Name: "mock-gcp", HourlyCost: 1.30},
	}
	if len(mockConfig.Providers) == 0 {
		return defaults
	}
	byName := map[string]config.MockProviderConfig{}
	for _, item := range defaults {
		byName[item.Name] = item
	}
	for _, item := range mockConfig.Providers {
		byName[item.Name] = item
	}
	out := make([]config.MockProviderConfig, 0, len(byName))
	for _, name := range []string{"mock-lambda", "mock-gcp"} {
		if item, ok := byName[name]; ok {
			out = append(out, item)
			delete(byName, name)
		}
	}
	for _, item := range byName {
		out = append(out, item)
	}
	return out
}

func mockEvents(configs []config.MockEventConfig) []app.Event {
	events := make([]app.Event, 0, len(configs))
	for _, cfg := range configs {
		events = append(events, app.Event{
			Type:          app.EventType(cfg.Type),
			Step:          cfg.Step,
			Split:         cfg.Split,
			State:         cfg.State,
			CheckpointURI: cfg.CheckpointURI,
			Message:       cfg.Message,
			Metrics:       cfg.Metrics,
		})
	}
	return events
}

func resolveHome(flag string) (string, error) {
	return home.Resolve(flag)
}

type exitCodeError struct {
	code int
}

func (e exitCodeError) Error() string {
	return fmt.Sprintf("run failed with exit code %d", e.code)
}
