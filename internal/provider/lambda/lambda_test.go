package lambda

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
	"github.com/anthonylu23/switchboard-cli/internal/credentials"
	"github.com/anthonylu23/switchboard-cli/internal/home"
	"github.com/anthonylu23/switchboard-cli/internal/provider/contract"
	"github.com/anthonylu23/switchboard-cli/internal/redact"
)

func TestValidateJobRequiresImageAndLambdaFields(t *testing.T) {
	provider := NewWithClient(Config{}, &fakeClient{}, &fakeRemote{}, &bytes.Buffer{}, &bytes.Buffer{})
	report := provider.ValidateJob(context.Background(), app.JobSpec{Name: "bad", Script: "train.py"})
	if report.Supported {
		t.Fatalf("expected unsupported report: %#v", report)
	}
	for _, want := range []string{"lambda provider requires job.image", "lambda provider v1 does not package local scripts", "lambda.region_name is required"} {
		if !contains(report.Reasons, want) {
			t.Fatalf("expected %q in reasons %#v", want, report.Reasons)
		}
	}
}

func TestCapabilitiesMapsInstanceTypes(t *testing.T) {
	provider := NewWithClient(testConfig(), &fakeClient{
		instanceTypes: map[string]InstanceTypesItem{
			"gpu_1x_a10": {
				InstanceType: InstanceType{
					Name:              "gpu_1x_a10",
					GPUDescription:    "A10 (24 GB)",
					PriceCentsPerHour: 75,
					Specs:             InstanceTypeSpecs{GPUs: 1, VCPUs: 30, MemoryGiB: 200, StorageGiB: 1400},
				},
				RegionsWithCapacityAvailable: []Region{{Name: "us-west-1"}},
			},
		},
	}, &fakeRemote{}, &bytes.Buffer{}, &bytes.Buffer{})
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities returned error: %v", err)
	}
	if len(capabilities.HardwareShapes) != 1 {
		t.Fatalf("hardware shapes = %#v", capabilities.HardwareShapes)
	}
	shape := capabilities.HardwareShapes[0]
	if shape.Provider != ProviderName || shape.MachineType != "gpu_1x_a10" || shape.GPUFamily != "A10" || shape.VRAMGBPerGPU != 24 || shape.OnDemandHourlyUSD != 0.75 {
		t.Fatalf("shape = %#v", shape)
	}
}

func TestCloudInitRunsDockerWithoutLambdaAPIKey(t *testing.T) {
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
	userData := cloudInitUserData(req, RegistryAuth{})
	for _, want := range []string{"docker pull 'ghcr.io/example/smoke:latest'", "docker' 'run' '--rm' '--gpus' 'all' '--env-file' '/tmp/switchboard/container.env'", "printf '%s\\n' 'SWITCHBOARD_EVENTS_PATH=/tmp/switchboard/events.jsonl' >> '/tmp/switchboard/container.env'", "'python' '/app/train.py' '--epochs' '1'"} {
		if !strings.Contains(userData, want) {
			t.Fatalf("expected %q in user data:\n%s", want, userData)
		}
	}
	if strings.Contains(userData, "secret-lambda-api-key") {
		t.Fatalf("user data leaked Lambda API key:\n%s", userData)
	}
}

func TestCloudInitLogsIntoPrivateRegistry(t *testing.T) {
	req := app.SubmitRequest{
		JobSpec: app.JobSpec{
			Name:  "private-smoke",
			Image: "registry.example.com/switchboard/smoke:latest",
		},
		RunID:     "r_test",
		AttemptID: "a_test",
	}
	userData := cloudInitUserData(req, RegistryAuth{
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
		if !strings.Contains(userData, want) {
			t.Fatalf("expected %q in user data:\n%s", want, userData)
		}
	}
}

func TestAppendNewLogContentRedactsJSONAndDedupesEvents(t *testing.T) {
	redactor := redact.FromEnvironment(map[string]string{"API_KEY": "secret-value"})
	var logs, events, stdout bytes.Buffer
	seen := map[string]bool{}
	line := "{\"type\":\"metric\",\"step\":1,\"metrics\":{\"loss\":0.5},\"api_key\":\"secret-value\"}\n"
	offset := appendNewLogContent(&logs, &events, line, 0, "r_lambda", "a_lambda", time.Unix(0, 0), redactor, &stdout, seen)
	if offset != len(line) {
		t.Fatalf("offset = %d, want %d", offset, len(line))
	}
	_ = appendNewEventContent(&events, line, 0, "r_lambda", "a_lambda", time.Unix(0, 0), redactor, seen)
	if strings.Contains(logs.String(), "secret-value") || strings.Contains(stdout.String(), "secret-value") || strings.Contains(events.String(), "secret-value") {
		t.Fatalf("secret was not redacted: logs=%s stdout=%s events=%s", logs.String(), stdout.String(), events.String())
	}
	if got := strings.Count(events.String(), "\"run_id\":\"r_lambda\""); got != 1 {
		t.Fatalf("event count = %d, events=%s", got, events.String())
	}
}

func TestAppendNewLogContentKeepsPartialLineOffset(t *testing.T) {
	redactor := redact.FromEnvironment()
	var logs, events bytes.Buffer
	seen := map[string]bool{}
	content := "partial"
	offset := appendNewLogContent(&logs, &events, content, 0, "r_lambda", "a_lambda", time.Unix(0, 0), redactor, nil, seen)
	if offset != 0 || logs.Len() != 0 {
		t.Fatalf("offset=%d logs=%q", offset, logs.String())
	}
	content += " line\n"
	offset = appendNewLogContent(&logs, &events, content, offset, "r_lambda", "a_lambda", time.Unix(0, 0), redactor, nil, seen)
	if offset != len(content) || logs.String() != "partial line\n" {
		t.Fatalf("offset=%d logs=%q", offset, logs.String())
	}
}

func TestSubmitLaunchesCollectsArtifactsAndTerminates(t *testing.T) {
	home := t.TempDir()
	paths := artifact.ForRun(home, "r_lambda")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	client := &fakeClient{
		launchIDs: []string{"i-123"},
		instances: []Instance{
			{ID: "i-123", Status: "active", IP: "203.0.113.10"},
			{ID: "i-123", Status: "active", IP: "203.0.113.10"},
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
		RunID:      "r_lambda",
		AttemptID:  "a_lambda",
		RuntimeEnv: map[string]string{"SWITCHBOARD_RUN_ID": "r_lambda"},
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
	if result.ProviderJobRef != "lambda:i-123" || started != result.ProviderJobRef || result.ExitCode != 0 {
		t.Fatalf("result=%#v started=%q", result, started)
	}
	if len(resources) != 3 || resources[0].State != app.ProviderResourceStateBooting || resources[1].State != app.ProviderResourceStateRunning || resources[2].State != app.ProviderResourceStateTerminating {
		t.Fatalf("resources = %#v", resources)
	}
	if resources[0].CleanupPolicy != app.ProviderResourceCleanupAlways || resources[0].Metadata["instance_type"] == "" {
		t.Fatalf("resource metadata = %#v", resources[0])
	}
	if len(client.launchReq.Tags) != 2 || client.launchReq.Tags[0].Key != "switchboard:run-id" || client.launchReq.Tags[1].Key != "switchboard:attempt-id" {
		t.Fatalf("launch tags = %#v", client.launchReq.Tags)
	}
	if len(client.terminated) != 1 || client.terminated[0] != "i-123" {
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

func TestSubmitCreatesPrivateArtifactsWhenMissing(t *testing.T) {
	home := t.TempDir()
	paths := artifact.ForRun(home, "r_private")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	if err := os.Remove(paths.Logs); err != nil {
		t.Fatalf("remove logs: %v", err)
	}
	if err := os.Remove(paths.EventsJSONL); err != nil {
		t.Fatalf("remove events: %v", err)
	}
	client := &fakeClient{
		launchIDs: []string{"i-private"},
		instances: []Instance{
			{ID: "i-private", Status: "active", IP: "203.0.113.10"},
			{ID: "i-private", Status: "active", IP: "203.0.113.10"},
		},
	}
	remote := &fakeRemote{files: map[string]string{
		remoteLogsPath: "private log\n",
		remoteExitPath: "{\"exit_code\":0,\"exit_reason\":\"completed\"}",
	}}
	provider := NewWithClient(testConfig(), client, remote, &bytes.Buffer{}, &bytes.Buffer{})
	provider.Sleep = func(ctx context.Context, d time.Duration) error { return nil }

	result, err := provider.Submit(context.Background(), app.SubmitRequest{
		JobSpec:   app.JobSpec{Name: "valid", Image: "ghcr.io/example/smoke:latest"},
		RunID:     "r_private",
		AttemptID: "a_private",
		RunDir:    paths.RunDir,
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertFileMode(t, paths.Logs, 0o600)
	assertFileMode(t, paths.EventsJSONL, 0o600)
}

func TestGetStatusIncludesProviderResourceState(t *testing.T) {
	provider := NewWithClient(testConfig(), &fakeClient{
		instances: []Instance{{ID: "i-terminated", Status: "terminated"}},
	}, &fakeRemote{}, &bytes.Buffer{}, &bytes.Buffer{})

	status, err := provider.GetStatus(context.Background(), app.ProviderJobRef{ID: "lambda:i-terminated"})
	if err != nil {
		t.Fatalf("GetStatus returned error: %v", err)
	}
	if status.State != app.AttemptStateCanceled || status.ResourceState != app.ProviderResourceStateTerminated {
		t.Fatalf("status = %#v", status)
	}
}

func TestNormalizeErrorMapsLambdaAPIErrorKinds(t *testing.T) {
	tests := []struct {
		err  error
		kind app.ProviderErrorKind
	}{
		{&apiError{StatusCode: http.StatusUnauthorized, Code: "global/invalid-api-key", Message: "bad key"}, app.ProviderErrorAuth},
		{&apiError{StatusCode: http.StatusBadRequest, Code: "global/invalid-parameters", Message: "bad request"}, app.ProviderErrorInvalidSpec},
		{&apiError{StatusCode: http.StatusConflict, Code: "provider/internal-unavailable", Message: "unavailable"}, app.ProviderErrorCapacity},
		{errors.New("dial tcp timeout"), app.ProviderErrorNetwork},
	}
	for _, tt := range tests {
		if got := app.ProviderErrorKindOf(normalizeError(tt.err)); got != tt.kind {
			t.Fatalf("kind = %s, want %s for %v", got, tt.kind, tt.err)
		}
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
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
			launchIDs: []string{"i-contract"},
			instances: []Instance{
				{ID: "i-contract", Status: "active", IP: "203.0.113.20"},
			},
			instanceTypes: map[string]InstanceTypesItem{
				"gpu_1x_a10": {
					InstanceType:                 InstanceType{Name: "gpu_1x_a10", GPUDescription: "A10 (24 GB)", PriceCentsPerHour: 75, Specs: InstanceTypeSpecs{GPUs: 1}},
					RegionsWithCapacityAvailable: []Region{{Name: "us-west-1"}},
				},
			},
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
			ProviderRefPrefix: "lambda:",
			StreamLogs:        contract.StreamLogsUnsupported,
			Cancel: func(t *testing.T, adapter app.ProviderAdapter) {
				if err := adapter.Cancel(context.Background(), app.ProviderJobRef{ID: "lambda:i-contract"}); err != nil {
					t.Fatalf("Cancel returned error: %v", err)
				}
			},
		}
	})
}

func TestLiveValidateAuth(t *testing.T) {
	if os.Getenv("SWITCHBOARD_LAMBDA_LIVE") != "1" {
		t.Skip("set SWITCHBOARD_LAMBDA_LIVE=1 to run live Lambda auth validation")
	}
	provider := New(Config{Credentials: liveCredentialResolver(t)}, &bytes.Buffer{}, &bytes.Buffer{})
	if err := provider.ValidateAuth(context.Background()); err != nil {
		t.Fatalf("ValidateAuth returned error: %v", err)
	}
}

func TestLiveSubmitSmoke(t *testing.T) {
	if os.Getenv("SWITCHBOARD_LAMBDA_LIVE_SUBMIT") != "1" {
		t.Skip("set SWITCHBOARD_LAMBDA_LIVE_SUBMIT=1 to run a billable live Lambda submit smoke")
	}
	image := requiredEnv(t, "SWITCHBOARD_LAMBDA_IMAGE")
	config := Config{
		RegionName:               requiredEnv(t, "SWITCHBOARD_LAMBDA_REGION"),
		InstanceTypeName:         requiredEnv(t, "SWITCHBOARD_LAMBDA_INSTANCE_TYPE"),
		SSHKeyName:               requiredEnv(t, "SWITCHBOARD_LAMBDA_SSH_KEY_NAME"),
		SSHPrivateKey:            requiredEnv(t, "SWITCHBOARD_LAMBDA_SSH_PRIVATE_KEY"),
		ImageFamily:              os.Getenv("SWITCHBOARD_LAMBDA_IMAGE_FAMILY"),
		PollIntervalSeconds:      15,
		TerminateOnCompletion:    true,
		TerminateOnCompletionSet: true,
		SSHReadyTimeoutSeconds:   900,
		Credentials:              liveCredentialResolver(t),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	home := t.TempDir()
	paths := artifact.ForRun(home, "r_live_lambda")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	provider := New(config, &bytes.Buffer{}, &bytes.Buffer{})
	result, err := provider.Submit(ctx, app.SubmitRequest{
		JobSpec: app.JobSpec{
			Name:    "lambda-live-smoke",
			Image:   image,
			Command: []string{"python", "/app/train.py"},
			Args:    []string{"--epochs", "1"},
		},
		RunID:      "r_live_lambda",
		AttemptID:  "a_live_lambda",
		RuntimeEnv: map[string]string{"SWITCHBOARD_RUN_ID": "r_live_lambda", "SWITCHBOARD_ATTEMPT_ID": "a_live_lambda"},
		RunDir:     paths.RunDir,
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v result=%#v", err, result)
	}
	if result.ExitCode != 0 {
		t.Fatalf("live smoke failed: %#v", result)
	}
	assertLiveArtifacts(t, paths)
}

func TestLiveSubmitFailureCleanup(t *testing.T) {
	if os.Getenv("SWITCHBOARD_LAMBDA_LIVE_FAILURE") != "1" {
		t.Skip("set SWITCHBOARD_LAMBDA_LIVE_FAILURE=1 to run a billable live Lambda failure cleanup smoke")
	}
	image := requiredEnv(t, "SWITCHBOARD_LAMBDA_IMAGE")
	config := liveSubmitConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	home := t.TempDir()
	paths := artifact.ForRun(home, "r_live_lambda_failure")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	provider := New(config, &bytes.Buffer{}, &bytes.Buffer{})
	result, err := provider.Submit(ctx, app.SubmitRequest{
		JobSpec: app.JobSpec{
			Name:    "lambda-live-failure",
			Image:   image,
			Command: []string{"python", "-c"},
			Args:    []string{"import sys; print('intentional lambda smoke failure', flush=True); sys.exit(7)"},
		},
		RunID:      "r_live_lambda_failure",
		AttemptID:  "a_live_lambda_failure",
		RuntimeEnv: map[string]string{"SWITCHBOARD_RUN_ID": "r_live_lambda_failure", "SWITCHBOARD_ATTEMPT_ID": "a_live_lambda_failure"},
		RunDir:     paths.RunDir,
	})
	if err != nil {
		t.Fatalf("Submit returned provider error instead of container exit result: %v result=%#v", err, result)
	}
	if result.ExitCode != 7 {
		t.Fatalf("failure smoke exit code = %d, want 7 result=%#v", result.ExitCode, result)
	}
	waitLiveInstanceTerminal(t, ctx, provider, result.ProviderJobRef)
}

func TestLiveCancelSmoke(t *testing.T) {
	if os.Getenv("SWITCHBOARD_LAMBDA_LIVE_CANCEL") != "1" {
		t.Skip("set SWITCHBOARD_LAMBDA_LIVE_CANCEL=1 to run a billable live Lambda cancel smoke")
	}
	image := requiredEnv(t, "SWITCHBOARD_LAMBDA_IMAGE")
	config := liveSubmitConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	home := t.TempDir()
	paths := artifact.ForRun(home, "r_live_lambda_cancel")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	provider := New(config, &bytes.Buffer{}, &bytes.Buffer{})
	started := make(chan string, 1)
	done := make(chan liveSubmitOutcome, 1)
	go func() {
		result, err := provider.Submit(ctx, app.SubmitRequest{
			JobSpec: app.JobSpec{
				Name:    "lambda-live-cancel",
				Image:   image,
				Command: []string{"python", "-c"},
				Args:    []string{"import time; print('sleeping for cancel smoke', flush=True); time.sleep(600)"},
			},
			RunID:      "r_live_lambda_cancel",
			AttemptID:  "a_live_lambda_cancel",
			RuntimeEnv: map[string]string{"SWITCHBOARD_RUN_ID": "r_live_lambda_cancel", "SWITCHBOARD_ATTEMPT_ID": "a_live_lambda_cancel"},
			RunDir:     paths.RunDir,
			OnStarted: func(ref app.ProviderJobRef) error {
				started <- ref.ID
				return nil
			},
		})
		done <- liveSubmitOutcome{Result: result, Err: err}
	}()
	var providerRef string
	select {
	case providerRef = <-started:
	case <-time.After(5 * time.Minute):
		t.Fatal("timed out waiting for Lambda provider ref")
	}
	if err := provider.Cancel(ctx, app.ProviderJobRef{ID: providerRef}); err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	waitLiveInstanceTerminal(t, ctx, provider, providerRef)
	select {
	case outcome := <-done:
		if outcome.Err == nil && outcome.Result.ExitCode == 0 {
			t.Fatalf("cancel smoke unexpectedly succeeded: %#v", outcome.Result)
		}
	case <-time.After(5 * time.Minute):
		t.Fatal("timed out waiting for Submit to observe cancellation")
	}
}

func testConfig() Config {
	return Config{
		RegionName:             "us-west-1",
		InstanceTypeName:       "gpu_1x_a10",
		SSHKeyName:             "switchboard",
		SSHPrivateKey:          filepath.Join("testdata", "id_ed25519"),
		PollIntervalSeconds:    1,
		TerminateOnCompletion:  true,
		SSHReadyTimeoutSeconds: 1,
		SSHConnectTimeoutSecs:  1,
	}
}

func liveSubmitConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		RegionName:               requiredEnv(t, "SWITCHBOARD_LAMBDA_REGION"),
		InstanceTypeName:         requiredEnv(t, "SWITCHBOARD_LAMBDA_INSTANCE_TYPE"),
		SSHKeyName:               requiredEnv(t, "SWITCHBOARD_LAMBDA_SSH_KEY_NAME"),
		SSHPrivateKey:            requiredEnv(t, "SWITCHBOARD_LAMBDA_SSH_PRIVATE_KEY"),
		ImageFamily:              os.Getenv("SWITCHBOARD_LAMBDA_IMAGE_FAMILY"),
		PollIntervalSeconds:      15,
		TerminateOnCompletion:    true,
		TerminateOnCompletionSet: true,
		SSHReadyTimeoutSeconds:   900,
		Credentials:              liveCredentialResolver(t),
	}
}

func assertLiveArtifacts(t *testing.T, paths artifact.Paths) {
	t.Helper()
	logs, err := os.ReadFile(paths.Logs)
	if err != nil {
		t.Fatalf("read live logs: %v", err)
	}
	if !strings.Contains(string(logs), "lambda switchboard job starting") {
		t.Fatalf("live logs did not contain runner start marker:\n%s", string(logs))
	}
	events, err := os.ReadFile(paths.EventsJSONL)
	if err != nil {
		t.Fatalf("read live events: %v", err)
	}
	for _, want := range []string{`"type":"metric"`, `"type":"checkpoint"`, `"type":"status"`} {
		if !strings.Contains(string(events), want) {
			t.Fatalf("live events missing %s:\n%s", want, string(events))
		}
	}
}

func waitLiveInstanceTerminal(t *testing.T, ctx context.Context, provider *Provider, providerRef string) {
	t.Helper()
	client, err := provider.clientFor()
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	deadline := time.Now().Add(10 * time.Minute)
	instanceID := instanceIDFromRef(providerRef)
	for {
		instance, err := client.GetInstance(ctx, instanceID)
		if err == nil && (instance.Status == "terminated" || instance.Status == "terminating") {
			return
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("instance %s did not become terminal; last error: %v", instanceID, err)
			}
			t.Fatalf("instance %s did not become terminal; last status: %s", instanceID, instance.Status)
		}
		if sleepErr := sleep(ctx, 15*time.Second); sleepErr != nil {
			t.Fatalf("waiting for instance terminal state: %v", sleepErr)
		}
	}
}

type liveSubmitOutcome struct {
	Result app.SubmitResult
	Err    error
}

func requiredEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is required", key)
	}
	return value
}

func liveCredentialResolver(t *testing.T) credentials.Resolver {
	t.Helper()
	homeDir, err := home.Resolve("")
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	passphrase := os.Getenv(credentials.PassphraseEnv)
	if passphrase == "" {
		t.Fatalf("%s is required for live Lambda tests", credentials.PassphraseEnv)
	}
	store, err := credentials.OpenEncryptedFile(credentials.DefaultPath(homeDir), passphrase)
	if err != nil {
		t.Fatalf("open credentials store: %v", err)
	}
	return credentials.Resolver{Store: store}
}

type fakeClient struct {
	validateErr   error
	instanceTypes map[string]InstanceTypesItem
	launchReq     LaunchInstanceRequest
	launchIDs     []string
	instances     []Instance
	terminated    []string
	mu            sync.Mutex
}

func (c *fakeClient) ValidateAuth(ctx context.Context) error {
	return c.validateErr
}

func (c *fakeClient) ListInstanceTypes(ctx context.Context) (map[string]InstanceTypesItem, error) {
	return c.instanceTypes, nil
}

func (c *fakeClient) ListInstances(ctx context.Context) ([]Instance, error) {
	return c.instances, nil
}

func (c *fakeClient) GetInstance(ctx context.Context, id string) (Instance, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.instances) == 0 {
		return Instance{ID: id, Status: "active", IP: "203.0.113.5"}, nil
	}
	instance := c.instances[0]
	if len(c.instances) > 1 {
		c.instances = c.instances[1:]
	}
	return instance, nil
}

func (c *fakeClient) LaunchInstance(ctx context.Context, req LaunchInstanceRequest) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.launchReq = req
	if len(c.launchIDs) == 0 {
		return []string{"i-fake"}, nil
	}
	return c.launchIDs, nil
}

func (c *fakeClient) TerminateInstances(ctx context.Context, ids []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terminated = append(c.terminated, ids...)
	return nil
}

type fakeRemote struct {
	waitErr error
	files   map[string]string
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

func contains(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
