package lambda

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/credentials"
	"github.com/anthonylu23/switchboard-cli/internal/event"
	"github.com/anthonylu23/switchboard-cli/internal/redact"
)

const ProviderName = "lambda"

type Config struct {
	RegionName               string
	InstanceTypeName         string
	SSHKeyName               string
	SSHPrivateKey            string
	ImageFamily              string
	RegistryAuth             RegistryAuth
	PollIntervalSeconds      int
	TerminateOnCompletion    bool
	TerminateOnCompletionSet bool
	KeepInstanceOnFailure    bool
	APITimeoutSeconds        int
	SSHConnectTimeoutSecs    int
	SSHReadyTimeoutSeconds   int
	BaseURL                  string
	Credentials              credentials.Resolver
}

type RegistryAuth struct {
	Server   string
	Username string
	Password string
}

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
		reasons = append(reasons, "lambda provider requires job.image")
	}
	if spec.Script != "" {
		reasons = append(reasons, "lambda provider v1 does not package local scripts; provide job.image")
	}
	for _, input := range spec.Data {
		if input.Mode == app.DataInputModeBundle {
			reasons = append(reasons, fmt.Sprintf("lambda provider v1 does not support bundled data input %q", input.Name))
		}
	}
	if p.config.RegionName == "" {
		reasons = append(reasons, "lambda.region_name is required")
	}
	if p.config.InstanceTypeName == "" {
		reasons = append(reasons, "lambda.instance_type_name is required")
	}
	if p.config.SSHKeyName == "" {
		reasons = append(reasons, "lambda.ssh_key_name is required")
	}
	if p.config.SSHPrivateKey == "" {
		reasons = append(reasons, "lambda.ssh_private_key is required")
	}
	return app.SupportReport{Supported: len(reasons) == 0, Reasons: reasons}
}

func (p *Provider) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	shapes := p.hardwareShapes(ctx)
	for _, shape := range shapes {
		if shape.MachineType == p.config.InstanceTypeName && shape.Region == p.config.RegionName {
			return app.CostEstimate{HourlyUSD: shape.OnDemandHourlyUSD, Currency: "USD"}, nil
		}
	}
	return app.CostEstimate{Currency: "USD"}, nil
}

func (p *Provider) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
	redactor := redact.FromEnvironment(req.JobSpec.Env, req.RuntimeEnv, p.registryAuthRedactionEnv())
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

	client, err := p.clientFor()
	if err != nil {
		wrapped := normalizeError(err)
		return app.SubmitResult{ExitCode: exitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
	}

	launchReq := p.launchRequest(req)
	p.writeLog(logFile, redactor.String(fmt.Sprintf("lambda instance launch: %s", req.JobSpec.Name)))
	instanceIDs, err := client.LaunchInstance(ctx, launchReq)
	if err != nil {
		wrapped := normalizeError(err)
		return app.SubmitResult{ExitCode: exitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
	}
	if len(instanceIDs) == 0 {
		wrapped := &app.ProviderError{Kind: app.ProviderErrorInternal, Message: "lambda launch returned no instance IDs"}
		return app.SubmitResult{ExitCode: exitCodeForProviderError(wrapped), ExitReason: wrapped.Error()}, wrapped
	}
	instanceID := instanceIDs[0]
	providerRef := providerRef(instanceID)
	if err := p.notifyResource(req, instanceID, providerRef, app.ProviderResourceStateBooting, true, map[string]string{"native_status": "booting"}); err != nil {
		_ = client.TerminateInstances(ctx, []string{instanceID})
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
	}
	if req.OnStarted != nil {
		if err := req.OnStarted(app.ProviderJobRef{ID: providerRef}); err != nil {
			_ = client.TerminateInstances(ctx, []string{instanceID})
			return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
		}
	}
	p.writeLog(logFile, redactor.String(fmt.Sprintf("lambda instance started: %s", instanceID)))

	instance, err := p.waitForActive(ctx, client, instanceID, logFile, redactor)
	if err != nil {
		return p.failureResult(ctx, client, req, instanceID, providerRef, err, redactor)
	}
	if err := p.notifyResource(req, instanceID, providerRef, app.ProviderResourceStateRunning, false, map[string]string{"native_status": instance.Status}); err != nil {
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
	}
	target := RemoteTarget{
		Host:           instance.IP,
		User:           "ubuntu",
		PrivateKeyPath: p.config.SSHPrivateKey,
		ConnectTimeout: time.Duration(p.config.SSHConnectTimeoutSecs) * time.Second,
	}
	if err := p.remote.WaitForReady(ctx, target, time.Duration(p.config.SSHReadyTimeoutSeconds)*time.Second, p.sleep); err != nil {
		wrapped := &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: fmt.Sprintf("lambda ssh not ready: %v", err), Err: err}
		return p.failureResult(ctx, client, req, instanceID, providerRef, wrapped, redactor)
	}

	exitCode, exitReason, err := p.monitorRemote(ctx, client, instanceID, target, logFile, eventFile, req, redactor)
	if err != nil {
		return p.failureResult(ctx, client, req, instanceID, providerRef, err, redactor)
	}
	finalState := app.ProviderResourceStateSucceeded
	if exitCode != 0 {
		finalState = app.ProviderResourceStateFailed
	}
	metadata := map[string]string{"exit_reason": exitReason}
	if p.shouldTerminate(exitCode) {
		if err := client.TerminateInstances(ctx, []string{instanceID}); err != nil {
			p.writeLog(logFile, redactor.String(fmt.Sprintf("lambda terminate failed: %v", normalizeError(err))))
			metadata["cleanup_error"] = normalizeError(err).Error()
		} else {
			p.writeLog(logFile, redactor.String(fmt.Sprintf("lambda instance terminated: %s", instanceID)))
			finalState = app.ProviderResourceStateTerminating
		}
	}
	if err := p.notifyResource(req, instanceID, providerRef, finalState, false, metadata); err != nil {
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
	}
	return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: exitCode, ExitReason: redactor.String(exitReason)}, nil
}

func (p *Provider) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	client, err := p.clientFor()
	if err != nil {
		return app.ProviderJobStatus{}, normalizeError(err)
	}
	instance, err := client.GetInstance(ctx, instanceIDFromRef(ref.ID))
	if err != nil {
		return app.ProviderJobStatus{}, normalizeError(err)
	}
	return app.ProviderJobStatus{State: attemptStateFromInstanceStatus(instance.Status)}, nil
}

func (p *Provider) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, fmt.Errorf("lambda provider logs are read from run artifacts")
}

func (p *Provider) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	client, err := p.clientFor()
	if err != nil {
		return normalizeError(err)
	}
	return normalizeError(client.TerminateInstances(ctx, []string{instanceIDFromRef(ref.ID)}))
}

func (p *Provider) launchRequest(req app.SubmitRequest) LaunchInstanceRequest {
	launchReq := LaunchInstanceRequest{
		RegionName:       p.config.RegionName,
		InstanceTypeName: p.config.InstanceTypeName,
		SSHKeyNames:      []string{p.config.SSHKeyName},
		Hostname:         hostname(req.RunID),
		Name:             req.JobSpec.Name,
		UserData:         cloudInitUserData(req, p.config.RegistryAuth),
		Tags: []RequestedTagEntry{
			{Key: "switchboard:run-id", Value: req.RunID},
			{Key: "switchboard:attempt-id", Value: req.AttemptID},
		},
	}
	if p.config.ImageFamily != "" {
		launchReq.Image = map[string]string{"family": p.config.ImageFamily}
	}
	return launchReq
}

func (p *Provider) notifyResource(req app.SubmitRequest, instanceID string, providerRef string, state app.ProviderResourceState, created bool, metadata map[string]string) error {
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
		ExternalID:           instanceID,
		ProviderRef:          providerRef,
		Region:               p.config.RegionName,
		State:                state,
		CreatedBySwitchboard: true,
		CleanupPolicy:        p.cleanupPolicy(),
		Metadata:             p.resourceMetadata(metadata),
		LastObservedAt:       &observedAt,
	})
}

func (p *Provider) resourceMetadata(extra map[string]string) map[string]string {
	metadata := map[string]string{
		"instance_type": p.config.InstanceTypeName,
	}
	if p.config.ImageFamily != "" {
		metadata["image_family"] = p.config.ImageFamily
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

func (p *Provider) waitForActive(ctx context.Context, client Client, instanceID string, logFile io.Writer, redactor redact.Redactor) (Instance, error) {
	for {
		instance, err := client.GetInstance(ctx, instanceID)
		if err != nil {
			return Instance{}, normalizeError(err)
		}
		p.writeLog(logFile, redactor.String(fmt.Sprintf("lambda instance status: %s", instance.Status)))
		switch instance.Status {
		case "active":
			if instance.IP == "" {
				return Instance{}, &app.ProviderError{Kind: app.ProviderErrorInternal, Message: "lambda instance is active but has no public IP"}
			}
			return instance, nil
		case "booting":
		case "preempted":
			return Instance{}, &app.ProviderError{Kind: app.ProviderErrorCapacity, Message: "lambda instance was preempted before job startup"}
		case "unhealthy":
			return Instance{}, &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: "lambda instance became unhealthy before job startup"}
		case "terminated", "terminating":
			return Instance{}, &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: "lambda instance terminated before job startup"}
		default:
			return Instance{}, &app.ProviderError{Kind: app.ProviderErrorUnknown, Message: fmt.Sprintf("lambda instance entered unknown status %q", instance.Status)}
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
				return 1, "", &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: fmt.Sprintf("lambda exit marker was invalid: %v", err), Err: err}
			}
			return exitCode, exitReason, nil
		}
		instance, err := client.GetInstance(ctx, instanceID)
		if err != nil {
			return 1, "", normalizeError(err)
		}
		switch instance.Status {
		case "active", "booting":
		case "preempted":
			return 1, "", &app.ProviderError{Kind: app.ProviderErrorCapacity, Message: "lambda instance was preempted"}
		case "unhealthy":
			return 1, "", &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: "lambda instance became unhealthy"}
		case "terminated", "terminating":
			return 1, "", &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: "lambda instance terminated before writing exit marker"}
		default:
			return 1, "", &app.ProviderError{Kind: app.ProviderErrorUnknown, Message: fmt.Sprintf("lambda instance entered unknown status %q", instance.Status)}
		}
		if err := p.sleep(ctx, time.Duration(p.config.PollIntervalSeconds)*time.Second); err != nil {
			return 1, "", err
		}
	}
}

func appendNewLogContent(logFile io.Writer, eventFile io.Writer, content string, offset int, runID string, attemptID string, now time.Time, redactor redact.Redactor, stdout io.Writer, seenEvents map[string]bool) int {
	if offset > len(content) {
		offset = 0
	}
	if offset == len(content) {
		return offset
	}
	newContent, nextOffset := completeContent(content, offset)
	if newContent == "" {
		return offset
	}
	redactedContent := redactedLogContent(newContent, redactor)
	_, _ = io.WriteString(logFile, redactedContent)
	if stdout != nil {
		_, _ = io.WriteString(stdout, redactedContent)
	}
	scanner := bufio.NewScanner(strings.NewReader(newContent))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		parsed := event.ParseLine(line, runID, attemptID, now)
		if parsed.Structured {
			if seenEvents[line] {
				continue
			}
			seenEvents[line] = true
			_ = event.WriteJSONL(eventFile, redactor.Event(parsed.Event))
		}
	}
	if err := scanner.Err(); err != nil {
		_, _ = fmt.Fprintf(logFile, "log scanner error: %s\n", redactor.String(err.Error()))
	}
	return nextOffset
}

func appendNewEventContent(eventFile io.Writer, content string, offset int, runID string, attemptID string, now time.Time, redactor redact.Redactor, seenEvents map[string]bool) int {
	if offset > len(content) {
		offset = 0
	}
	if offset == len(content) {
		return offset
	}
	newContent, nextOffset := completeContent(content, offset)
	if newContent == "" {
		return offset
	}
	scanner := bufio.NewScanner(strings.NewReader(newContent))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		parsed := event.ParseLine(line, runID, attemptID, now)
		if parsed.Structured {
			if seenEvents[line] {
				continue
			}
			seenEvents[line] = true
			_ = event.WriteJSONL(eventFile, redactor.Event(parsed.Event))
		}
	}
	return nextOffset
}

func completeContent(content string, offset int) (string, int) {
	if offset >= len(content) {
		return "", offset
	}
	next := content[offset:]
	if strings.HasSuffix(next, "\n") {
		return next, len(content)
	}
	lastNewline := strings.LastIndex(next, "\n")
	if lastNewline < 0 {
		return "", offset
	}
	end := offset + lastNewline + 1
	return content[offset:end], end
}

func redactedLogContent(content string, redactor redact.Redactor) string {
	var out strings.Builder
	for _, part := range strings.SplitAfter(content, "\n") {
		if part == "" {
			continue
		}
		if strings.HasSuffix(part, "\n") {
			line := strings.TrimSuffix(part, "\n")
			out.WriteString(redactor.Line(line))
			out.WriteString("\n")
			continue
		}
		out.WriteString(redactor.Line(part))
	}
	return out.String()
}

func parseExit(content string) (int, string, error) {
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

func (p *Provider) failureResult(ctx context.Context, client Client, req app.SubmitRequest, instanceID string, providerRef string, err error, redactor redact.Redactor) (app.SubmitResult, error) {
	wrapped := normalizeError(err)
	state := app.ProviderResourceStateFailed
	metadata := map[string]string{"error": wrapped.Error()}
	if !p.config.KeepInstanceOnFailure {
		if terminateErr := client.TerminateInstances(ctx, []string{instanceID}); terminateErr != nil {
			metadata["cleanup_error"] = normalizeError(terminateErr).Error()
		} else {
			state = app.ProviderResourceStateTerminating
		}
	}
	if resourceErr := p.notifyResource(req, instanceID, providerRef, state, false, metadata); resourceErr != nil {
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: redactor.String(resourceErr.Error())}, resourceErr
	}
	return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: exitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
}

func (p *Provider) hardwareShapes(ctx context.Context) []app.HardwareShape {
	client, err := p.clientFor()
	if err != nil {
		return p.configuredHardwareShape()
	}
	instanceTypes, err := client.ListInstanceTypes(ctx)
	if err != nil {
		return p.configuredHardwareShape()
	}
	var shapes []app.HardwareShape
	for _, item := range instanceTypes {
		instanceType := item.InstanceType
		for _, region := range item.RegionsWithCapacityAvailable {
			shapes = append(shapes, hardwareShape(instanceType, region, "available", "reported by Lambda instance-types API"))
		}
		if len(item.RegionsWithCapacityAvailable) == 0 && instanceType.Name == p.config.InstanceTypeName {
			shapes = append(shapes, hardwareShape(instanceType, Region{Name: p.config.RegionName}, "unavailable", "Lambda API reported no current regional capacity"))
		}
	}
	if len(shapes) == 0 {
		return p.configuredHardwareShape()
	}
	sort.Slice(shapes, func(i, j int) bool {
		if shapes[i].Region == shapes[j].Region {
			return shapes[i].ID < shapes[j].ID
		}
		return shapes[i].Region < shapes[j].Region
	})
	return shapes
}

func hardwareShape(instanceType InstanceType, region Region, availabilityHint string, availabilityReason string) app.HardwareShape {
	vramPerGPU := vramGBPerGPU(instanceType.GPUDescription)
	totalVRAM := vramPerGPU * instanceType.Specs.GPUs
	return app.HardwareShape{
		ID:                 "lambda-" + region.Name + "-" + strings.ReplaceAll(instanceType.Name, "_", "-"),
		Provider:           ProviderName,
		Region:             region.Name,
		MachineType:        instanceType.Name,
		AcceleratorType:    instanceType.GPUDescription,
		AcceleratorCount:   instanceType.Specs.GPUs,
		GPUFamily:          gpuFamily(instanceType.GPUDescription),
		VRAMGBPerGPU:       vramPerGPU,
		TotalVRAMGB:        totalVRAM,
		OnDemandHourlyUSD:  float64(instanceType.PriceCentsPerHour) / 100,
		SupportsOnDemand:   true,
		AvailabilityHint:   availabilityHint,
		AvailabilityReason: availabilityReason,
	}
}

func (p *Provider) configuredHardwareShape() []app.HardwareShape {
	return []app.HardwareShape{{
		ID:                 "lambda-" + p.config.RegionName + "-" + strings.ReplaceAll(p.config.InstanceTypeName, "_", "-"),
		Provider:           ProviderName,
		Region:             p.config.RegionName,
		MachineType:        p.config.InstanceTypeName,
		SupportsOnDemand:   true,
		AvailabilityHint:   "configured",
		AvailabilityReason: "configured from lambda.instance_type_name and lambda.region_name",
	}}
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
		switch {
		case apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden || strings.Contains(apiErr.Code, "api-key"):
			kind = app.ProviderErrorAuth
		case strings.Contains(apiErr.Code, "quota"):
			kind = app.ProviderErrorQuota
		case strings.Contains(apiErr.Code, "capacity") || strings.Contains(apiErr.Code, "unavailable") || strings.HasPrefix(apiErr.Code, "provider/"):
			kind = app.ProviderErrorCapacity
		case apiErr.StatusCode == http.StatusBadRequest || strings.Contains(apiErr.Code, "invalid"):
			kind = app.ProviderErrorInvalidSpec
		case apiErr.StatusCode >= 500:
			kind = app.ProviderErrorInternal
		}
		return &app.ProviderError{Kind: kind, Message: "lambda api: " + apiErr.Error(), Err: err}
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
	switch status {
	case "booting", "active":
		return app.AttemptStateRunning
	case "terminating", "terminated":
		return app.AttemptStateCanceled
	case "unhealthy", "preempted":
		return app.AttemptStateFailed
	default:
		return app.AttemptStateFailed
	}
}

func providerRef(instanceID string) string {
	return ProviderName + ":" + instanceID
}

func instanceIDFromRef(ref string) string {
	return strings.TrimPrefix(ref, ProviderName+":")
}

func hostname(runID string) string {
	value := strings.ToLower(runID)
	value = strings.ReplaceAll(value, "_", "-")
	value = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "switchboard"
	}
	if len(value) > 52 {
		value = value[:52]
	}
	return "sw-" + value
}

func (p *Provider) shouldTerminate(exitCode int) bool {
	if exitCode == 0 {
		return p.config.TerminateOnCompletion
	}
	return !p.config.KeepInstanceOnFailure
}

func (p *Provider) registryAuthRedactionEnv() map[string]string {
	if p.config.RegistryAuth.Password == "" {
		return nil
	}
	return map[string]string{
		"SWITCHBOARD_LAMBDA_REGISTRY_PASSWORD": p.config.RegistryAuth.Password,
	}
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

func withDefaults(config Config) Config {
	if config.PollIntervalSeconds == 0 {
		config.PollIntervalSeconds = 30
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
	if !config.TerminateOnCompletionSet {
		config.TerminateOnCompletion = true
	}
	return config
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

func gpuFamily(description string) string {
	normalized := strings.ToUpper(description)
	for _, family := range []string{"H100", "A100", "A10", "A6000", "RTX", "V100"} {
		if strings.Contains(normalized, family) {
			return family
		}
	}
	return ""
}

func vramGBPerGPU(description string) int {
	matches := regexp.MustCompile(`(?i)(\d+)\s*GB`).FindStringSubmatch(description)
	if len(matches) != 2 {
		return 0
	}
	var vram int
	_, _ = fmt.Sscanf(matches[1], "%d", &vram)
	return vram
}
