package hyperbolic

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/provider/contract"
	"github.com/anthonylu23/switchboard-cli/internal/redact"
)

func TestValidateJobRequiresImageAndHyperbolicFields(t *testing.T) {
	provider := NewWithClient(Config{}, &fakeClient{}, &fakeRemote{}, &bytes.Buffer{}, &bytes.Buffer{})
	report := provider.ValidateJob(context.Background(), app.JobSpec{Name: "bad", Script: "train.py"})
	if report.Supported {
		t.Fatalf("expected unsupported report: %#v", report)
	}
	for _, want := range []string{"hyperbolic provider requires job.image", "hyperbolic provider v1 does not package local scripts", "hyperbolic.ssh_private_key is required"} {
		if !contains(report.Reasons, want) {
			t.Fatalf("expected %q in reasons %#v", want, report.Reasons)
		}
	}
}

func TestCapabilitiesMapsVirtualMachineOptions(t *testing.T) {
	provider := NewWithClient(testConfig(), &fakeClient{
		options: []VirtualMachineOption{
			{GPUCount: 1, CostPerHour: 2.5},
			{GPUCount: 4, CostPerHour: 9.5},
		},
	}, &fakeRemote{}, &bytes.Buffer{}, &bytes.Buffer{})
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities returned error: %v", err)
	}
	if len(capabilities.HardwareShapes) != 2 {
		t.Fatalf("hardware shapes = %#v", capabilities.HardwareShapes)
	}
	shape := capabilities.HardwareShapes[1]
	if shape.Provider != ProviderName || shape.MachineType != "ondemand-vm" || shape.Region != "global" || shape.GPUFamily != "H100" || shape.VRAMGBPerGPU != 80 || shape.OnDemandHourlyUSD != 38.0 {
		t.Fatalf("shape = %#v", shape)
	}
}

func TestRunnerScriptRunsDockerWithoutAPIKey(t *testing.T) {
	req := app.SubmitRequest{
		JobSpec: app.JobSpec{
			Name:    "smoke",
			Image:   "ghcr.io/example/smoke:latest",
			Command: []string{"python", "/app/train.py"},
			Args:    []string{"--epochs", "1"},
			Env:     map[string]string{"TRAIN_TOKEN": "job-token-value"},
		},
		RunID:      "r_test",
		AttemptID:  "a_test",
		RuntimeEnv: map[string]string{"SWITCHBOARD_RUN_ID": "r_test"},
	}
	script := runnerScript(req, RegistryAuth{})
	for _, want := range []string{"docker pull 'ghcr.io/example/smoke:latest'", "docker' 'run' '--rm' '--gpus' 'all' '--env-file' '/tmp/switchboard/container.env'", "printf '%s\\n' 'SWITCHBOARD_EVENTS_PATH=/tmp/switchboard/events.jsonl' >> '/tmp/switchboard/container.env'", "'python' '/app/train.py' '--epochs' '1'"} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected %q in runner script:\n%s", want, script)
		}
	}
	if strings.Contains(script, "test-api-key") {
		t.Fatalf("runner script leaked API key:\n%s", script)
	}
}

func TestRunnerScriptLogsIntoPrivateRegistry(t *testing.T) {
	req := app.SubmitRequest{
		JobSpec: app.JobSpec{
			Name:  "private-smoke",
			Image: "registry.example.com/switchboard/smoke:latest",
		},
		RunID:     "r_test",
		AttemptID: "a_test",
	}
	script := runnerScript(req, RegistryAuth{
		Server:   "registry.example.com",
		Username: "switchboard",
		Password: "registry-token-value",
	})
	for _, want := range []string{
		"printf '%s' 'registry-token-value' > '/tmp/switchboard/registry-password'",
		"docker login 'registry.example.com' --username 'switchboard' --password-stdin < '/tmp/switchboard/registry-password'",
		"rm -f '/tmp/switchboard/registry-password'",
		"docker pull 'registry.example.com/switchboard/smoke:latest'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected %q in runner script:\n%s", want, script)
		}
	}
}

func TestAppendNewLogContentRedactsJSONAndDedupesEvents(t *testing.T) {
	redactor := redact.FromEnvironment(map[string]string{"API_KEY": "secret-value"})
	var logs, events, stdout bytes.Buffer
	seen := map[string]bool{}
	line := "{\"type\":\"metric\",\"step\":1,\"metrics\":{\"loss\":0.5},\"api_key\":\"secret-value\"}\n"
	offset := appendNewLogContent(&logs, &events, line, 0, "r_hyperbolic", "a_hyperbolic", time.Unix(0, 0), redactor, &stdout, seen)
	if offset != len(line) {
		t.Fatalf("offset = %d, want %d", offset, len(line))
	}
	_ = appendNewEventContent(&events, line, 0, "r_hyperbolic", "a_hyperbolic", time.Unix(0, 0), redactor, seen)
	if strings.Contains(logs.String(), "secret-value") || strings.Contains(stdout.String(), "secret-value") || strings.Contains(events.String(), "secret-value") {
		t.Fatalf("secret was not redacted: logs=%s stdout=%s events=%s", logs.String(), stdout.String(), events.String())
	}
	if got := strings.Count(events.String(), "\"run_id\":\"r_hyperbolic\""); got != 1 {
		t.Fatalf("event count = %d, events=%s", got, events.String())
	}
}

func TestSubmitRentsStartsRemoteCollectsArtifactsAndTerminates(t *testing.T) {
	home := t.TempDir()
	paths := artifact.ForRun(home, "r_hyperbolic")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	client := &fakeClient{
		rental: VirtualMachineRental{ID: 123, ExternalID: "rental-external", CostPerHour: 500, Status: "starting"},
		instances: []Instance{
			{ID: 123, ExternalID: "rental-external", Status: "running", CostPerHour: 500, Meta: InstanceMeta{PublicIP: "203.0.113.10", Username: "ubuntu", GPUCount: 1}},
			{ID: 123, ExternalID: "rental-external", Status: "running", CostPerHour: 500, Meta: InstanceMeta{PublicIP: "203.0.113.10", Username: "ubuntu", GPUCount: 1}},
		},
	}
	remote := &fakeRemote{
		files: map[string]string{
			remoteLogsPath:   "plain log\n{\"type\":\"metric\",\"step\":1,\"metrics\":{\"loss\":0.5}}\n",
			remoteEventsPath: "{\"type\":\"checkpoint\",\"step\":1,\"checkpoint_uri\":\"s3://bucket/ckpt.pt\"}\n",
			remoteExitPath:   "{\"exit_code\":0,\"exit_reason\":\"completed\"}",
		},
	}
	var stdout bytes.Buffer
	provider := NewWithClient(testConfig(), client, remote, &stdout, &bytes.Buffer{})
	provider.Sleep = func(ctx context.Context, d time.Duration) error { return nil }
	var started string
	var resources []app.ProviderResource
	result, err := provider.Submit(context.Background(), app.SubmitRequest{
		JobSpec:    app.JobSpec{Name: "valid", Image: "ghcr.io/example/smoke:latest"},
		RunID:      "r_hyperbolic",
		AttemptID:  "a_hyperbolic",
		RuntimeEnv: map[string]string{"SWITCHBOARD_RUN_ID": "r_hyperbolic"},
		RunDir:     paths.RunDir,
		OnStarted: func(ref app.ProviderJobRef) error {
			started = ref.ID
			return nil
		},
		OnResourceCreated: func(resource app.ProviderResource) error {
			resources = append(resources, resource)
			return nil
		},
		OnResourceUpdated: func(resource app.ProviderResource) error {
			resources = append(resources, resource)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if result.ProviderJobRef != "hyperbolic:123" || started != result.ProviderJobRef || result.ExitCode != 0 {
		t.Fatalf("result=%#v started=%q", result, started)
	}
	if client.rentReq.ConfigID != defaultVMConfigID || client.rentReq.GPUCount != 1 {
		t.Fatalf("rent request = %#v", client.rentReq)
	}
	if len(remote.writes) != 1 || !strings.Contains(remote.runs[0], "mkdir -p '/tmp/switchboard' && nohup bash") {
		t.Fatalf("remote writes=%#v runs=%#v", remote.writes, remote.runs)
	}
	if len(resources) != 3 || resources[0].State != app.ProviderResourceStateBooting || resources[1].State != app.ProviderResourceStateRunning || resources[2].State != app.ProviderResourceStateTerminating {
		t.Fatalf("resources = %#v", resources)
	}
	if resources[0].CleanupPolicy != app.ProviderResourceCleanupAlways || resources[0].Metadata["vm_config_id"] == "" {
		t.Fatalf("resource metadata = %#v", resources[0])
	}
	if len(client.terminated) != 1 || client.terminated[0] != "123" {
		t.Fatalf("terminated = %#v", client.terminated)
	}
	logs, err := os.ReadFile(paths.Logs)
	if err != nil {
		t.Fatalf("read logs: %v", err)
	}
	if !strings.Contains(string(logs), "plain log") {
		t.Fatalf("logs = %s", string(logs))
	}
	events, err := os.ReadFile(paths.EventsJSONL)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(events), `"type":"metric"`) || !strings.Contains(string(events), `"type":"checkpoint"`) {
		t.Fatalf("events = %s", string(events))
	}
}

func TestGetStatusIncludesProviderResourceState(t *testing.T) {
	provider := NewWithClient(testConfig(), &fakeClient{
		instances: []Instance{{ID: 123, Status: "terminated"}},
	}, &fakeRemote{}, &bytes.Buffer{}, &bytes.Buffer{})

	status, err := provider.GetStatus(context.Background(), app.ProviderJobRef{ID: "hyperbolic:123"})
	if err != nil {
		t.Fatalf("GetStatus returned error: %v", err)
	}
	if status.State != app.AttemptStateCanceled || status.ResourceState != app.ProviderResourceStateTerminated {
		t.Fatalf("status = %#v", status)
	}
}

func TestNormalizeErrorMapsHyperbolicAPIErrorKinds(t *testing.T) {
	tests := []struct {
		err  error
		kind app.ProviderErrorKind
	}{
		{&apiError{StatusCode: http.StatusUnauthorized, Code: "missing-api-key", Message: "bad key"}, app.ProviderErrorAuth},
		{&apiError{StatusCode: http.StatusBadRequest, Code: "invalid-rental-id", Message: "bad request"}, app.ProviderErrorInvalidSpec},
		{&apiError{StatusCode: http.StatusConflict, Code: "capacity", Message: "sold out"}, app.ProviderErrorCapacity},
		{&apiError{StatusCode: http.StatusPaymentRequired, Code: "low-balance", Message: "fund account"}, app.ProviderErrorQuota},
		{errors.New("dial tcp timeout"), app.ProviderErrorNetwork},
	}
	for _, tt := range tests {
		if got := app.ProviderErrorKindOf(normalizeError(tt.err)); got != tt.kind {
			t.Fatalf("kind = %s, want %s for %v", got, tt.kind, tt.err)
		}
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
			rental:    VirtualMachineRental{ID: 123, Status: "starting"},
			options:   []VirtualMachineOption{{GPUCount: 1, CostPerHour: 2.5}},
			instances: []Instance{{ID: 123, Status: "running", Meta: InstanceMeta{PublicIP: "203.0.113.20", Username: "ubuntu"}}},
		}
		remote := &fakeRemote{files: map[string]string{
			remoteLogsPath: "contract log\n",
			remoteExitPath: "{\"exit_code\":0,\"exit_reason\":\"completed\"}",
		}}
		provider := NewWithClient(testConfig(), client, remote, &bytes.Buffer{}, &bytes.Buffer{})
		provider.Sleep = func(ctx context.Context, d time.Duration) error { return nil }
		return contract.Subject{
			Name:       ProviderName,
			Adapter:    provider,
			ValidJob:   app.JobSpec{Name: "valid", Image: "image"},
			InvalidJob: app.JobSpec{Name: "invalid", Script: "train.py"},
			SubmitRequest: app.SubmitRequest{
				JobSpec:   app.JobSpec{Name: "valid", Image: "image"},
				RunID:     "r_contract",
				AttemptID: "a_contract",
				RunDir:    paths.RunDir,
			},
			ProviderRefPrefix: "hyperbolic:",
			StreamLogs:        contract.StreamLogsUnsupported,
			Cancel: func(t *testing.T, adapter app.ProviderAdapter) {
				if err := adapter.Cancel(context.Background(), app.ProviderJobRef{ID: "hyperbolic:123"}); err != nil {
					t.Fatalf("Cancel returned error: %v", err)
				}
			},
		}
	})
}

func testConfig() Config {
	return Config{
		SSHPrivateKey:          filepath.Join("testdata", "id_ed25519"),
		PollIntervalSeconds:    1,
		TerminateOnCompletion:  true,
		SSHReadyTimeoutSeconds: 1,
		SSHConnectTimeoutSecs:  1,
	}
}

type fakeClient struct {
	validateErr error
	options     []VirtualMachineOption
	rentReq     VirtualMachineRentalRequest
	rental      VirtualMachineRental
	instances   []Instance
	terminated  []string
	mu          sync.Mutex
}

func (c *fakeClient) ValidateAuth(ctx context.Context) error {
	return c.validateErr
}

func (c *fakeClient) ListVirtualMachineOptions(ctx context.Context) ([]VirtualMachineOption, error) {
	return c.options, nil
}

func (c *fakeClient) RentVirtualMachine(ctx context.Context, req VirtualMachineRentalRequest) (VirtualMachineRental, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rentReq = req
	if c.rental.ID == 0 && c.rental.ExternalID == "" {
		return VirtualMachineRental{ID: 123, Status: "starting"}, nil
	}
	return c.rental, nil
}

func (c *fakeClient) ListVirtualMachineInstances(ctx context.Context) ([]Instance, error) {
	return c.instances, nil
}

func (c *fakeClient) GetVirtualMachineInstance(ctx context.Context, id string) (Instance, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.instances) == 0 {
		return Instance{ID: 123, Status: "running", Meta: InstanceMeta{PublicIP: "203.0.113.5", Username: "ubuntu"}}, nil
	}
	instance := c.instances[0]
	if len(c.instances) > 1 {
		c.instances = c.instances[1:]
	}
	return instance, nil
}

func (c *fakeClient) TerminateVirtualMachine(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terminated = append(c.terminated, id)
	return nil
}

type fakeRemote struct {
	waitErr error
	files   map[string]string
	writes  []string
	runs    []string
}

func (r *fakeRemote) WaitForReady(ctx context.Context, target RemoteTarget, timeout time.Duration, sleep func(context.Context, time.Duration) error) error {
	return r.waitErr
}

func (r *fakeRemote) ReadFile(ctx context.Context, target RemoteTarget, path string) (string, error) {
	content, ok := r.files[path]
	if !ok {
		return "", os.ErrNotExist
	}
	return content, nil
}

func (r *fakeRemote) WriteFile(ctx context.Context, target RemoteTarget, path string, content string, perm os.FileMode) error {
	r.writes = append(r.writes, path)
	if r.files == nil {
		r.files = map[string]string{}
	}
	r.files[path] = content
	return nil
}

func (r *fakeRemote) Run(ctx context.Context, target RemoteTarget, command string) (string, error) {
	r.runs = append(r.runs, command)
	return "", nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
