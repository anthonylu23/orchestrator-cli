package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/checkpoint"
	"github.com/anthonylu23/switchboard-cli/internal/config"
	"github.com/anthonylu23/switchboard-cli/internal/data"
	"github.com/anthonylu23/switchboard-cli/internal/event"
	"github.com/anthonylu23/switchboard-cli/internal/home"
	"github.com/anthonylu23/switchboard-cli/internal/provider"
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
	Stdout io.Writer
	Stderr io.Writer
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
	paths := artifact.ForRun(resolved.SwitchboardHome, runID)
	if err := artifact.EnsureRun(paths); err != nil {
		return exitCodeInternal, err
	}
	store, err := state.Open(paths.DB)
	if err != nil {
		return exitCodeInternal, err
	}
	defer store.Close()

	run := app.Run{ID: runID, JobName: resolved.Job.Name, Script: resolved.Job.Script, Provider: resolved.Provider, State: app.RunStateRunning, StartedAt: now}
	if err := store.CreateRun(ctx, run); err != nil {
		return exitCodeInternal, err
	}

	registry := buildProviderRegistry(opts, resolved.Mock)
	maxAttempts := resolved.Routing.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	excluded := map[string]bool{}
	var resumeFrom *app.CheckpointRef
	for attemptNumber := 1; attemptNumber <= maxAttempts; attemptNumber++ {
		selectedProvider := resolved.Provider
		if selectedProvider == string(app.ProviderAuto) {
			decision, err := routing.Select(ctx, registry, job, routing.Options{Objective: resolved.Routing.Objective, Exclude: excluded})
			decision.RunID = runID
			if decision.RunID != "" {
				_ = store.SaveRoutingDecision(ctx, decision)
			}
			if err != nil {
				finishRunOnly(ctx, store, runID, exitCodeRouting, runRedactor.String(err.Error()))
				_ = writeSummary(ctx, store, paths, runID)
				return exitCodeRouting, err
			}
			selectedProvider = decision.SelectedProvider
			fmt.Fprintf(opts.Stdout, "Selected %s: %s\n", selectedProvider, decision.SelectionReason)
		}
		code, retryable, err := runAttempt(ctx, opts, store, registry, paths, runID, selectedProvider, job, manifest, resumeFrom)
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

func finishFailed(ctx context.Context, store *state.Store, runID string, attemptID string, code int, reason string, providerRef string) {
	endedAt := time.Now().UTC()
	_ = store.FinishAttempt(ctx, attemptID, app.AttemptStateFailed, code, reason, providerRef, endedAt)
	_ = store.FinishRun(ctx, runID, app.RunStateFailed, code, reason, endedAt)
}

func finishRunOnly(ctx context.Context, store *state.Store, runID string, code int, reason string) {
	_ = store.FinishRun(ctx, runID, app.RunStateFailed, code, reason, time.Now().UTC())
}

func runAttempt(ctx context.Context, opts Options, store *state.Store, registry *provider.Registry, paths artifact.Paths, runID string, selectedProvider string, job app.JobSpec, manifest app.DataManifest, resumeFrom *app.CheckpointRef) (int, bool, error) {
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
	runtimeEnv := map[string]string{
		"SWITCHBOARD_RUN_ID":          runID,
		"SWITCHBOARD_ATTEMPT_ID":      attemptID,
		"SWITCHBOARD_CHECKPOINT_DIR":  paths.Checkpoints,
		"SWITCHBOARD_RESUME_FROM":     resumeValue,
		"SWITCHBOARD_EVENTS_PATH":     paths.EventsJSONL,
		"ORCHESTRATOR_RUN_ID":         runID,
		"ORCHESTRATOR_ATTEMPT_ID":     attemptID,
		"ORCHESTRATOR_CHECKPOINT_DIR": paths.Checkpoints,
		"ORCHESTRATOR_RESUME_FROM":    resumeValue,
		"ORCHESTRATOR_EVENTS_PATH":    paths.EventsJSONL,
	}
	attemptRedactor := redact.FromEnvironment(attemptJob.Env, runtimeEnv)
	estimate, err := adapter.Estimate(ctx, attemptJob)
	if err != nil {
		reason := attemptRedactor.String(err.Error())
		finishFailed(ctx, store, runID, attemptID, exitCodeRouting, reason, "")
		_ = writeSummary(ctx, store, paths, runID)
		return exitCodeRouting, false, err
	}
	if err := store.UpdateAttemptEstimate(ctx, attemptID, estimate); err != nil {
		return exitCodeInternal, false, err
	}
	result, err := adapter.Submit(ctx, app.SubmitRequest{
		JobSpec:    attemptJob,
		RunID:      runID,
		AttemptID:  attemptID,
		ResumeFrom: resumeFrom,
		RuntimeEnv: runtimeEnv,
		RunDir:     paths.RunDir,
		OnStarted: func(ref app.ProviderJobRef) error {
			return store.UpdateAttemptProviderRef(ctx, attemptID, attemptRedactor.String(ref.ID))
		},
	})
	if err != nil {
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
			fmt.Fprintf(opts.Stdout, "%s\t%s\t%s\t%s\n", run.ID, run.State, run.Provider, run.Script)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON")
	return cmd
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
	registry := provider.NewRegistry(localprovider.New(opts.Stdout, opts.Stderr))
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
			registry := buildProviderRegistry(opts, config.MockConfig{})
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
	return cmd
}

func buildProviderRegistry(opts Options, mockConfig config.MockConfig) *provider.Registry {
	adapters := []app.ProviderAdapter{localprovider.New(opts.Stdout, opts.Stderr)}
	for _, providerConfig := range mergedMockProviders(mockConfig) {
		adapters = append(adapters, mockprovider.New(mockprovider.Config{
			Name:        providerConfig.Name,
			HourlyCost:  providerConfig.HourlyCost,
			FailureMode: providerConfig.FailureMode,
			Events:      mockEvents(providerConfig.Events),
		}, opts.Stdout, opts.Stderr))
	}
	return provider.NewRegistry(adapters...)
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
