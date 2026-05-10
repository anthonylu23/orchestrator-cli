package gcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/aiplatform/apiv1/aiplatformpb"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	"github.com/anthonylu23/orchestrator-cli/internal/app"
	"github.com/anthonylu23/orchestrator-cli/internal/artifact"
	"github.com/anthonylu23/orchestrator-cli/internal/provider/contract"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestValidateJobRequiresContainerImageAndGCSInputs(t *testing.T) {
	provider := NewWithClient(testConfig(), &fakeClient{}, &bytes.Buffer{}, &bytes.Buffer{})
	report := provider.ValidateJob(context.Background(), app.JobSpec{
		Script: "train.py",
		Data: []app.DataInput{
			{Name: "local", Source: "./data", Mode: app.DataInputModeBundle},
			{Name: "remote", Source: "s3://bucket/data", Mode: app.DataInputModeURI},
		},
	})
	if report.Supported {
		t.Fatalf("expected unsupported job: %#v", report)
	}
	for _, want := range []string{"job.image", "does not package local scripts", "does not support bundled", "only supports gs://"} {
		if !containsReason(report.Reasons, want) {
			t.Fatalf("expected reason containing %q, got %#v", want, report.Reasons)
		}
	}
}

func TestValidateJobValidatesAcceleratorConfig(t *testing.T) {
	validCPU := testConfig()
	validCPU.AcceleratorType = ""
	validCPU.AcceleratorCount = 0

	tests := []struct {
		name       string
		config     Config
		supported  bool
		wantReason string
	}{
		{
			name:      "valid cpu only",
			config:    validCPU,
			supported: true,
		},
		{
			name:      "valid gpu",
			config:    testConfig(),
			supported: true,
		},
		{
			name: "unknown accelerator",
			config: func() Config {
				cfg := testConfig()
				cfg.AcceleratorType = "NVIDIA_UNKNOWN"
				return cfg
			}(),
			wantReason: "not recognized",
		},
		{
			name: "count without type",
			config: func() Config {
				cfg := validCPU
				cfg.AcceleratorCount = 1
				return cfg
			}(),
			wantReason: "accelerator_type is required",
		},
		{
			name: "type without positive count",
			config: func() Config {
				cfg := testConfig()
				cfg.AcceleratorCount = 0
				return cfg
			}(),
			wantReason: "accelerator_count must be greater than 0",
		},
		{
			name: "negative count",
			config: func() Config {
				cfg := testConfig()
				cfg.AcceleratorCount = -1
				return cfg
			}(),
			wantReason: "greater than or equal to 0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewWithClient(tt.config, &fakeClient{}, &bytes.Buffer{}, &bytes.Buffer{})
			report := provider.ValidateJob(context.Background(), app.JobSpec{Name: "train", Image: "image"})
			if report.Supported != tt.supported {
				t.Fatalf("supported = %v, reasons = %#v", report.Supported, report.Reasons)
			}
			if tt.wantReason != "" && !containsReason(report.Reasons, tt.wantReason) {
				t.Fatalf("expected reason containing %q, got %#v", tt.wantReason, report.Reasons)
			}
		})
	}
}

func TestCreateRequestBuildsVertexCustomJob(t *testing.T) {
	provider := NewWithClient(testConfig(), &fakeClient{}, &bytes.Buffer{}, &bytes.Buffer{})
	req := provider.createRequest(app.SubmitRequest{
		JobSpec: app.JobSpec{
			Name:    "training/job",
			Image:   "us-docker.pkg.dev/project/train:latest",
			Command: []string{"python", "-m", "trainer"},
			Args:    []string{"--epochs", "3"},
			Env:     map[string]string{"USER_ENV": "value"},
		},
		RunID:     "r_123",
		AttemptID: "a_456",
		RuntimeEnv: map[string]string{
			"ORCHESTRATOR_RUN_ID": "r_123",
		},
	})
	if req.Parent != "projects/test-project/locations/us-central1" {
		t.Fatalf("parent = %q", req.Parent)
	}
	job := req.GetCustomJob()
	if job.GetDisplayName() != "training-job" {
		t.Fatalf("display name = %q", job.GetDisplayName())
	}
	spec := job.GetJobSpec()
	if spec.GetBaseOutputDirectory().GetOutputUriPrefix() != "gs://outputs/r_123" {
		t.Fatalf("output uri = %q", spec.GetBaseOutputDirectory().GetOutputUriPrefix())
	}
	worker := spec.GetWorkerPoolSpecs()[0]
	if worker.GetMachineSpec().GetMachineType() != "n1-standard-8" {
		t.Fatalf("machine = %#v", worker.GetMachineSpec())
	}
	if worker.GetMachineSpec().GetAcceleratorType() != aiplatformpb.AcceleratorType_NVIDIA_TESLA_T4 {
		t.Fatalf("accelerator = %s", worker.GetMachineSpec().GetAcceleratorType())
	}
	container := worker.GetContainerSpec()
	if container.GetImageUri() != "us-docker.pkg.dev/project/train:latest" {
		t.Fatalf("image = %q", container.GetImageUri())
	}
	if len(container.GetEnv()) != 2 {
		t.Fatalf("env = %#v", container.GetEnv())
	}
}

func TestSubmitPollsAndWritesLogsAndEvents(t *testing.T) {
	home := t.TempDir()
	paths := artifact.ForRun(home, "r_123")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	payload, err := structpb.NewStruct(map[string]interface{}{
		"type":    "metric",
		"step":    float64(10),
		"split":   "train",
		"metrics": map[string]interface{}{"loss": float64(0.4)},
	})
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	client := &fakeClient{
		createName: "projects/test-project/locations/us-central1/customJobs/123",
		statuses: []aiplatformpb.JobState{
			aiplatformpb.JobState_JOB_STATE_RUNNING,
			aiplatformpb.JobState_JOB_STATE_SUCCEEDED,
		},
		logs: []*loggingpb.LogEntry{
			{InsertId: "1", Payload: &loggingpb.LogEntry_TextPayload{TextPayload: "plain cloud log"}},
			{InsertId: "2", Payload: &loggingpb.LogEntry_JsonPayload{JsonPayload: payload}},
		},
	}
	var stdout bytes.Buffer
	provider := NewWithClient(testConfig(), client, &stdout, &bytes.Buffer{})
	provider.Sleep = func(ctx context.Context, d time.Duration) error { return nil }
	result, err := provider.Submit(context.Background(), app.SubmitRequest{
		JobSpec:   app.JobSpec{Name: "train", Image: "image", Env: map[string]string{"TOKEN": "secret-value"}},
		RunID:     "r_123",
		AttemptID: "a_123",
		RuntimeEnv: map[string]string{
			"ORCHESTRATOR_TOKEN": "secret-value",
		},
		RunDir: paths.RunDir,
		OnStarted: func(ref app.ProviderJobRef) error {
			if ref.ID != "projects/test-project/locations/us-central1/customJobs/123" {
				t.Fatalf("provider ref = %q", ref.ID)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if result.ExitCode != 0 || result.ProviderJobRef == "" {
		t.Fatalf("result = %#v", result)
	}
	logs, err := os.ReadFile(paths.Logs)
	if err != nil {
		t.Fatalf("read logs: %v", err)
	}
	if !strings.Contains(string(logs), "plain cloud log") || strings.Contains(string(logs), "secret-value") {
		t.Fatalf("logs = %s", string(logs))
	}
	events, err := os.ReadFile(paths.EventsJSONL)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(events), `"type":"metric"`) || !strings.Contains(string(events), `"loss":0.4`) {
		t.Fatalf("events = %s", string(events))
	}
}

func TestSubmitMapsGRPCErrors(t *testing.T) {
	client := &fakeClient{createErr: grpcstatus.Error(codes.ResourceExhausted, "quota exceeded")}
	provider := NewWithClient(testConfig(), client, &bytes.Buffer{}, &bytes.Buffer{})
	paths := artifact.ForRun(t.TempDir(), "r_err")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	result, err := provider.Submit(context.Background(), app.SubmitRequest{
		JobSpec:   app.JobSpec{Name: "train", Image: "image"},
		RunID:     "r_err",
		AttemptID: "a_err",
		RunDir:    paths.RunDir,
	})
	if err == nil {
		t.Fatal("expected submit error")
	}
	if app.ProviderErrorKindOf(err) != app.ProviderErrorQuota {
		t.Fatalf("kind = %s, err = %v", app.ProviderErrorKindOf(err), err)
	}
	if result.ExitCode != 30 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
}

func TestSubmitMapsTerminalJobStatusCodes(t *testing.T) {
	tests := []struct {
		name     string
		code     codes.Code
		wantKind app.ProviderErrorKind
		wantExit int
	}{
		{name: "quota", code: codes.ResourceExhausted, wantKind: app.ProviderErrorQuota, wantExit: 30},
		{name: "capacity", code: codes.Unavailable, wantKind: app.ProviderErrorCapacity, wantExit: 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeClient{
				createName:  "projects/test-project/locations/us-central1/customJobs/failed",
				statuses:    []aiplatformpb.JobState{aiplatformpb.JobState_JOB_STATE_FAILED},
				statusError: &status.Status{Code: int32(tt.code), Message: tt.name + " failure"},
			}
			provider := NewWithClient(testConfig(), client, &bytes.Buffer{}, &bytes.Buffer{})
			paths := artifact.ForRun(t.TempDir(), "r_failed")
			if err := artifact.EnsureRun(paths); err != nil {
				t.Fatalf("ensure run: %v", err)
			}
			result, err := provider.Submit(context.Background(), app.SubmitRequest{
				JobSpec:   app.JobSpec{Name: "train", Image: "image"},
				RunID:     "r_failed",
				AttemptID: "a_failed",
				RunDir:    paths.RunDir,
			})
			if err == nil {
				t.Fatal("expected terminal job error")
			}
			if app.ProviderErrorKindOf(err) != tt.wantKind {
				t.Fatalf("kind = %s, err = %v", app.ProviderErrorKindOf(err), err)
			}
			if result.ExitCode != tt.wantExit {
				t.Fatalf("exit code = %d, want %d", result.ExitCode, tt.wantExit)
			}
		})
	}
}

func TestCancelParsesProjectAndLocationFromProviderRef(t *testing.T) {
	client := &fakeClient{}
	provider := NewWithClient(Config{}, client, &bytes.Buffer{}, &bytes.Buffer{})
	err := provider.Cancel(context.Background(), app.ProviderJobRef{ID: "projects/p1/locations/europe-west4/customJobs/9"})
	if err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	if client.canceled != "projects/p1/locations/europe-west4/customJobs/9" {
		t.Fatalf("canceled = %q", client.canceled)
	}
}

func TestProviderContract(t *testing.T) {
	contract.Run(t, func(t *testing.T) contract.Subject {
		home := t.TempDir()
		paths := artifact.ForRun(home, "r_contract")
		if err := artifact.EnsureRun(paths); err != nil {
			t.Fatalf("ensure run: %v", err)
		}
		client := &fakeClient{
			createName: "projects/test-project/locations/us-central1/customJobs/contract",
			statuses:   []aiplatformpb.JobState{aiplatformpb.JobState_JOB_STATE_SUCCEEDED},
		}
		return contract.Subject{
			Name:       ProviderName,
			Adapter:    NewWithClient(testConfig(), client, &bytes.Buffer{}, &bytes.Buffer{}),
			ValidJob:   app.JobSpec{Name: "valid", Image: "image", Data: []app.DataInput{{Name: "train", Source: "gs://bucket/data", Mode: app.DataInputModeURI}}},
			InvalidJob: app.JobSpec{Name: "invalid"},
			SubmitRequest: app.SubmitRequest{
				JobSpec:   app.JobSpec{Name: "valid", Image: "image"},
				RunID:     "r_contract",
				AttemptID: "a_contract",
				RunDir:    paths.RunDir,
			},
			ProviderRefPrefix: "projects/test-project/locations/us-central1/customJobs/",
			StreamLogs:        contract.StreamLogsUnsupported,
			Cancel: func(t *testing.T, adapter app.ProviderAdapter) {
				if err := adapter.Cancel(context.Background(), app.ProviderJobRef{ID: "projects/test-project/locations/us-central1/customJobs/contract"}); err != nil {
					t.Fatalf("Cancel returned error: %v", err)
				}
			},
		}
	})
}

func TestLiveValidateAuth(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_GCP_LIVE") != "1" {
		t.Skip("set ORCHESTRATOR_GCP_LIVE=1 to run live GCP auth validation")
	}
	projectID := os.Getenv("ORCHESTRATOR_GCP_PROJECT_ID")
	if projectID == "" {
		t.Fatal("ORCHESTRATOR_GCP_PROJECT_ID is required")
	}
	provider := New(Config{
		ProjectID:       projectID,
		Location:        envDefault("ORCHESTRATOR_GCP_LOCATION", "us-central1"),
		OutputURIPrefix: "gs://unused",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err := provider.ValidateAuth(context.Background()); err != nil {
		t.Fatalf("ValidateAuth returned error: %v", err)
	}
}

func TestLiveSubmitContainerJob(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_GCP_LIVE") != "1" {
		t.Skip("set ORCHESTRATOR_GCP_LIVE=1 to run live GCP checks")
	}
	if os.Getenv("ORCHESTRATOR_GCP_LIVE_SUBMIT") != "1" {
		t.Skip("set ORCHESTRATOR_GCP_LIVE_SUBMIT=1 to submit a billable Vertex AI CustomJob")
	}
	projectID := requireEnv(t, "ORCHESTRATOR_GCP_PROJECT_ID")
	image := requireEnv(t, "ORCHESTRATOR_GCP_IMAGE")
	outputURIPrefix := requireEnv(t, "ORCHESTRATOR_GCP_OUTPUT_URI_PREFIX")

	ctx, cancel := context.WithTimeout(context.Background(), envDurationDefault("ORCHESTRATOR_GCP_TIMEOUT", 10*time.Minute))
	defer cancel()

	home := t.TempDir()
	paths := artifact.ForRun(home, "r_live")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	provider := New(Config{
		ProjectID:           projectID,
		Location:            envDefault("ORCHESTRATOR_GCP_LOCATION", "us-central1"),
		OutputURIPrefix:     outputURIPrefix,
		MachineType:         envDefault("ORCHESTRATOR_GCP_MACHINE_TYPE", "n1-standard-4"),
		AcceleratorType:     os.Getenv("ORCHESTRATOR_GCP_ACCELERATOR_TYPE"),
		AcceleratorCount:    envInt32Default("ORCHESTRATOR_GCP_ACCELERATOR_COUNT", 0),
		BootDiskType:        envDefault("ORCHESTRATOR_GCP_BOOT_DISK_TYPE", "pd-ssd"),
		BootDiskSizeGB:      envInt32Default("ORCHESTRATOR_GCP_BOOT_DISK_SIZE_GB", 100),
		ServiceAccount:      os.Getenv("ORCHESTRATOR_GCP_SERVICE_ACCOUNT"),
		Network:             os.Getenv("ORCHESTRATOR_GCP_NETWORK"),
		PollIntervalSeconds: int(envInt32Default("ORCHESTRATOR_GCP_POLL_INTERVAL_SECONDS", 15)),
	}, &bytes.Buffer{}, &bytes.Buffer{})

	result, err := provider.Submit(ctx, app.SubmitRequest{
		JobSpec:   app.JobSpec{Name: "gcp-live-smoke", Image: image},
		RunID:     "r_live",
		AttemptID: "a_live",
		RunDir:    paths.RunDir,
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if result.ExitCode != 0 || result.ProviderJobRef == "" {
		t.Fatalf("result = %#v", result)
	}
}

type fakeClient struct {
	validateErr error
	createName  string
	createErr   error
	statuses    []aiplatformpb.JobState
	statusError *status.Status
	statusErr   error
	logs        []*loggingpb.LogEntry
	canceled    string
}

func (c *fakeClient) ValidateAuth(ctx context.Context, parent string) error {
	return c.validateErr
}

func (c *fakeClient) CreateCustomJob(ctx context.Context, req *aiplatformpb.CreateCustomJobRequest) (*aiplatformpb.CustomJob, error) {
	if c.createErr != nil {
		return nil, c.createErr
	}
	name := c.createName
	if name == "" {
		name = filepath.Join(req.GetParent(), "customJobs", "fake")
	}
	return &aiplatformpb.CustomJob{Name: name, State: aiplatformpb.JobState_JOB_STATE_RUNNING}, nil
}

func (c *fakeClient) GetCustomJob(ctx context.Context, name string) (*aiplatformpb.CustomJob, error) {
	if c.statusErr != nil {
		return nil, c.statusErr
	}
	if len(c.statuses) == 0 {
		return &aiplatformpb.CustomJob{Name: name, State: aiplatformpb.JobState_JOB_STATE_SUCCEEDED}, nil
	}
	state := c.statuses[0]
	if len(c.statuses) > 1 {
		c.statuses = c.statuses[1:]
	}
	job := &aiplatformpb.CustomJob{Name: name, State: state}
	if state == aiplatformpb.JobState_JOB_STATE_FAILED {
		job.Error = &status.Status{Message: "training failed"}
		if c.statusError != nil {
			job.Error = c.statusError
		}
	}
	return job, nil
}

func (c *fakeClient) CancelCustomJob(ctx context.Context, name string) error {
	c.canceled = name
	return nil
}

func (c *fakeClient) ListLogEntries(ctx context.Context, req *loggingpb.ListLogEntriesRequest) ([]*loggingpb.LogEntry, error) {
	return c.logs, nil
}

func (c *fakeClient) Close() error {
	return nil
}

func testConfig() Config {
	return Config{
		ProjectID:           "test-project",
		Location:            "us-central1",
		OutputURIPrefix:     "gs://outputs",
		MachineType:         "n1-standard-8",
		AcceleratorType:     "NVIDIA_TESLA_T4",
		AcceleratorCount:    1,
		BootDiskType:        "pd-ssd",
		BootDiskSizeGB:      100,
		PollIntervalSeconds: 1,
		EstimateHourlyUSD:   2.5,
	}
}

func containsReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, want) {
			return true
		}
	}
	return false
}

func envDefault(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is required", key)
	}
	return value
}

func envInt32Default(key string, fallback int32) int32 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return fallback
	}
	return int32(parsed)
}

func envDurationDefault(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
