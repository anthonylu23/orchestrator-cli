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
	"github.com/anthonylu23/switchboard-cli/internal/sizing"
	"github.com/anthonylu23/switchboard-cli/internal/staging"
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
	StagingUploader       staging.Uploader
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

	logFollowPollInterval = 100 * time.Millisecond
)

type planOptions struct {
	AsJSON        bool
	ResumeFromURI string
	ResumeStep    int64
}

type planReport struct {
	PlanRunID         string               `json:"plan_run_id"`
	Provider          string               `json:"provider"`
	Job               app.JobSpec          `json:"job"`
	DataManifest      app.DataManifest     `json:"data_manifest"`
	Staging           planStagingReport    `json:"staging"`
	Packaging         planPackagingReport  `json:"packaging"`
	Routing           *app.RoutingDecision `json:"routing,omitempty"`
	ProviderReport    *planProviderReport  `json:"provider_report,omitempty"`
	Checkpoint        planCheckpointReport `json:"checkpoint"`
	SuppressedActions []string             `json:"suppressed_actions"`
}

type planStagingReport struct {
	WouldUpload     bool            `json:"would_upload"`
	DataURIPrefix   string          `json:"data_uri_prefix,omitempty"`
	PlannedUploads  []string        `json:"planned_uploads,omitempty"`
	ResolvedInputs  []app.DataInput `json:"resolved_inputs,omitempty"`
	NoUploadStarted bool            `json:"no_upload_started"`
}

type planPackagingReport struct {
	WouldPackage     bool     `json:"would_package"`
	Image            string   `json:"image,omitempty"`
	Command          []string `json:"command,omitempty"`
	NoBuildOrPushRun bool     `json:"no_build_or_push_run"`
}

type planProviderReport struct {
	Name         string                   `json:"name"`
	Supported    bool                     `json:"supported"`
	Reasons      []string                 `json:"reasons,omitempty"`
	Capabilities app.ProviderCapabilities `json:"capabilities"`
	Estimate     app.CostEstimate         `json:"estimate"`
}

type planCheckpointReport struct {
	ResumeFromURI       string   `json:"resume_from_uri,omitempty"`
	Scheme              string   `json:"scheme,omitempty"`
	SupportedSchemes    []string `json:"supported_schemes,omitempty"`
	SupportedBySelected bool     `json:"supported_by_selected"`
	Reason              string   `json:"reason,omitempty"`
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
		Use:           "switchboard-cli",
		Short:         "Local-first ML job orchestration",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&home, "home", "", "Switchboard home directory")
	root.AddCommand(newTrainCommand(opts, &home))
	root.AddCommand(newPlanCommand(opts, &home))
	root.AddCommand(newResumeCommand(opts, &home))
	root.AddCommand(newStatusCommand(opts, &home))
	root.AddCommand(newLogsCommand(opts, &home))
	root.AddCommand(newCancelCommand(opts, &home))
	root.AddCommand(newProvidersCommand(opts))
	root.AddCommand(newResourcesCommand(opts, &home))
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

func newPlanCommand(opts Options, home *string) *cobra.Command {
	var flags config.TrainFlags
	var planFlags planOptions
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Preview provider routing, packaging, staging, and cost without submitting",
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.SwitchboardHome = *home
			resolved, err := config.LoadTrain(flags)
			if err != nil {
				return err
			}
			code, err := runPlan(cmd.Context(), opts, resolved, planFlags)
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
	cmd.Flags().StringVar(&flags.Provider, "provider", "local", "Provider to plan")
	cmd.Flags().StringVar(&flags.Script, "script", "", "Training script path")
	cmd.Flags().StringArrayVar(&flags.Args, "arg", nil, "Argument to pass to the script; repeat for multiple args")
	cmd.Flags().BoolVar(&flags.AllowLargeDataBundle, "allow-large-data-bundle", false, "Allow local data bundles above configured limit")
	cmd.Flags().BoolVar(&planFlags.AsJSON, "json", false, "Print JSON")
	cmd.Flags().StringVar(&planFlags.ResumeFromURI, "resume-from", "", "Checkpoint URI to include in compatibility planning")
	cmd.Flags().Int64Var(&planFlags.ResumeStep, "resume-step", 0, "Checkpoint step for --resume-from")
	return cmd
}

func newResumeCommand(opts Options, home *string) *cobra.Command {
	var flags config.TrainFlags
	cmd := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "Resume a run from its latest checkpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.SwitchboardHome = *home
			resolved, err := config.LoadTrain(flags)
			if err != nil {
				return err
			}
			code, err := runResume(cmd.Context(), opts, args[0], resolved)
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
	cmd.Flags().StringVar(&flags.Provider, "provider", "auto", "Provider to use for the resumed attempt")
	cmd.Flags().StringVar(&flags.Script, "script", "", "Training script path")
	cmd.Flags().StringArrayVar(&flags.Args, "arg", nil, "Argument to pass to the script; repeat for multiple args")
	cmd.Flags().BoolVar(&flags.AllowLargeDataBundle, "allow-large-data-bundle", false, "Allow local data bundles above configured limit")
	return cmd
}

func runTrain(ctx context.Context, opts Options, resolved config.ResolvedTrainConfig) (int, error) {
	if err := artifact.EnsureHome(resolved.SwitchboardHome); err != nil {
		return exitCodeInternal, err
	}
	var err error
	resolved, err = applySizingProbe(ctx, opts, resolved)
	if err != nil {
		return exitCodeInvalidSpec, err
	}
	credentialResolver, err := credentialResolverForTrain(opts, resolved)
	if err != nil {
		return exitCodeInvalidSpec, err
	}

	runID := app.NewRunID()
	job, manifest, originalScript, err := prepareRunJob(ctx, opts, resolved, runID)
	if err != nil {
		return exitCodeInvalidSpec, err
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

	run := app.Run{ID: runID, JobName: resolved.Job.Name, Script: originalScript, Image: job.Image, Provider: resolved.Provider, State: app.RunStateRunning, StartedAt: time.Now().UTC()}
	if err := store.CreateRun(ctx, run); err != nil {
		return exitCodeInternal, err
	}
	return runAttempts(ctx, opts, resolved, credentialResolver, store, paths, runID, job, manifest, nil)
}

func runResume(ctx context.Context, opts Options, runID string, resolved config.ResolvedTrainConfig) (int, error) {
	if err := artifact.ValidateRunID(runID); err != nil {
		return exitCodeInvalidSpec, err
	}
	if err := artifact.EnsureHome(resolved.SwitchboardHome); err != nil {
		return exitCodeInternal, err
	}
	var err error
	resolved, err = applySizingProbe(ctx, opts, resolved)
	if err != nil {
		return exitCodeInvalidSpec, err
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
	if _, err := store.GetRun(ctx, runID); err != nil {
		return exitCodeInvalidSpec, fmt.Errorf("run %q not found: %w", runID, err)
	}
	resumeFrom, err := (checkpoint.Resolver{Home: resolved.SwitchboardHome}).Latest(ctx, runID)
	if err != nil {
		return exitCodeMissingResume, err
	}
	if resumeFrom == nil {
		return exitCodeMissingResume, fmt.Errorf("run %s has no checkpoint events to resume from", runID)
	}
	credentialResolver, err := credentialResolverForTrain(opts, resolved)
	if err != nil {
		return exitCodeInvalidSpec, err
	}
	job, manifest, originalScript, err := prepareRunJob(ctx, opts, resolved, runID)
	if err != nil {
		return exitCodeInvalidSpec, err
	}
	run := app.Run{ID: runID, JobName: resolved.Job.Name, Script: originalScript, Image: job.Image, Provider: resolved.Provider, State: app.RunStateRunning, StartedAt: time.Now().UTC()}
	if err := store.RestartRun(ctx, run); err != nil {
		return exitCodeInternal, err
	}
	fmt.Fprintf(opts.Stdout, "Found checkpoint: step %d\n", resumeFrom.Step)
	return runAttempts(ctx, opts, resolved, credentialResolver, store, paths, runID, job, manifest, resumeFrom)
}

func runPlan(ctx context.Context, opts Options, resolved config.ResolvedTrainConfig, planFlags planOptions) (int, error) {
	runID := app.NewRunID()
	resumeFrom := checkpointFromPlanFlags(planFlags)
	report := planReport{
		PlanRunID: runID,
		Provider:  resolved.Provider,
		SuppressedActions: []string{
			"provider submit",
			"docker build",
			"docker push",
			"managed data upload",
			"run state/artifact writes",
		},
	}
	job, manifest, stagingReport, packagingReport, err := preparePlanJob(ctx, resolved, runID)
	if err != nil {
		report.Job = resolved.Job
		renderPlanReport(opts, report, planFlags.AsJSON)
		return exitCodeInvalidSpec, err
	}
	report.Job = job
	report.DataManifest = manifest
	report.Staging = stagingReport
	report.Packaging = packagingReport
	registry := buildPlanProviderRegistry(opts, resolved)
	if resolved.Provider == string(app.ProviderAuto) {
		decision, err := routing.Select(ctx, registry, job, routingOptions(resolved, nil, resumeFrom))
		decision.RunID = runID
		report.Routing = &decision
		if decision.SelectedProvider != "" {
			report.Checkpoint = checkpointCompatibilityFromDecision(ctx, registry, decision.SelectedProvider, resumeFrom)
		} else {
			report.Checkpoint = checkpointCompatibilityForResume(resumeFrom)
		}
		renderPlanReport(opts, report, planFlags.AsJSON)
		if err != nil {
			return exitCodeRouting, err
		}
		return 0, nil
	}
	providerReport, checkpointReport, err := planExplicitProvider(ctx, registry, resolved.Provider, job, resumeFrom)
	report.ProviderReport = &providerReport
	report.Checkpoint = checkpointReport
	renderPlanReport(opts, report, planFlags.AsJSON)
	if err != nil {
		return exitCodeRouting, err
	}
	if !providerReport.Supported {
		reason := "job is not supported"
		if len(providerReport.Reasons) > 0 {
			reason = providerReport.Reasons[0]
		}
		return exitCodeInvalidSpec, fmt.Errorf("%s", reason)
	}
	return 0, nil
}

func checkpointFromPlanFlags(planFlags planOptions) *app.CheckpointRef {
	if strings.TrimSpace(planFlags.ResumeFromURI) == "" {
		return nil
	}
	step := planFlags.ResumeStep
	return &app.CheckpointRef{URI: strings.TrimSpace(planFlags.ResumeFromURI), Step: step}
}

func prepareRunJob(ctx context.Context, opts Options, resolved config.ResolvedTrainConfig, runID string) (app.JobSpec, app.DataManifest, string, error) {
	manifest, err := data.Prepare(resolved.Job, data.PreflightOptions{
		BundleSizeLimitBytes: resolved.BundleMaxSizeBytes,
		RequireOverride:      resolved.RequireOverrideAboveLimit,
		AllowLargeBundle:     resolved.AllowLargeDataBundle,
	})
	if err != nil {
		return app.JobSpec{}, app.DataManifest{}, "", err
	}
	job := resolved.Job
	job.Data = append([]app.DataInput(nil), manifest.Inputs...)
	if shouldStageBundledInputs(resolved, manifest) {
		if err := validateStagingDataPrefixForProvider(resolved); err != nil {
			return app.JobSpec{}, app.DataManifest{}, "", err
		}
		staged, err := staging.StageBundledInputs(ctx, resolved.Staging, runID, job, manifest, opts.StagingUploader)
		if err != nil {
			return app.JobSpec{}, app.DataManifest{}, "", err
		}
		job = staged.Job
		manifest = staged.Manifest
		for _, uri := range staged.UploadedObjects {
			fmt.Fprintf(opts.Stdout, "Staged data: %s\n", uri)
		}
	}
	originalScript := job.Script
	if shouldPackageForProvider(resolved, job) {
		packaged, err := packageJob(ctx, opts, resolved, job, runID)
		if err != nil {
			return app.JobSpec{}, app.DataManifest{}, "", err
		}
		job = packaged
	}
	return job, manifest, originalScript, nil
}

func preparePlanJob(ctx context.Context, resolved config.ResolvedTrainConfig, runID string) (app.JobSpec, app.DataManifest, planStagingReport, planPackagingReport, error) {
	manifest, err := data.Prepare(resolved.Job, data.PreflightOptions{
		BundleSizeLimitBytes: resolved.BundleMaxSizeBytes,
		RequireOverride:      resolved.RequireOverrideAboveLimit,
		AllowLargeBundle:     resolved.AllowLargeDataBundle,
	})
	if err != nil {
		return app.JobSpec{}, app.DataManifest{}, planStagingReport{}, planPackagingReport{}, err
	}
	job := resolved.Job
	job.Data = append([]app.DataInput(nil), manifest.Inputs...)
	stagingReport := planStagingReport{
		DataURIPrefix:   resolved.Staging.DataURIPrefix,
		ResolvedInputs:  append([]app.DataInput(nil), job.Data...),
		NoUploadStarted: true,
	}
	if shouldStageBundledInputs(resolved, manifest) {
		if err := validateStagingDataPrefixForProvider(resolved); err != nil {
			return app.JobSpec{}, app.DataManifest{}, planStagingReport{}, planPackagingReport{}, err
		}
		uploader := &planningUploader{}
		staged, err := staging.StageBundledInputs(ctx, resolved.Staging, runID, job, manifest, uploader)
		if err != nil {
			return app.JobSpec{}, app.DataManifest{}, planStagingReport{}, planPackagingReport{}, err
		}
		job = staged.Job
		manifest = staged.Manifest
		stagingReport.WouldUpload = len(uploader.destinations) > 0
		stagingReport.PlannedUploads = append([]string(nil), uploader.destinations...)
		stagingReport.ResolvedInputs = append([]app.DataInput(nil), job.Data...)
	}
	packagingReport := planPackagingReport{NoBuildOrPushRun: true}
	if shouldPackageForProvider(resolved, job) {
		packaged, err := planPackagedJob(resolved, job, runID)
		if err != nil {
			return app.JobSpec{}, app.DataManifest{}, planStagingReport{}, planPackagingReport{}, err
		}
		job = packaged
		packagingReport.WouldPackage = true
		packagingReport.Image = job.Image
		packagingReport.Command = append([]string(nil), job.Command...)
	}
	return job, manifest, stagingReport, packagingReport, nil
}

type planningUploader struct {
	destinations []string
}

func (u *planningUploader) UploadFile(ctx context.Context, sourcePath string, destinationURI string) error {
	u.destinations = append(u.destinations, destinationURI)
	return nil
}

func planPackagedJob(resolved config.ResolvedTrainConfig, job app.JobSpec, runID string) (app.JobSpec, error) {
	image := resolved.Packaging.Image
	var err error
	if image == "" {
		image, err = packaging.ArtifactRegistryImage(resolved.GCP.Location, resolved.GCP.ProjectID, resolved.GCP.ArtifactRegistryRepository, runID)
		if err != nil {
			return app.JobSpec{}, err
		}
	}
	packaged := job
	packaged.Image = image
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

func applySizingProbe(ctx context.Context, opts Options, resolved config.ResolvedTrainConfig) (config.ResolvedTrainConfig, error) {
	if len(resolved.Sizing.Probe.Command) == 0 && resolved.Sizing.Probe.Output == "" {
		return resolved, nil
	}
	profile, err := sizing.RunProbe(ctx, resolved.Sizing.Probe, opts.Stdout, opts.Stderr)
	if err != nil {
		return config.ResolvedTrainConfig{}, err
	}
	resolved.Sizing.Hints = sizing.ApplyProfile(resolved.Sizing.Hints, profile)
	if profile.RequiredVRAMGB > 0 || profile.PeakVRAMGB > 0 {
		fmt.Fprintf(opts.Stdout, "Loaded sizing profile: required_vram_gb=%.1f\n", resolved.Sizing.Hints.RequiredVRAMGB)
	} else {
		fmt.Fprintln(opts.Stdout, "Loaded sizing profile")
	}
	return resolved, nil
}

func runAttempts(ctx context.Context, opts Options, resolved config.ResolvedTrainConfig, credentialResolver credentials.Resolver, store *state.Store, paths artifact.Paths, runID string, job app.JobSpec, manifest app.DataManifest, initialResumeFrom *app.CheckpointRef) (int, error) {
	runRedactor := redact.FromEnvironment(job.Env)
	registry := buildTrainProviderRegistry(opts, resolved, credentialResolver)
	maxAttempts := resolved.Routing.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	excluded := map[string]bool{}
	resumeFrom := initialResumeFrom
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
		} else if resumeFrom != nil {
			if err := ensureProviderSupportsCheckpoint(ctx, registry, selectedProvider, resumeFrom); err != nil {
				reason := runRedactor.String(err.Error())
				finishRunOnly(ctx, store, runID, exitCodeMissingResume, reason)
				_ = writeSummary(ctx, store, paths, runID)
				return exitCodeMissingResume, err
			}
		}
		code, retryable, err := runAttempt(ctx, opts, store, registry, paths, runID, selectedProvider, job, manifest, resolved.Staging, resumeFrom, selectedHardware)
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

func ensureProviderSupportsCheckpoint(ctx context.Context, registry *provider.Registry, selectedProvider string, resumeFrom *app.CheckpointRef) error {
	adapter, err := registry.Get(selectedProvider)
	if err != nil {
		return err
	}
	capabilities, err := adapter.Capabilities(ctx)
	if err != nil {
		return err
	}
	scheme := checkpointScheme(resumeFrom.URI)
	for _, supported := range capabilities.SupportedCheckpointSchemes {
		if strings.EqualFold(supported, scheme) {
			return nil
		}
	}
	return fmt.Errorf("provider %s does not support %q checkpoint resume URI %q", selectedProvider, scheme, resumeFrom.URI)
}

func planExplicitProvider(ctx context.Context, registry *provider.Registry, providerName string, job app.JobSpec, resumeFrom *app.CheckpointRef) (planProviderReport, planCheckpointReport, error) {
	adapter, err := registry.Get(providerName)
	if err != nil {
		return planProviderReport{Name: providerName}, checkpointCompatibilityForResume(resumeFrom), err
	}
	capabilities, capErr := adapter.Capabilities(ctx)
	report := planProviderReport{Name: string(adapter.Name()), Capabilities: capabilities}
	checkpointReport := checkpointCompatibility(adapter.Name(), capabilities, resumeFrom)
	if capErr != nil {
		report.Reasons = []string{capErr.Error()}
		return report, checkpointReport, capErr
	}
	support := adapter.ValidateJob(ctx, job)
	report.Supported = support.Supported
	report.Reasons = append([]string(nil), support.Reasons...)
	if resumeFrom != nil && !checkpointReport.SupportedBySelected {
		report.Supported = false
		report.Reasons = append(report.Reasons, checkpointReport.Reason)
		return report, checkpointReport, fmt.Errorf("%s", checkpointReport.Reason)
	}
	estimate, err := adapter.Estimate(ctx, job)
	if err != nil {
		report.Reasons = append(report.Reasons, err.Error())
		return report, checkpointReport, err
	}
	report.Estimate = estimate
	return report, checkpointReport, nil
}

func checkpointCompatibilityFromDecision(ctx context.Context, registry *provider.Registry, selectedProvider string, resumeFrom *app.CheckpointRef) planCheckpointReport {
	if resumeFrom == nil {
		return checkpointCompatibilityForResume(nil)
	}
	adapter, err := registry.Get(selectedProvider)
	if err != nil {
		return planCheckpointReport{ResumeFromURI: resumeFrom.URI, Scheme: checkpointScheme(resumeFrom.URI), Reason: err.Error()}
	}
	capabilities, err := adapter.Capabilities(ctx)
	if err != nil {
		return planCheckpointReport{ResumeFromURI: resumeFrom.URI, Scheme: checkpointScheme(resumeFrom.URI), Reason: err.Error()}
	}
	return checkpointCompatibility(adapter.Name(), capabilities, resumeFrom)
}

func checkpointCompatibility(providerName app.ProviderName, capabilities app.ProviderCapabilities, resumeFrom *app.CheckpointRef) planCheckpointReport {
	if resumeFrom == nil {
		return planCheckpointReport{SupportedSchemes: append([]string(nil), capabilities.SupportedCheckpointSchemes...)}
	}
	scheme := checkpointScheme(resumeFrom.URI)
	report := planCheckpointReport{
		ResumeFromURI:    resumeFrom.URI,
		Scheme:           scheme,
		SupportedSchemes: append([]string(nil), capabilities.SupportedCheckpointSchemes...),
	}
	for _, supported := range capabilities.SupportedCheckpointSchemes {
		if strings.EqualFold(supported, scheme) {
			report.SupportedBySelected = true
			return report
		}
	}
	report.Reason = fmt.Sprintf("provider %s does not support %q checkpoint resume URI %q", providerName, scheme, resumeFrom.URI)
	return report
}

func checkpointCompatibilityForResume(resumeFrom *app.CheckpointRef) planCheckpointReport {
	if resumeFrom == nil {
		return planCheckpointReport{}
	}
	return planCheckpointReport{ResumeFromURI: resumeFrom.URI, Scheme: checkpointScheme(resumeFrom.URI)}
}

func checkpointScheme(uri string) string {
	if before, _, ok := strings.Cut(uri, "://"); ok && before != "" {
		return strings.ToLower(before)
	}
	return "file"
}

func routingOptions(resolved config.ResolvedTrainConfig, excluded map[string]bool, resumeFrom *app.CheckpointRef) routing.Options {
	return routing.Options{
		Mode:                resolved.Routing.Mode,
		Objective:           resolved.Routing.Objective,
		Exclude:             excluded,
		BudgetMaxRunCostUSD: resolved.Routing.Budget.MaxRunCostUSD,
		Sizing: routing.SizingHints{
			RequiredVRAMGB:            resolved.Sizing.Hints.RequiredVRAMGB,
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

func shouldStageBundledInputs(resolved config.ResolvedTrainConfig, manifest app.DataManifest) bool {
	if resolved.Staging.DataURIPrefix == "" || resolved.Provider == string(app.ProviderLocal) {
		return false
	}
	for _, input := range manifest.Inputs {
		if input.Mode == app.DataInputModeBundle {
			return true
		}
	}
	return false
}

func validateStagingDataPrefixForProvider(resolved config.ResolvedTrainConfig) error {
	if resolved.Provider == string(app.ProviderAuto) {
		return nil
	}
	scheme := objectStoreScheme(resolved.Staging.DataURIPrefix)
	switch resolved.Provider {
	case gcpprovider.ProviderName:
		if scheme != "gs" {
			return fmt.Errorf("provider gcp cannot read staged data prefix scheme %q; use a gs:// staging.data_uri_prefix", scheme)
		}
	case lambdaprovider.ProviderName:
		if scheme != "s3" && scheme != "gs" {
			return fmt.Errorf("provider lambda cannot read staged data prefix scheme %q; use an s3:// or gs:// staging.data_uri_prefix", scheme)
		}
	}
	return nil
}

func objectStoreScheme(value string) string {
	before, _, ok := strings.Cut(value, "://")
	if !ok {
		return ""
	}
	return strings.ToLower(before)
}

func shouldPackageForProvider(resolved config.ResolvedTrainConfig, job app.JobSpec) bool {
	if job.Image != "" || job.Script == "" {
		return false
	}
	if resolved.Provider == string(app.ProviderLocal) {
		return false
	}
	if resolved.Packaging.Image != "" {
		return true
	}
	return resolved.Provider == gcpprovider.ProviderName && (resolved.Packaging.Dockerfile != "" || resolved.GCP.ArtifactRegistryRepository != "")
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

func runAttempt(ctx context.Context, opts Options, store *state.Store, registry *provider.Registry, paths artifact.Paths, runID string, selectedProvider string, job app.JobSpec, manifest app.DataManifest, stagingConfig config.StagingConfig, resumeFrom *app.CheckpointRef, selectedHardware *app.HardwareSelection) (int, bool, error) {
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
	runtimeEnv := runtimeEnvForProvider(selectedProvider, runID, attemptID, resumeValue, paths, stagingConfig, attemptJob.Data)
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
	resourceIDs := map[string]string{}
	persistResource := func(resource app.ProviderResource) error {
		return persistProviderResource(ctx, store, resourceIDs, attemptRedactor, resource)
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
		OnResourceCreated: persistResource,
		OnResourceUpdated: persistResource,
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

func persistProviderResource(ctx context.Context, store *state.Store, resourceIDs map[string]string, redactor redact.Redactor, resource app.ProviderResource) error {
	resource = redactProviderResource(resource, redactor)
	key := providerResourceKey(resource)
	if key != "" {
		if id := resourceIDs[key]; id != "" {
			resource.ID = id
		}
	}
	saved, err := store.SaveProviderResource(ctx, resource)
	if err != nil {
		return err
	}
	if key != "" {
		resourceIDs[key] = saved.ID
	}
	return nil
}

func redactProviderResource(resource app.ProviderResource, redactor redact.Redactor) app.ProviderResource {
	resource.ExternalID = redactor.String(resource.ExternalID)
	resource.ProviderRef = redactor.String(resource.ProviderRef)
	resource.ProjectOrAccount = redactor.String(resource.ProjectOrAccount)
	if len(resource.Metadata) > 0 {
		metadata := make(map[string]string, len(resource.Metadata))
		for key, value := range resource.Metadata {
			metadata[key] = redactor.String(value)
		}
		resource.Metadata = metadata
	}
	return resource
}

func providerResourceKey(resource app.ProviderResource) string {
	externalID := resource.ExternalID
	if externalID == "" {
		externalID = resource.ProviderRef
	}
	if resource.Provider == "" || resource.Kind == "" || externalID == "" {
		return ""
	}
	return resource.Provider + "|" + string(resource.Kind) + "|" + externalID
}

func runtimeEnvForProvider(selectedProvider string, runID string, attemptID string, resumeValue string, paths artifact.Paths, stagingConfig config.StagingConfig, inputs []app.DataInput) map[string]string {
	checkpointDir := paths.Checkpoints
	eventsPath := paths.EventsJSONL
	if selectedProvider == gcpprovider.ProviderName || selectedProvider == lambdaprovider.ProviderName || isChinaCloudProvider(selectedProvider) {
		checkpointDir = "/tmp/switchboard/checkpoints"
		eventsPath = "/tmp/switchboard/events.jsonl"
	}
	env := map[string]string{
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
	for key, value := range staging.RuntimeEnv(stagingConfig, runID, inputs) {
		if selectedProvider == gcpprovider.ProviderName && (key == "SWITCHBOARD_CHECKPOINT_URI_PREFIX" || key == "ORCHESTRATOR_CHECKPOINT_URI_PREFIX") {
			continue
		}
		env[key] = value
	}
	return env
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
			if err := artifact.EnsureHome(resolvedHome); err != nil {
				return err
			}
			if err := artifact.ValidateRunID(args[0]); err != nil {
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
			if err := artifact.ValidateRunID(args[0]); err != nil {
				return err
			}
			path := artifact.ForRun(resolvedHome, args[0]).Logs
			if follow {
				return followLogs(cmd.Context(), opts.Stdout, resolvedHome, args[0], path)
			}
			return artifact.StreamLogs(opts.Stdout, artifact.ForRun(resolvedHome, args[0]))
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
	if err := artifact.ValidateRunID(runID); err != nil {
		return err
	}
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
	adapter, err := cancelAdapterForAttempt(ctx, opts, home, store, runID, *running)
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

func cancelAdapterForAttempt(ctx context.Context, opts Options, resolvedHome string, store *state.Store, runID string, attempt app.Attempt) (app.ProviderAdapter, error) {
	resources, err := store.ProviderResources(ctx, state.ProviderResourceFilter{RunID: runID, Provider: attempt.Provider})
	if err == nil {
		var resolver *credentials.Resolver
		for _, resource := range resources {
			if resource.ProviderRef == attempt.ProviderRef || resource.ExternalID != "" {
				return cleanupAdapterForResource(opts, resolvedHome, resource, &resolver)
			}
		}
	}
	resolver := credentials.Resolver{}
	if attempt.Provider == lambdaprovider.ProviderName || isChinaCloudProvider(attempt.Provider) {
		optionalResolver, err := optionalCredentialResolverAtHome(opts, resolvedHome)
		if err != nil {
			return nil, err
		}
		resolver = optionalResolver
	}
	registry := buildProviderRegistryWithOptions(opts, config.MockConfig{}, config.GCPConfig{}, config.LambdaConfig{}, config.ChinaCloudConfig{}, resolver, true, true)
	return registry.Get(attempt.Provider)
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
			return drainFinalLogAfterTerminal(ctx, w, path, offset)
		}

		select {
		case <-ctx.Done():
			if _, err := copyLogFromOffset(w, path, offset); err != nil {
				return err
			}
			return ctx.Err()
		case <-time.After(logFollowPollInterval):
		}
	}
}

func drainFinalLogAfterTerminal(ctx context.Context, w io.Writer, path string, offset int64) error {
	for {
		select {
		case <-ctx.Done():
			if _, err := copyLogFromOffset(w, path, offset); err != nil {
				return err
			}
			return ctx.Err()
		case <-time.After(logFollowPollInterval):
			nextOffset, err := copyLogFromOffset(w, path, offset)
			if err != nil {
				return err
			}
			if nextOffset == offset {
				return nil
			}
			offset = nextOffset
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

func newResourcesCommand(opts Options, home *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resources",
		Short: "Inspect and clean up provider resources",
	}

	var listRunID, listProvider string
	var listJSON, listActive bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List tracked provider resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedHome, err := resolveHome(*home)
			if err != nil {
				return err
			}
			if err := artifact.EnsureHome(resolvedHome); err != nil {
				return err
			}
			store, err := state.Open(artifact.DBPath(resolvedHome))
			if err != nil {
				return err
			}
			defer store.Close()
			resources, err := store.ProviderResources(cmd.Context(), state.ProviderResourceFilter{RunID: listRunID, Provider: listProvider})
			if err != nil {
				return err
			}
			if listActive {
				resources = filterActiveResources(resources)
			}
			if listJSON {
				return json.NewEncoder(opts.Stdout).Encode(resources)
			}
			for _, resource := range resources {
				fmt.Fprintf(opts.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					resource.ID, resource.Provider, resource.Kind, resource.State, resource.CleanupPolicy, resource.RunID, resource.ProviderRef)
			}
			return nil
		},
	}
	list.Flags().StringVar(&listRunID, "run", "", "Filter resources by run ID")
	list.Flags().StringVar(&listProvider, "provider", "", "Filter resources by provider")
	list.Flags().BoolVar(&listActive, "active", false, "Only show active resources")
	list.Flags().BoolVar(&listJSON, "json", false, "Print JSON")

	var refreshRunID, refreshProvider string
	var refreshJSON bool
	refresh := &cobra.Command{
		Use:   "refresh",
		Short: "Refresh tracked provider resource states",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedHome, err := resolveHome(*home)
			if err != nil {
				return err
			}
			if err := artifact.EnsureHome(resolvedHome); err != nil {
				return err
			}
			store, err := state.Open(artifact.DBPath(resolvedHome))
			if err != nil {
				return err
			}
			defer store.Close()
			resources, err := store.ProviderResources(cmd.Context(), state.ProviderResourceFilter{RunID: refreshRunID, Provider: refreshProvider})
			if err != nil {
				return err
			}
			var lambdaResolver *credentials.Resolver
			refreshed := make([]app.ProviderResource, 0, len(resources))
			for _, resource := range resources {
				if !resourceRefreshSupported(resource) {
					continue
				}
				adapter, err := refreshAdapterForResource(opts, resolvedHome, resource, &lambdaResolver)
				if err != nil {
					return err
				}
				status, err := adapter.GetStatus(cmd.Context(), app.ProviderJobRef{ID: resource.ProviderRef})
				if err != nil {
					return err
				}
				now := time.Now().UTC()
				resource.State = providerResourceStateFromStatus(status)
				resource.UpdatedAt = now
				resource.LastObservedAt = &now
				resource.Metadata = mergeResourceMetadata(resource.Metadata, map[string]string{"refreshed_at": now.Format(time.RFC3339Nano)})
				saved, err := store.SaveProviderResource(cmd.Context(), resource)
				if err != nil {
					return err
				}
				refreshed = append(refreshed, saved)
				if !refreshJSON {
					fmt.Fprintf(opts.Stdout, "refreshed\t%s\t%s\t%s\t%s\n", saved.ID, saved.Provider, saved.State, saved.ProviderRef)
				}
			}
			if refreshJSON {
				return json.NewEncoder(opts.Stdout).Encode(refreshed)
			}
			if len(refreshed) == 0 {
				fmt.Fprintln(opts.Stdout, "No refreshable provider resources found")
			}
			return nil
		},
	}
	refresh.Flags().StringVar(&refreshRunID, "run", "", "Refresh resources for one run ID")
	refresh.Flags().StringVar(&refreshProvider, "provider", "", "Refresh resources for one provider")
	refresh.Flags().BoolVar(&refreshJSON, "json", false, "Print refreshed resources as JSON")

	var cleanupRunID, cleanupProvider string
	var dryRun bool
	cleanup := &cobra.Command{
		Use:   "cleanup",
		Short: "Request cleanup for tracked provider resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedHome, err := resolveHome(*home)
			if err != nil {
				return err
			}
			if err := artifact.EnsureHome(resolvedHome); err != nil {
				return err
			}
			store, err := state.Open(artifact.DBPath(resolvedHome))
			if err != nil {
				return err
			}
			defer store.Close()
			resources, err := store.ProviderResources(cmd.Context(), state.ProviderResourceFilter{RunID: cleanupRunID, Provider: cleanupProvider})
			if err != nil {
				return err
			}
			var lambdaResolver *credentials.Resolver
			cleaned := 0
			for _, resource := range resources {
				if !resourceCleanupEligible(resource) {
					continue
				}
				if dryRun {
					fmt.Fprintf(opts.Stdout, "would cleanup\t%s\t%s\t%s\n", resource.ID, resource.Provider, resource.ProviderRef)
					cleaned++
					continue
				}
				adapter, err := cleanupAdapterForResource(opts, resolvedHome, resource, &lambdaResolver)
				if err != nil {
					return err
				}
				if err := adapter.Cancel(cmd.Context(), app.ProviderJobRef{ID: resource.ProviderRef}); err != nil {
					return err
				}
				now := time.Now().UTC()
				resource.State = cleanupRequestedState(resource)
				resource.UpdatedAt = now
				resource.LastObservedAt = &now
				resource.Metadata = mergeResourceMetadata(resource.Metadata, map[string]string{"cleanup_requested_at": now.Format(time.RFC3339Nano)})
				if _, err := store.SaveProviderResource(cmd.Context(), resource); err != nil {
					return err
				}
				fmt.Fprintf(opts.Stdout, "cleanup requested\t%s\t%s\t%s\n", resource.ID, resource.Provider, resource.ProviderRef)
				cleaned++
			}
			if cleaned == 0 {
				fmt.Fprintln(opts.Stdout, "No eligible provider resources found")
			}
			return nil
		},
	}
	cleanup.Flags().StringVar(&cleanupRunID, "run", "", "Clean resources for one run ID")
	cleanup.Flags().StringVar(&cleanupProvider, "provider", "", "Clean resources for one provider")
	cleanup.Flags().BoolVar(&dryRun, "dry-run", false, "Show resources that would be cleaned")

	cmd.AddCommand(list, refresh, cleanup)
	return cmd
}

func filterActiveResources(resources []app.ProviderResource) []app.ProviderResource {
	filtered := make([]app.ProviderResource, 0, len(resources))
	for _, resource := range resources {
		if providerResourceStateActive(resource.State) {
			filtered = append(filtered, resource)
		}
	}
	return filtered
}

func resourceCleanupEligible(resource app.ProviderResource) bool {
	if !resource.CreatedBySwitchboard || resource.ProviderRef == "" || !providerResourceStateActive(resource.State) {
		return false
	}
	return resource.CleanupPolicy != app.ProviderResourceCleanupNever
}

func providerResourceStateActive(state app.ProviderResourceState) bool {
	switch state {
	case app.ProviderResourceStateCreating, app.ProviderResourceStateBooting, app.ProviderResourceStateRunning, app.ProviderResourceStateUnknown:
		return true
	default:
		return false
	}
}

func resourceRefreshSupported(resource app.ProviderResource) bool {
	return resource.ProviderRef != "" && (resource.Provider == gcpprovider.ProviderName || resource.Provider == lambdaprovider.ProviderName)
}

func refreshAdapterForResource(opts Options, resolvedHome string, resource app.ProviderResource, lambdaResolver **credentials.Resolver) (app.ProviderAdapter, error) {
	switch resource.Provider {
	case gcpprovider.ProviderName, lambdaprovider.ProviderName:
		return cleanupAdapterForResource(opts, resolvedHome, resource, lambdaResolver)
	default:
		return nil, fmt.Errorf("resource refresh is not supported for provider %q", resource.Provider)
	}
}

func providerResourceStateFromAttempt(state app.AttemptState) app.ProviderResourceState {
	switch state {
	case app.AttemptStateRunning:
		return app.ProviderResourceStateRunning
	case app.AttemptStateSucceeded:
		return app.ProviderResourceStateSucceeded
	case app.AttemptStateFailed:
		return app.ProviderResourceStateFailed
	case app.AttemptStateCanceled:
		return app.ProviderResourceStateCanceled
	default:
		return app.ProviderResourceStateUnknown
	}
}

func providerResourceStateFromStatus(status app.ProviderJobStatus) app.ProviderResourceState {
	if status.ResourceState != "" {
		return status.ResourceState
	}
	return providerResourceStateFromAttempt(status.State)
}

func cleanupAdapterForResource(opts Options, resolvedHome string, resource app.ProviderResource, lambdaResolver **credentials.Resolver) (app.ProviderAdapter, error) {
	switch resource.Provider {
	case gcpprovider.ProviderName:
		cfg := config.GCPConfig{ProjectID: resource.ProjectOrAccount, Location: resource.Region}
		if opts.GCPProviderFactory != nil {
			return opts.GCPProviderFactory(cfg, opts.Stdout, opts.Stderr), nil
		}
		return gcpprovider.New(gcpConfigFromConfig(cfg), opts.Stdout, opts.Stderr), nil
	case lambdaprovider.ProviderName:
		cfg := config.LambdaConfig{RegionName: resource.Region}
		if opts.LambdaProviderFactory != nil {
			return opts.LambdaProviderFactory(cfg, opts.Stdout, opts.Stderr), nil
		}
		if *lambdaResolver == nil {
			resolver, err := optionalCredentialResolverAtHome(opts, resolvedHome)
			if err != nil {
				return nil, err
			}
			*lambdaResolver = &resolver
		}
		return lambdaprovider.New(lambdaConfigFromConfig(cfg, **lambdaResolver), opts.Stdout, opts.Stderr), nil
	default:
		if def, err := chinacloudprovider.DefinitionFor(resource.Provider); err == nil {
			if *lambdaResolver == nil {
				resolver, err := optionalCredentialResolverAtHome(opts, resolvedHome)
				if err != nil {
					return nil, err
				}
				*lambdaResolver = &resolver
			}
			cfg := chinacloudprovider.VMProviderConfig{
				Region:           resource.Region,
				ProjectOrAccount: resource.ProjectOrAccount,
				Credentials:      **lambdaResolver,
			}
			return chinacloudprovider.NewVMProvider(def, cfg, opts.Stdout, opts.Stderr), nil
		}
		return nil, fmt.Errorf("resource cleanup is not supported for provider %q", resource.Provider)
	}
}

func cleanupRequestedState(resource app.ProviderResource) app.ProviderResourceState {
	switch resource.Provider {
	case lambdaprovider.ProviderName:
		return app.ProviderResourceStateTerminating
	case gcpprovider.ProviderName:
		return app.ProviderResourceStateCanceled
	default:
		if isChinaCloudProvider(resource.Provider) {
			return app.ProviderResourceStateTerminating
		}
		return app.ProviderResourceStateUnknown
	}
}

func mergeResourceMetadata(current map[string]string, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(current)+len(extra))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func renderPlanReport(opts Options, report planReport, asJSON bool) {
	if asJSON {
		_ = json.NewEncoder(opts.Stdout).Encode(report)
		return
	}
	fmt.Fprintf(opts.Stdout, "Plan %s\n", report.PlanRunID)
	fmt.Fprintf(opts.Stdout, "Provider: %s\n", report.Provider)
	if report.Job.Image != "" {
		fmt.Fprintf(opts.Stdout, "Job image: %s\n", report.Job.Image)
	} else {
		fmt.Fprintf(opts.Stdout, "Job script: %s\n", report.Job.Script)
	}
	fmt.Fprintf(opts.Stdout, "Data inputs: %d\n", len(report.DataManifest.Inputs))
	if report.DataManifest.BundleSizeBytes > 0 {
		fmt.Fprintf(opts.Stdout, "Bundle size: %d bytes\n", report.DataManifest.BundleSizeBytes)
	}
	if report.Staging.WouldUpload {
		fmt.Fprintln(opts.Stdout, "Planned staged data uploads:")
		for _, uri := range report.Staging.PlannedUploads {
			fmt.Fprintf(opts.Stdout, "  %s\n", uri)
		}
	}
	if report.Packaging.WouldPackage {
		fmt.Fprintf(opts.Stdout, "Packaging: would build and push %s\n", report.Packaging.Image)
	}
	if report.Routing != nil {
		if report.Routing.SelectedProvider != "" {
			fmt.Fprintf(opts.Stdout, "Selected provider: %s\n", report.Routing.SelectedProvider)
		}
		if report.Routing.SelectedHardware != nil {
			hardware := report.Routing.SelectedHardware
			fmt.Fprintf(opts.Stdout, "Selected hardware: %s", hardware.ShapeID)
			if hardware.Region != "" {
				fmt.Fprintf(opts.Stdout, " in %s", hardware.Region)
			}
			if hardware.HourlyUSD > 0 {
				fmt.Fprintf(opts.Stdout, " at $%.2f/hr", hardware.HourlyUSD)
			}
			fmt.Fprintln(opts.Stdout)
		}
		if report.Routing.EstimatedTotalCostUSD != nil {
			fmt.Fprintf(opts.Stdout, "Estimated run cost: $%.2f\n", *report.Routing.EstimatedTotalCostUSD)
		}
		if len(report.Routing.RejectedProviders) > 0 {
			fmt.Fprintln(opts.Stdout, "Rejected providers:")
			for _, rejected := range report.Routing.RejectedProviders {
				fmt.Fprintf(opts.Stdout, "  %s: %s\n", rejected.Provider, strings.Join(rejected.Reasons, ", "))
			}
		}
	} else if report.ProviderReport != nil {
		fmt.Fprintf(opts.Stdout, "Provider supported: %t\n", report.ProviderReport.Supported)
		if report.ProviderReport.Estimate.HourlyUSD > 0 {
			fmt.Fprintf(opts.Stdout, "Estimated hourly cost: $%.2f\n", report.ProviderReport.Estimate.HourlyUSD)
		}
		if len(report.ProviderReport.Reasons) > 0 {
			fmt.Fprintf(opts.Stdout, "Provider reasons: %s\n", strings.Join(report.ProviderReport.Reasons, ", "))
		}
	}
	if report.Checkpoint.ResumeFromURI != "" {
		fmt.Fprintf(opts.Stdout, "Checkpoint compatibility: %t", report.Checkpoint.SupportedBySelected)
		if report.Checkpoint.Reason != "" {
			fmt.Fprintf(opts.Stdout, " (%s)", report.Checkpoint.Reason)
		}
		fmt.Fprintln(opts.Stdout)
	}
	fmt.Fprintln(opts.Stdout, "Suppressed actions:")
	for _, action := range report.SuppressedActions {
		fmt.Fprintf(opts.Stdout, "  %s\n", action)
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
	var strictAuth bool
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
			if chinaProvider, ok := adapter.(interface {
				ValidateConnection(context.Context, chinacloudprovider.ConnectionOptions) (chinacloudprovider.ConnectionReport, error)
			}); ok {
				connection, authErr := chinaProvider.ValidateConnection(cmd.Context(), chinacloudprovider.ConnectionOptions{RequireAuthenticated: strictAuth})
				report.AuthMode = connection.Mode
				report.Authenticated = connection.Authenticated
				report.Endpoint = connection.Endpoint
				report.AuthCommandEnv = connection.AuthCommandEnv
				report.BuiltInAuth = connection.BuiltInAuth
				report.BuiltInEndpoint = connection.BuiltInEndpoint
				report.Documentation = connection.Documentation
				report.Warnings = connection.Warnings
				report.CredentialNames = connection.CredentialNames
				if authErr != nil {
					report.Ready = false
					report.Error = authErr.Error()
				}
			} else if authErr := adapter.ValidateAuth(cmd.Context()); authErr != nil {
				report.Ready = false
				report.Error = authErr.Error()
			} else {
				report.AuthMode = "provider"
				report.Authenticated = true
			}
			if asJSON {
				if err := json.NewEncoder(opts.Stdout).Encode(report); err != nil {
					return err
				}
			} else if report.Ready {
				if report.Authenticated {
					fmt.Fprintf(opts.Stdout, "%s ready", report.Name)
				} else {
					fmt.Fprintf(opts.Stdout, "%s ready; authenticated provider API was not verified", report.Name)
				}
				if report.AuthMode != "" {
					fmt.Fprintf(opts.Stdout, " (%s)", report.AuthMode)
				}
				fmt.Fprintln(opts.Stdout)
				for _, warning := range report.Warnings {
					fmt.Fprintf(opts.Stdout, "warning: %s\n", warning)
				}
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
	check.Flags().BoolVar(&strictAuth, "strict-auth", false, "Require authenticated provider validation instead of endpoint-only readiness checks when supported")
	cmd.AddCommand(inspect, check)
	return cmd
}

type providerInspectReport struct {
	Name         string                   `json:"name"`
	Capabilities app.ProviderCapabilities `json:"capabilities"`
}

type providerCheckReport struct {
	Name            string                   `json:"name"`
	Ready           bool                     `json:"ready"`
	Error           string                   `json:"error,omitempty"`
	AuthMode        string                   `json:"auth_mode,omitempty"`
	Authenticated   bool                     `json:"authenticated"`
	Endpoint        string                   `json:"endpoint,omitempty"`
	AuthCommandEnv  string                   `json:"auth_command_env,omitempty"`
	BuiltInAuth     bool                     `json:"built_in_auth"`
	BuiltInEndpoint string                   `json:"built_in_endpoint,omitempty"`
	Documentation   string                   `json:"documentation,omitempty"`
	Warnings        []string                 `json:"warnings,omitempty"`
	CredentialNames []string                 `json:"credential_names,omitempty"`
	Capabilities    app.ProviderCapabilities `json:"capabilities,omitempty"`
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

func optionalCredentialResolverAtHome(opts Options, resolvedHome string) (credentials.Resolver, error) {
	path := credentials.DefaultPath(resolvedHome)
	if strings.TrimSpace(os.Getenv(credentials.PassphraseEnv)) == "" {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return credentials.Resolver{}, nil
		} else if err != nil {
			return credentials.Resolver{}, err
		}
	}
	store, _, err := openCredentialStoreAtHome(opts, resolvedHome)
	if err != nil {
		return credentials.Resolver{}, err
	}
	return credentials.Resolver{Store: store}, nil
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
	return buildProviderRegistryWithOptions(opts, mockConfig, gcpConfig, config.LambdaConfig{}, config.ChinaCloudConfig{}, credentials.Resolver{}, true, true)
}

func buildTrainProviderRegistry(opts Options, resolved config.ResolvedTrainConfig, credentialResolver credentials.Resolver) *provider.Registry {
	return buildProviderRegistryWithOptions(opts, resolved.Mock, resolved.GCP, resolved.Lambda, resolved.ChinaCloud, credentialResolver, shouldRegisterGCPForTrain(resolved), shouldRegisterLambdaForTrain(resolved))
}

func buildPlanProviderRegistry(opts Options, resolved config.ResolvedTrainConfig) *provider.Registry {
	return buildProviderRegistryWithOptions(opts, resolved.Mock, resolved.GCP, resolved.Lambda, resolved.ChinaCloud, credentials.Resolver{}, shouldRegisterGCPForTrain(resolved), shouldRegisterLambdaForTrain(resolved))
}

func buildProviderRegistryWithOptions(opts Options, mockConfig config.MockConfig, gcpConfig config.GCPConfig, lambdaConfig config.LambdaConfig, chinaConfig config.ChinaCloudConfig, credentialResolver credentials.Resolver, includeGCP bool, includeLambda bool) *provider.Registry {
	adapters := []app.ProviderAdapter{localprovider.New(opts.Stdout, opts.Stderr)}
	adapters = append(adapters, chinaCloudAdapters(chinaConfig, credentialResolver, opts.Stdout, opts.Stderr)...)
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
	if opts.LambdaProviderFactory != nil && !shouldRegisterAnyChinaVMForTrain(resolved) {
		return credentials.Resolver{}, nil
	}
	if !shouldRegisterLambdaForTrain(resolved) && !shouldRegisterAnyChinaVMForTrain(resolved) {
		return credentials.Resolver{}, nil
	}
	return optionalCredentialResolverAtHome(opts, resolved.SwitchboardHome)
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

func shouldRegisterAnyChinaVMForTrain(resolved config.ResolvedTrainConfig) bool {
	if isChinaCloudProvider(resolved.Provider) {
		return true
	}
	if resolved.Provider != string(app.ProviderAuto) {
		return false
	}
	for _, name := range chinacloudprovider.Names() {
		if chinaCloudProviderConfigured(chinaProviderConfigByName(resolved.ChinaCloud, name)) {
			return true
		}
	}
	return false
}

func chinaCloudAdapters(china config.ChinaCloudConfig, credentialResolver credentials.Resolver, stdout io.Writer, stderr io.Writer) []app.ProviderAdapter {
	defs := chinacloudprovider.Definitions()
	adapters := make([]app.ProviderAdapter, 0, len(defs))
	for _, def := range defs {
		providerConfig := chinaProviderConfigByName(china, def.Name)
		if chinaCloudProviderConfigured(providerConfig) {
			adapters = append(adapters, chinacloudprovider.NewVMProvider(def, chinaVMProviderConfigFromConfig(providerConfig, credentialResolver), stdout, stderr))
			continue
		}
		adapters = append(adapters, chinacloudprovider.New(def))
	}
	return adapters
}

func chinaCloudProviderConfigured(cfg config.ChinaCloudProviderConfig) bool {
	return cfg.Region != "" || cfg.InstanceType != "" || cfg.ImageID != "" || cfg.SubnetID != "" || cfg.SecurityGroupID != "" || cfg.SSHKeyName != "" || cfg.SSHPrivateKey != ""
}

func chinaProviderConfigByName(china config.ChinaCloudConfig, name string) config.ChinaCloudProviderConfig {
	switch name {
	case "alibaba-cloud":
		return china.AlibabaCloud
	case "huawei-cloud":
		return china.HuaweiCloud
	case "tencent-cloud":
		return china.TencentCloud
	case "tianyi-cloud":
		return china.TianyiCloud
	case "baidu-ai-cloud":
		return china.BaiduAICloud
	default:
		return config.ChinaCloudProviderConfig{}
	}
}

func chinaVMProviderConfigFromConfig(cfg config.ChinaCloudProviderConfig, credentialResolver credentials.Resolver) chinacloudprovider.VMProviderConfig {
	terminateOnCompletion := true
	terminateSet := false
	if cfg.TerminateOnCompletion != nil {
		terminateOnCompletion = *cfg.TerminateOnCompletion
		terminateSet = true
	}
	projectOrAccount := cfg.ProjectID
	if projectOrAccount == "" {
		projectOrAccount = cfg.AccountID
	}
	return chinacloudprovider.VMProviderConfig{
		Region:                   cfg.Region,
		Zone:                     cfg.Zone,
		InstanceType:             cfg.InstanceType,
		ImageID:                  cfg.ImageID,
		SSHUser:                  cfg.SSHUser,
		SSHPrivateKey:            cfg.SSHPrivateKey,
		SSHKeyName:               cfg.SSHKeyName,
		VPCID:                    cfg.VPCID,
		SubnetID:                 cfg.SubnetID,
		SecurityGroupID:          cfg.SecurityGroupID,
		SystemDiskType:           cfg.SystemDiskCategory,
		SystemDiskSizeGB:         cfg.SystemDiskSizeGB,
		InternetBandwidthMbps:    cfg.InternetBandwidthMbps,
		PollIntervalSeconds:      cfg.PollIntervalSeconds,
		APITimeoutSeconds:        cfg.APITimeoutSeconds,
		SSHConnectTimeoutSecs:    cfg.SSHConnectTimeoutSecs,
		SSHReadyTimeoutSeconds:   cfg.SSHReadyTimeoutSeconds,
		TerminateOnCompletion:    terminateOnCompletion,
		TerminateOnCompletionSet: terminateSet,
		KeepInstanceOnFailure:    cfg.KeepInstanceOnFailure,
		EstimateHourlyUSD:        cfg.EstimateHourlyUSD,
		ProjectOrAccount:         projectOrAccount,
		Endpoint:                 cfg.Endpoint,
		Credentials:              credentialResolver,
	}
}

func isChinaCloudProvider(provider string) bool {
	_, err := chinacloudprovider.DefinitionFor(provider)
	return err == nil
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
		RegistryAuth: lambdaprovider.RegistryAuth{
			Server:   cfg.RegistryAuth.Server,
			Username: os.Getenv(cfg.RegistryAuth.UsernameEnv),
			Password: os.Getenv(cfg.RegistryAuth.PasswordEnv),
		},
		Credentials: credentialResolver,
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
