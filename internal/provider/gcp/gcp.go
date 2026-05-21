package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	cloudbilling "google.golang.org/api/cloudbilling/v1"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

const ProviderName = "gcp"
const computeEngineServiceID = "services/6F81-5844-456A"

type Config struct {
	ProjectID                  string
	Location                   string
	OutputURIPrefix            string
	MachineType                string
	AcceleratorType            string
	AcceleratorCount           int32
	BootDiskType               string
	BootDiskSizeGB             int32
	ServiceAccount             string
	Network                    string
	PollIntervalSeconds        int
	EstimateHourlyUSD          float64
	ArtifactRegistryRepository string
}

type Client interface {
	ValidateAuth(ctx context.Context, parent string) error
	CreateCustomJob(ctx context.Context, req *aiplatformpb.CreateCustomJobRequest) (*aiplatformpb.CustomJob, error)
	GetCustomJob(ctx context.Context, name string) (*aiplatformpb.CustomJob, error)
	CancelCustomJob(ctx context.Context, name string) error
	ListLogEntries(ctx context.Context, req *loggingpb.ListLogEntriesRequest) ([]*loggingpb.LogEntry, error)
	ListCatalogSKUs(ctx context.Context, serviceID string) ([]*cloudbilling.Sku, error)
	ListMachineTypes(ctx context.Context, projectID string) ([]*compute.MachineType, error)
	ListAcceleratorTypes(ctx context.Context, projectID string) ([]*compute.AcceleratorType, error)
	GetRegion(ctx context.Context, projectID string, region string) (*compute.Region, error)
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
	hardwareShapes := p.hardwareShapes(ctx)
	return app.ProviderCapabilities{
		GPUFamilies:                gpuFamiliesFromShapes(hardwareShapes),
		Regions:                    []string{p.config.Location},
		HardwareShapes:             hardwareShapes,
		SupportsOnDemand:           true,
		SupportsDockerImage:        true,
		SupportsLocalScript:        false,
		SupportsDataBundle:         false,
		SupportedURISchemes:        []string{"gs"},
		SupportedCheckpointSchemes: []string{"gs"},
		SupportsObjectStorePull:    true,
	}, nil
}

func (p *Provider) configuredHardwareShape() app.HardwareShape {
	idParts := []string{"gcp", p.config.Location, p.config.MachineType}
	if p.config.AcceleratorType != "" {
		idParts = append(idParts, strings.ToLower(strings.ReplaceAll(p.config.AcceleratorType, "_", "-")), fmt.Sprintf("%dg", p.config.AcceleratorCount))
	}
	vramPerGPU := vramGBPerGPU(p.config.AcceleratorType)
	totalVRAM := vramPerGPU * int(p.config.AcceleratorCount)
	hourlyUSD := p.config.EstimateHourlyUSD
	if hourlyUSD == 0 {
		hourlyUSD = catalogHourlyUSD(p.config.Location, p.config.MachineType, p.config.AcceleratorType, int(p.config.AcceleratorCount))
	}
	return app.HardwareShape{
		ID:                 strings.Join(idParts, "-"),
		Provider:           ProviderName,
		Region:             p.config.Location,
		MachineType:        p.config.MachineType,
		AcceleratorType:    p.config.AcceleratorType,
		AcceleratorCount:   int(p.config.AcceleratorCount),
		GPUFamily:          firstGPUFamily(p.config.AcceleratorType),
		VRAMGBPerGPU:       vramPerGPU,
		TotalVRAMGB:        totalVRAM,
		OnDemandHourlyUSD:  hourlyUSD,
		SupportsOnDemand:   true,
		AvailabilityHint:   "configured",
		AvailabilityReason: "configured from gcp.machine_type and gcp.accelerator_type",
	}
}

func (p *Provider) hardwareShapes(ctx context.Context) []app.HardwareShape {
	configured := p.configuredHardwareShape()
	shapes := []app.HardwareShape{configured}
	seen := map[string]bool{configured.ID: true}
	for _, shape := range catalogHardwareShapes(p.config.Location) {
		if seen[shape.ID] {
			continue
		}
		shapes = append(shapes, shape)
		seen[shape.ID] = true
	}
	return p.enrichHardwareShapes(ctx, shapes)
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
	hourlyUSD := p.config.EstimateHourlyUSD
	if hourlyUSD == 0 {
		hourlyUSD = p.estimateHourlyUSD(ctx, p.config.MachineType, p.config.AcceleratorType, int(p.config.AcceleratorCount))
	}
	return app.CostEstimate{HourlyUSD: hourlyUSD, Currency: "USD"}, nil
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
	if err := p.notifyResource(req, providerRef, app.ProviderResourceStateRunning, true); err != nil {
		_ = client.CancelCustomJob(ctx, providerRef)
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
	}
	if req.OnStarted != nil {
		if err := req.OnStarted(app.ProviderJobRef{ID: providerRef}); err != nil {
			_ = client.CancelCustomJob(ctx, providerRef)
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
			if err := p.notifyResource(req, providerRef, app.ProviderResourceStateSucceeded, false); err != nil {
				return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
			}
			return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 0, ExitReason: "completed"}, nil
		case aiplatformpb.JobState_JOB_STATE_FAILED, aiplatformpb.JobState_JOB_STATE_EXPIRED:
			wrapped := providerErrorFromJob(current, app.ProviderErrorRuntime)
			if err := p.notifyResource(req, providerRef, app.ProviderResourceStateFailed, false); err != nil {
				return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
			}
			return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: exitCodeForProviderError(wrapped), ExitReason: redactor.String(wrapped.Error())}, wrapped
		case aiplatformpb.JobState_JOB_STATE_CANCELLED:
			reason := jobErrorMessage(current)
			if reason == "" {
				reason = "gcp custom job canceled"
			}
			wrapped := &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: reason}
			if err := p.notifyResource(req, providerRef, app.ProviderResourceStateCanceled, false); err != nil {
				return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
			}
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
	for k, v := range p.gcpRuntimeEnv(req.RunID) {
		if v == "" {
			continue
		}
		env = append(env, &aiplatformpb.EnvVar{Name: k, Value: v})
	}
	machineType, acceleratorTypeValue, acceleratorCount := p.workerPoolHardware(req.SelectedHardware)
	machine := &aiplatformpb.MachineSpec{
		MachineType:      machineType,
		AcceleratorType:  acceleratorType(acceleratorTypeValue),
		AcceleratorCount: acceleratorCount,
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
			OutputUriPrefix: p.gcpOutputDir(req.RunID),
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

func (p *Provider) notifyResource(req app.SubmitRequest, providerRef string, state app.ProviderResourceState, created bool) error {
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
		Kind:                 app.ProviderResourceKindCustomJob,
		ExternalID:           providerRef,
		ProviderRef:          providerRef,
		Region:               p.config.Location,
		ProjectOrAccount:     p.config.ProjectID,
		State:                state,
		CreatedBySwitchboard: true,
		CleanupPolicy:        app.ProviderResourceCleanupNever,
		Metadata: map[string]string{
			"output_uri_prefix": p.gcpOutputDir(req.RunID),
		},
		LastObservedAt: &observedAt,
	})
}

func (p *Provider) gcpRuntimeEnv(runID string) map[string]string {
	outputDir := p.gcpOutputDir(runID)
	return map[string]string{
		"SWITCHBOARD_GCS_OUTPUT_DIR":         outputDir,
		"SWITCHBOARD_CHECKPOINT_URI_PREFIX":  outputDir + "/checkpoints",
		"ORCHESTRATOR_GCS_OUTPUT_DIR":        outputDir,
		"ORCHESTRATOR_CHECKPOINT_URI_PREFIX": outputDir + "/checkpoints",
	}
}

func (p *Provider) gcpOutputDir(runID string) string {
	return strings.TrimRight(p.config.OutputURIPrefix, "/") + "/" + runID
}

func (p *Provider) workerPoolHardware(selected *app.HardwareSelection) (string, string, int32) {
	if selected != nil && selected.Provider == ProviderName {
		machineType := selected.MachineType
		if machineType == "" {
			machineType = p.config.MachineType
		}
		acceleratorType := selected.AcceleratorType
		if acceleratorType == "" {
			acceleratorType = p.config.AcceleratorType
		}
		acceleratorCount := int32(selected.AcceleratorCount)
		if acceleratorCount == 0 {
			acceleratorCount = p.config.AcceleratorCount
		}
		return machineType, acceleratorType, acceleratorCount
	}
	return p.config.MachineType, p.config.AcceleratorType, p.config.AcceleratorCount
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
	jobs    *aiplatform.JobClient
	logs    *logging.Client
	billing *cloudbilling.APIService
	compute *compute.Service
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
	billing, err := cloudbilling.NewService(ctx)
	if err != nil {
		_ = jobs.Close()
		_ = logs.Close()
		return nil, err
	}
	computeClient, err := compute.NewService(ctx)
	if err != nil {
		_ = jobs.Close()
		_ = logs.Close()
		return nil, err
	}
	return &realClient{jobs: jobs, logs: logs, billing: billing, compute: computeClient}, nil
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

func (c *realClient) ListCatalogSKUs(ctx context.Context, serviceID string) ([]*cloudbilling.Sku, error) {
	var skus []*cloudbilling.Sku
	err := c.billing.Services.Skus.List(serviceID).CurrencyCode("USD").PageSize(5000).Pages(ctx, func(resp *cloudbilling.ListSkusResponse) error {
		skus = append(skus, resp.Skus...)
		return nil
	})
	return skus, err
}

func (c *realClient) ListMachineTypes(ctx context.Context, projectID string) ([]*compute.MachineType, error) {
	var machineTypes []*compute.MachineType
	err := c.compute.MachineTypes.AggregatedList(projectID).ReturnPartialSuccess(true).Pages(ctx, func(resp *compute.MachineTypeAggregatedList) error {
		for _, scoped := range resp.Items {
			machineTypes = append(machineTypes, scoped.MachineTypes...)
		}
		return nil
	})
	return machineTypes, err
}

func (c *realClient) ListAcceleratorTypes(ctx context.Context, projectID string) ([]*compute.AcceleratorType, error) {
	var acceleratorTypes []*compute.AcceleratorType
	err := c.compute.AcceleratorTypes.AggregatedList(projectID).ReturnPartialSuccess(true).Pages(ctx, func(resp *compute.AcceleratorTypeAggregatedList) error {
		for _, scoped := range resp.Items {
			acceleratorTypes = append(acceleratorTypes, scoped.AcceleratorTypes...)
		}
		return nil
	})
	return acceleratorTypes, err
}

func (c *realClient) GetRegion(ctx context.Context, projectID string, region string) (*compute.Region, error) {
	return c.compute.Regions.Get(projectID, region).Context(ctx).Do()
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

func gpuFamiliesFromShapes(shapes []app.HardwareShape) []string {
	seen := map[string]bool{}
	var families []string
	for _, shape := range shapes {
		if shape.GPUFamily == "" || seen[shape.GPUFamily] {
			continue
		}
		families = append(families, shape.GPUFamily)
		seen[shape.GPUFamily] = true
	}
	return families
}

func firstGPUFamily(value string) string {
	families := gpuFamilies(value)
	if len(families) == 0 {
		return ""
	}
	return families[0]
}

type inventorySnapshot struct {
	skus             []*cloudbilling.Sku
	machineTypes     map[string]*compute.MachineType
	machineZones     map[string][]string
	acceleratorZones map[string][]string
	acceleratorMax   map[string]int64
	quotas           map[string]*compute.Quota
	errors           []string
}

func (p *Provider) enrichHardwareShapes(ctx context.Context, shapes []app.HardwareShape) []app.HardwareShape {
	snapshot := p.inventorySnapshot(ctx)
	for i := range shapes {
		shape := shapes[i]
		if price := priceFromSKUs(snapshot.skus, shape, snapshot.machineTypes[shape.MachineType]); price > 0 {
			shape.OnDemandHourlyUSD = price
			if shape.AvailabilityHint == "static_estimate" {
				shape.AvailabilityHint = "live_pricing"
				shape.AvailabilityReason = "price from Cloud Billing Catalog API; regional capacity still estimated"
			}
		}
		shape = applyLiveAvailability(shape, snapshot)
		shapes[i] = shape
	}
	return shapes
}

func (p *Provider) estimateHourlyUSD(ctx context.Context, machineType string, acceleratorType string, acceleratorCount int) float64 {
	shape := app.HardwareShape{
		Region:           p.config.Location,
		MachineType:      machineType,
		AcceleratorType:  acceleratorType,
		AcceleratorCount: acceleratorCount,
	}
	snapshot := p.inventorySnapshot(ctx)
	if price := priceFromSKUs(snapshot.skus, shape, snapshot.machineTypes[machineType]); price > 0 {
		return price
	}
	return catalogHourlyUSD(p.config.Location, machineType, acceleratorType, acceleratorCount)
}

func (p *Provider) inventorySnapshot(ctx context.Context) inventorySnapshot {
	snapshot := inventorySnapshot{
		machineTypes:     map[string]*compute.MachineType{},
		machineZones:     map[string][]string{},
		acceleratorZones: map[string][]string{},
		acceleratorMax:   map[string]int64{},
		quotas:           map[string]*compute.Quota{},
	}
	client, err := p.clientFor(ctx)
	if err != nil {
		snapshot.errors = append(snapshot.errors, err.Error())
		return snapshot
	}
	if skus, err := client.ListCatalogSKUs(ctx, computeEngineServiceID); err == nil {
		snapshot.skus = skus
	} else {
		snapshot.errors = append(snapshot.errors, fmt.Sprintf("pricing unavailable: %v", err))
	}
	if p.config.ProjectID == "" {
		return snapshot
	}
	if machineTypes, err := client.ListMachineTypes(ctx, p.config.ProjectID); err == nil {
		for _, machineType := range machineTypes {
			if machineType == nil || !zoneInRegion(machineType.Zone, p.config.Location) {
				continue
			}
			snapshot.machineTypes[machineType.Name] = machineType
			snapshot.machineZones[machineType.Name] = appendUnique(snapshot.machineZones[machineType.Name], zoneName(machineType.Zone))
		}
	} else {
		snapshot.errors = append(snapshot.errors, fmt.Sprintf("machine inventory unavailable: %v", err))
	}
	if acceleratorTypes, err := client.ListAcceleratorTypes(ctx, p.config.ProjectID); err == nil {
		for _, acceleratorType := range acceleratorTypes {
			if acceleratorType == nil || !zoneInRegion(acceleratorType.Zone, p.config.Location) {
				continue
			}
			name := normalizeAcceleratorName(acceleratorType.Name)
			snapshot.acceleratorZones[name] = appendUnique(snapshot.acceleratorZones[name], zoneName(acceleratorType.Zone))
			if acceleratorType.MaximumCardsPerInstance > snapshot.acceleratorMax[name] {
				snapshot.acceleratorMax[name] = acceleratorType.MaximumCardsPerInstance
			}
		}
	} else {
		snapshot.errors = append(snapshot.errors, fmt.Sprintf("accelerator inventory unavailable: %v", err))
	}
	if region, err := client.GetRegion(ctx, p.config.ProjectID, p.config.Location); err == nil && region != nil {
		for _, quota := range region.Quotas {
			if quota == nil || quota.Metric == "" {
				continue
			}
			snapshot.quotas[quota.Metric] = quota
		}
	} else if err != nil {
		snapshot.errors = append(snapshot.errors, fmt.Sprintf("region quota unavailable: %v", err))
	}
	sortSnapshot(snapshot)
	return snapshot
}

func applyLiveAvailability(shape app.HardwareShape, snapshot inventorySnapshot) app.HardwareShape {
	if len(snapshot.machineZones) == 0 && len(snapshot.acceleratorZones) == 0 && len(snapshot.quotas) == 0 {
		if len(snapshot.errors) > 0 && shape.AvailabilityReason != "" {
			shape.AvailabilityReason += "; " + strings.Join(snapshot.errors, "; ")
		}
		return shape
	}
	machineZones := snapshot.machineZones[shape.MachineType]
	if len(snapshot.machineZones) > 0 && len(machineZones) == 0 {
		shape.AvailabilityHint = "unavailable"
		shape.AvailabilityReason = fmt.Sprintf("machine type %q was not listed in region %q", shape.MachineType, shape.Region)
		return shape
	}
	zones := append([]string(nil), machineZones...)
	if shape.AcceleratorType != "" && shape.AcceleratorCount > 0 {
		acceleratorName := normalizeAcceleratorName(shape.AcceleratorType)
		acceleratorZones := snapshot.acceleratorZones[acceleratorName]
		if len(snapshot.acceleratorZones) > 0 && len(acceleratorZones) == 0 {
			shape.AvailabilityHint = "unavailable"
			shape.AvailabilityReason = fmt.Sprintf("accelerator %q was not listed in region %q", shape.AcceleratorType, shape.Region)
			return shape
		}
		if len(zones) > 0 && len(acceleratorZones) > 0 {
			zones = intersectStrings(zones, acceleratorZones)
		} else if len(acceleratorZones) > 0 {
			zones = append([]string(nil), acceleratorZones...)
		}
		if maxCards := snapshot.acceleratorMax[acceleratorName]; maxCards > 0 && int64(shape.AcceleratorCount) > maxCards {
			shape.AvailabilityHint = "unavailable"
			shape.AvailabilityReason = fmt.Sprintf("accelerator %q supports at most %d cards per instance in listed zones", shape.AcceleratorType, maxCards)
			return shape
		}
		applyQuota(&shape, snapshot, gpuQuotaMetric(shape.AcceleratorType), float64(shape.AcceleratorCount))
	} else if machine := snapshot.machineTypes[shape.MachineType]; machine != nil {
		applyQuota(&shape, snapshot, cpuQuotaMetric(shape.MachineType), float64(machine.GuestCpus))
	}
	if len(zones) > 0 {
		sort.Strings(zones)
		shape.Zones = zones
	}
	if shape.AvailabilityHint == "" || shape.AvailabilityHint == "configured" || shape.AvailabilityHint == "static_estimate" || shape.AvailabilityHint == "live_pricing" {
		if len(zones) > 0 {
			shape.AvailabilityHint = "live_inventory"
			shape.AvailabilityReason = "machine, accelerator, and regional quota facts came from Compute Engine APIs"
		}
	}
	return shape
}

func applyQuota(shape *app.HardwareShape, snapshot inventorySnapshot, metric string, required float64) {
	if metric == "" {
		return
	}
	shape.QuotaMetric = metric
	quota := snapshot.quotas[metric]
	if quota == nil {
		if len(snapshot.quotas) > 0 && shape.AvailabilityHint == "" {
			shape.AvailabilityHint = "unknown_quota"
			shape.AvailabilityReason = fmt.Sprintf("regional quota %q was not present in Compute Engine quota response", metric)
		}
		return
	}
	shape.QuotaLimit = quota.Limit
	shape.QuotaUsage = quota.Usage
	shape.QuotaAvailable = quota.Limit - quota.Usage
	if quota.Limit > 0 && shape.QuotaAvailable < required {
		shape.AvailabilityHint = "no_quota"
		shape.AvailabilityReason = fmt.Sprintf("regional quota %q has %.0f available, requires %.0f", metric, shape.QuotaAvailable, required)
	}
}

func priceFromSKUs(skus []*cloudbilling.Sku, shape app.HardwareShape, machineType *compute.MachineType) float64 {
	if len(skus) == 0 {
		return 0
	}
	total := 0.0
	if machineType != nil {
		family := machineFamily(shape.MachineType)
		corePrice := firstSKUPrice(skus, shape.Region, func(description string) bool {
			return containsAll(description, family, "core") && onDemandSKU(description)
		})
		ramPrice := firstSKUPrice(skus, shape.Region, func(description string) bool {
			return containsAll(description, family, "ram") && onDemandSKU(description)
		})
		if corePrice > 0 {
			total += float64(machineType.GuestCpus) * corePrice
		}
		if ramPrice > 0 {
			total += float64(machineType.MemoryMb) / 1024 * ramPrice
		}
	}
	if shape.AcceleratorType != "" && shape.AcceleratorCount > 0 {
		token := acceleratorPriceToken(shape.AcceleratorType)
		gpuPrice := firstSKUPrice(skus, shape.Region, func(description string) bool {
			return containsAll(description, token, "gpu") && onDemandSKU(description)
		})
		if gpuPrice > 0 {
			total += float64(shape.AcceleratorCount) * gpuPrice
		}
	}
	return total
}

func firstSKUPrice(skus []*cloudbilling.Sku, region string, match func(string) bool) float64 {
	for _, sku := range skus {
		if sku == nil || !skuAppliesToRegion(sku, region) {
			continue
		}
		description := normalizeDescription(sku.Description)
		if !match(description) {
			continue
		}
		if price := skuHourlyPriceUSD(sku); price > 0 {
			return price
		}
	}
	return 0
}

func skuHourlyPriceUSD(sku *cloudbilling.Sku) float64 {
	if len(sku.PricingInfo) == 0 {
		return 0
	}
	info := sku.PricingInfo[len(sku.PricingInfo)-1]
	if info == nil || info.PricingExpression == nil {
		return 0
	}
	expr := info.PricingExpression
	if expr.UsageUnit != "" && !strings.EqualFold(expr.UsageUnit, "h") {
		return 0
	}
	for _, tier := range expr.TieredRates {
		if tier == nil || tier.UnitPrice == nil || tier.UnitPrice.CurrencyCode != "USD" {
			continue
		}
		return float64(tier.UnitPrice.Units) + float64(tier.UnitPrice.Nanos)/1_000_000_000
	}
	return 0
}

func skuAppliesToRegion(sku *cloudbilling.Sku, region string) bool {
	if len(sku.ServiceRegions) == 0 {
		return true
	}
	for _, serviceRegion := range sku.ServiceRegions {
		if serviceRegion == region || serviceRegion == regionGroup(region) {
			return true
		}
	}
	return false
}

func onDemandSKU(description string) bool {
	for _, excluded := range []string{"preemptible", "spot", "commitment", "committed", "sole tenancy", "license"} {
		if strings.Contains(description, excluded) {
			return false
		}
	}
	return true
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle == "" {
			continue
		}
		if !strings.Contains(value, normalizeDescription(needle)) {
			return false
		}
	}
	return true
}

func normalizeDescription(value string) string {
	value = strings.ToLower(strings.ReplaceAll(value, "_", " "))
	value = strings.ReplaceAll(value, "-", " ")
	return strings.Join(strings.Fields(value), " ")
}

func acceleratorPriceToken(value string) string {
	name := normalizeAcceleratorName(value)
	switch name {
	case "nvidia-tesla-t4":
		return "t4"
	case "nvidia-l4":
		return "l4"
	case "nvidia-tesla-a100":
		return "a100"
	case "nvidia-a100-80gb":
		return "a100 80gb"
	case "nvidia-h100-80gb":
		return "h100"
	default:
		return strings.TrimPrefix(strings.ReplaceAll(name, "-", " "), "nvidia ")
	}
}

func machineFamily(machineType string) string {
	if idx := strings.Index(machineType, "-"); idx > 0 {
		return strings.ToLower(machineType[:idx])
	}
	return strings.ToLower(machineType)
}

func gpuQuotaMetric(acceleratorType string) string {
	switch normalizeAcceleratorName(acceleratorType) {
	case "nvidia-tesla-t4":
		return "NVIDIA_T4_GPUS"
	case "nvidia-l4":
		return "NVIDIA_L4_GPUS"
	case "nvidia-tesla-a100":
		return "NVIDIA_A100_GPUS"
	case "nvidia-a100-80gb":
		return "NVIDIA_A100_80GB_GPUS"
	case "nvidia-h100-80gb":
		return "NVIDIA_H100_GPUS"
	default:
		return ""
	}
}

func cpuQuotaMetric(machineType string) string {
	switch machineFamily(machineType) {
	case "a2":
		return "A2_CPUS"
	case "c2":
		return "C2_CPUS"
	case "c2d":
		return "C2D_CPUS"
	case "c3":
		return "C3_CPUS"
	case "e2":
		return "E2_CPUS"
	case "m1":
		return "M1_CPUS"
	case "m2":
		return "M2_CPUS"
	case "m3":
		return "M3_CPUS"
	case "n2":
		return "N2_CPUS"
	case "n2d":
		return "N2D_CPUS"
	case "t2a":
		return "T2A_CPUS"
	case "t2d":
		return "T2D_CPUS"
	default:
		return "CPUS"
	}
}

func normalizeAcceleratorName(value string) string {
	value = strings.ToLower(strings.ReplaceAll(value, "_", "-"))
	if value == "tesla-t4" {
		return "nvidia-tesla-t4"
	}
	if value == "tesla-a100" {
		return "nvidia-tesla-a100"
	}
	if strings.HasPrefix(value, "nvidia-") {
		return value
	}
	return "nvidia-" + value
}

func zoneInRegion(zone string, region string) bool {
	zone = zoneName(zone)
	return region == "" || strings.HasPrefix(zone, region+"-")
}

func zoneName(value string) string {
	if idx := strings.LastIndex(value, "/"); idx >= 0 {
		return value[idx+1:]
	}
	return value
}

func regionGroup(region string) string {
	if idx := strings.Index(region, "-"); idx > 0 {
		return region[:idx]
	}
	return region
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func intersectStrings(a []string, b []string) []string {
	seen := map[string]bool{}
	for _, value := range a {
		seen[value] = true
	}
	var out []string
	for _, value := range b {
		if seen[value] {
			out = appendUnique(out, value)
		}
	}
	return out
}

func sortSnapshot(snapshot inventorySnapshot) {
	for key := range snapshot.machineZones {
		sort.Strings(snapshot.machineZones[key])
	}
	for key := range snapshot.acceleratorZones {
		sort.Strings(snapshot.acceleratorZones[key])
	}
}

func catalogHardwareShapes(location string) []app.HardwareShape {
	if location == "" {
		location = "us-central1"
	}
	return []app.HardwareShape{
		catalogShape(location, "n1-standard-4-t4-1", "n1-standard-4", "NVIDIA_TESLA_T4", 1, 16, 0.60),
		catalogShape(location, "g2-standard-12-l4-1", "g2-standard-12", "NVIDIA_L4", 1, 24, 1.20),
		catalogShape(location, "a2-highgpu-1g-a100-1", "a2-highgpu-1g", "NVIDIA_TESLA_A100", 1, 40, 3.70),
		catalogShape(location, "a2-ultragpu-1g-a100-80gb-1", "a2-ultragpu-1g", "NVIDIA_A100_80GB", 1, 80, 5.50),
		catalogShape(location, "a3-highgpu-8g-h100-8", "a3-highgpu-8g", "NVIDIA_H100_80GB", 8, 80, 88.00),
	}
}

func catalogShape(location string, idSuffix string, machineType string, acceleratorType string, acceleratorCount int, vramPerGPU int, hourlyUSD float64) app.HardwareShape {
	return app.HardwareShape{
		ID:                 "gcp-" + location + "-" + idSuffix,
		Provider:           ProviderName,
		Region:             location,
		MachineType:        machineType,
		AcceleratorType:    acceleratorType,
		AcceleratorCount:   acceleratorCount,
		GPUFamily:          firstGPUFamily(acceleratorType),
		VRAMGBPerGPU:       vramPerGPU,
		TotalVRAMGB:        vramPerGPU * acceleratorCount,
		OnDemandHourlyUSD:  hourlyUSD,
		SupportsOnDemand:   true,
		AvailabilityHint:   "static_estimate",
		AvailabilityReason: "static Switchboard catalog estimate; validate quota and regional availability before large runs",
	}
}

func vramGBPerGPU(acceleratorType string) int {
	switch strings.ToUpper(strings.ReplaceAll(acceleratorType, "-", "_")) {
	case "NVIDIA_TESLA_T4", "TESLA_T4":
		return 16
	case "NVIDIA_L4", "L4":
		return 24
	case "NVIDIA_TESLA_A100", "TESLA_A100":
		return 40
	case "NVIDIA_A100_80GB", "A100_80GB", "NVIDIA_H100_80GB", "H100_80GB":
		return 80
	default:
		return 0
	}
}

func catalogHourlyUSD(location string, machineType string, acceleratorType string, acceleratorCount int) float64 {
	for _, shape := range catalogHardwareShapes(location) {
		if shape.MachineType == machineType && strings.EqualFold(shape.AcceleratorType, acceleratorType) && shape.AcceleratorCount == acceleratorCount {
			return shape.OnDemandHourlyUSD
		}
	}
	return 0
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
