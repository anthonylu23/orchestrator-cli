package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/config"
	"github.com/anthonylu23/switchboard-cli/internal/packaging"
	mockprovider "github.com/anthonylu23/switchboard-cli/internal/provider/mock"
	"github.com/anthonylu23/switchboard-cli/internal/state"
)

func TestLocalTrainStatusLogsIntegration(t *testing.T) {
	repo := repoRoot(t)
	home := filepath.Join(t.TempDir(), "home")
	var stdout, stderr bytes.Buffer

	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "local", "--script", filepath.Join(repo, "examples", "train.py")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := extractRunID(t, stdout.String())

	eventsPath := filepath.Join(home, "runs", runID, "events.jsonl")
	events, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(events), `"type":"metric"`) || !strings.Contains(string(events), `"type":"checkpoint"`) {
		t.Fatalf("events missing expected records:\n%s", string(events))
	}
	summaryPath := filepath.Join(home, "runs", runID, "summary.json")
	var summary app.Summary
	content, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if err := json.Unmarshal(content, &summary); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	if summary.State != app.RunStateSucceeded || summary.CheckpointCount != 1 {
		t.Fatalf("summary = %#v", summary)
	}

	stdout.Reset()
	cmd = NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "status", runID, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), `"state":"succeeded"`) {
		t.Fatalf("status output = %s", stdout.String())
	}

	stdout.Reset()
	cmd = NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "logs", runID})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("logs returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "finished local training") {
		t.Fatalf("logs output = %s", stdout.String())
	}
}

func TestLocalTrainMaterializesBundledData(t *testing.T) {
	repo := repoRoot(t)
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	dataPath := filepath.Join(dir, "train.txt")
	if err := os.WriteFile(dataPath, []byte("materialized-data\n"), 0o600); err != nil {
		t.Fatalf("write data: %v", err)
	}
	configPath := filepath.Join(dir, "switchboard.yaml")
	config := `
job:
  script: "` + filepath.Join(repo, "examples", "read_data.py") + `"
  args: ["/workspace/data/train.txt"]
data:
  inputs:
    - name: train
      source: "` + dataPath + `"
      mount: "/workspace/data/train.txt"
      mode: bundle
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "local", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := extractRunID(t, stdout.String())
	if !strings.Contains(stdout.String(), "materialized-data") {
		t.Fatalf("stdout = %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(home, "runs", runID, "workspace", "data", "train.txt")); err != nil {
		t.Fatalf("materialized file missing: %v", err)
	}
}

func TestLocalTrainRunsPyTorchIrisDemo(t *testing.T) {
	requirePythonTorch(t)

	repo := repoRoot(t)
	t.Chdir(repo)
	home := filepath.Join(t.TempDir(), "home")
	var stdout, stderr bytes.Buffer

	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "local", "--config", filepath.Join(repo, "examples", "iris-pytorch.yaml")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := extractRunID(t, stdout.String())
	paths := artifact.ForRun(home, runID)

	events, err := os.ReadFile(paths.EventsJSONL)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(events), `"type":"metric"`) || !strings.Contains(string(events), `"val_accuracy"`) {
		t.Fatalf("events missing expected PyTorch metrics:\n%s", string(events))
	}

	var summary app.Summary
	content, err := os.ReadFile(paths.Summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if err := json.Unmarshal(content, &summary); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	if summary.State != app.RunStateSucceeded {
		t.Fatalf("summary state = %s, want %s: %#v", summary.State, app.RunStateSucceeded, summary)
	}
	if summary.CheckpointCount == 0 {
		t.Fatalf("expected checkpoint events: %#v", summary)
	}
	if summary.FinalMetrics["val_accuracy"] == 0 || summary.BestMetrics["val_accuracy"] == 0 {
		t.Fatalf("expected validation accuracy in summary: %#v", summary)
	}
	if _, err := os.Stat(filepath.Join(paths.Workspace, "data", "iris", "Iris.csv")); err != nil {
		t.Fatalf("materialized Iris CSV missing: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(paths.Checkpoints, "iris-epoch-*.pt"))
	if err != nil {
		t.Fatalf("glob checkpoints: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one PyTorch checkpoint file")
	}
}

func TestCancelRunningLocalRun(t *testing.T) {
	repo := repoRoot(t)
	home := filepath.Join(t.TempDir(), "home")
	var trainStdout, trainStderr bytes.Buffer
	trainCmd := NewRootCommand(Options{Stdout: &trainStdout, Stderr: &trainStderr})
	trainCmd.SetArgs([]string{"--home", home, "train", "--provider", "local", "--script", filepath.Join(repo, "examples", "slow.py")})

	var wg sync.WaitGroup
	wg.Add(1)
	var trainErr error
	go func() {
		defer wg.Done()
		trainErr = trainCmd.Execute()
	}()

	runID := waitForRunID(t, home)
	var followStdout bytes.Buffer
	followCtx, cancelFollow := context.WithCancel(context.Background())
	followCmd := NewRootCommand(Options{Stdout: &followStdout, Stderr: &bytes.Buffer{}})
	followCmd.SetContext(followCtx)
	followCmd.SetArgs([]string{"--home", home, "logs", runID, "--follow"})
	var followWG sync.WaitGroup
	followWG.Add(1)
	go func() {
		defer followWG.Done()
		_ = followCmd.Execute()
	}()

	waitForText(t, &trainStdout, "slow start")
	var cancelStdout bytes.Buffer
	cancelCmd := NewRootCommand(Options{Stdout: &cancelStdout, Stderr: &bytes.Buffer{}})
	cancelCmd.SetArgs([]string{"--home", home, "cancel", runID})
	if err := cancelCmd.Execute(); err != nil {
		t.Fatalf("cancel returned error: %v", err)
	}
	wg.Wait()
	cancelFollow()
	followWG.Wait()
	if trainErr == nil {
		t.Fatal("expected train command to return cancellation exit error")
	}
	if !strings.Contains(cancelStdout.String(), "canceled") {
		t.Fatalf("cancel stdout = %s", cancelStdout.String())
	}
	statusStdout := bytes.Buffer{}
	statusCmd := NewRootCommand(Options{Stdout: &statusStdout, Stderr: &bytes.Buffer{}})
	statusCmd.SetArgs([]string{"--home", home, "status", runID, "--json"})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("status returned error: %v", err)
	}
	if !strings.Contains(statusStdout.String(), `"state":"canceled"`) {
		t.Fatalf("status = %s", statusStdout.String())
	}
	if !strings.Contains(followStdout.String(), "slow start") {
		t.Fatalf("follow output = %s", followStdout.String())
	}
}

func TestAutoProviderMockFailoverIntegration(t *testing.T) {
	repo := repoRoot(t)
	home := filepath.Join(t.TempDir(), "home")
	configPath := filepath.Join(repo, "examples", "failover.yaml")
	var stdout, stderr bytes.Buffer

	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "auto", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := extractRunID(t, stdout.String())
	if !strings.Contains(stdout.String(), "Selected mock-lambda") || !strings.Contains(stdout.String(), "Selected mock-gcp") {
		t.Fatalf("stdout = %s", stdout.String())
	}
	paths := artifact.ForRun(home, runID)
	var summary app.Summary
	content, err := os.ReadFile(paths.Summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if err := json.Unmarshal(content, &summary); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	if summary.State != app.RunStateSucceeded || summary.ResumeCount != 1 || summary.CheckpointCount != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if len(summary.ProviderAttempts) != 2 {
		t.Fatalf("attempts = %#v", summary.ProviderAttempts)
	}
	if summary.ProviderAttempts[0].EstimatedHourlyUSD == nil || *summary.ProviderAttempts[0].EstimatedHourlyUSD != 1.10 {
		t.Fatalf("first attempt estimate = %#v", summary.ProviderAttempts[0])
	}
	second := summary.ProviderAttempts[1]
	if second.ResumeFromURI != "mock://checkpoints/r_123/ckpt-800" || second.ResumeFromStep == nil || *second.ResumeFromStep != 800 {
		t.Fatalf("second attempt resume provenance = %#v", second)
	}
	if second.EstimatedHourlyUSD == nil || *second.EstimatedHourlyUSD != 1.30 || second.EstimateCurrency != "USD" {
		t.Fatalf("second attempt estimate = %#v", second)
	}
	store, err := state.Open(paths.DB)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer store.Close()
	decision, err := store.GetRoutingDecision(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRoutingDecision returned error: %v", err)
	}
	if decision.SelectedProvider != "mock-gcp" {
		t.Fatalf("routing decision = %#v", decision)
	}
	if len(decision.RejectedProviders) == 0 {
		t.Fatalf("expected rejected providers: %#v", decision)
	}
}

func TestLocalTrainRedactsSecretsFromArtifacts(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	script := filepath.Join(dir, "secret.py")
	if err := os.WriteFile(script, []byte(`
import json
import os

secret = os.environ["API_TOKEN"]
print("plain " + secret)
print(json.dumps({
    "type": "status",
    "state": "ok",
    "api_key": "raw-event-secret",
    "nested": {"message": secret},
}))
`), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	configPath := filepath.Join(dir, "switchboard.yaml")
	config := `
job:
  script: "` + script + `"
  env:
    API_TOKEN: "secret-value-123"
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "local", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := extractRunID(t, stdout.String())
	paths := artifact.ForRun(home, runID)
	logs, err := os.ReadFile(paths.Logs)
	if err != nil {
		t.Fatalf("read logs: %v", err)
	}
	events, err := os.ReadFile(paths.EventsJSONL)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	summary, err := os.ReadFile(paths.Summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	for _, content := range []string{string(logs), string(events), string(summary)} {
		if strings.Contains(content, "secret-value-123") || strings.Contains(content, "raw-event-secret") {
			t.Fatalf("secret persisted in artifact:\n%s", content)
		}
	}
	for _, content := range []string{string(logs), string(events)} {
		if !strings.Contains(content, "[REDACTED]") {
			t.Fatalf("expected redaction marker in artifact:\n%s", content)
		}
	}
}

func TestLocalTrainInjectsSwitchboardAndLegacyRuntimeEnv(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	script := filepath.Join(dir, "runtime_env.py")
	if err := os.WriteFile(script, []byte(`
import os

pairs = [
    ("SWITCHBOARD_RUN_ID", "ORCHESTRATOR_RUN_ID"),
    ("SWITCHBOARD_ATTEMPT_ID", "ORCHESTRATOR_ATTEMPT_ID"),
    ("SWITCHBOARD_CHECKPOINT_DIR", "ORCHESTRATOR_CHECKPOINT_DIR"),
    ("SWITCHBOARD_RESUME_FROM", "ORCHESTRATOR_RESUME_FROM"),
    ("SWITCHBOARD_EVENTS_PATH", "ORCHESTRATOR_EVENTS_PATH"),
]
for current, legacy in pairs:
    assert current in os.environ, current
    assert legacy in os.environ, legacy
    assert os.environ[current] == os.environ[legacy], (current, legacy)
print("runtime env ok")
`), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}

	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "local", "--script", script})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "runtime env ok") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestProvidersListIncludesMocks(t *testing.T) {
	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"providers", "list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("providers list returned error: %v", err)
	}
	for _, name := range []string{"local", "mock-lambda", "mock-gcp", "gcp"} {
		if !strings.Contains(stdout.String(), name) {
			t.Fatalf("%q missing from %s", name, stdout.String())
		}
	}
}

func TestGCPTrainIntegrationUsesConfiguredProvider(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	configPath := filepath.Join(dir, "switchboard.yaml")
	configContent := `
job:
  name: gcp-test
  image: us-docker.pkg.dev/project/repo/train:latest
  command: ["python", "-m", "trainer"]
  args: ["--epochs", "1"]
  env:
    FOO: bar
data:
  inputs:
    - name: train
      source: gs://bucket/train
      mode: uri
gcp:
  project_id: test-project
  location: us-central1
  output_uri_prefix: gs://bucket/outputs
  machine_type: n1-standard-8
  accelerator_type: NVIDIA_TESLA_T4
  accelerator_count: 1
  estimate_hourly_usd: 2.50
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var captured config.GCPConfig
	fake := &fakeGCPAdapter{}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout: &stdout,
		Stderr: &stderr,
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			captured = cfg
			return fake
		},
	})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "gcp", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if captured.ProjectID != "test-project" || captured.EstimateHourlyUSD != 2.50 {
		t.Fatalf("captured config = %#v", captured)
	}
	runID := extractRunID(t, stdout.String())
	paths := artifact.ForRun(home, runID)
	store, err := state.Open(paths.DB)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer store.Close()
	attempts, err := store.AttemptsByRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %#v", attempts)
	}
	if attempts[0].Provider != "gcp" || attempts[0].ProviderRef != "projects/test-project/locations/us-central1/customJobs/fake" {
		t.Fatalf("attempt = %#v", attempts[0])
	}
	if attempts[0].EstimatedHourlyUSD == nil || *attempts[0].EstimatedHourlyUSD != 2.5 {
		t.Fatalf("estimate = %#v", attempts[0])
	}
	if fake.lastSubmit.RuntimeEnv["SWITCHBOARD_CHECKPOINT_DIR"] != "/tmp/switchboard/checkpoints" {
		t.Fatalf("gcp checkpoint env = %#v", fake.lastSubmit.RuntimeEnv)
	}
	if strings.Contains(fake.lastSubmit.RuntimeEnv["SWITCHBOARD_EVENTS_PATH"], home) {
		t.Fatalf("gcp events path should not point at local artifact path: %#v", fake.lastSubmit.RuntimeEnv)
	}

	stdout.Reset()
	statusCmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	statusCmd.SetArgs([]string{"--home", home, "status", runID, "--json"})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("status json returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), `"image":"us-docker.pkg.dev/project/repo/train:latest"`) {
		t.Fatalf("status json = %s", stdout.String())
	}

	stdout.Reset()
	statusCmd = NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	statusCmd.SetArgs([]string{"--home", home, "status", runID})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("status returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "us-docker.pkg.dev/project/repo/train:latest") {
		t.Fatalf("status = %s", stdout.String())
	}
}

func TestGCPProviderFailureUsesStableRoutingExit(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "switchboard.yaml")
	configContent := `
job:
  name: gcp-test
  image: us-docker.pkg.dev/project/repo/train:latest
gcp:
  project_id: test-project
  location: us-central1
  output_uri_prefix: gs://bucket/outputs
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	code, err := runTrain(context.Background(), Options{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return &fakeGCPAdapter{submitErr: &app.ProviderError{Kind: app.ProviderErrorQuota, Message: "quota exceeded"}, submitExitCode: exitCodeRouting}
		},
	}, config.ResolvedTrainConfig{
		Provider:        "gcp",
		SwitchboardHome: filepath.Join(dir, "home"),
		Job:             app.JobSpec{Name: "gcp-test", Image: "image"},
		GCP: config.GCPConfig{
			ProjectID:       "test-project",
			Location:        "us-central1",
			OutputURIPrefix: "gs://bucket/outputs",
		},
	})
	if err == nil {
		t.Fatal("expected gcp submit error")
	}
	if code != exitCodeRouting {
		t.Fatalf("exit code = %d, want %d", code, exitCodeRouting)
	}
}

func TestRunTrainPackagesLocalScriptForGCP(t *testing.T) {
	dir := t.TempDir()
	contextDir := filepath.Join(dir, "context")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatalf("create context: %v", err)
	}
	script := filepath.Join(contextDir, "train.py")
	if err := os.WriteFile(script, []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte("FROM python:3.11-slim\nCOPY . /workspace\nWORKDIR /workspace\n"), 0o600); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	fakeGCP := &fakeGCPAdapter{}
	fakeBuilder := &fakeImageBuilder{image: "us-central1-docker.pkg.dev/test-project/switchboard/train:latest"}
	var stdout bytes.Buffer
	code, err := runTrain(context.Background(), Options{
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fakeGCP
		},
		ImageBuilderFactory: func(stdout io.Writer, stderr io.Writer) ImageBuilder {
			return fakeBuilder
		},
	}, config.ResolvedTrainConfig{
		Provider:        "gcp",
		SwitchboardHome: filepath.Join(dir, "home"),
		Job:             app.JobSpec{Name: "train.py", Script: script},
		Packaging: config.PackagingConfig{
			Dockerfile: "Dockerfile",
			Context:    contextDir,
			Image:      "us-central1-docker.pkg.dev/test-project/switchboard/train:latest",
			Platform:   "linux/amd64",
		},
		GCP: config.GCPConfig{
			ProjectID:       "test-project",
			Location:        "us-central1",
			OutputURIPrefix: "gs://bucket/outputs",
		},
	})
	if err != nil || code != 0 {
		t.Fatalf("runTrain code=%d err=%v stdout=%s", code, err, stdout.String())
	}
	if fakeBuilder.request.Config.ContextDir != contextDir || fakeBuilder.request.Config.Platform != "linux/amd64" {
		t.Fatalf("build request = %#v", fakeBuilder.request)
	}
	if fakeGCP.lastSubmit.JobSpec.Image != fakeBuilder.image || fakeGCP.lastSubmit.JobSpec.Script != "" {
		t.Fatalf("submitted job = %#v", fakeGCP.lastSubmit.JobSpec)
	}
	if !reflect.DeepEqual(fakeGCP.lastSubmit.JobSpec.Command, []string{"python3", "train.py"}) {
		t.Fatalf("command = %#v", fakeGCP.lastSubmit.JobSpec.Command)
	}
}

func TestGCPCancelStateIsNotOverwrittenBySubmitError(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	fake := &fakeGCPAdapter{
		submitErr:      &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: "gcp custom job canceled"},
		submitExitCode: exitCodeCanceled,
		submitStarted:  make(chan struct{}),
		releaseSubmit:  make(chan struct{}),
		cancelCalled:   make(chan struct{}),
	}
	opts := Options{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fake
		},
	}

	type trainResult struct {
		code int
		err  error
	}
	done := make(chan trainResult, 1)
	go func() {
		code, err := runTrain(context.Background(), opts, config.ResolvedTrainConfig{
			Provider:        "gcp",
			SwitchboardHome: home,
			Job:             app.JobSpec{Name: "gcp-test", Image: "image"},
			GCP: config.GCPConfig{
				ProjectID:       "test-project",
				Location:        "us-central1",
				OutputURIPrefix: "gs://bucket/outputs",
			},
		})
		done <- trainResult{code: code, err: err}
	}()

	select {
	case <-fake.submitStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("submit did not start")
	}
	runID := waitForRunID(t, home)
	if err := cancelRun(context.Background(), opts, home, runID); err != nil {
		t.Fatalf("cancelRun returned error: %v", err)
	}
	select {
	case <-fake.cancelCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("fake cancel was not called")
	}
	close(fake.releaseSubmit)

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("runTrain returned error: %v", result.err)
		}
		if result.code != exitCodeCanceled {
			t.Fatalf("exit code = %d, want %d", result.code, exitCodeCanceled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runTrain did not finish")
	}

	store, err := state.Open(artifact.ForRun(home, runID).DB)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer store.Close()
	run, err := store.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun returned error: %v", err)
	}
	if run.State != app.RunStateCanceled || run.ExitCode != exitCodeCanceled {
		t.Fatalf("run = %#v", run)
	}
	attempts, err := store.AttemptsByRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("AttemptsByRun returned error: %v", err)
	}
	if len(attempts) != 1 || attempts[0].State != app.AttemptStateCanceled || attempts[0].ExitCode != exitCodeCanceled {
		t.Fatalf("attempts = %#v", attempts)
	}
}

func TestLocalTrainFailureProducesArtifacts(t *testing.T) {
	repo := repoRoot(t)
	home := filepath.Join(t.TempDir(), "home")
	var stdout, stderr bytes.Buffer

	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "local", "--script", filepath.Join(repo, "examples", "fail.py")})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected train failure")
	}
	runID := extractRunID(t, stdout.String())
	content, readErr := os.ReadFile(filepath.Join(home, "runs", runID, "summary.json"))
	if readErr != nil {
		t.Fatalf("read summary: %v", readErr)
	}
	if !strings.Contains(string(content), `"state": "failed"`) {
		t.Fatalf("summary = %s", string(content))
	}
	if !strings.Contains(stderr.String(), "runtime failure") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestRunTrainMissingDataReturnsInvalidSpecExit(t *testing.T) {
	code, err := runTrain(context.Background(), Options{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, config.ResolvedTrainConfig{
		Provider:        string(app.ProviderLocal),
		SwitchboardHome: filepath.Join(t.TempDir(), "home"),
		Job: app.JobSpec{
			Script: filepath.Join(repoRoot(t), "examples", "train.py"),
			Data: []app.DataInput{{
				Name:   "missing",
				Source: filepath.Join(t.TempDir(), "missing.csv"),
				Mode:   app.DataInputModeBundle,
			}},
		},
	})
	if err == nil {
		t.Fatal("expected missing data error")
	}
	if code != exitCodeInvalidSpec {
		t.Fatalf("exit code = %d, want %d", code, exitCodeInvalidSpec)
	}
}

func TestRunTrainRetryableFailureWithoutCheckpointReturnsMissingResumeExit(t *testing.T) {
	var stdout bytes.Buffer
	code, err := runTrain(context.Background(), Options{Stdout: &stdout, Stderr: &bytes.Buffer{}}, config.ResolvedTrainConfig{
		Provider:        string(app.ProviderAuto),
		SwitchboardHome: filepath.Join(t.TempDir(), "home"),
		Job:             app.JobSpec{Script: "train.py"},
		Routing:         config.RoutingConfig{Objective: "min_cost", MaxAttempts: 2},
		Mock: config.MockConfig{Providers: []config.MockProviderConfig{{
			Name:        "mock-lambda",
			HourlyCost:  1.10,
			FailureMode: mockprovider.FailureCapacity,
		}}},
	})
	if err == nil {
		t.Fatal("expected missing checkpoint error")
	}
	if code != exitCodeMissingResume {
		t.Fatalf("exit code = %d, want %d", code, exitCodeMissingResume)
	}
	if !strings.Contains(err.Error(), "no checkpoint") {
		t.Fatalf("error = %v", err)
	}
}

func TestAutoProviderWithoutGCPConfigDoesNotInitializeGCP(t *testing.T) {
	dir := t.TempDir()
	factoryCalls := 0
	code, err := runTrain(context.Background(), Options{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			factoryCalls++
			return &fakeGCPAdapter{}
		},
	}, config.ResolvedTrainConfig{
		Provider:        string(app.ProviderAuto),
		SwitchboardHome: filepath.Join(dir, "home"),
		Job:             app.JobSpec{Script: "train.py"},
		Routing:         config.RoutingConfig{Objective: "min_cost", MaxAttempts: 1},
		Mock: config.MockConfig{Providers: []config.MockProviderConfig{{
			Name:       "mock-fast",
			HourlyCost: 0.5,
		}}},
	})
	if err != nil || code != 0 {
		t.Fatalf("runTrain code=%d err=%v", code, err)
	}
	if factoryCalls != 0 {
		t.Fatalf("gcp factory calls = %d", factoryCalls)
	}
}

func TestRunTrainTerminalProviderFailureDoesNotFailOver(t *testing.T) {
	var stdout bytes.Buffer
	code, err := runTrain(context.Background(), Options{Stdout: &stdout, Stderr: &bytes.Buffer{}}, config.ResolvedTrainConfig{
		Provider:        string(app.ProviderAuto),
		SwitchboardHome: filepath.Join(t.TempDir(), "home"),
		Job:             app.JobSpec{Script: "train.py"},
		Routing:         config.RoutingConfig{Objective: "min_cost", MaxAttempts: 2},
		Mock: config.MockConfig{Providers: []config.MockProviderConfig{{
			Name:        "mock-lambda",
			HourlyCost:  1.10,
			FailureMode: mockprovider.FailureRuntime,
		}}},
	})
	if err == nil {
		t.Fatal("expected terminal runtime failure")
	}
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if strings.Contains(stdout.String(), "Selected mock-gcp") {
		t.Fatalf("terminal failure should not fail over, stdout = %s", stdout.String())
	}
}

func extractRunID(t *testing.T, output string) string {
	t.Helper()
	for _, field := range strings.Fields(output) {
		if strings.HasPrefix(field, "r_") {
			return field
		}
	}
	t.Fatalf("run id not found in output: %s", output)
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

func waitForRunID(t *testing.T, home string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(filepath.Join(home, "runs"))
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), "r_") {
				return entry.Name()
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("run id not created")
	return ""
}

func waitForText(t *testing.T, buf *bytes.Buffer, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), text) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%q not found in %q", text, buf.String())
}

func requirePythonTorch(t *testing.T) {
	t.Helper()
	cmd := exec.Command("python3", "-c", "import torch")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("python3 with torch is required for PyTorch Iris demo: %v\n%s", err, string(output))
	}
}

type fakeGCPAdapter struct {
	submitErr      error
	submitExitCode int
	submitStarted  chan struct{}
	releaseSubmit  chan struct{}
	cancelCalled   chan struct{}
	lastSubmit     app.SubmitRequest
}

type fakeImageBuilder struct {
	image   string
	request packaging.BuildRequest
	err     error
}

func (b *fakeImageBuilder) BuildAndPush(ctx context.Context, req packaging.BuildRequest) (packaging.BuildResult, error) {
	b.request = req
	if b.err != nil {
		return packaging.BuildResult{}, b.err
	}
	return packaging.BuildResult{Image: b.image}, nil
}

func (a *fakeGCPAdapter) Name() app.ProviderName {
	return "gcp"
}

func (a *fakeGCPAdapter) ValidateAuth(ctx context.Context) error {
	return nil
}

func (a *fakeGCPAdapter) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	return app.ProviderCapabilities{
		SupportsDockerImage:     true,
		SupportedURISchemes:     []string{"gs"},
		SupportsObjectStorePull: true,
	}, nil
}

func (a *fakeGCPAdapter) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	if spec.Image == "" {
		return app.SupportReport{Supported: false, Reasons: []string{"gcp provider requires job.image"}}
	}
	return app.SupportReport{Supported: true}
}

func (a *fakeGCPAdapter) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	return app.CostEstimate{HourlyUSD: 2.5, Currency: "USD"}, nil
}

func (a *fakeGCPAdapter) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
	a.lastSubmit = req
	ref := "projects/test-project/locations/us-central1/customJobs/fake"
	if req.OnStarted != nil {
		if err := req.OnStarted(app.ProviderJobRef{ID: ref}); err != nil {
			return app.SubmitResult{}, err
		}
	}
	if a.submitStarted != nil {
		close(a.submitStarted)
	}
	if a.releaseSubmit != nil {
		<-a.releaseSubmit
	}
	if a.submitErr != nil {
		code := a.submitExitCode
		if code == 0 {
			code = 1
		}
		return app.SubmitResult{ProviderJobRef: ref, ExitCode: code, ExitReason: a.submitErr.Error()}, a.submitErr
	}
	return app.SubmitResult{ProviderJobRef: ref, ExitCode: 0, ExitReason: "completed"}, nil
}

func (a *fakeGCPAdapter) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	return app.ProviderJobStatus{State: app.AttemptStateSucceeded}, nil
}

func (a *fakeGCPAdapter) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, errUnsupportedFakeLogs
}

func (a *fakeGCPAdapter) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	if a.cancelCalled != nil {
		close(a.cancelCalled)
	}
	return nil
}

var errUnsupportedFakeLogs = errors.New("unsupported")
