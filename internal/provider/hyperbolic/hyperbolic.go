package hyperbolic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/redact"
)

type Provider struct {
	config    Config
	client    Client
	newClient func(Config) (Client, error)
	remote    RemoteRunner
	Stdout    io.Writer
	Stderr    io.Writer
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
}

func New(config Config, stdout io.Writer, stderr io.Writer) *Provider {
	return &Provider{
		config:    withDefaults(config),
		newClient: newRealClient,
		remote:    newSSHRemoteRunner(),
		Stdout:    stdout,
		Stderr:    stderr,
		Now:       time.Now,
		Sleep:     sleep,
	}
}

func NewWithClient(config Config, client Client, remote RemoteRunner, stdout io.Writer, stderr io.Writer) *Provider {
	if remote == nil {
		remote = newSSHRemoteRunner()
	}
	return &Provider{
		config: withDefaults(config),
		client: client,
		remote: remote,
		Stdout: stdout,
		Stderr: stderr,
		Now:    time.Now,
		Sleep:  sleep,
	}
}

func withDefaults(config Config) Config {
	if config.VMConfigID == "" {
		config.VMConfigID = defaultVMConfigID
	}
	if config.GPUCount == 0 {
		config.GPUCount = 1
	}
	if config.GPUType == "" {
		config.GPUType = "H100-SXM5-80GB"
	}
	if config.SSHUser == "" {
		config.SSHUser = "ubuntu"
	}
	if config.PollIntervalSeconds == 0 {
		config.PollIntervalSeconds = 30
	}
	if !config.TerminateOnCompletionSet {
		config.TerminateOnCompletion = true
	}
	if config.APITimeoutSeconds == 0 {
		config.APITimeoutSeconds = 30
	}
	if config.SSHConnectTimeoutSecs == 0 {
		config.SSHConnectTimeoutSecs = 10
	}
	if config.SSHReadyTimeoutSeconds == 0 {
		config.SSHReadyTimeoutSeconds = 600
	}
	return config
}

func (p *Provider) Name() app.ProviderName {
	return app.ProviderName(ProviderName)
}

func (p *Provider) ValidateAuth(ctx context.Context) error {
	client, err := p.clientFor()
	if err != nil {
		return normalizeError(err)
	}
	return normalizeError(client.ValidateAuth(ctx))
}

func (p *Provider) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	shapes := p.hardwareShapes(ctx)
	return app.ProviderCapabilities{
		GPUFamilies:                gpuFamiliesFromShapes(shapes),
		Regions:                    regionsFromShapes(shapes),
		HardwareShapes:             shapes,
		SupportsOnDemand:           true,
		SupportsDockerImage:        true,
		SupportsLocalScript:        false,
		SupportsDataBundle:         false,
		SupportedURISchemes:        []string{"http", "https", "s3", "gs"},
		SupportedCheckpointSchemes: []string{"s3", "gs"},
		SupportsObjectStorePull:    false,
	}, nil
}

func (p *Provider) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	var reasons []string
	if spec.Image == "" {
		reasons = append(reasons, "hyperbolic provider requires job.image")
	}
	if spec.Script != "" {
		reasons = append(reasons, "hyperbolic provider v1 does not package local scripts; provide job.image")
	}
	for _, input := range spec.Data {
		if input.Mode == app.DataInputModeBundle {
			reasons = append(reasons, fmt.Sprintf("hyperbolic provider v1 does not support bundled data input %q", input.Name))
		}
	}
	if p.config.GPUCount <= 0 {
		reasons = append(reasons, "hyperbolic.gpu_count must be greater than 0")
	}
	if p.config.VMConfigID == "" {
		reasons = append(reasons, "hyperbolic.vm_config_id is required")
	}
	if p.config.SSHPrivateKey == "" {
		reasons = append(reasons, "hyperbolic.ssh_private_key is required")
	}
	return app.SupportReport{Supported: len(reasons) == 0, Reasons: reasons}
}

func (p *Provider) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	if p.config.EstimateHourlyUSD > 0 {
		return app.CostEstimate{HourlyUSD: p.config.EstimateHourlyUSD, Currency: "USD"}, nil
	}
	for _, shape := range p.hardwareShapes(ctx) {
		if shape.AcceleratorCount == p.config.GPUCount && shape.OnDemandHourlyUSD > 0 {
			return app.CostEstimate{HourlyUSD: shape.OnDemandHourlyUSD, Currency: "USD"}, nil
		}
	}
	return app.CostEstimate{Currency: "USD"}, nil
}

func (p *Provider) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
	redactor := redact.FromEnvironment(req.JobSpec.Env, req.RuntimeEnv, p.registryAuthRedactionEnv())
	paths := artifact.ForRun(filepath.Dir(filepath.Dir(req.RunDir)), req.RunID)
	logFile, err := os.OpenFile(paths.Logs, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return app.SubmitResult{}, fmt.Errorf("open logs artifact: %w", err)
	}
	defer logFile.Close()
	eventFile, err := os.OpenFile(paths.EventsJSONL, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return app.SubmitResult{}, fmt.Errorf("open events artifact: %w", err)
	}
	defer eventFile.Close()

	client, err := p.clientFor()
	if err != nil {
		wrapped := normalizeError(err)
		return app.SubmitResult{ExitCode: exitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
	}
	rentReq := p.rentalRequest(req)
	p.writeLog(logFile, redactor.String(fmt.Sprintf("hyperbolic on-demand VM rent: %s", req.JobSpec.Name)))
	rental, err := client.RentVirtualMachine(ctx, rentReq)
	if err != nil {
		wrapped := normalizeError(err)
		return app.SubmitResult{ExitCode: exitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
	}
	instanceID := rental.RefID()
	if instanceID == "" {
		wrapped := &app.ProviderError{Kind: app.ProviderErrorInternal, Message: "hyperbolic rent returned no rental ID"}
		return app.SubmitResult{ExitCode: exitCodeForProviderError(wrapped), ExitReason: wrapped.Error()}, wrapped
	}
	providerRef := providerRef(instanceID)
	if err := p.notifyResource(req, Instance{ID: rental.ID, ExternalID: rental.ExternalID, Status: "starting", Meta: rental.Meta, CostPerHour: rental.CostPerHour}, providerRef, app.ProviderResourceStateBooting, true, map[string]string{"native_status": "starting"}); err != nil {
		_ = client.TerminateVirtualMachine(ctx, instanceID)
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
	}
	if req.OnStarted != nil {
		if err := req.OnStarted(app.ProviderJobRef{ID: providerRef}); err != nil {
			_ = client.TerminateVirtualMachine(ctx, instanceID)
			return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
		}
	}
	p.writeLog(logFile, redactor.String(fmt.Sprintf("hyperbolic on-demand VM rental created: %s", instanceID)))

	instance, err := p.waitForRunning(ctx, client, instanceID, logFile, redactor)
	if err != nil {
		return p.failureResult(ctx, client, req, Instance{ID: rental.ID, ExternalID: rental.ExternalID, Status: "failed", Meta: rental.Meta}, providerRef, err, redactor)
	}
	if err := p.notifyResource(req, instance, providerRef, app.ProviderResourceStateRunning, false, map[string]string{"native_status": instance.Status}); err != nil {
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
	}
	target := RemoteTarget{
		Host:           instance.PublicIP(),
		User:           instance.User(p.config.SSHUser),
		PrivateKeyPath: p.config.SSHPrivateKey,
		ConnectTimeout: time.Duration(p.config.SSHConnectTimeoutSecs) * time.Second,
	}
	if err := p.remote.WaitForReady(ctx, target, time.Duration(p.config.SSHReadyTimeoutSeconds)*time.Second, p.sleep); err != nil {
		wrapped := &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: fmt.Sprintf("hyperbolic ssh not ready: %v", err), Err: err}
		return p.failureResult(ctx, client, req, instance, providerRef, wrapped, redactor)
	}
	if err := p.startRemote(ctx, target, req); err != nil {
		wrapped := &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: fmt.Sprintf("hyperbolic remote runner failed to start: %v", err), Err: err}
		return p.failureResult(ctx, client, req, instance, providerRef, wrapped, redactor)
	}

	exitCode, exitReason, err := p.monitorRemote(ctx, client, instance.RefID(), target, logFile, eventFile, req, redactor)
	if err != nil {
		return p.failureResult(ctx, client, req, instance, providerRef, err, redactor)
	}
	finalState := app.ProviderResourceStateSucceeded
	if exitCode != 0 {
		finalState = app.ProviderResourceStateFailed
	}
	metadata := map[string]string{"exit_reason": exitReason}
	if p.shouldTerminate(exitCode) {
		if err := client.TerminateVirtualMachine(ctx, instance.RefID()); err != nil {
			p.writeLog(logFile, redactor.String(fmt.Sprintf("hyperbolic terminate failed: %v", normalizeError(err))))
			metadata["cleanup_error"] = normalizeError(err).Error()
		} else {
			p.writeLog(logFile, redactor.String(fmt.Sprintf("hyperbolic on-demand VM termination requested: %s", instance.RefID())))
			finalState = app.ProviderResourceStateTerminating
		}
	}
	if err := p.notifyResource(req, instance, providerRef, finalState, false, metadata); err != nil {
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
	}
	return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: exitCode, ExitReason: redactor.String(exitReason)}, nil
}

func (p *Provider) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	client, err := p.clientFor()
	if err != nil {
		return app.ProviderJobStatus{}, normalizeError(err)
	}
	instance, err := client.GetVirtualMachineInstance(ctx, instanceIDFromRef(ref.ID))
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return app.ProviderJobStatus{State: app.AttemptStateCanceled, ResourceState: app.ProviderResourceStateTerminated}, nil
		}
		return app.ProviderJobStatus{}, normalizeError(err)
	}
	return app.ProviderJobStatus{
		State:         attemptStateFromInstanceStatus(instance.Status),
		ResourceState: providerResourceStateFromInstanceStatus(instance.Status),
	}, nil
}

func (p *Provider) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, fmt.Errorf("hyperbolic provider logs are read from run artifacts")
}

func (p *Provider) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	client, err := p.clientFor()
	if err != nil {
		return normalizeError(err)
	}
	return normalizeError(client.TerminateVirtualMachine(ctx, instanceIDFromRef(ref.ID)))
}

func (p *Provider) rentalRequest(req app.SubmitRequest) VirtualMachineRentalRequest {
	gpuCount := p.config.GPUCount
	if req.SelectedHardware != nil && req.SelectedHardware.Provider == ProviderName && req.SelectedHardware.AcceleratorCount > 0 {
		gpuCount = req.SelectedHardware.AcceleratorCount
	}
	return VirtualMachineRentalRequest{ConfigID: p.config.VMConfigID, GPUCount: gpuCount}
}

func (p *Provider) startRemote(ctx context.Context, target RemoteTarget, req app.SubmitRequest) error {
	if err := p.remote.WriteFile(ctx, target, remoteRunScriptPath, runnerScript(req, p.config.RegistryAuth), 0o755); err != nil {
		return err
	}
	_, err := p.remote.Run(ctx, target, "mkdir -p "+shellQuote(remoteSwitchboardDir)+" && nohup bash "+shellQuote(remoteRunScriptPath)+" > "+shellQuote(remoteSwitchboardDir+"/bootstrap.log")+" 2>&1 < /dev/null &")
	return err
}

func (p *Provider) waitForRunning(ctx context.Context, client Client, instanceID string, logFile io.Writer, redactor redact.Redactor) (Instance, error) {
	for {
		instance, err := client.GetVirtualMachineInstance(ctx, instanceID)
		if err != nil {
			return Instance{}, normalizeError(err)
		}
		p.writeLog(logFile, redactor.String(fmt.Sprintf("hyperbolic on-demand VM status: %s", instance.Status)))
		switch instanceStatusCategory(instance.Status) {
		case "running":
			if instance.PublicIP() == "" {
				return Instance{}, &app.ProviderError{Kind: app.ProviderErrorInternal, Message: "hyperbolic on-demand VM is running but has no public IP or SSH command"}
			}
			return instance, nil
		case "pending":
		case "terminated":
			return Instance{}, &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: "hyperbolic on-demand VM terminated before job startup"}
		case "failed":
			return Instance{}, &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: "hyperbolic on-demand VM entered failed state before job startup"}
		default:
			return Instance{}, &app.ProviderError{Kind: app.ProviderErrorUnknown, Message: fmt.Sprintf("hyperbolic on-demand VM entered unknown status %q", instance.Status)}
		}
		if err := p.sleep(ctx, time.Duration(p.config.PollIntervalSeconds)*time.Second); err != nil {
			return Instance{}, err
		}
	}
}

func (p *Provider) monitorRemote(ctx context.Context, client Client, instanceID string, target RemoteTarget, logFile io.Writer, eventFile io.Writer, req app.SubmitRequest, redactor redact.Redactor) (int, string, error) {
	var logOffset int
	var eventOffset int
	seenEvents := map[string]bool{}
	for {
		if content, err := p.remote.ReadFile(ctx, target, remoteLogsPath); err == nil {
			logOffset = appendNewLogContent(logFile, eventFile, content, logOffset, req.RunID, req.AttemptID, p.now(), redactor, p.stdout(), seenEvents)
		}
		if content, err := p.remote.ReadFile(ctx, target, remoteEventsPath); err == nil {
			eventOffset = appendNewEventContent(eventFile, content, eventOffset, req.RunID, req.AttemptID, p.now(), redactor, seenEvents)
		}
		if content, err := p.remote.ReadFile(ctx, target, remoteExitPath); err == nil && strings.TrimSpace(content) != "" {
			exitCode, exitReason, err := parseExit(content)
			if err != nil {
				return 1, "", &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: fmt.Sprintf("hyperbolic exit marker was invalid: %v", err), Err: err}
			}
			return exitCode, exitReason, nil
		}
		instance, err := client.GetVirtualMachineInstance(ctx, instanceID)
		if err != nil {
			return 1, "", normalizeError(err)
		}
		switch instanceStatusCategory(instance.Status) {
		case "running", "pending":
		case "terminated":
			return 1, "", &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: "hyperbolic on-demand VM terminated before writing exit marker"}
		case "failed":
			return 1, "", &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: "hyperbolic on-demand VM entered failed state"}
		default:
			return 1, "", &app.ProviderError{Kind: app.ProviderErrorUnknown, Message: fmt.Sprintf("hyperbolic on-demand VM entered unknown status %q", instance.Status)}
		}
		if err := p.sleep(ctx, time.Duration(p.config.PollIntervalSeconds)*time.Second); err != nil {
			return 1, "", err
		}
	}
}

func (p *Provider) failureResult(ctx context.Context, client Client, req app.SubmitRequest, instance Instance, providerRef string, err error, redactor redact.Redactor) (app.SubmitResult, error) {
	wrapped := normalizeError(err)
	state := app.ProviderResourceStateFailed
	metadata := map[string]string{"error": wrapped.Error()}
	if !p.config.KeepInstanceOnFailure && instance.RefID() != "" {
		if terminateErr := client.TerminateVirtualMachine(ctx, instance.RefID()); terminateErr != nil {
			metadata["cleanup_error"] = normalizeError(terminateErr).Error()
		} else {
			state = app.ProviderResourceStateTerminating
		}
	}
	if resourceErr := p.notifyResource(req, instance, providerRef, state, false, metadata); resourceErr != nil {
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: redactor.String(resourceErr.Error())}, resourceErr
	}
	return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: exitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
}

func (p *Provider) notifyResource(req app.SubmitRequest, instance Instance, providerRef string, state app.ProviderResourceState, created bool, metadata map[string]string) error {
	callback := req.OnResourceUpdated
	if created {
		callback = req.OnResourceCreated
	}
	if callback == nil {
		return nil
	}
	observedAt := p.now()
	return callback(app.ProviderResource{
		RunID:                req.RunID,
		AttemptID:            req.AttemptID,
		Provider:             ProviderName,
		Kind:                 app.ProviderResourceKindInstance,
		ExternalID:           instance.RefID(),
		ProviderRef:          providerRef,
		State:                state,
		CreatedBySwitchboard: true,
		CleanupPolicy:        p.cleanupPolicy(),
		Metadata:             p.resourceMetadata(instance, metadata),
		LastObservedAt:       &observedAt,
	})
}

func (p *Provider) resourceMetadata(instance Instance, extra map[string]string) map[string]string {
	metadata := map[string]string{
		"vm_config_id": p.config.VMConfigID,
		"gpu_count":    strconv.Itoa(instance.GPUCount(p.config.GPUCount)),
		"gpu_type":     p.config.GPUType,
	}
	if instance.ExternalID != "" {
		metadata["external_id"] = instance.ExternalID
	}
	if instance.Status != "" {
		metadata["native_status"] = instance.Status
	}
	if instance.CostPerHour > 0 {
		metadata["cost_per_hour_cents"] = strconv.Itoa(instance.CostPerHour)
	}
	if instance.Meta.SSHCommand != "" {
		metadata["ssh_command"] = instance.Meta.SSHCommand
	}
	if p.config.RegistryAuth.Server != "" {
		metadata["registry_auth_server"] = p.config.RegistryAuth.Server
	}
	for key, value := range extra {
		metadata[key] = value
	}
	return metadata
}

func (p *Provider) cleanupPolicy() app.ProviderResourceCleanupPolicy {
	switch {
	case p.config.TerminateOnCompletion && !p.config.KeepInstanceOnFailure:
		return app.ProviderResourceCleanupAlways
	case p.config.TerminateOnCompletion && p.config.KeepInstanceOnFailure:
		return app.ProviderResourceCleanupOnSuccess
	case !p.config.TerminateOnCompletion && !p.config.KeepInstanceOnFailure:
		return app.ProviderResourceCleanupOnFailure
	default:
		return app.ProviderResourceCleanupNever
	}
}

func (p *Provider) shouldTerminate(exitCode int) bool {
	if exitCode == 0 {
		return p.config.TerminateOnCompletion
	}
	return !p.config.KeepInstanceOnFailure
}

func (p *Provider) hardwareShapes(ctx context.Context) []app.HardwareShape {
	client, err := p.clientFor()
	if err != nil {
		return []app.HardwareShape{p.configuredHardwareShape()}
	}
	options, err := client.ListVirtualMachineOptions(ctx)
	if err != nil || len(options) == 0 {
		return []app.HardwareShape{p.configuredHardwareShape()}
	}
	shapes := make([]app.HardwareShape, 0, len(options))
	for _, option := range options {
		if option.GPUCount <= 0 {
			continue
		}
		shapes = append(shapes, p.hardwareShape(option.GPUCount, option.CostPerHour*float64(option.GPUCount), "available", "reported by Hyperbolic virtual-machine-options API"))
	}
	if len(shapes) == 0 {
		return []app.HardwareShape{p.configuredHardwareShape()}
	}
	sort.Slice(shapes, func(i, j int) bool {
		return shapes[i].AcceleratorCount < shapes[j].AcceleratorCount
	})
	return shapes
}

func (p *Provider) configuredHardwareShape() app.HardwareShape {
	return p.hardwareShape(p.config.GPUCount, p.config.EstimateHourlyUSD, "configured", "configured from hyperbolic.gpu_count")
}

func (p *Provider) hardwareShape(gpuCount int, hourlyUSD float64, availabilityHint string, availabilityReason string) app.HardwareShape {
	vramPerGPU := vramGBPerGPU(p.config.GPUType)
	return app.HardwareShape{
		ID:                 fmt.Sprintf("hyperbolic-ondemand-vm-%dg", gpuCount),
		Provider:           ProviderName,
		Region:             "global",
		MachineType:        "ondemand-vm",
		AcceleratorType:    p.config.GPUType,
		AcceleratorCount:   gpuCount,
		GPUFamily:          gpuFamily(p.config.GPUType),
		VRAMGBPerGPU:       vramPerGPU,
		TotalVRAMGB:        vramPerGPU * gpuCount,
		OnDemandHourlyUSD:  hourlyUSD,
		SupportsOnDemand:   true,
		AvailabilityHint:   availabilityHint,
		AvailabilityReason: availabilityReason,
	}
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var providerErr *app.ProviderError
	if errors.As(err, &providerErr) {
		return err
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		kind := app.ProviderErrorUnknown
		code := strings.ToLower(apiErr.Code + " " + apiErr.Message)
		switch {
		case apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden || strings.Contains(code, "api-key") || strings.Contains(code, "auth"):
			kind = app.ProviderErrorAuth
		case strings.Contains(code, "quota") || strings.Contains(code, "credit") || strings.Contains(code, "balance") || strings.Contains(code, "fund"):
			kind = app.ProviderErrorQuota
		case strings.Contains(code, "capacity") || strings.Contains(code, "unavailable") || strings.Contains(code, "sold out"):
			kind = app.ProviderErrorCapacity
		case apiErr.StatusCode == http.StatusBadRequest || strings.Contains(code, "invalid"):
			kind = app.ProviderErrorInvalidSpec
		case apiErr.StatusCode >= 500:
			kind = app.ProviderErrorInternal
		case apiErr.StatusCode == http.StatusNotFound:
			kind = app.ProviderErrorRuntime
		}
		return &app.ProviderError{Kind: kind, Message: "hyperbolic api: " + apiErr.Error(), Err: err}
	}
	return &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: err.Error(), Err: err}
}

func exitCodeForProviderError(err error) int {
	switch app.ProviderErrorKindOf(err) {
	case app.ProviderErrorInvalidSpec, app.ProviderErrorAuth:
		return 10
	case app.ProviderErrorCapacity, app.ProviderErrorQuota, app.ProviderErrorNetwork, app.ProviderErrorInternal:
		return 40
	default:
		return 1
	}
}

func attemptStateFromInstanceStatus(status string) app.AttemptState {
	switch instanceStatusCategory(status) {
	case "running", "pending":
		return app.AttemptStateRunning
	case "terminated":
		return app.AttemptStateCanceled
	case "failed":
		return app.AttemptStateFailed
	default:
		return app.AttemptStateFailed
	}
}

func providerResourceStateFromInstanceStatus(status string) app.ProviderResourceState {
	switch instanceStatusCategory(status) {
	case "pending":
		return app.ProviderResourceStateBooting
	case "running":
		return app.ProviderResourceStateRunning
	case "terminated":
		return app.ProviderResourceStateTerminated
	case "failed":
		return app.ProviderResourceStateFailed
	default:
		return app.ProviderResourceStateUnknown
	}
}

func instanceStatusCategory(status string) string {
	value := strings.ToLower(strings.TrimSpace(status))
	switch value {
	case "pending", "starting", "booting", "creating", "initializing", "provisioning", "requested":
		return "pending"
	case "running", "active", "ready", "node_ready":
		return "running"
	case "terminating", "terminated", "deleted", "stopped":
		return "terminated"
	case "failed", "error", "unhealthy":
		return "failed"
	default:
		return ""
	}
}

func providerRef(instanceID string) string {
	return ProviderName + ":" + instanceID
}

func instanceIDFromRef(ref string) string {
	return strings.TrimPrefix(ref, ProviderName+":")
}

func (p *Provider) registryAuthRedactionEnv() map[string]string {
	if p.config.RegistryAuth.Password == "" {
		return nil
	}
	return map[string]string{"SWITCHBOARD_HYPERBOLIC_REGISTRY_PASSWORD": p.config.RegistryAuth.Password}
}

func (p *Provider) clientFor() (Client, error) {
	if p.client != nil {
		return p.client, nil
	}
	client, err := p.newClient(p.config)
	if err != nil {
		return nil, err
	}
	p.client = client
	return client, nil
}

func (p *Provider) writeLog(logFile io.Writer, line string) {
	if line == "" {
		return
	}
	_, _ = fmt.Fprintln(logFile, line)
	_, _ = fmt.Fprintln(p.stdout(), line)
}

func (p *Provider) stdout() io.Writer {
	if p.Stdout != nil {
		return p.Stdout
	}
	return os.Stdout
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *Provider) sleep(ctx context.Context, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	return sleep(ctx, d)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func gpuFamiliesFromShapes(shapes []app.HardwareShape) []string {
	seen := map[string]bool{}
	var out []string
	for _, shape := range shapes {
		if shape.GPUFamily == "" || seen[shape.GPUFamily] {
			continue
		}
		seen[shape.GPUFamily] = true
		out = append(out, shape.GPUFamily)
	}
	sort.Strings(out)
	return out
}

func regionsFromShapes(shapes []app.HardwareShape) []string {
	seen := map[string]bool{}
	var out []string
	for _, shape := range shapes {
		if shape.Region == "" || seen[shape.Region] {
			continue
		}
		seen[shape.Region] = true
		out = append(out, shape.Region)
	}
	sort.Strings(out)
	return out
}

func gpuFamily(value string) string {
	upper := strings.ToUpper(value)
	for _, family := range []string{"H200", "H100", "A100", "A10", "L40", "L4", "4090", "3090"} {
		if strings.Contains(upper, family) {
			return family
		}
	}
	return strings.TrimSpace(value)
}

func vramGBPerGPU(value string) int {
	upper := strings.ToUpper(value)
	switch {
	case strings.Contains(upper, "H200"):
		return 141
	case strings.Contains(upper, "80GB") || strings.Contains(upper, "80 GB") || strings.Contains(upper, "H100"):
		return 80
	case strings.Contains(upper, "A100") && (strings.Contains(upper, "40GB") || strings.Contains(upper, "40 GB")):
		return 40
	case strings.Contains(upper, "A10"):
		return 24
	case strings.Contains(upper, "L4"):
		return 24
	case strings.Contains(upper, "4090"):
		return 24
	default:
		return 0
	}
}
