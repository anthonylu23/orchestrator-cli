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
	"github.com/anthonylu23/orchestrator-cli/internal/config"
	"github.com/anthonylu23/orchestrator-cli/internal/data"
	"github.com/anthonylu23/orchestrator-cli/internal/event"
	"github.com/anthonylu23/orchestrator-cli/internal/provider"
	localprovider "github.com/anthonylu23/orchestrator-cli/internal/provider/local"
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
	if resolved.Provider != string(app.ProviderLocal) {
		return 1, fmt.Errorf("provider %q is not implemented yet", resolved.Provider)
	}
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
	_ = manifest

	now := time.Now().UTC()
	runID := app.NewRunID()
	attemptID := app.NewAttemptID()
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
	attempt := app.Attempt{ID: attemptID, RunID: runID, Provider: resolved.Provider, State: app.AttemptStateRunning, StartedAt: now}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		return 1, err
	}

	registry := provider.NewRegistry(localprovider.New(opts.Stdout, opts.Stderr))
	adapter, err := registry.Get(resolved.Provider)
	if err != nil {
		return 1, err
	}
	if report := adapter.ValidateJob(ctx, resolved.Job); !report.Supported {
		reason := "job is not supported"
		if len(report.Reasons) > 0 {
			reason = report.Reasons[0]
		}
		finishFailed(ctx, store, runID, attemptID, 10, reason, "")
		_ = writeSummary(ctx, store, paths, runID)
		return 10, fmt.Errorf("%s", reason)
	}

	runtimeEnv := map[string]string{
		"ORCHESTRATOR_RUN_ID":         runID,
		"ORCHESTRATOR_ATTEMPT_ID":     attemptID,
		"ORCHESTRATOR_CHECKPOINT_DIR": paths.Checkpoints,
		"ORCHESTRATOR_RESUME_FROM":    "",
		"ORCHESTRATOR_EVENTS_PATH":    paths.EventsJSONL,
	}
	result, err := adapter.Submit(ctx, app.SubmitRequest{
		JobSpec:    resolved.Job,
		RunID:      runID,
		AttemptID:  attemptID,
		RuntimeEnv: runtimeEnv,
		RunDir:     paths.RunDir,
	})
	if err != nil {
		finishFailed(ctx, store, runID, attemptID, 1, err.Error(), result.ProviderJobRef)
		_ = writeSummary(ctx, store, paths, runID)
		return 1, err
	}
	endedAt := time.Now().UTC()
	runState, attemptState := app.RunStateSucceeded, app.AttemptStateSucceeded
	if result.ExitCode != 0 {
		runState = app.RunStateFailed
		attemptState = app.AttemptStateFailed
	}
	if err := store.FinishAttempt(ctx, attemptID, attemptState, result.ExitCode, result.ExitReason, result.ProviderJobRef, endedAt); err != nil {
		return 1, err
	}
	if err := store.FinishRun(ctx, runID, runState, result.ExitCode, result.ExitReason, endedAt); err != nil {
		return 1, err
	}
	if err := writeSummary(ctx, store, paths, runID); err != nil {
		return 1, err
	}
	fmt.Fprintf(opts.Stdout, "Run %s %s\n", runID, runState)
	return result.ExitCode, nil
}

func finishFailed(ctx context.Context, store *state.Store, runID string, attemptID string, code int, reason string, providerRef string) {
	endedAt := time.Now().UTC()
	_ = store.FinishAttempt(ctx, attemptID, app.AttemptStateFailed, code, reason, providerRef, endedAt)
	_ = store.FinishRun(ctx, runID, app.RunStateFailed, code, reason, endedAt)
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
			registry := provider.NewRegistry(localprovider.New(opts.Stdout, opts.Stderr))
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
