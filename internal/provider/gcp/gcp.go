package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	aiplatform "cloud.google.com/go/aiplatform/apiv1"
	"cloud.google.com/go/aiplatform/apiv1/aiplatformpb"
	logging "cloud.google.com/go/logging/apiv2"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/event"
	"github.com/anthonylu23/switchboard-cli/internal/redact"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

const ProviderName = "gcp"

type Config struct {
	ProjectID           string
	Location            string
	OutputURIPrefix     string
	MachineType         string
	AcceleratorType     string
	AcceleratorCount    int32
	BootDiskType        string
	BootDiskSizeGB      int32
	ServiceAccount      string
	Network             string
	PollIntervalSeconds int
	EstimateHourlyUSD   float64
}

type Client interface {
	ValidateAuth(ctx context.Context, parent string) error
	CreateCustomJob(ctx context.Context, req *aiplatformpb.CreateCustomJobRequest) (*aiplatformpb.CustomJob, error)
	GetCustomJob(ctx context.Context, name string) (*aiplatformpb.CustomJob, error)
	CancelCustomJob(ctx context.Context, name string) error
	ListLogEntries(ctx context.Context, req *loggingpb.ListLogEntriesRequest) ([]*loggingpb.LogEntry, error)
	Close() error
}

type Provider struct {
	config    Config
	client    Client
	newClient func(context.Context, Config) (Client, error)
	Stdout    io.Writer
	Stderr    io.Writer
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
}

func New(config Config, stdout io.Writer, stderr io.Writer) *Provider {
	return &Provider{
		config:    withDefaults(config),
		newClient: newRealClient,
		Stdout:    stdout,
		Stderr:    stderr,
		Now:       time.Now,
		Sleep:     sleep,
	}
}

func NewWithClient(config Config, client Client, stdout io.Writer, stderr io.Writer) *Provider {
	return &Provider{
		config: withDefaults(config),
		client: client,
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
	client, err := p.clientFor(ctx)
	if err != nil {
		return normalizeError(err)
	}
	return normalizeError(client.ValidateAuth(ctx, p.parent()))
}

func (p *Provider) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	return app.ProviderCapabilities{
		GPUFamilies:             gpuFamilies(p.config.AcceleratorType),
		Regions:                 []string{p.config.Location},
		SupportsOnDemand:        true,
		SupportsDockerImage:     true,
		SupportsLocalScript:     false,
		SupportsDataBundle:      false,
		SupportedURISchemes:     []string{"gs"},
		SupportsObjectStorePull: true,
	}, nil
}

func (p *Provider) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	var reasons []string
	if spec.Image == "" {
		reasons = append(reasons, "gcp provider requires job.image")
	}
	if spec.Script != "" {
		reasons = append(reasons, "gcp provider v1 does not package local scripts; provide job.image")
	}
	for _, input := range spec.Data {
		switch input.Mode {
		case app.DataInputModeBundle:
			reasons = append(reasons, fmt.Sprintf("gcp provider v1 does not support bundled data input %q", input.Name))
		case app.DataInputModeURI:
			if !strings.HasPrefix(strings.ToLower(input.Source), "gs://") {
				reasons = append(reasons, fmt.Sprintf("gcp provider v1 only supports gs:// URI data input %q", input.Name))
			}
		}
	}
	if p.config.ProjectID == "" {
		reasons = append(reasons, "gcp.project_id is required")
	}
	if p.config.Location == "" {
		reasons = append(reasons, "gcp.location is required")
	}
	if p.config.OutputURIPrefix == "" {
		reasons = append(reasons, "gcp.output_uri_prefix is required")
	}
	if p.config.MachineType == "" {
		reasons = append(reasons, "gcp.machine_type is required")
	}
	acceleratorConfigured := p.config.AcceleratorType != ""
	_, acceleratorKnown := acceleratorTypeValue(p.config.AcceleratorType)
	if acceleratorConfigured && !acceleratorKnown {
		reasons = append(reasons, fmt.Sprintf("gcp.accelerator_type %q is not recognized", p.config.AcceleratorType))
	}
	if p.config.AcceleratorCount < 0 {
		reasons = append(reasons, "gcp.accelerator_count must be greater than or equal to 0")
	}
	if p.config.AcceleratorCount > 0 && !acceleratorConfigured {
		reasons = append(reasons, "gcp.accelerator_type is required when gcp.accelerator_count is greater than 0")
	}
	if acceleratorConfigured && acceleratorKnown && p.config.AcceleratorCount <= 0 {
		reasons = append(reasons, "gcp.accelerator_count must be greater than 0 when gcp.accelerator_type is set")
	}
	return app.SupportReport{Supported: len(reasons) == 0, Reasons: reasons}
}

func (p *Provider) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	return app.CostEstimate{HourlyUSD: p.config.EstimateHourlyUSD, Currency: "USD"}, nil
}

func (p *Provider) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
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

	client, err := p.clientFor(ctx)
	if err != nil {
		return app.SubmitResult{}, normalizeError(err)
	}
	customJobReq := p.createRequest(req)
	p.writeLog(logFile, redactor.String(fmt.Sprintf("gcp custom job submit: %s", req.JobSpec.Name)))
	customJob, err := client.CreateCustomJob(ctx, customJobReq)
	if err != nil {
		wrapped := normalizeError(err)
		return app.SubmitResult{ExitCode: exitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
	}
	providerRef := customJob.GetName()
	if providerRef == "" {
		providerRef = fmt.Sprintf("%s/customJobs/%s", p.parent(), req.AttemptID)
	}
	if req.OnStarted != nil {
		if err := req.OnStarted(app.ProviderJobRef{ID: providerRef}); err != nil {
			return app.SubmitResult{}, err
		}
	}
	p.writeLog(logFile, redactor.String(fmt.Sprintf("gcp custom job started: %s", providerRef)))

	seenLogs := map[string]bool{}
	for {
		p.drainLogs(ctx, client, providerRef, seenLogs, logFile, eventFile, req.RunID, req.AttemptID, redactor)
		current, err := client.GetCustomJob(ctx, providerRef)
		if err != nil {
			wrapped := normalizeError(err)
			return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: exitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
		}
		switch current.GetState() {
		case aiplatformpb.JobState_JOB_STATE_SUCCEEDED:
			p.drainLogs(ctx, client, providerRef, seenLogs, logFile, eventFile, req.RunID, req.AttemptID, redactor)
			p.writeLog(logFile, redactor.String("gcp custom job completed"))
			return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 0, ExitReason: "completed"}, nil
		case aiplatformpb.JobState_JOB_STATE_FAILED, aiplatformpb.JobState_JOB_STATE_EXPIRED:
			wrapped := providerErrorFromJob(current, app.ProviderErrorRuntime)
			return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: exitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
		case aiplatformpb.JobState_JOB_STATE_CANCELLED:
			reason := jobErrorMessage(current)
			if reason == "" {
				reason = "gcp custom job canceled"
			}
			wrapped := &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: reason}
			return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 130, ExitReason: redactor.String(reason)}, wrapped
		default:
			if err := p.sleep(ctx, p.pollInterval()); err != nil {
				return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 130, ExitReason: "context canceled"}, err
			}
		}
	}
}

func (p *Provider) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	client, err := p.clientFor(ctx)
	if err != nil {
		return app.ProviderJobStatus{}, normalizeError(err)
	}
	job, err := client.GetCustomJob(ctx, ref.ID)
	if err != nil {
		return app.ProviderJobStatus{}, normalizeError(err)
	}
	return app.ProviderJobStatus{State: attemptState(job.GetState())}, nil
}

func (p *Provider) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, fmt.Errorf("gcp provider logs are read from run artifacts")
}

func (p *Provider) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	if project, location, ok := projectLocationFromRef(ref.ID); ok && p.client == nil {
		p.config.ProjectID = project
		p.config.Location = location
	}
	client, err := p.clientFor(ctx)
	if err != nil {
		return normalizeError(err)
	}
	return normalizeError(client.CancelCustomJob(ctx, ref.ID))
}

func (p *Provider) createRequest(req app.SubmitRequest) *aiplatformpb.CreateCustomJobRequest {
	env := make([]*aiplatformpb.EnvVar, 0, len(req.JobSpec.Env)+len(req.RuntimeEnv))
	for k, v := range req.JobSpec.Env {
		if v == "" {
			continue
		}
		env = append(env, &aiplatformpb.EnvVar{Name: k, Value: v})
	}
	for k, v := range req.RuntimeEnv {
		if v == "" {
			continue
		}
		env = append(env, &aiplatformpb.EnvVar{Name: k, Value: v})
	}
	machine := &aiplatformpb.MachineSpec{
		MachineType:      p.config.MachineType,
		AcceleratorType:  acceleratorType(p.config.AcceleratorType),
		AcceleratorCount: p.config.AcceleratorCount,
	}
	worker := &aiplatformpb.WorkerPoolSpec{
		MachineSpec:  machine,
		ReplicaCount: 1,
		DiskSpec: &aiplatformpb.DiskSpec{
			BootDiskType:   p.config.BootDiskType,
			BootDiskSizeGb: p.config.BootDiskSizeGB,
		},
		Task: &aiplatformpb.WorkerPoolSpec_ContainerSpec{ContainerSpec: &aiplatformpb.ContainerSpec{
			ImageUri: req.JobSpec.Image,
			Command:  append([]string(nil), req.JobSpec.Command...),
			Args:     append([]string(nil), req.JobSpec.Args...),
			Env:      env,
		}},
	}
	spec := &aiplatformpb.CustomJobSpec{
		WorkerPoolSpecs: []*aiplatformpb.WorkerPoolSpec{worker},
		BaseOutputDirectory: &aiplatformpb.GcsDestination{
			OutputUriPrefix: strings.TrimRight(p.config.OutputURIPrefix, "/") + "/" + req.RunID,
		},
		ServiceAccount: p.config.ServiceAccount,
		Network:        p.config.Network,
	}
	return &aiplatformpb.CreateCustomJobRequest{
		Parent: p.parent(),
		CustomJob: &aiplatformpb.CustomJob{
			DisplayName: safeDisplayName(req.JobSpec.Name, req.RunID),
			JobSpec:     spec,
			Labels: map[string]string{
				"switchboard_run_id":     labelValue(req.RunID),
				"switchboard_attempt_id": labelValue(req.AttemptID),
			},
		},
	}
}

func (p *Provider) drainLogs(ctx context.Context, client Client, providerRef string, seen map[string]bool, logFile io.Writer, eventFile io.Writer, runID string, attemptID string, redactor redact.Redactor) {
	entries, err := client.ListLogEntries(ctx, &loggingpb.ListLogEntriesRequest{
		ResourceNames: []string{"projects/" + p.config.ProjectID},
		Filter:        logFilter(providerRef),
		OrderBy:       "timestamp asc",
		PageSize:      1000,
	})
	if err != nil {
		p.writeLog(logFile, redactor.String(fmt.Sprintf("gcp log fetch failed: %v", err)))
		return
	}
	for _, entry := range entries {
		key := entry.GetInsertId()
		if key == "" {
			key = fmt.Sprintf("%s:%s", entry.GetTimestamp().AsTime().Format(time.RFC3339Nano), logPayload(entry))
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		line := logPayload(entry)
		if strings.TrimSpace(line) == "" {
			continue
		}
		redactedLine := redactor.Line(line)
		_, _ = fmt.Fprintln(logFile, redactedLine)
		_, _ = fmt.Fprintln(p.stdout(), redactedLine)
		parsed := event.ParseLine(line, runID, attemptID, p.now())
		if parsed.Structured {
			_ = event.WriteJSONL(eventFile, redactor.Event(parsed.Event))
		}
	}
}

func (p *Provider) clientFor(ctx context.Context) (Client, error) {
	if p.client != nil {
		return p.client, nil
	}
	if p.newClient == nil {
		p.newClient = newRealClient
	}
	client, err := p.newClient(ctx, p.config)
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

func (p *Provider) parent() string {
	return fmt.Sprintf("projects/%s/locations/%s", p.config.ProjectID, p.config.Location)
}

func (p *Provider) pollInterval() time.Duration {
	return time.Duration(p.config.PollIntervalSeconds) * time.Second
}

type realClient struct {
	jobs *aiplatform.JobClient
	logs *logging.Client
}

func newRealClient(ctx context.Context, config Config) (Client, error) {
	endpoint := fmt.Sprintf("%s-aiplatform.googleapis.com:443", config.Location)
	jobs, err := aiplatform.NewJobClient(ctx, option.WithEndpoint(endpoint))
	if err != nil {
		return nil, err
	}
	logs, err := logging.NewClient(ctx)
	if err != nil {
		_ = jobs.Close()
		return nil, err
	}
	return &realClient{jobs: jobs, logs: logs}, nil
}

func (c *realClient) ValidateAuth(ctx context.Context, parent string) error {
	it := c.jobs.ListCustomJobs(ctx, &aiplatformpb.ListCustomJobsRequest{
		Parent:   parent,
		PageSize: 1,
		ReadMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}},
	})
	_, err := it.Next()
	if err == iterator.Done {
		return nil
	}
	return err
}

func (c *realClient) CreateCustomJob(ctx context.Context, req *aiplatformpb.CreateCustomJobRequest) (*aiplatformpb.CustomJob, error) {
	return c.jobs.CreateCustomJob(ctx, req)
}

func (c *realClient) GetCustomJob(ctx context.Context, name string) (*aiplatformpb.CustomJob, error) {
	return c.jobs.GetCustomJob(ctx, &aiplatformpb.GetCustomJobRequest{Name: name})
}

func (c *realClient) CancelCustomJob(ctx context.Context, name string) error {
	return c.jobs.CancelCustomJob(ctx, &aiplatformpb.CancelCustomJobRequest{Name: name})
}

func (c *realClient) ListLogEntries(ctx context.Context, req *loggingpb.ListLogEntriesRequest) ([]*loggingpb.LogEntry, error) {
	var entries []*loggingpb.LogEntry
	it := c.logs.ListLogEntries(ctx, req)
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
}

func (c *realClient) Close() error {
	if c == nil {
		return nil
	}
	if c.logs != nil {
		_ = c.logs.Close()
	}
	if c.jobs != nil {
		return c.jobs.Close()
	}
	return nil
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
	if config.Location == "" {
		config.Location = "us-central1"
	}
	if config.MachineType == "" {
		config.MachineType = "n1-standard-4"
	}
	if config.BootDiskType == "" {
		config.BootDiskType = "pd-ssd"
	}
	if config.BootDiskSizeGB == 0 {
		config.BootDiskSizeGB = 100
	}
	if config.PollIntervalSeconds == 0 {
		config.PollIntervalSeconds = 30
	}
	return config
}

func acceleratorType(value string) aiplatformpb.AcceleratorType {
	accelerator, _ := acceleratorTypeValue(value)
	return accelerator
}

func acceleratorTypeValue(value string) (aiplatformpb.AcceleratorType, bool) {
	if value == "" {
		return aiplatformpb.AcceleratorType_ACCELERATOR_TYPE_UNSPECIFIED, true
	}
	key := strings.ToUpper(strings.ReplaceAll(value, "-", "_"))
	if !strings.HasPrefix(key, "NVIDIA") && !strings.HasPrefix(key, "ACCELERATOR") {
		key = "NVIDIA_" + key
	}
	if number, ok := aiplatformpb.AcceleratorType_value[key]; ok {
		return aiplatformpb.AcceleratorType(number), true
	}
	return aiplatformpb.AcceleratorType_ACCELERATOR_TYPE_UNSPECIFIED, false
}

func gpuFamilies(value string) []string {
	if value == "" {
		return nil
	}
	return []string{strings.ToLower(strings.ReplaceAll(value, "_", "-"))}
}

func attemptState(state aiplatformpb.JobState) app.AttemptState {
	switch state {
	case aiplatformpb.JobState_JOB_STATE_SUCCEEDED, aiplatformpb.JobState_JOB_STATE_PARTIALLY_SUCCEEDED:
		return app.AttemptStateSucceeded
	case aiplatformpb.JobState_JOB_STATE_FAILED, aiplatformpb.JobState_JOB_STATE_EXPIRED:
		return app.AttemptStateFailed
	case aiplatformpb.JobState_JOB_STATE_CANCELLED:
		return app.AttemptStateCanceled
	default:
		return app.AttemptStateRunning
	}
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	return &app.ProviderError{Kind: providerErrorKindForCode(status.Code(err)), Message: err.Error(), Err: err}
}

func providerErrorFromJob(job *aiplatformpb.CustomJob, fallback app.ProviderErrorKind) *app.ProviderError {
	kind := fallback
	if job.GetError() != nil && job.GetError().GetCode() != 0 {
		kind = providerErrorKindForCode(codes.Code(job.GetError().GetCode()))
	}
	return &app.ProviderError{Kind: kind, Message: jobErrorMessage(job)}
}

func providerErrorKindForCode(code codes.Code) app.ProviderErrorKind {
	switch code {
	case codes.Unauthenticated, codes.PermissionDenied:
		return app.ProviderErrorAuth
	case codes.ResourceExhausted:
		return app.ProviderErrorQuota
	case codes.Unavailable:
		return app.ProviderErrorCapacity
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound:
		return app.ProviderErrorInvalidSpec
	case codes.DeadlineExceeded:
		return app.ProviderErrorNetwork
	case codes.Internal:
		return app.ProviderErrorInternal
	default:
		return app.ProviderErrorUnknown
	}
}

func exitCodeForProviderError(err error) int {
	switch app.ProviderErrorKindOf(err) {
	case app.ProviderErrorInvalidSpec, app.ProviderErrorAuth:
		return 10
	case app.ProviderErrorQuota, app.ProviderErrorCapacity, app.ProviderErrorNetwork:
		return 30
	default:
		return 1
	}
}

func jobErrorMessage(job *aiplatformpb.CustomJob) string {
	if job.GetError() != nil && job.GetError().GetMessage() != "" {
		return job.GetError().GetMessage()
	}
	return fmt.Sprintf("gcp custom job ended with state %s", job.GetState().String())
}

func logPayload(entry *loggingpb.LogEntry) string {
	if entry.GetTextPayload() != "" {
		return entry.GetTextPayload()
	}
	if entry.GetJsonPayload() != nil {
		content, err := json.Marshal(entry.GetJsonPayload().AsMap())
		if err == nil {
			return string(content)
		}
	}
	if entry.GetProtoPayload() != nil {
		return entry.GetProtoPayload().String()
	}
	return ""
}

func logFilter(providerRef string) string {
	jobID := providerRef
	if idx := strings.LastIndex(providerRef, "/"); idx >= 0 {
		jobID = providerRef[idx+1:]
	}
	return fmt.Sprintf(`resource.type="ml_job" AND resource.labels.job_id="%s"`, jobID)
}

func projectLocationFromRef(ref string) (string, string, bool) {
	parts := strings.Split(ref, "/")
	for i := 0; i+3 < len(parts); i++ {
		if parts[i] == "projects" && parts[i+2] == "locations" {
			return parts[i+1], parts[i+3], true
		}
	}
	return "", "", false
}

func safeDisplayName(name string, runID string) string {
	value := strings.TrimSpace(name)
	if value == "" {
		value = runID
	}
	value = strings.ReplaceAll(value, "/", "-")
	if len(value) > 128 {
		value = value[:128]
	}
	return value
}

func labelValue(value string) string {
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "_", "-")
	if len(value) > 63 {
		value = value[:63]
	}
	return value
}
