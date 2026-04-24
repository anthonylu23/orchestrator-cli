package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
	"github.com/anthonylu23/orchestrator-cli/internal/artifact"
	"github.com/anthonylu23/orchestrator-cli/internal/checkpoint"
	"github.com/anthonylu23/orchestrator-cli/internal/config"
	"github.com/anthonylu23/orchestrator-cli/internal/data"
	"github.com/anthonylu23/orchestrator-cli/internal/event"
	"github.com/anthonylu23/orchestrator-cli/internal/provider"
	localprovider "github.com/anthonylu23/orchestrator-cli/internal/provider/local"
	mockprovider "github.com/anthonylu23/orchestrator-cli/internal/provider/mock"
	"github.com/anthonylu23/orchestrator-cli/internal/routing"
	"github.com/anthonylu23/orchestrator-cli/internal/runtimeprep"
	"github.com/anthonylu23/orchestrator-cli/internal/state"
	"github.com/anthonylu23/orchestrator-cli/internal/summary"
	"github.com/spf13/cobra"
)

type Options struct {
	Stdout io.Writer
	Stderr io.Writer
}

func NewRootCommand(opts Options) *cobra.Command {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	var home string
	root := &cobra.Command{
		Use:           "orchestrator-cli",
		Short:         "Local-first ML job orchestration",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&home, "home", "", "Orchestrator home directory")
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
			flags.OrchestratorHome = *home
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
	cmd.Flags().StringVar(&flags.ConfigPath, "config", "", "Path to orchestrator-cli YAML config")
	cmd.Flags().StringVar(&flags.Provider, "provider", "local", "Provider to use")
	cmd.Flags().StringVar(&flags.Script, "script", "", "Training script path")
	cmd.Flags().StringArrayVar(&flags.Args, "arg", nil, "Argument to pass to the script; repeat for multiple args")
	cmd.Flags().BoolVar(&flags.AllowLargeDataBundle, "allow-large-data-bundle", false, "Allow local data bundles above configured limit")
	return cmd
}

func runTrain(ctx context.Context, opts Options, resolved config.ResolvedTrainConfig) (int, error) {
	if err := artifact.EnsureHome(resolved.OrchestratorHome); err != nil {
		return 1, err
	}

	manifest, err := data.Prepare(resolved.Job, data.PreflightOptions{
		BundleSizeLimitBytes: resolved.BundleMaxSizeBytes,
		RequireOverride:      resolved.RequireOverrideAboveLimit,
		AllowLargeBundle:     resolved.AllowLargeDataBundle,
	})
	if err != nil {
		return 10, err
	}
	job := resolved.Job
	job.Data = append([]app.DataInput(nil), manifest.Inputs...)

	now := time.Now().UTC()
	runID := app.NewRunID()
	paths := artifact.ForRun(resolved.OrchestratorHome, runID)
	if err := artifact.EnsureRun(paths); err != nil {
		return 1, err
	}
	store, err := state.Open(paths.DB)
	if err != nil {
		return 1, err
	}
	defer store.Close()

	run := app.Run{ID: runID, JobName: resolved.Job.Name, Script: resolved.Job.Script, Provider: resolved.Provider, State: app.RunStateRunning, StartedAt: now}
	if err := store.CreateRun(ctx, run); err != nil {
		return 1, err
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
				finishRunOnly(ctx, store, runID, 30, err.Error())
				_ = writeSummary(ctx, store, paths, runID)
				return 30, err
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
				finishRunOnly(ctx, store, runID, code, err.Error())
			}
			_ = writeSummary(ctx, store, paths, runID)
			return code, err
		}
		excluded[selectedProvider] = true
		checkpointRef, checkpointErr := (checkpoint.Resolver{Home: resolved.OrchestratorHome}).Latest(ctx, runID)
		if checkpointErr != nil || checkpointRef == nil {
			message := "retryable provider failure but no checkpoint was found"
			finishRunOnly(ctx, store, runID, 40, message)
			_ = writeSummary(ctx, store, paths, runID)
			return 40, fmt.Errorf("%s", message)
		}
		resumeFrom = checkpointRef
		fmt.Fprintf(opts.Stdout, "Found checkpoint: step %d\n", checkpointRef.Step)
	}
	return 1, fmt.Errorf("run did not complete")
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
	attemptID := app.NewAttemptID()
	attempt := app.Attempt{ID: attemptID, RunID: runID, Provider: selectedProvider, State: app.AttemptStateRunning, StartedAt: time.Now().UTC()}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		return 1, false, err
	}
	adapter, err := registry.Get(selectedProvider)
	if err != nil {
		finishFailed(ctx, store, runID, attemptID, 1, err.Error(), "")
		return 1, false, err
	}
	attemptJob := job
	if selectedProvider == string(app.ProviderLocal) {
		prepared, err := runtimeprep.PrepareLocal(job, manifest, paths.Workspace)
		if err != nil {
			finishFailed(ctx, store, runID, attemptID, 10, err.Error(), "")
			_ = writeSummary(ctx, store, paths, runID)
			return 10, false, err
		}
		attemptJob = prepared.Job
	}
	if report := adapter.ValidateJob(ctx, attemptJob); !report.Supported {
		reason := "job is not supported"
		if len(report.Reasons) > 0 {
			reason = report.Reasons[0]
		}
		finishFailed(ctx, store, runID, attemptID, 10, reason, "")
		_ = writeSummary(ctx, store, paths, runID)
		return 10, false, fmt.Errorf("%s", reason)
	}
	resumeValue := ""
	if resumeFrom != nil {
		resumeValue = resumeFrom.URI
	}
	runtimeEnv := map[string]string{
		"ORCHESTRATOR_RUN_ID":         runID,
		"ORCHESTRATOR_ATTEMPT_ID":     attemptID,
		"ORCHESTRATOR_CHECKPOINT_DIR": paths.Checkpoints,
		"ORCHESTRATOR_RESUME_FROM":    resumeValue,
		"ORCHESTRATOR_EVENTS_PATH":    paths.EventsJSONL,
	}
	result, err := adapter.Submit(ctx, app.SubmitRequest{
		JobSpec:    attemptJob,
		RunID:      runID,
		AttemptID:  attemptID,
		ResumeFrom: resumeFrom,
		RuntimeEnv: runtimeEnv,
		RunDir:     paths.RunDir,
		OnStarted: func(ref app.ProviderJobRef) error {
			return store.UpdateAttemptProviderRef(ctx, attemptID, ref.ID)
		},
	})
	if err != nil {
		var providerErr *app.ProviderError
		retryable := errors.As(err, &providerErr) && providerErr.Retryable()
		endedAt := time.Now().UTC()
		if finishErr := store.FinishAttempt(ctx, attemptID, app.AttemptStateFailed, result.ExitCode, err.Error(), result.ProviderJobRef, endedAt); finishErr != nil {
			return 1, false, finishErr
		}
		if !retryable {
			if finishErr := store.FinishRun(ctx, runID, app.RunStateFailed, result.ExitCode, err.Error(), endedAt); finishErr != nil {
				return 1, false, finishErr
			}
		}
		return result.ExitCode, retryable, err
	}
	currentRun, err := store.GetRun(ctx, runID)
	if err != nil {
		return 1, false, err
	}
	if currentRun.State == app.RunStateCanceled {
		if err := writeSummary(ctx, store, paths, runID); err != nil {
			return 1, false, err
		}
		fmt.Fprintf(opts.Stdout, "Run %s %s\n", runID, app.RunStateCanceled)
		return 130, false, nil
	}
	endedAt := time.Now().UTC()
	runState, attemptState := app.RunStateSucceeded, app.AttemptStateSucceeded
	if result.ExitCode != 0 {
		runState = app.RunStateFailed
		attemptState = app.AttemptStateFailed
	}
	if err := store.FinishAttempt(ctx, attemptID, attemptState, result.ExitCode, result.ExitReason, result.ProviderJobRef, endedAt); err != nil {
		return 1, false, err
	}
	if err := store.FinishRun(ctx, runID, runState, result.ExitCode, result.ExitReason, endedAt); err != nil {
		return 1, false, err
	}
	if err := writeSummary(ctx, store, paths, runID); err != nil {
		return 1, false, err
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
	return artifact.WriteSummary(paths.Summary, summary.Build(run, attempts, events))
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
			store, err := state.Open(filepath.Join(resolvedHome, "orchestrator.db"))
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
	if err := store.FinishAttempt(ctx, running.ID, app.AttemptStateCanceled, 130, "canceled", running.ProviderRef, endedAt); err != nil {
		return err
	}
	if err := store.FinishRun(ctx, runID, app.RunStateCanceled, 130, "canceled", endedAt); err != nil {
		return err
	}
	if err := writeSummary(ctx, store, paths, runID); err != nil {
		return err
	}
	fmt.Fprintf(opts.Stdout, "Run %s canceled\n", runID)
	return nil
}

func followLogs(ctx context.Context, w io.Writer, home string, runID string, path string) error {
	store, err := state.Open(filepath.Join(home, "orchestrator.db"))
	if err != nil {
		return err
	}
	defer store.Close()

	var offset int64
	for {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			_ = file.Close()
			return err
		}
		written, copyErr := io.Copy(w, file)
		offset += written
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}

		run, err := store.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.State != app.RunStateRunning {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
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
	if flag != "" {
		return flag, nil
	}
	if env := os.Getenv("ORCHESTRATOR_CLI_HOME"); env != "" {
		return env, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".orchestrator-cli"), nil
}

type exitCodeError struct {
	code int
}

func (e exitCodeError) Error() string {
	return fmt.Sprintf("run failed with exit code %d", e.code)
}
