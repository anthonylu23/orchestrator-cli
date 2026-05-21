package chinacloud

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/event"
	"github.com/anthonylu23/switchboard-cli/internal/redact"
)

const (
	vmRemoteSwitchboardDir = "/tmp/switchboard"
	vmRemoteLogsPath       = vmRemoteSwitchboardDir + "/logs.txt"
	vmRemoteEventsPath     = vmRemoteSwitchboardDir + "/events.jsonl"
	vmRemoteExitPath       = vmRemoteSwitchboardDir + "/exit.json"
	vmRemoteCheckpointsDir = vmRemoteSwitchboardDir + "/checkpoints"
	vmRemoteEnvPath        = vmRemoteSwitchboardDir + "/container.env"
)

type VMRuntimeJob struct {
	Image   string
	Command []string
	Args    []string
	Env     map[string]string
	WorkDir string
}

type VMState string

const (
	VMStatePending     VMState = "pending"
	VMStateRunning     VMState = "running"
	VMStateTerminating VMState = "terminating"
	VMStateTerminated  VMState = "terminated"
	VMStateFailed      VMState = "failed"
	VMStateUnknown     VMState = "unknown"
)

type VMRuntimeConfig struct {
	Region                   string
	Zone                     string
	InstanceType             string
	ImageID                  string
	SSHUser                  string
	SSHPrivateKey            string
	SSHKeyName               string
	NetworkID                string
	SubnetID                 string
	VSwitchID                string
	SecurityGroupID          string
	SystemDiskType           string
	SystemDiskSizeGB         int
	InternetBandwidthMbps    int
	PollIntervalSeconds      int
	SSHConnectTimeoutSecs    int
	SSHReadyTimeoutSeconds   int
	TerminateOnCompletion    bool
	TerminateOnCompletionSet bool
	KeepInstanceOnFailure    bool
	EstimateHourlyUSD        float64
	ProjectOrAccount         string
	RequireProjectOrAccount  bool
	HardwareShapes           []app.HardwareShape
}

type VMCreateRequest struct {
	RunID                 string
	AttemptID             string
	Name                  string
	Image                 string
	Command               []string
	Args                  []string
	Env                   map[string]string
	WorkDir               string
	UserData              string
	Region                string
	Zone                  string
	InstanceType          string
	ImageID               string
	SSHKeyName            string
	NetworkID             string
	SubnetID              string
	VSwitchID             string
	SecurityGroupID       string
	SystemDiskType        string
	SystemDiskSizeGB      int
	InternetBandwidthMbps int
}

type VMInstance struct {
	ID          string
	State       VMState
	NativeState string
	PublicIP    string
	PrivateIP   string
	Region      string
	Zone        string
}

type VMClient interface {
	ValidateAuth(ctx context.Context) error
	CreateVM(ctx context.Context, req VMCreateRequest) (VMInstance, error)
	GetVM(ctx context.Context, id string) (VMInstance, error)
	TerminateVM(ctx context.Context, id string) error
}

type VMRemoteTarget struct {
	Host           string
	User           string
	PrivateKeyPath string
	ConnectTimeout time.Duration
}

type VMRemoteRunner interface {
	WaitForReady(ctx context.Context, target VMRemoteTarget, timeout time.Duration, sleep func(context.Context, time.Duration) error) error
	ReadFile(ctx context.Context, target VMRemoteTarget, path string) (string, error)
}

type vmRuntime struct {
	config VMRuntimeConfig
	client VMClient
	remote VMRemoteRunner
}

func newVMRuntime(config VMRuntimeConfig, client VMClient, remote VMRemoteRunner) *vmRuntime {
	if remote == nil {
		remote = vmSSHRemoteRunner{}
	}
	return &vmRuntime{config: vmRuntimeDefaults(config), client: client, remote: remote}
}

func vmRuntimeDefaults(config VMRuntimeConfig) VMRuntimeConfig {
	if config.SSHUser == "" {
		config.SSHUser = "root"
	}
	if config.PollIntervalSeconds == 0 {
		config.PollIntervalSeconds = 30
	}
	if config.SSHConnectTimeoutSecs == 0 {
		config.SSHConnectTimeoutSecs = 10
	}
	if config.SSHReadyTimeoutSeconds == 0 {
		config.SSHReadyTimeoutSeconds = 600
	}
	if !config.TerminateOnCompletionSet {
		config.TerminateOnCompletion = true
	}
	return config
}

func (r *vmRuntime) Capabilities(ctx context.Context, def Definition) app.ProviderCapabilities {
	shapes := append([]app.HardwareShape(nil), r.config.HardwareShapes...)
	if len(shapes) == 0 && (r.config.Region != "" || r.config.InstanceType != "") {
		shapes = []app.HardwareShape{{
			ID:                providerShapeID(def.Name, r.config.Region, r.config.InstanceType),
			Provider:          def.Name,
			Region:            r.config.Region,
			MachineType:       r.config.InstanceType,
			SupportsOnDemand:  true,
			OnDemandHourlyUSD: r.config.EstimateHourlyUSD,
			AvailabilityHint:  "configured",
		}}
	}
	return app.ProviderCapabilities{
		GPUFamilies:                gpuFamiliesFromHardware(shapes),
		Regions:                    providerRegions(def, r.config.Region),
		HardwareShapes:             shapes,
		SupportsOnDemand:           true,
		SupportsDockerImage:        true,
		SupportsLocalScript:        false,
		SupportsDataBundle:         false,
		SupportedURISchemes:        append([]string(nil), def.URISchemes...),
		SupportedCheckpointSchemes: append([]string(nil), def.URISchemes...),
		SupportsObjectStorePull:    false,
	}
}

func (r *vmRuntime) ValidateJob(ctx context.Context, def Definition, spec app.JobSpec) app.SupportReport {
	var reasons []string
	if r.client == nil {
		reasons = append(reasons, fmt.Sprintf("%s VM client is not configured", def.Name))
	}
	if spec.Image == "" {
		reasons = append(reasons, fmt.Sprintf("%s VM runtime requires job.image", def.Name))
	}
	if spec.Script != "" {
		reasons = append(reasons, fmt.Sprintf("%s VM runtime v1 does not package local scripts; provide job.image", def.Name))
	}
	for _, input := range spec.Data {
		if input.Mode == app.DataInputModeBundle {
			reasons = append(reasons, fmt.Sprintf("%s VM runtime v1 does not support bundled data input %q", def.Name, input.Name))
		}
	}
	if r.config.Region == "" {
		reasons = append(reasons, fmt.Sprintf("%s region is required", def.Name))
	}
	if r.config.InstanceType == "" {
		reasons = append(reasons, fmt.Sprintf("%s instance type is required", def.Name))
	}
	if r.config.ImageID == "" {
		reasons = append(reasons, fmt.Sprintf("%s image id is required", def.Name))
	}
	if r.config.SSHPrivateKey == "" {
		reasons = append(reasons, fmt.Sprintf("%s ssh private key is required", def.Name))
	}
	if r.config.RequireProjectOrAccount && r.config.ProjectOrAccount == "" {
		reasons = append(reasons, fmt.Sprintf("%s project/account id is required", def.Name))
	}
	return app.SupportReport{Supported: len(reasons) == 0, Reasons: reasons}
}

func (r *vmRuntime) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	return app.CostEstimate{HourlyUSD: r.config.EstimateHourlyUSD, Currency: "USD"}, nil
}

func (r *vmRuntime) Submit(ctx context.Context, p *Provider, req app.SubmitRequest) (app.SubmitResult, error) {
	redactor := redact.FromEnvironment(req.JobSpec.Env, req.RuntimeEnv)
	paths := artifact.ForRun(filepath.Dir(filepath.Dir(req.RunDir)), req.RunID)
	logFile, err := os.OpenFile(paths.Logs, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return app.SubmitResult{}, fmt.Errorf("open logs artifact: %w", err)
	}
	defer logFile.Close()
	eventFile, err := os.OpenFile(paths.EventsJSONL, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return app.SubmitResult{}, fmt.Errorf("open events artifact: %w", err)
	}
	defer eventFile.Close()

	if r.client == nil {
		wrapped := &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("%s VM client is not configured", p.definition.Name)}
		return app.SubmitResult{ExitCode: chinaExitCodeForProviderError(wrapped), ExitReason: wrapped.Error()}, wrapped
	}
	createReq := r.createRequest(p.definition, req)
	writeVMLog(p, logFile, redactor.String(fmt.Sprintf("%s VM create: %s", p.definition.Name, req.JobSpec.Name)))
	instance, err := r.client.CreateVM(ctx, createReq)
	if err != nil {
		wrapped := normalizeChinaCloudRuntimeError(err)
		return app.SubmitResult{ExitCode: chinaExitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
	}
	if instance.ID == "" {
		wrapped := &app.ProviderError{Kind: app.ProviderErrorInternal, Message: fmt.Sprintf("%s VM create returned no instance ID", p.definition.Name)}
		return app.SubmitResult{ExitCode: chinaExitCodeForProviderError(wrapped), ExitReason: wrapped.Error()}, wrapped
	}
	providerRef := vmProviderRef(p.definition.Name, instance.ID)
	if err := r.notifyResource(req, p.definition, instance, providerRef, app.ProviderResourceStateBooting, true, nil); err != nil {
		_ = r.client.TerminateVM(ctx, instance.ID)
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
	}
	if req.OnStarted != nil {
		if err := req.OnStarted(app.ProviderJobRef{ID: providerRef}); err != nil {
			_ = r.client.TerminateVM(ctx, instance.ID)
			return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
		}
	}

	active, err := r.waitForRunning(ctx, p, instance.ID, logFile, redactor)
	if err != nil {
		return r.failureResult(ctx, req, p.definition, instance.ID, providerRef, err, redactor)
	}
	if err := r.notifyResource(req, p.definition, active, providerRef, app.ProviderResourceStateRunning, false, nil); err != nil {
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
	}
	target := VMRemoteTarget{
		Host:           active.PublicIP,
		User:           r.config.SSHUser,
		PrivateKeyPath: r.config.SSHPrivateKey,
		ConnectTimeout: time.Duration(r.config.SSHConnectTimeoutSecs) * time.Second,
	}
	if err := r.remote.WaitForReady(ctx, target, time.Duration(r.config.SSHReadyTimeoutSeconds)*time.Second, p.sleep); err != nil {
		wrapped := &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: fmt.Sprintf("%s ssh not ready: %v", p.definition.Name, err), Err: err}
		return r.failureResult(ctx, req, p.definition, instance.ID, providerRef, wrapped, redactor)
	}

	exitCode, exitReason, err := r.monitorRemote(ctx, p, instance.ID, target, logFile, eventFile, req, redactor)
	if err != nil {
		return r.failureResult(ctx, req, p.definition, instance.ID, providerRef, err, redactor)
	}
	finalState := app.ProviderResourceStateSucceeded
	if exitCode != 0 {
		finalState = app.ProviderResourceStateFailed
	}
	metadata := map[string]string{"exit_reason": exitReason}
	if r.shouldTerminate(exitCode) {
		if err := r.client.TerminateVM(ctx, instance.ID); err != nil {
			metadata["cleanup_error"] = normalizeChinaCloudRuntimeError(err).Error()
		} else {
			writeVMLog(p, logFile, redactor.String(fmt.Sprintf("%s VM termination requested: %s", p.definition.Name, instance.ID)))
			finalState = app.ProviderResourceStateTerminating
		}
	}
	if err := r.notifyResource(req, p.definition, active, providerRef, finalState, false, metadata); err != nil {
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
	}
	return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: exitCode, ExitReason: redactor.String(exitReason)}, nil
}

func (r *vmRuntime) GetStatus(ctx context.Context, def Definition, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	if r.client == nil {
		return app.ProviderJobStatus{}, &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("%s VM client is not configured", def.Name)}
	}
	instance, err := r.client.GetVM(ctx, vmIDFromProviderRef(def.Name, ref.ID))
	if err != nil {
		return app.ProviderJobStatus{}, normalizeChinaCloudRuntimeError(err)
	}
	return app.ProviderJobStatus{State: attemptStateFromVMState(instance.State)}, nil
}

func (r *vmRuntime) Cancel(ctx context.Context, def Definition, ref app.ProviderJobRef) error {
	if r.client == nil {
		return &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("%s VM client is not configured", def.Name)}
	}
	return normalizeChinaCloudRuntimeError(r.client.TerminateVM(ctx, vmIDFromProviderRef(def.Name, ref.ID)))
}

func (r *vmRuntime) createRequest(def Definition, req app.SubmitRequest) VMCreateRequest {
	env := map[string]string{}
	for k, v := range req.JobSpec.Env {
		env[k] = v
	}
	for k, v := range req.RuntimeEnv {
		env[k] = v
	}
	env["SWITCHBOARD_EVENTS_PATH"] = vmRemoteEventsPath
	env["ORCHESTRATOR_EVENTS_PATH"] = vmRemoteEventsPath
	env["SWITCHBOARD_CHECKPOINT_DIR"] = vmRemoteCheckpointsDir
	env["ORCHESTRATOR_CHECKPOINT_DIR"] = vmRemoteCheckpointsDir

	region := r.config.Region
	instanceType := r.config.InstanceType
	if req.SelectedHardware != nil && req.SelectedHardware.Provider == def.Name {
		if req.SelectedHardware.Region != "" {
			region = req.SelectedHardware.Region
		}
		if req.SelectedHardware.MachineType != "" {
			instanceType = req.SelectedHardware.MachineType
		}
	}
	createReq := VMCreateRequest{
		RunID:                 req.RunID,
		AttemptID:             req.AttemptID,
		Name:                  safeVMName(req.JobSpec.Name, req.RunID),
		Image:                 req.JobSpec.Image,
		Command:               append([]string(nil), req.JobSpec.Command...),
		Args:                  append([]string(nil), req.JobSpec.Args...),
		Env:                   env,
		WorkDir:               req.JobSpec.WorkDir,
		Region:                region,
		Zone:                  r.config.Zone,
		InstanceType:          instanceType,
		ImageID:               r.config.ImageID,
		SSHKeyName:            r.config.SSHKeyName,
		NetworkID:             r.config.NetworkID,
		SubnetID:              r.config.SubnetID,
		VSwitchID:             r.config.VSwitchID,
		SecurityGroupID:       r.config.SecurityGroupID,
		SystemDiskType:        r.config.SystemDiskType,
		SystemDiskSizeGB:      r.config.SystemDiskSizeGB,
		InternetBandwidthMbps: r.config.InternetBandwidthMbps,
	}
	createReq.UserData = vmCloudInitUserData(createReq)
	return createReq
}

func BuildVMUserData(job VMRuntimeJob) (string, error) {
	if strings.TrimSpace(job.Image) == "" {
		return "", &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: "China VM runtime requires job.image"}
	}
	env := map[string]string{}
	for key, value := range job.Env {
		env[key] = value
	}
	env["SWITCHBOARD_EVENTS_PATH"] = vmRemoteEventsPath
	env["ORCHESTRATOR_EVENTS_PATH"] = vmRemoteEventsPath
	env["SWITCHBOARD_CHECKPOINT_DIR"] = vmRemoteCheckpointsDir
	env["ORCHESTRATOR_CHECKPOINT_DIR"] = vmRemoteCheckpointsDir
	return vmRunnerScript(VMCreateRequest{
		Image:   job.Image,
		Command: append([]string(nil), job.Command...),
		Args:    append([]string(nil), job.Args...),
		Env:     env,
		WorkDir: job.WorkDir,
	}), nil
}

func BuildVMUserDataBase64(job VMRuntimeJob) (string, error) {
	userData, err := BuildVMUserData(job)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString([]byte(userData)), nil
}

func vmCloudInitUserData(req VMCreateRequest) string {
	var out strings.Builder
	out.WriteString("#cloud-config\n")
	out.WriteString("write_files:\n")
	out.WriteString("  - path: /tmp/switchboard-run.sh\n")
	out.WriteString("    permissions: '0755'\n")
	out.WriteString("    content: |\n")
	for _, line := range strings.Split(vmRunnerScript(req), "\n") {
		out.WriteString("      ")
		out.WriteString(line)
		out.WriteString("\n")
	}
	out.WriteString("runcmd:\n")
	out.WriteString("  - [ bash, /tmp/switchboard-run.sh ]\n")
	return out.String()
}

func vmRunnerScript(req VMCreateRequest) string {
	var dockerArgs []string
	dockerArgs = append(dockerArgs, "docker", "run", "--rm", "--gpus", "all", "--env-file", vmRemoteEnvPath)
	keys := make([]string, 0, len(req.Env))
	for key := range req.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	dockerArgs = append(dockerArgs, "-v", vmRemoteSwitchboardDir+":"+vmRemoteSwitchboardDir)
	if req.WorkDir != "" && req.WorkDir != "." {
		dockerArgs = append(dockerArgs, "-w", req.WorkDir)
	}
	dockerArgs = append(dockerArgs, req.Image)
	dockerArgs = append(dockerArgs, req.Command...)
	dockerArgs = append(dockerArgs, req.Args...)

	return strings.Join([]string{
		"#!/usr/bin/env bash",
		"set +e",
		"mkdir -p " + vmShellQuote(vmRemoteCheckpointsDir),
		"touch " + vmShellQuote(vmRemoteEventsPath),
		"rm -f " + vmShellQuote(vmRemoteExitPath),
		"rm -f " + vmShellQuote(vmRemoteEnvPath),
		vmEnvFileScript(keys, req.Env, vmRemoteEnvPath),
		"chmod 0600 " + vmShellQuote(vmRemoteEnvPath),
		"{",
		"  echo 'switchboard VM job starting; switchboard china vm job starting'",
		"  docker pull " + vmShellQuote(req.Image),
		"  " + vmShellCommand(dockerArgs),
		"  code=$?",
		"  if [ \"$code\" -eq 0 ]; then reason='completed'; else reason=\"container exited with code $code\"; fi",
		"  printf '{\"exit_code\":%d,\"exit_reason\":\"%s\"}\\n' \"$code\" \"$reason\" > " + vmShellQuote(vmRemoteExitPath),
		"  exit \"$code\"",
		"} >> " + vmShellQuote(vmRemoteLogsPath) + " 2>&1",
	}, "\n")
}

func vmEnvFileScript(keys []string, env map[string]string, path string) string {
	var lines []string
	for _, key := range keys {
		lines = append(lines, "printf '%s\\n' "+vmShellQuote(fmt.Sprintf("%s=%s", key, env[key]))+" >> "+vmShellQuote(path))
	}
	return strings.Join(lines, "\n")
}

func (r *vmRuntime) waitForRunning(ctx context.Context, p *Provider, instanceID string, logFile io.Writer, redactor redact.Redactor) (VMInstance, error) {
	for {
		instance, err := r.client.GetVM(ctx, instanceID)
		if err != nil {
			return VMInstance{}, normalizeChinaCloudRuntimeError(err)
		}
		writeVMLog(p, logFile, redactor.String(fmt.Sprintf("%s VM state: %s", p.definition.Name, nativeVMState(instance))))
		switch instance.State {
		case VMStateRunning:
			if instance.PublicIP == "" {
				return VMInstance{}, &app.ProviderError{Kind: app.ProviderErrorInternal, Message: fmt.Sprintf("%s VM is running but has no public IP", p.definition.Name)}
			}
			return instance, nil
		case VMStatePending, VMStateUnknown:
		case VMStateTerminating, VMStateTerminated:
			return VMInstance{}, &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: fmt.Sprintf("%s VM terminated before job startup", p.definition.Name)}
		case VMStateFailed:
			return VMInstance{}, &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: fmt.Sprintf("%s VM entered failed state before job startup", p.definition.Name)}
		}
		if err := p.sleep(ctx, time.Duration(r.config.PollIntervalSeconds)*time.Second); err != nil {
			return VMInstance{}, err
		}
	}
}

func (r *vmRuntime) monitorRemote(ctx context.Context, p *Provider, instanceID string, target VMRemoteTarget, logFile io.Writer, eventFile io.Writer, req app.SubmitRequest, redactor redact.Redactor) (int, string, error) {
	var logOffset int
	var eventOffset int
	for {
		if content, err := r.remote.ReadFile(ctx, target, vmRemoteLogsPath); err == nil {
			logOffset = appendVMNewLogContent(logFile, eventFile, content, logOffset, req.RunID, req.AttemptID, p.now(), redactor, p.stdout())
		}
		if content, err := r.remote.ReadFile(ctx, target, vmRemoteEventsPath); err == nil {
			eventOffset = appendVMNewEventContent(eventFile, content, eventOffset, req.RunID, req.AttemptID, p.now(), redactor)
		}
		if content, err := r.remote.ReadFile(ctx, target, vmRemoteExitPath); err == nil && strings.TrimSpace(content) != "" {
			exitCode, exitReason, err := parseVMExit(content)
			if err != nil {
				return 1, "", &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: fmt.Sprintf("%s exit marker was invalid: %v", p.definition.Name, err), Err: err}
			}
			return exitCode, exitReason, nil
		}
		instance, err := r.client.GetVM(ctx, instanceID)
		if err != nil {
			return 1, "", normalizeChinaCloudRuntimeError(err)
		}
		switch instance.State {
		case VMStateRunning, VMStatePending, VMStateUnknown:
		case VMStateTerminating, VMStateTerminated:
			return 1, "", &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: fmt.Sprintf("%s VM terminated before writing exit marker", p.definition.Name)}
		case VMStateFailed:
			return 1, "", &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: fmt.Sprintf("%s VM entered failed state", p.definition.Name)}
		}
		if err := p.sleep(ctx, time.Duration(r.config.PollIntervalSeconds)*time.Second); err != nil {
			return 1, "", err
		}
	}
}

func (r *vmRuntime) failureResult(ctx context.Context, req app.SubmitRequest, def Definition, instanceID string, providerRef string, err error, redactor redact.Redactor) (app.SubmitResult, error) {
	wrapped := normalizeChinaCloudRuntimeError(err)
	state := app.ProviderResourceStateFailed
	metadata := map[string]string{"error": wrapped.Error()}
	if !r.config.KeepInstanceOnFailure {
		if terminateErr := r.client.TerminateVM(ctx, instanceID); terminateErr != nil {
			metadata["cleanup_error"] = normalizeChinaCloudRuntimeError(terminateErr).Error()
		} else {
			state = app.ProviderResourceStateTerminating
		}
	}
	instance := VMInstance{ID: instanceID, State: VMStateFailed}
	if resourceErr := r.notifyResource(req, def, instance, providerRef, state, false, metadata); resourceErr != nil {
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: redactor.String(resourceErr.Error())}, resourceErr
	}
	return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: chinaExitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
}

func (r *vmRuntime) notifyResource(req app.SubmitRequest, def Definition, instance VMInstance, providerRef string, state app.ProviderResourceState, created bool, metadata map[string]string) error {
	callback := req.OnResourceUpdated
	if created {
		callback = req.OnResourceCreated
	}
	if callback == nil {
		return nil
	}
	observedAt := time.Now().UTC()
	return callback(app.ProviderResource{
		RunID:                req.RunID,
		AttemptID:            req.AttemptID,
		Provider:             def.Name,
		Kind:                 app.ProviderResourceKindInstance,
		ExternalID:           instance.ID,
		ProviderRef:          providerRef,
		Region:               valueOrDefault(instance.Region, r.config.Region),
		ProjectOrAccount:     r.config.ProjectOrAccount,
		State:                state,
		CreatedBySwitchboard: true,
		CleanupPolicy:        r.cleanupPolicy(),
		Metadata:             r.resourceMetadata(instance, metadata),
		LastObservedAt:       &observedAt,
	})
}

func (r *vmRuntime) resourceMetadata(instance VMInstance, extra map[string]string) map[string]string {
	metadata := map[string]string{
		"instance_type": r.config.InstanceType,
		"image_id":      r.config.ImageID,
	}
	if instance.NativeState != "" {
		metadata["native_status"] = instance.NativeState
	}
	if instance.PrivateIP != "" {
		metadata["private_ip"] = instance.PrivateIP
	}
	if instance.Zone != "" {
		metadata["zone"] = instance.Zone
	}
	for key, value := range extra {
		metadata[key] = value
	}
	return metadata
}

func (r *vmRuntime) cleanupPolicy() app.ProviderResourceCleanupPolicy {
	switch {
	case r.config.TerminateOnCompletion && !r.config.KeepInstanceOnFailure:
		return app.ProviderResourceCleanupAlways
	case r.config.TerminateOnCompletion && r.config.KeepInstanceOnFailure:
		return app.ProviderResourceCleanupOnSuccess
	case !r.config.TerminateOnCompletion && !r.config.KeepInstanceOnFailure:
		return app.ProviderResourceCleanupOnFailure
	default:
		return app.ProviderResourceCleanupNever
	}
}

func (r *vmRuntime) shouldTerminate(exitCode int) bool {
	if exitCode == 0 {
		return r.config.TerminateOnCompletion
	}
	return !r.config.KeepInstanceOnFailure
}

type vmSSHRemoteRunner struct{}

func (r vmSSHRemoteRunner) WaitForReady(ctx context.Context, target VMRemoteTarget, timeout time.Duration, sleep func(context.Context, time.Duration) error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("ssh did not become ready before timeout: %w", lastErr)
			}
			return errors.New("ssh did not become ready before timeout")
		}
		if _, err := r.run(ctx, target, "true"); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if err := sleep(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

func (r vmSSHRemoteRunner) ReadFile(ctx context.Context, target VMRemoteTarget, path string) (string, error) {
	return r.run(ctx, target, "cat "+vmShellQuote(path))
}

func (r vmSSHRemoteRunner) run(ctx context.Context, target VMRemoteTarget, remoteCommand string) (string, error) {
	args := []string{
		"-i", target.PrivateKeyPath,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(target.ConnectTimeout.Seconds())),
		target.User + "@" + target.Host,
		remoteCommand,
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh %s: %w: %s", target.Host, err, stderr.String())
	}
	return stdout.String(), nil
}

func appendVMNewLogContent(logFile io.Writer, eventFile io.Writer, content string, offset int, runID string, attemptID string, now time.Time, redactor redact.Redactor, stdout io.Writer) int {
	if offset > len(content) {
		offset = 0
	}
	if offset == len(content) {
		return offset
	}
	newContent := content[offset:]
	_, _ = io.WriteString(logFile, redactor.String(newContent))
	if stdout != nil {
		_, _ = io.WriteString(stdout, redactor.String(newContent))
	}
	scanner := bufio.NewScanner(strings.NewReader(newContent))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		parsed := event.ParseLine(scanner.Text(), runID, attemptID, now)
		if parsed.Structured {
			_ = event.WriteJSONL(eventFile, redactor.Event(parsed.Event))
		}
	}
	if err := scanner.Err(); err != nil {
		_, _ = fmt.Fprintf(logFile, "log scanner error: %s\n", redactor.String(err.Error()))
	}
	return len(content)
}

func appendVMNewEventContent(eventFile io.Writer, content string, offset int, runID string, attemptID string, now time.Time, redactor redact.Redactor) int {
	if offset > len(content) {
		offset = 0
	}
	if offset == len(content) {
		return offset
	}
	scanner := bufio.NewScanner(strings.NewReader(content[offset:]))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		parsed := event.ParseLine(scanner.Text(), runID, attemptID, now)
		if parsed.Structured {
			_ = event.WriteJSONL(eventFile, redactor.Event(parsed.Event))
		}
	}
	return len(content)
}

func parseVMExit(content string) (int, string, error) {
	var out struct {
		ExitCode   int    `json:"exit_code"`
		ExitReason string `json:"exit_reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &out); err != nil {
		return 1, "", err
	}
	if out.ExitReason == "" {
		out.ExitReason = "completed"
		if out.ExitCode != 0 {
			out.ExitReason = fmt.Sprintf("container exited with code %d", out.ExitCode)
		}
	}
	return out.ExitCode, out.ExitReason, nil
}

func writeVMLog(p *Provider, logFile io.Writer, line string) {
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

func vmShellCommand(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, vmShellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func vmShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func vmProviderRef(providerName string, id string) string {
	return providerName + ":" + id
}

func vmIDFromProviderRef(providerName string, ref string) string {
	return strings.TrimPrefix(ref, providerName+":")
}

func attemptStateFromVMState(state VMState) app.AttemptState {
	switch state {
	case VMStateRunning, VMStatePending, VMStateUnknown:
		return app.AttemptStateRunning
	case VMStateTerminated, VMStateTerminating:
		return app.AttemptStateCanceled
	case VMStateFailed:
		return app.AttemptStateFailed
	default:
		return app.AttemptStateFailed
	}
}

func normalizeChinaCloudRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	var providerErr *app.ProviderError
	if errors.As(err, &providerErr) {
		return err
	}
	return &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: err.Error(), Err: err}
}

func chinaExitCodeForProviderError(err error) int {
	switch app.ProviderErrorKindOf(err) {
	case app.ProviderErrorInvalidSpec, app.ProviderErrorAuth:
		return 10
	case app.ProviderErrorCapacity, app.ProviderErrorQuota, app.ProviderErrorNetwork, app.ProviderErrorInternal:
		return 40
	default:
		return 1
	}
}

func providerRegions(def Definition, configured string) []string {
	seen := map[string]bool{}
	var out []string
	if configured != "" {
		seen[configured] = true
		out = append(out, configured)
	}
	for _, region := range def.Regions {
		if region == "" || seen[region] {
			continue
		}
		seen[region] = true
		out = append(out, region)
	}
	return out
}

func gpuFamiliesFromHardware(shapes []app.HardwareShape) []string {
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

func providerShapeID(providerName string, region string, instanceType string) string {
	value := providerName + "-" + region + "-" + instanceType
	value = strings.ToLower(strings.ReplaceAll(value, "_", "-"))
	value = strings.Trim(value, "-")
	if value == "" {
		return providerName + "-configured"
	}
	return value
}

func safeVMName(name string, runID string) string {
	value := strings.TrimSpace(name)
	if value == "" {
		value = runID
	}
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "switchboard"
	}
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	return out
}

func nativeVMState(instance VMInstance) string {
	if instance.NativeState != "" {
		return instance.NativeState
	}
	return string(instance.State)
}

func valueOrDefault(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func requestHTTPClient(timeoutSeconds int) *http.Client {
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}
