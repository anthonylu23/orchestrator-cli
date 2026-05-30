package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/anthonylu23/switchboard-cli/internal/credentials"
	"github.com/anthonylu23/switchboard-cli/internal/event"
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

func TestResumeLocalRunUsesLatestCheckpoint(t *testing.T) {
	repo := repoRoot(t)
	home := filepath.Join(t.TempDir(), "home")
	var stdout, stderr bytes.Buffer

	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "local", "--script", filepath.Join(repo, "examples", "train.py")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := extractRunID(t, stdout.String())

	stdout.Reset()
	cmd = NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "resume", runID, "--provider", "local", "--script", filepath.Join(repo, "examples", "train.py")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("resume returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Found checkpoint: step 3") {
		t.Fatalf("resume stdout = %s", stdout.String())
	}

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
	if len(attempts) != 2 {
		t.Fatalf("attempts = %#v", attempts)
	}
	if attempts[1].ResumeFromStep == nil || *attempts[1].ResumeFromStep != 3 || !strings.HasPrefix(attempts[1].ResumeFromURI, "file://") {
		t.Fatalf("resume provenance = %#v", attempts[1])
	}

	var summary app.Summary
	content, err := os.ReadFile(paths.Summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if err := json.Unmarshal(content, &summary); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	if summary.State != app.RunStateSucceeded || summary.ResumeCount != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestResumeRejectsInvalidRunIDWithoutPanic(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "resume", "../bad", "--provider", "local", "--script", filepath.Join(repoRoot(t), "examples", "train.py")})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid run id error")
	}
	if !strings.Contains(err.Error(), "invalid run id") {
		t.Fatalf("error = %v", err)
	}
}

func TestResumeRejectsProviderWithoutCheckpointScheme(t *testing.T) {
	repo := repoRoot(t)
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	var stdout, stderr bytes.Buffer

	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "local", "--script", filepath.Join(repo, "examples", "train.py")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := extractRunID(t, stdout.String())

	configPath := filepath.Join(dir, "gcp.yaml")
	configContent := `
job:
  image: us-docker.pkg.dev/project/repo/train:latest
gcp:
  project_id: test-project
  location: us-central1
  output_uri_prefix: gs://bucket/outputs
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	fake := &fakeGCPAdapter{}
	stdout.Reset()
	cmd = NewRootCommand(Options{
		Stdout: &stdout,
		Stderr: &stderr,
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fake
		},
	})
	cmd.SetArgs([]string{"--home", home, "resume", runID, "--provider", "gcp", "--config", configPath})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected resume to reject incompatible checkpoint scheme")
	}
	if !strings.Contains(err.Error(), "does not support \"file\" checkpoint resume") {
		t.Fatalf("error = %v", err)
	}
	if fake.lastSubmit.RunID != "" {
		t.Fatalf("submit should not be called: %#v", fake.lastSubmit)
	}
}

func TestGCPResumeUsesSharedGCSCheckpoint(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	runID := "r_gcp_resume"
	paths := artifact.ForRun(home, runID)
	if err := artifact.EnsureHome(home); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	store, err := state.Open(paths.DB)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	started := time.Now().UTC().Add(-time.Hour)
	if err := store.CreateRun(context.Background(), app.Run{ID: runID, JobName: "gcp-complete", Image: "us-docker.pkg.dev/project/repo/train:latest", Provider: "gcp", State: app.RunStateSucceeded, StartedAt: started}); err != nil {
		t.Fatalf("CreateRun returned error: %v", err)
	}
	if err := store.CreateAttempt(context.Background(), app.Attempt{ID: "a_initial", RunID: runID, Provider: "gcp", State: app.AttemptStateSucceeded, StartedAt: started}); err != nil {
		t.Fatalf("CreateAttempt returned error: %v", err)
	}
	if err := store.FinishAttempt(context.Background(), "a_initial", app.AttemptStateSucceeded, 0, "completed", "projects/test-project/locations/us-central1/customJobs/initial", started.Add(time.Minute)); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
	if err := store.FinishRun(context.Background(), runID, app.RunStateSucceeded, 0, "completed", started.Add(time.Minute)); err != nil {
		t.Fatalf("FinishRun returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}
	checkpointEvent := `{"type":"checkpoint","step":5,"checkpoint_uri":"gs://bucket/checkpoints/epoch-5.pt"}` + "\n"
	if err := os.WriteFile(paths.EventsJSONL, []byte(checkpointEvent), 0o600); err != nil {
		t.Fatalf("write checkpoint event: %v", err)
	}
	configPath := filepath.Join(dir, "gcp.yaml")
	configContent := `
job:
  image: us-docker.pkg.dev/project/repo/train:latest
gcp:
  project_id: test-project
  location: us-central1
  output_uri_prefix: gs://bucket/outputs
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	fake := &fakeGCPAdapter{}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout: &stdout,
		Stderr: &stderr,
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fake
		},
	})
	cmd.SetArgs([]string{"--home", home, "resume", runID, "--provider", "gcp", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("resume returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if fake.lastSubmit.ResumeFrom == nil || fake.lastSubmit.ResumeFrom.URI != "gs://bucket/checkpoints/epoch-5.pt" || fake.lastSubmit.ResumeFrom.Step != 5 {
		t.Fatalf("resume checkpoint = %#v", fake.lastSubmit.ResumeFrom)
	}
	if !strings.Contains(stdout.String(), "Found checkpoint: step 5") {
		t.Fatalf("stdout = %s", stdout.String())
	}
	var summary app.Summary
	content, err := os.ReadFile(paths.Summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if err := json.Unmarshal(content, &summary); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	if summary.State != app.RunStateSucceeded || summary.ResumeCount != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestLambdaCredentialErrorBeforeSubmitDoesNotCreateRun(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := artifact.EnsureHome(home); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	if err := os.WriteFile(credentials.DefaultPath(home), []byte("not-a-valid-store"), 0o600); err != nil {
		t.Fatalf("write credentials marker: %v", err)
	}
	t.Setenv(credentials.PassphraseEnv, "")

	code, err := runTrain(context.Background(), Options{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, config.ResolvedTrainConfig{
		Provider:        "lambda",
		SwitchboardHome: home,
		Job:             app.JobSpec{Name: "lambda-credential-check", Image: "ghcr.io/example/smoke:latest"},
		Lambda: config.LambdaConfig{
			RegionName:       "us-west-1",
			InstanceTypeName: "gpu_1x_a10",
			SSHKeyName:       "switchboard",
			SSHPrivateKey:    "~/.ssh/id_ed25519",
		},
	})
	if err == nil {
		t.Fatal("expected credential resolver error")
	}
	if code != exitCodeInvalidSpec {
		t.Fatalf("exit code = %d, want %d", code, exitCodeInvalidSpec)
	}
	entries, readErr := os.ReadDir(filepath.Join(home, "runs"))
	if readErr != nil {
		t.Fatalf("read runs directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no run directories after pre-submit credential failure, got %#v", entries)
	}
}

func TestHyperbolicEnvCredentialBypassesEncryptedStore(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := artifact.EnsureHome(home); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	if err := os.WriteFile(credentials.DefaultPath(home), []byte("not-a-valid-store"), 0o600); err != nil {
		t.Fatalf("write credentials marker: %v", err)
	}
	t.Setenv(credentials.PassphraseEnv, "")
	t.Setenv("HYPERBOLIC_API_KEY", "env-api-key")

	resolver, err := credentialResolverForTrain(Options{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, config.ResolvedTrainConfig{
		Provider:        "hyperbolic",
		SwitchboardHome: home,
		Job:             app.JobSpec{Name: "hyperbolic-credential-check", Image: "ghcr.io/example/smoke:latest"},
		Hyperbolic: config.HyperbolicConfig{
			SSHPrivateKey: "~/.ssh/id_ed25519",
		},
	})
	if err != nil {
		t.Fatalf("credentialResolverForTrain returned error: %v", err)
	}
	secret, err := resolver.Resolve(credentials.Query{Provider: "hyperbolic", Name: "api_key", Env: []string{"HYPERBOLIC_API_KEY"}})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if secret.Value != "env-api-key" {
		t.Fatalf("secret = %#v", secret)
	}
}

func TestLambdaResumeCredentialErrorDoesNotRestartExistingRun(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	runID := "r_resume_credentials"
	paths := artifact.ForRun(home, runID)
	if err := artifact.EnsureHome(home); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	store, err := state.Open(paths.DB)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	started := time.Now().UTC()
	if err := store.CreateRun(context.Background(), app.Run{ID: runID, JobName: "complete", Image: "image", Provider: "lambda", State: app.RunStateSucceeded, StartedAt: started}); err != nil {
		t.Fatalf("CreateRun returned error: %v", err)
	}
	if err := store.FinishRun(context.Background(), runID, app.RunStateSucceeded, 0, "completed", started.Add(time.Minute)); err != nil {
		t.Fatalf("FinishRun returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}
	file, err := os.OpenFile(paths.EventsJSONL, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	step := int64(1)
	if err := event.WriteJSONL(file, app.Event{Type: app.EventTypeCheckpoint, Step: &step, CheckpointURI: "s3://bucket/ckpt.pt"}); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close events: %v", err)
	}
	if err := os.WriteFile(credentials.DefaultPath(home), []byte("not-a-valid-store"), 0o600); err != nil {
		t.Fatalf("write credentials marker: %v", err)
	}
	t.Setenv(credentials.PassphraseEnv, "")

	code, err := runResume(context.Background(), Options{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, runID, config.ResolvedTrainConfig{
		Provider:        "lambda",
		SwitchboardHome: home,
		Job:             app.JobSpec{Name: "lambda-resume", Image: "ghcr.io/example/smoke:latest"},
		Lambda: config.LambdaConfig{
			RegionName:       "us-west-1",
			InstanceTypeName: "gpu_1x_a10",
			SSHKeyName:       "switchboard",
			SSHPrivateKey:    "~/.ssh/id_ed25519",
		},
	})
	if err == nil {
		t.Fatal("expected credential resolver error")
	}
	if code != exitCodeInvalidSpec {
		t.Fatalf("exit code = %d, want %d", code, exitCodeInvalidSpec)
	}
	store, err = state.Open(paths.DB)
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	defer store.Close()
	run, err := store.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun returned error: %v", err)
	}
	if run.State != app.RunStateSucceeded || run.ExitCode != 0 || run.Error != "completed" {
		t.Fatalf("run should remain completed after pre-restart credential failure: %#v", run)
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
	trainStdout := &lockedBuffer{}
	var trainStderr bytes.Buffer
	trainCmd := NewRootCommand(Options{Stdout: trainStdout, Stderr: &trainStderr})
	trainCmd.SetArgs([]string{"--home", home, "train", "--provider", "local", "--script", filepath.Join(repo, "examples", "slow.py")})

	var wg sync.WaitGroup
	wg.Add(1)
	var trainErr error
	go func() {
		defer wg.Done()
		trainErr = trainCmd.Execute()
	}()

	runID := waitForRunID(t, home)
	followStdout := &lockedBuffer{}
	followCtx, cancelFollow := context.WithCancel(context.Background())
	defer cancelFollow()
	followCmd := NewRootCommand(Options{Stdout: followStdout, Stderr: &bytes.Buffer{}})
	followCmd.SetContext(followCtx)
	followCmd.SetArgs([]string{"--home", home, "logs", runID, "--follow"})
	var followWG sync.WaitGroup
	followWG.Add(1)
	var followErr error
	go func() {
		defer followWG.Done()
		followErr = followCmd.Execute()
	}()

	waitForText(t, followStdout, "slow start")
	var cancelStdout bytes.Buffer
	cancelCmd := NewRootCommand(Options{Stdout: &cancelStdout, Stderr: &bytes.Buffer{}})
	cancelCmd.SetArgs([]string{"--home", home, "cancel", runID})
	if err := cancelCmd.Execute(); err != nil {
		t.Fatalf("cancel returned error: %v", err)
	}
	wg.Wait()
	cancelFollow()
	followWG.Wait()
	if followErr != nil && !errors.Is(followErr, context.Canceled) {
		t.Fatalf("logs follow returned error: %v", followErr)
	}
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

func TestFollowLogsDrainsWritesAfterTerminalState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	runID := "r_follow_terminal"
	paths := artifact.ForRun(home, runID)
	if err := artifact.EnsureHome(home); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	store, err := state.Open(paths.DB)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.CreateRun(context.Background(), app.Run{
		ID:        runID,
		JobName:   "follow terminal",
		Script:    "script.py",
		Provider:  string(app.ProviderLocal),
		State:     app.RunStateRunning,
		StartedAt: now,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	output := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- followLogs(context.Background(), output, home, runID, paths.Logs)
	}()

	appendLogLine(t, paths.Logs, "before terminal")
	waitForText(t, output, "before terminal")
	if err := store.FinishRun(context.Background(), runID, app.RunStateCanceled, exitCodeCanceled, "canceled", now); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	time.Sleep(logFollowPollInterval / 2)
	appendLogLine(t, paths.Logs, "after terminal")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follow logs returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follow logs did not return after terminal state")
	}
	if !strings.Contains(output.String(), "after terminal") {
		t.Fatalf("follow output = %s", output.String())
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
	for _, name := range []string{"local", "mock-lambda", "mock-gcp", "gcp", "lambda", "hyperbolic", "alibaba-cloud", "huawei-cloud", "tencent-cloud", "tianyi-cloud", "baidu-ai-cloud"} {
		if !strings.Contains(stdout.String(), name) {
			t.Fatalf("%q missing from %s", name, stdout.String())
		}
	}
}

func TestProvidersInspectChinaCloudReadinessProvider(t *testing.T) {
	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"providers", "inspect", "alibaba-cloud", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("providers inspect returned error: %v", err)
	}
	for _, want := range []string{`"name":"alibaba-cloud"`, `"oss"`, `"cn-hangzhou"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("%q missing from %s", want, stdout.String())
		}
	}
}

func TestTrainRegistryConfiguresChinaVMProvider(t *testing.T) {
	registry := buildTrainProviderRegistry(Options{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, config.ResolvedTrainConfig{
		Provider:        "alibaba-cloud",
		SwitchboardHome: filepath.Join(t.TempDir(), "home"),
		Job:             app.JobSpec{Name: "china-smoke", Image: "registry.example.cn/switchboard/smoke:latest"},
		ChinaCloud: config.ChinaCloudConfig{
			AlibabaCloud: config.ChinaCloudProviderConfig{
				Region:          "cn-hangzhou",
				Zone:            "cn-hangzhou-i",
				InstanceType:    "ecs.g6.large",
				ImageID:         "m-test",
				VPCID:           "vpc-test",
				SubnetID:        "vsw-test",
				SecurityGroupID: "sg-test",
				SSHKeyName:      "switchboard",
				SSHPrivateKey:   filepath.Join(t.TempDir(), "china.pem"),
			},
		},
	}, credentials.Resolver{})
	adapter, err := registry.Get("alibaba-cloud")
	if err != nil {
		t.Fatalf("registry.Get returned error: %v", err)
	}
	capabilities, err := adapter.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities returned error: %v", err)
	}
	if !capabilities.SupportsDockerImage {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	report := adapter.ValidateJob(context.Background(), app.JobSpec{Name: "china-smoke", Image: "registry.example.cn/switchboard/smoke:latest"})
	if !report.Supported {
		t.Fatalf("ValidateJob report = %#v", report)
	}
}

func TestChinaTrainCredentialResolverAllowsEnvFallbackWithoutStore(t *testing.T) {
	t.Setenv(credentials.PassphraseEnv, "")
	resolver, err := credentialResolverForTrain(Options{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, config.ResolvedTrainConfig{
		Provider:        "alibaba-cloud",
		SwitchboardHome: filepath.Join(t.TempDir(), "home"),
	})
	if err != nil {
		t.Fatalf("credentialResolverForTrain returned error: %v", err)
	}
	if resolver.Store != nil {
		t.Fatalf("resolver should not require a store for env fallback: %#v", resolver.Store)
	}
}

func TestProvidersCheckChinaCloudReportsMissingCredentials(t *testing.T) {
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "")
	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"providers", "check", "alibaba-cloud", "--json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected missing credentials error")
	}
	if !strings.Contains(stdout.String(), `"ready":false`) || !strings.Contains(stdout.String(), "credentials are not configured") {
		t.Fatalf("stdout = %s err=%v", stdout.String(), err)
	}
}

func TestProvidersCheckChinaCloudStrictAuthRequiresAuthenticatedValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error_code":"APIGW.0301","error_msg":"Incorrect IAM authentication information"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	t.Setenv("HUAWEICLOUD_SDK_AK", "test-ak")
	t.Setenv("HUAWEICLOUD_SDK_SK", "test-sk")
	t.Setenv("SWITCHBOARD_HUAWEI_CLOUD_IAM_ENDPOINT", server.URL+"/v3/regions/cn-north-4")
	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"providers", "check", "huawei-cloud", "--strict-auth", "--json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected strict auth error")
	}
	for _, want := range []string{`"ready":false`, `"authenticated":false`, `"built_in_auth":true`, "signed auth request was rejected"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("%q missing from %s", want, stdout.String())
		}
	}
}

func TestProvidersCheckChinaCloudStrictBuiltInAuthPasses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Action") != "DescribeRegions" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"Regions":{"Region":[]}}`))
	}))
	defer server.Close()

	t.Setenv("SWITCHBOARD_ALIBABA_CLOUD_ENDPOINT", server.URL+"/")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "test-ak")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "test-sk")
	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"providers", "check", "alibaba-cloud", "--strict-auth", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("providers check returned error: %v\nstdout=%s", err, stdout.String())
	}
	for _, want := range []string{`"ready":true`, `"auth_mode":"built_in_signed_request"`, `"authenticated":true`, `"built_in_auth":true`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("%q missing from %s", want, stdout.String())
		}
	}
}

func TestProvidersCheckChinaCloudStrictAuthCommandPasses(t *testing.T) {
	t.Setenv("SWITCHBOARD_ALIBABA_CLOUD_AUTH_COMMAND", "exit 0")
	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"providers", "check", "alibaba-cloud", "--strict-auth", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("providers check returned error: %v\nstdout=%s", err, stdout.String())
	}
	for _, want := range []string{`"ready":true`, `"auth_mode":"auth_command"`, `"authenticated":true`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("%q missing from %s", want, stdout.String())
		}
	}
}

func TestProvidersCheckChinaCloudStrictAuthCommandFailureIsUnauthenticated(t *testing.T) {
	t.Setenv("SWITCHBOARD_ALIBABA_CLOUD_AUTH_COMMAND", "exit 3")
	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"providers", "check", "alibaba-cloud", "--strict-auth", "--json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected auth command failure")
	}
	for _, want := range []string{`"ready":false`, `"auth_mode":"auth_command"`, `"authenticated":false`, "auth command failed"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("%q missing from %s", want, stdout.String())
		}
	}
}

func TestCredentialsCommandsSetListGetAndDelete(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	t.Setenv("SWITCHBOARD_CREDENTIALS_PASSPHRASE", "test-passphrase")
	t.Setenv("TEST_LAMBDA_KEY", "test-secret-value")
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "credentials", "set", "lambda", "api-key", "--from-env", "TEST_LAMBDA_KEY"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("set returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "test-secret-value") {
		t.Fatalf("import output leaked secret: %s", stdout.String())
	}

	stdout.Reset()
	cmd = NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "credentials", "list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), `"key":"lambda/api_key"`) || strings.Contains(stdout.String(), "test-secret-value") {
		t.Fatalf("list output = %s", stdout.String())
	}

	stdout.Reset()
	cmd = NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "credentials", "get", "lambda", "api-key", "--show"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("get returned error: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != "test-secret-value" {
		t.Fatalf("get output = %q", stdout.String())
	}

	stdout.Reset()
	cmd = NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "credentials", "delete", "lambda", "api-key"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("delete returned error: %v", err)
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
	resources, err := store.ProviderResourcesByRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("resources: %v", err)
	}
	if len(resources) != 1 || resources[0].Kind != app.ProviderResourceKindCustomJob || resources[0].State != app.ProviderResourceStateSucceeded {
		t.Fatalf("resources = %#v", resources)
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

func TestPlanSimulatesPackagingAndStagingWithoutSideEffects(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	contextDir := filepath.Join(dir, "context")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatalf("create context: %v", err)
	}
	script := filepath.Join(contextDir, "train.py")
	if err := os.WriteFile(script, []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	dataPath := filepath.Join(dir, "train.csv")
	if err := os.WriteFile(dataPath, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatalf("write data: %v", err)
	}
	configPath := filepath.Join(dir, "switchboard.yaml")
	configContent := fmt.Sprintf(`
job:
  name: plan-gcp
  script: %q
data:
  inputs:
    - name: train
      source: %q
      mount: /workspace/data/train.csv
      mode: bundle
staging:
  data_uri_prefix: gs://bucket/staged
packaging:
  context: %q
  image: us-central1-docker.pkg.dev/test-project/switchboard/train:latest
  platform: linux/amd64
gcp:
  project_id: test-project
  location: us-central1
  output_uri_prefix: gs://bucket/outputs
`, script, dataPath, contextDir)
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	fakeGCP := &fakeGCPAdapter{}
	fakeBuilder := &fakeImageBuilder{image: "should-not-be-used"}
	uploader := &fakeStagingUploader{}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout:          &stdout,
		Stderr:          &stderr,
		StagingUploader: uploader,
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fakeGCP
		},
		ImageBuilderFactory: func(stdout io.Writer, stderr io.Writer) ImageBuilder {
			return fakeBuilder
		},
	})
	cmd.SetArgs([]string{"--home", home, "plan", "--provider", "auto", "--config", configPath, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("plan returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var report planReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("parse plan: %v\n%s", err, stdout.String())
	}
	if fakeGCP.lastSubmit.RunID != "" {
		t.Fatalf("plan should not submit: %#v", fakeGCP.lastSubmit)
	}
	if fakeBuilder.request.Config.Image != "" {
		t.Fatalf("plan should not build or push: %#v", fakeBuilder.request)
	}
	if len(uploader.destinations) != 0 {
		t.Fatalf("plan should not call configured uploader: %#v", uploader.destinations)
	}
	if !report.Staging.WouldUpload || len(report.Staging.PlannedUploads) != 1 {
		t.Fatalf("staging report = %#v", report.Staging)
	}
	if !strings.HasPrefix(report.Staging.PlannedUploads[0], "gs://bucket/staged/"+report.PlanRunID+"/data/train/") {
		t.Fatalf("planned upload = %#v", report.Staging.PlannedUploads)
	}
	if !report.Packaging.WouldPackage || report.Packaging.Image != "us-central1-docker.pkg.dev/test-project/switchboard/train:latest" {
		t.Fatalf("packaging report = %#v", report.Packaging)
	}
	if report.Routing == nil || report.Routing.SelectedProvider != "gcp" {
		t.Fatalf("routing = %#v", report.Routing)
	}
	suppressed := strings.Join(report.SuppressedActions, ",")
	if !strings.Contains(suppressed, "provider submit") || !strings.Contains(suppressed, "managed data upload") {
		t.Fatalf("suppressed actions = %#v", report.SuppressedActions)
	}
}

func TestPlanReportsCheckpointIncompatibilityWithoutSubmit(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	configPath := filepath.Join(dir, "switchboard.yaml")
	configContent := `
job:
  name: gcp-resume-plan
  image: us-docker.pkg.dev/project/repo/train:latest
gcp:
  project_id: test-project
  location: us-central1
  output_uri_prefix: gs://bucket/outputs
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	fakeGCP := &fakeGCPAdapter{}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout: &stdout,
		Stderr: &stderr,
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fakeGCP
		},
	})
	cmd.SetArgs([]string{"--home", home, "plan", "--provider", "gcp", "--config", configPath, "--resume-from", "s3://bucket/checkpoints/epoch-3.pt", "--resume-step", "3", "--json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected incompatible checkpoint error")
	}
	var report planReport
	if parseErr := json.Unmarshal(stdout.Bytes(), &report); parseErr != nil {
		t.Fatalf("parse plan: %v\n%s", parseErr, stdout.String())
	}
	if fakeGCP.lastSubmit.RunID != "" {
		t.Fatalf("plan should not submit: %#v", fakeGCP.lastSubmit)
	}
	if report.Checkpoint.SupportedBySelected {
		t.Fatalf("checkpoint report = %#v", report.Checkpoint)
	}
	if !strings.Contains(report.Checkpoint.Reason, "does not support \"s3\" checkpoint resume") {
		t.Fatalf("checkpoint report = %#v", report.Checkpoint)
	}
}

func TestGCPTrainStagesBundledDataBeforeSubmit(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	dataPath := filepath.Join(dir, "train.csv")
	if err := os.WriteFile(dataPath, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatalf("write data: %v", err)
	}
	configPath := filepath.Join(dir, "switchboard.yaml")
	configContent := fmt.Sprintf(`
job:
  name: gcp-staged-data
  image: us-docker.pkg.dev/project/repo/train:latest
data:
  inputs:
    - name: train
      source: %q
      mount: /workspace/data/train.csv
      mode: bundle
staging:
  data_uri_prefix: gs://bucket/staged
gcp:
  project_id: test-project
  location: us-central1
  output_uri_prefix: gs://bucket/outputs
`, dataPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	fake := &fakeGCPAdapter{}
	uploader := &fakeStagingUploader{}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout:          &stdout,
		Stderr:          &stderr,
		StagingUploader: uploader,
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fake
		},
	})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "gcp", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := extractRunID(t, stdout.String())
	wantURI := "gs://bucket/staged/" + runID + "/data/train/train.csv"
	if len(uploader.destinations) != 1 || uploader.destinations[0] != wantURI {
		t.Fatalf("staged destinations = %#v, want %s", uploader.destinations, wantURI)
	}
	if len(fake.lastSubmit.JobSpec.Data) != 1 || fake.lastSubmit.JobSpec.Data[0].Mode != app.DataInputModeURI || fake.lastSubmit.JobSpec.Data[0].Source != wantURI {
		t.Fatalf("submitted data = %#v", fake.lastSubmit.JobSpec.Data)
	}
	if fake.lastSubmit.RuntimeEnv["SWITCHBOARD_DATA_TRAIN_URI"] != wantURI {
		t.Fatalf("runtime env = %#v", fake.lastSubmit.RuntimeEnv)
	}
	if !strings.Contains(stdout.String(), "Staged data: "+wantURI) {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestGCPTrainRejectsS3StagingBeforeUpload(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	dataPath := filepath.Join(dir, "train.csv")
	if err := os.WriteFile(dataPath, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatalf("write data: %v", err)
	}
	configPath := filepath.Join(dir, "switchboard.yaml")
	configContent := fmt.Sprintf(`
job:
  name: gcp-s3-staging
  image: us-docker.pkg.dev/project/repo/train:latest
data:
  inputs:
    - name: train
      source: %q
      mount: /workspace/data/train.csv
      mode: bundle
staging:
  data_uri_prefix: s3://bucket/staged
gcp:
  project_id: test-project
  location: us-central1
  output_uri_prefix: gs://bucket/outputs
`, dataPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	fake := &fakeGCPAdapter{}
	uploader := &fakeStagingUploader{}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout:          &stdout,
		Stderr:          &stderr,
		StagingUploader: uploader,
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fake
		},
	})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "gcp", "--config", configPath})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected train to reject s3 staging for gcp")
	}
	if !strings.Contains(err.Error(), "provider gcp cannot read staged data prefix scheme \"s3\"") {
		t.Fatalf("error = %v", err)
	}
	if len(uploader.destinations) != 0 {
		t.Fatalf("staging uploader should not be called: %#v", uploader.destinations)
	}
	if fake.lastSubmit.RunID != "" {
		t.Fatalf("submit should not be called: %#v", fake.lastSubmit)
	}
}

func TestLambdaTrainIntegrationUsesConfiguredProvider(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	configPath := filepath.Join(dir, "switchboard.yaml")
	configContent := `
job:
  name: lambda-test
  image: ghcr.io/example/switchboard-lambda-smoke:latest
  command: ["python", "/app/train.py"]
  args: ["--epochs", "1"]
data:
  inputs:
    - name: train-set
      source: s3://example-bucket/train.csv
      mode: uri
staging:
  checkpoint_uri_prefix: s3://example-bucket/switchboard/checkpoints
  data_uri_prefix: s3://example-bucket/switchboard/data
lambda:
  region_name: us-west-1
  instance_type_name: gpu_1x_a10
  ssh_key_name: switchboard
  ssh_private_key: ~/.ssh/id_ed25519
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var captured config.LambdaConfig
	fake := &fakeLambdaAdapter{}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout: &stdout,
		Stderr: &stderr,
		LambdaProviderFactory: func(cfg config.LambdaConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			captured = cfg
			return fake
		},
	})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "lambda", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if captured.RegionName != "us-west-1" || captured.InstanceTypeName != "gpu_1x_a10" || captured.SSHKeyName != "switchboard" {
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
	if attempts[0].Provider != "lambda" || attempts[0].ProviderRef != "lambda:i-fake" {
		t.Fatalf("attempt = %#v", attempts[0])
	}
	resources, err := store.ProviderResourcesByRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("resources: %v", err)
	}
	if len(resources) != 1 || resources[0].Kind != app.ProviderResourceKindInstance || resources[0].State != app.ProviderResourceStateTerminating {
		t.Fatalf("resources = %#v", resources)
	}
	if fake.lastSubmit.RuntimeEnv["SWITCHBOARD_CHECKPOINT_DIR"] != "/tmp/switchboard/checkpoints" {
		t.Fatalf("lambda checkpoint env = %#v", fake.lastSubmit.RuntimeEnv)
	}
	if fake.lastSubmit.RuntimeEnv["SWITCHBOARD_CHECKPOINT_URI_PREFIX"] != "s3://example-bucket/switchboard/checkpoints/"+runID+"/checkpoints" {
		t.Fatalf("lambda checkpoint uri prefix = %#v", fake.lastSubmit.RuntimeEnv)
	}
	if fake.lastSubmit.RuntimeEnv["SWITCHBOARD_DATA_TRAIN_SET_URI"] != "s3://example-bucket/train.csv" {
		t.Fatalf("lambda data env = %#v", fake.lastSubmit.RuntimeEnv)
	}
	if strings.Contains(fake.lastSubmit.RuntimeEnv["SWITCHBOARD_EVENTS_PATH"], home) {
		t.Fatalf("lambda events path should not point at local artifact path: %#v", fake.lastSubmit.RuntimeEnv)
	}
}

func TestLambdaTrainStagesBundledDataToS3BeforeSubmit(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	dataPath := filepath.Join(dir, "train.csv")
	if err := os.WriteFile(dataPath, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatalf("write data: %v", err)
	}
	configPath := filepath.Join(dir, "switchboard.yaml")
	configContent := fmt.Sprintf(`
job:
  name: lambda-staged-data
  image: ghcr.io/example/switchboard-lambda-smoke:latest
data:
  inputs:
    - name: train
      source: %q
      mount: /workspace/data/train.csv
      mode: bundle
staging:
  data_uri_prefix: s3://example-bucket/staged
lambda:
  region_name: us-west-1
  instance_type_name: gpu_1x_a10
  ssh_key_name: switchboard
  ssh_private_key: ~/.ssh/id_ed25519
`, dataPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	fake := &fakeLambdaAdapter{}
	uploader := &fakeStagingUploader{}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout:          &stdout,
		Stderr:          &stderr,
		StagingUploader: uploader,
		LambdaProviderFactory: func(cfg config.LambdaConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fake
		},
	})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "lambda", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := extractRunID(t, stdout.String())
	wantURI := "s3://example-bucket/staged/" + runID + "/data/train/train.csv"
	if len(uploader.destinations) != 1 || uploader.destinations[0] != wantURI {
		t.Fatalf("staged destinations = %#v, want %s", uploader.destinations, wantURI)
	}
	if len(fake.lastSubmit.JobSpec.Data) != 1 || fake.lastSubmit.JobSpec.Data[0].Mode != app.DataInputModeURI || fake.lastSubmit.JobSpec.Data[0].Source != wantURI {
		t.Fatalf("submitted data = %#v", fake.lastSubmit.JobSpec.Data)
	}
	if fake.lastSubmit.RuntimeEnv["SWITCHBOARD_DATA_TRAIN_URI"] != wantURI {
		t.Fatalf("runtime env = %#v", fake.lastSubmit.RuntimeEnv)
	}
}

func TestHyperbolicTrainIntegrationUsesConfiguredProvider(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	configPath := filepath.Join(dir, "switchboard.yaml")
	configContent := `
job:
  name: hyperbolic-test
  image: ghcr.io/example/switchboard-hyperbolic-smoke:latest
  command: ["python", "/app/train.py"]
  args: ["--epochs", "1"]
data:
  inputs:
    - name: train-set
      source: s3://example-bucket/train.csv
      mode: uri
staging:
  checkpoint_uri_prefix: s3://example-bucket/switchboard/checkpoints
  data_uri_prefix: s3://example-bucket/switchboard/data
hyperbolic:
  vm_config_id: vm-config-test
  gpu_count: 2
  gpu_type: H100-SXM5-80GB
  ssh_private_key: ~/.ssh/id_ed25519
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var captured config.HyperbolicConfig
	fake := &fakeHyperbolicAdapter{}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout: &stdout,
		Stderr: &stderr,
		HyperbolicProviderFactory: func(cfg config.HyperbolicConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			captured = cfg
			return fake
		},
	})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "hyperbolic", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if captured.VMConfigID != "vm-config-test" || captured.GPUCount != 2 || captured.GPUType != "H100-SXM5-80GB" {
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
	if attempts[0].Provider != "hyperbolic" || attempts[0].ProviderRef != "hyperbolic:123" {
		t.Fatalf("attempt = %#v", attempts[0])
	}
	resources, err := store.ProviderResourcesByRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("resources: %v", err)
	}
	if len(resources) != 1 || resources[0].Kind != app.ProviderResourceKindInstance || resources[0].State != app.ProviderResourceStateTerminating {
		t.Fatalf("resources = %#v", resources)
	}
	if fake.lastSubmit.RuntimeEnv["SWITCHBOARD_CHECKPOINT_DIR"] != "/tmp/switchboard/checkpoints" {
		t.Fatalf("hyperbolic checkpoint env = %#v", fake.lastSubmit.RuntimeEnv)
	}
	if fake.lastSubmit.RuntimeEnv["SWITCHBOARD_CHECKPOINT_URI_PREFIX"] != "s3://example-bucket/switchboard/checkpoints/"+runID+"/checkpoints" {
		t.Fatalf("hyperbolic checkpoint uri prefix = %#v", fake.lastSubmit.RuntimeEnv)
	}
	if fake.lastSubmit.RuntimeEnv["SWITCHBOARD_DATA_TRAIN_SET_URI"] != "s3://example-bucket/train.csv" {
		t.Fatalf("hyperbolic data env = %#v", fake.lastSubmit.RuntimeEnv)
	}
	if strings.Contains(fake.lastSubmit.RuntimeEnv["SWITCHBOARD_EVENTS_PATH"], home) {
		t.Fatalf("hyperbolic events path should not point at local artifact path: %#v", fake.lastSubmit.RuntimeEnv)
	}
}

func TestHyperbolicTrainStagesBundledDataToS3BeforeSubmit(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	dataPath := filepath.Join(dir, "train.csv")
	if err := os.WriteFile(dataPath, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatalf("write data: %v", err)
	}
	configPath := filepath.Join(dir, "switchboard.yaml")
	configContent := fmt.Sprintf(`
job:
  name: hyperbolic-staged-data
  image: ghcr.io/example/switchboard-hyperbolic-smoke:latest
data:
  inputs:
    - name: train
      source: %q
      mount: /workspace/data/train.csv
      mode: bundle
staging:
  data_uri_prefix: s3://example-bucket/staged
hyperbolic:
  ssh_private_key: ~/.ssh/id_ed25519
`, dataPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	fake := &fakeHyperbolicAdapter{}
	uploader := &fakeStagingUploader{}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout:          &stdout,
		Stderr:          &stderr,
		StagingUploader: uploader,
		HyperbolicProviderFactory: func(cfg config.HyperbolicConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fake
		},
	})
	cmd.SetArgs([]string{"--home", home, "train", "--provider", "hyperbolic", "--config", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("train returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := extractRunID(t, stdout.String())
	wantURI := "s3://example-bucket/staged/" + runID + "/data/train/train.csv"
	if len(uploader.destinations) != 1 || uploader.destinations[0] != wantURI {
		t.Fatalf("staged destinations = %#v, want %s", uploader.destinations, wantURI)
	}
	if len(fake.lastSubmit.JobSpec.Data) != 1 || fake.lastSubmit.JobSpec.Data[0].Mode != app.DataInputModeURI || fake.lastSubmit.JobSpec.Data[0].Source != wantURI {
		t.Fatalf("submitted data = %#v", fake.lastSubmit.JobSpec.Data)
	}
	if fake.lastSubmit.RuntimeEnv["SWITCHBOARD_DATA_TRAIN_URI"] != wantURI {
		t.Fatalf("runtime env = %#v", fake.lastSubmit.RuntimeEnv)
	}
}

func TestResourcesListAndCleanup(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := artifact.EnsureHome(home); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	store, err := state.Open(artifact.DBPath(home))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	started := time.Now().UTC()
	if err := store.CreateRun(context.Background(), app.Run{ID: "r_resources", JobName: "train", Image: "image", Provider: "lambda", State: app.RunStateFailed, StartedAt: started}); err != nil {
		t.Fatalf("CreateRun returned error: %v", err)
	}
	if err := store.CreateAttempt(context.Background(), app.Attempt{ID: "a_resources", RunID: "r_resources", Provider: "lambda", State: app.AttemptStateFailed, StartedAt: started}); err != nil {
		t.Fatalf("CreateAttempt returned error: %v", err)
	}
	if _, err := store.SaveProviderResource(context.Background(), app.ProviderResource{
		ID:                   "res_resources",
		RunID:                "r_resources",
		AttemptID:            "a_resources",
		Provider:             "lambda",
		Kind:                 app.ProviderResourceKindInstance,
		ExternalID:           "i-clean",
		ProviderRef:          "lambda:i-clean",
		Region:               "us-west-1",
		State:                app.ProviderResourceStateRunning,
		CreatedBySwitchboard: true,
		CleanupPolicy:        app.ProviderResourceCleanupAlways,
	}); err != nil {
		t.Fatalf("SaveProviderResource returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}

	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "resources", "list", "--run", "r_resources"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("resources list returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "lambda:i-clean") {
		t.Fatalf("resources list output = %s", stdout.String())
	}

	fake := &fakeLambdaAdapter{}
	stdout.Reset()
	cmd = NewRootCommand(Options{
		Stdout: &stdout,
		Stderr: &stderr,
		LambdaProviderFactory: func(cfg config.LambdaConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fake
		},
	})
	cmd.SetArgs([]string{"--home", home, "resources", "cleanup", "--run", "r_resources"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("resources cleanup returned error: %v", err)
	}
	if fake.cancelRef != "lambda:i-clean" {
		t.Fatalf("cancel ref = %q", fake.cancelRef)
	}
	store, err = state.Open(artifact.DBPath(home))
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	defer store.Close()
	resources, err := store.ProviderResourcesByRun(context.Background(), "r_resources")
	if err != nil {
		t.Fatalf("resources: %v", err)
	}
	if len(resources) != 1 || resources[0].State != app.ProviderResourceStateTerminating {
		t.Fatalf("resources = %#v", resources)
	}
}

func TestResourcesRefreshUpdatesProviderResourceState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := artifact.EnsureHome(home); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	store, err := state.Open(artifact.DBPath(home))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	started := time.Now().UTC()
	if err := store.CreateRun(context.Background(), app.Run{ID: "r_refresh", JobName: "train", Image: "image", Provider: "gcp", State: app.RunStateRunning, StartedAt: started}); err != nil {
		t.Fatalf("CreateRun returned error: %v", err)
	}
	if err := store.CreateAttempt(context.Background(), app.Attempt{ID: "a_refresh", RunID: "r_refresh", Provider: "gcp", State: app.AttemptStateRunning, StartedAt: started}); err != nil {
		t.Fatalf("CreateAttempt returned error: %v", err)
	}
	if _, err := store.SaveProviderResource(context.Background(), app.ProviderResource{
		ID:                   "res_refresh",
		RunID:                "r_refresh",
		AttemptID:            "a_refresh",
		Provider:             "gcp",
		Kind:                 app.ProviderResourceKindCustomJob,
		ExternalID:           "projects/test-project/locations/us-central1/customJobs/fake",
		ProviderRef:          "projects/test-project/locations/us-central1/customJobs/fake",
		Region:               "us-central1",
		ProjectOrAccount:     "test-project",
		State:                app.ProviderResourceStateRunning,
		CreatedBySwitchboard: true,
		CleanupPolicy:        app.ProviderResourceCleanupNever,
	}); err != nil {
		t.Fatalf("SaveProviderResource returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}

	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout: &stdout,
		Stderr: &stderr,
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return &fakeGCPAdapter{}
		},
	})
	cmd.SetArgs([]string{"--home", home, "resources", "refresh", "--run", "r_refresh"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("resources refresh returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "refreshed\tres_refresh\tgcp\tsucceeded") {
		t.Fatalf("refresh output = %s", stdout.String())
	}
	store, err = state.Open(artifact.DBPath(home))
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	defer store.Close()
	resources, err := store.ProviderResourcesByRun(context.Background(), "r_refresh")
	if err != nil {
		t.Fatalf("resources: %v", err)
	}
	if len(resources) != 1 || resources[0].State != app.ProviderResourceStateSucceeded || resources[0].LastObservedAt == nil || resources[0].Metadata["refreshed_at"] == "" {
		t.Fatalf("resources = %#v", resources)
	}
}

func TestResourcesRefreshUsesProviderResourceStateWhenAvailable(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := artifact.EnsureHome(home); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	store, err := state.Open(artifact.DBPath(home))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	started := time.Now().UTC()
	if err := store.CreateRun(context.Background(), app.Run{ID: "r_lambda_refresh", JobName: "train", Image: "image", Provider: "lambda", State: app.RunStateRunning, StartedAt: started}); err != nil {
		t.Fatalf("CreateRun returned error: %v", err)
	}
	if err := store.CreateAttempt(context.Background(), app.Attempt{ID: "a_lambda_refresh", RunID: "r_lambda_refresh", Provider: "lambda", State: app.AttemptStateRunning, StartedAt: started}); err != nil {
		t.Fatalf("CreateAttempt returned error: %v", err)
	}
	if _, err := store.SaveProviderResource(context.Background(), app.ProviderResource{
		ID:                   "res_lambda_refresh",
		RunID:                "r_lambda_refresh",
		AttemptID:            "a_lambda_refresh",
		Provider:             "lambda",
		Kind:                 app.ProviderResourceKindInstance,
		ExternalID:           "i-terminated",
		ProviderRef:          "lambda:i-terminated",
		Region:               "us-west-1",
		State:                app.ProviderResourceStateRunning,
		CreatedBySwitchboard: true,
		CleanupPolicy:        app.ProviderResourceCleanupAlways,
	}); err != nil {
		t.Fatalf("SaveProviderResource returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}

	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{
		Stdout: &stdout,
		Stderr: &stderr,
		LambdaProviderFactory: func(cfg config.LambdaConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return &fakeLambdaAdapter{status: app.ProviderJobStatus{
				State:         app.AttemptStateCanceled,
				ResourceState: app.ProviderResourceStateTerminated,
			}}
		},
	})
	cmd.SetArgs([]string{"--home", home, "resources", "refresh", "--run", "r_lambda_refresh"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("resources refresh returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "refreshed\tres_lambda_refresh\tlambda\tterminated") {
		t.Fatalf("refresh output = %s", stdout.String())
	}
	store, err = state.Open(artifact.DBPath(home))
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	defer store.Close()
	resources, err := store.ProviderResourcesByRun(context.Background(), "r_lambda_refresh")
	if err != nil {
		t.Fatalf("resources: %v", err)
	}
	if len(resources) != 1 || resources[0].State != app.ProviderResourceStateTerminated {
		t.Fatalf("resources = %#v", resources)
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

func TestRunTrainPackagesLocalScriptForLambdaWithExplicitImage(t *testing.T) {
	dir := t.TempDir()
	contextDir := filepath.Join(dir, "context")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatalf("create context: %v", err)
	}
	script := filepath.Join(contextDir, "train.py")
	if err := os.WriteFile(script, []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	fakeLambda := &fakeLambdaAdapter{}
	fakeBuilder := &fakeImageBuilder{image: "ghcr.io/example/switchboard-lambda:latest"}
	var stdout bytes.Buffer
	code, err := runTrain(context.Background(), Options{
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		LambdaProviderFactory: func(cfg config.LambdaConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fakeLambda
		},
		ImageBuilderFactory: func(stdout io.Writer, stderr io.Writer) ImageBuilder {
			return fakeBuilder
		},
	}, config.ResolvedTrainConfig{
		Provider:        "lambda",
		SwitchboardHome: filepath.Join(dir, "home"),
		Job:             app.JobSpec{Name: "train.py", Script: script},
		Packaging: config.PackagingConfig{
			Context: contextDir,
			Image:   "ghcr.io/example/switchboard-lambda:latest",
		},
		Lambda: config.LambdaConfig{
			RegionName:       "us-west-1",
			InstanceTypeName: "gpu_1x_a10",
			SSHKeyName:       "switchboard",
			SSHPrivateKey:    "~/.ssh/id_ed25519",
		},
	})
	if err != nil || code != 0 {
		t.Fatalf("runTrain code=%d err=%v stdout=%s", code, err, stdout.String())
	}
	if fakeBuilder.request.Config.Image != "ghcr.io/example/switchboard-lambda:latest" {
		t.Fatalf("build request = %#v", fakeBuilder.request)
	}
	if fakeLambda.lastSubmit.JobSpec.Image != fakeBuilder.image || fakeLambda.lastSubmit.JobSpec.Script != "" {
		t.Fatalf("submitted job = %#v", fakeLambda.lastSubmit.JobSpec)
	}
	if !reflect.DeepEqual(fakeLambda.lastSubmit.JobSpec.Command, []string{"python3", "train.py"}) {
		t.Fatalf("command = %#v", fakeLambda.lastSubmit.JobSpec.Command)
	}
}

func TestRunTrainPackagesLocalScriptForHyperbolicWithExplicitImage(t *testing.T) {
	dir := t.TempDir()
	contextDir := filepath.Join(dir, "context")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatalf("create context: %v", err)
	}
	script := filepath.Join(contextDir, "train.py")
	if err := os.WriteFile(script, []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	fakeHyperbolic := &fakeHyperbolicAdapter{}
	fakeBuilder := &fakeImageBuilder{image: "ghcr.io/example/switchboard-hyperbolic:latest"}
	var stdout bytes.Buffer
	code, err := runTrain(context.Background(), Options{
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		HyperbolicProviderFactory: func(cfg config.HyperbolicConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fakeHyperbolic
		},
		ImageBuilderFactory: func(stdout io.Writer, stderr io.Writer) ImageBuilder {
			return fakeBuilder
		},
	}, config.ResolvedTrainConfig{
		Provider:        "hyperbolic",
		SwitchboardHome: filepath.Join(dir, "home"),
		Job:             app.JobSpec{Name: "train.py", Script: script},
		Packaging: config.PackagingConfig{
			Context: contextDir,
			Image:   "ghcr.io/example/switchboard-hyperbolic:latest",
		},
		Hyperbolic: config.HyperbolicConfig{
			SSHPrivateKey: "~/.ssh/id_ed25519",
		},
	})
	if err != nil || code != 0 {
		t.Fatalf("runTrain code=%d err=%v stdout=%s", code, err, stdout.String())
	}
	if fakeBuilder.request.Config.Image != "ghcr.io/example/switchboard-hyperbolic:latest" {
		t.Fatalf("build request = %#v", fakeBuilder.request)
	}
	if fakeHyperbolic.lastSubmit.JobSpec.Image != fakeBuilder.image || fakeHyperbolic.lastSubmit.JobSpec.Script != "" {
		t.Fatalf("submitted job = %#v", fakeHyperbolic.lastSubmit.JobSpec)
	}
	if !reflect.DeepEqual(fakeHyperbolic.lastSubmit.JobSpec.Command, []string{"python3", "train.py"}) {
		t.Fatalf("command = %#v", fakeHyperbolic.lastSubmit.JobSpec.Command)
	}
}

func TestAutoProviderWithGCPConfigRoutesToGCPHardware(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	fakeGCP := &fakeGCPAdapter{
		capabilities: app.ProviderCapabilities{
			SupportsDockerImage:        true,
			SupportedURISchemes:        []string{"gs"},
			SupportedCheckpointSchemes: []string{"gs"},
			HardwareShapes: []app.HardwareShape{{
				ID:                "gcp-test-t4",
				Provider:          "gcp",
				Region:            "us-central1",
				MachineType:       "n1-standard-8",
				AcceleratorType:   "NVIDIA_TESLA_T4",
				AcceleratorCount:  1,
				GPUFamily:         "nvidia-tesla-t4",
				VRAMGBPerGPU:      16,
				TotalVRAMGB:       16,
				OnDemandHourlyUSD: 2.5,
				SupportsOnDemand:  true,
			}},
		},
	}
	var stdout bytes.Buffer
	code, err := runTrain(context.Background(), Options{
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		GCPProviderFactory: func(cfg config.GCPConfig, stdout io.Writer, stderr io.Writer) app.ProviderAdapter {
			return fakeGCP
		},
	}, config.ResolvedTrainConfig{
		Provider:        string(app.ProviderAuto),
		SwitchboardHome: home,
		Job: app.JobSpec{
			Name:  "gcp-auto",
			Image: "us-docker.pkg.dev/project/repo/train:latest",
			Data: []app.DataInput{{
				Name:   "train",
				Source: "gs://bucket/train",
				Mode:   app.DataInputModeURI,
			}},
		},
		Routing: config.RoutingConfig{
			Mode:      "full_auto",
			Objective: "fastest_within_budget",
			Budget:    config.BudgetConfig{MaxRunCostUSD: 10},
		},
		Sizing: config.SizingConfig{Hints: config.SizingHintsConfig{
			ModelParametersB: 1,
			BatchSize:        1,
			Precision:        "bf16",
			Optimizer:        "sgd",
		}},
		Hardware: config.HardwareConfig{Constraints: config.HardwareConstraintsConfig{
			AllowedGPUFamilies: []string{"nvidia-tesla-t4"},
			Regions:            []string{"us-central1"},
		}},
		GCP: config.GCPConfig{
			ProjectID:       "test-project",
			Location:        "us-central1",
			OutputURIPrefix: "gs://bucket/outputs",
		},
	})
	if err != nil || code != 0 {
		t.Fatalf("runTrain code=%d err=%v stdout=%s", code, err, stdout.String())
	}
	if fakeGCP.lastSubmit.SelectedHardware == nil || fakeGCP.lastSubmit.SelectedHardware.ShapeID != "gcp-test-t4" {
		t.Fatalf("selected hardware = %#v", fakeGCP.lastSubmit.SelectedHardware)
	}
	if !strings.Contains(stdout.String(), "Selected gcp") {
		t.Fatalf("stdout = %s", stdout.String())
	}
	runID := extractRunID(t, stdout.String())
	store, err := state.Open(artifact.ForRun(home, runID).DB)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer store.Close()
	decision, err := store.GetRoutingDecision(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRoutingDecision returned error: %v", err)
	}
	if decision.SelectedProvider != "gcp" || decision.SelectedHardware == nil || decision.SelectedHardware.ShapeID != "gcp-test-t4" {
		t.Fatalf("decision = %#v", decision)
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

func waitForText(t *testing.T, buf interface{ String() string }, text string) {
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

func appendLogLine(t *testing.T, path string, line string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	defer file.Close()
	if _, err := fmt.Fprintln(file, line); err != nil {
		t.Fatalf("write log line: %v", err)
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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
	capabilities   app.ProviderCapabilities
}

type fakeImageBuilder struct {
	image   string
	request packaging.BuildRequest
	err     error
}

type fakeStagingUploader struct {
	destinations []string
}

func (u *fakeStagingUploader) UploadFile(ctx context.Context, sourcePath string, destinationURI string) error {
	u.destinations = append(u.destinations, destinationURI)
	return nil
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
	if len(a.capabilities.HardwareShapes) > 0 || a.capabilities.SupportsDockerImage || a.capabilities.SupportsLocalScript {
		return a.capabilities, nil
	}
	return app.ProviderCapabilities{
		SupportsDockerImage:        true,
		SupportedURISchemes:        []string{"gs"},
		SupportedCheckpointSchemes: []string{"gs"},
		SupportsObjectStorePull:    true,
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
	if req.OnResourceCreated != nil {
		if err := req.OnResourceCreated(app.ProviderResource{
			RunID:                req.RunID,
			AttemptID:            req.AttemptID,
			Provider:             "gcp",
			Kind:                 app.ProviderResourceKindCustomJob,
			ExternalID:           ref,
			ProviderRef:          ref,
			Region:               "us-central1",
			ProjectOrAccount:     "test-project",
			State:                app.ProviderResourceStateRunning,
			CreatedBySwitchboard: true,
			CleanupPolicy:        app.ProviderResourceCleanupNever,
		}); err != nil {
			return app.SubmitResult{}, err
		}
	}
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
		if req.OnResourceUpdated != nil {
			_ = req.OnResourceUpdated(app.ProviderResource{
				RunID:                req.RunID,
				AttemptID:            req.AttemptID,
				Provider:             "gcp",
				Kind:                 app.ProviderResourceKindCustomJob,
				ExternalID:           ref,
				ProviderRef:          ref,
				Region:               "us-central1",
				ProjectOrAccount:     "test-project",
				State:                app.ProviderResourceStateFailed,
				CreatedBySwitchboard: true,
				CleanupPolicy:        app.ProviderResourceCleanupNever,
			})
		}
		code := a.submitExitCode
		if code == 0 {
			code = 1
		}
		return app.SubmitResult{ProviderJobRef: ref, ExitCode: code, ExitReason: a.submitErr.Error()}, a.submitErr
	}
	if req.OnResourceUpdated != nil {
		if err := req.OnResourceUpdated(app.ProviderResource{
			RunID:                req.RunID,
			AttemptID:            req.AttemptID,
			Provider:             "gcp",
			Kind:                 app.ProviderResourceKindCustomJob,
			ExternalID:           ref,
			ProviderRef:          ref,
			Region:               "us-central1",
			ProjectOrAccount:     "test-project",
			State:                app.ProviderResourceStateSucceeded,
			CreatedBySwitchboard: true,
			CleanupPolicy:        app.ProviderResourceCleanupNever,
		}); err != nil {
			return app.SubmitResult{}, err
		}
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

type fakeLambdaAdapter struct {
	lastSubmit app.SubmitRequest
	cancelRef  string
	status     app.ProviderJobStatus
}

func (a *fakeLambdaAdapter) Name() app.ProviderName {
	return "lambda"
}

func (a *fakeLambdaAdapter) ValidateAuth(ctx context.Context) error {
	return nil
}

func (a *fakeLambdaAdapter) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	return app.ProviderCapabilities{
		SupportsDockerImage:        true,
		SupportedURISchemes:        []string{"http", "https", "s3", "gs"},
		SupportedCheckpointSchemes: []string{"s3", "gs"},
		HardwareShapes: []app.HardwareShape{{
			ID:                "lambda-us-west-1-gpu-1x-a10",
			Provider:          "lambda",
			Region:            "us-west-1",
			MachineType:       "gpu_1x_a10",
			AcceleratorType:   "A10 (24 GB)",
			AcceleratorCount:  1,
			GPUFamily:         "A10",
			VRAMGBPerGPU:      24,
			TotalVRAMGB:       24,
			OnDemandHourlyUSD: 0.75,
			SupportsOnDemand:  true,
		}},
	}, nil
}

func (a *fakeLambdaAdapter) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	if spec.Image == "" {
		return app.SupportReport{Supported: false, Reasons: []string{"lambda provider requires job.image"}}
	}
	return app.SupportReport{Supported: true}
}

func (a *fakeLambdaAdapter) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	return app.CostEstimate{HourlyUSD: 0.75, Currency: "USD"}, nil
}

func (a *fakeLambdaAdapter) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
	a.lastSubmit = req
	ref := "lambda:i-fake"
	if req.OnResourceCreated != nil {
		if err := req.OnResourceCreated(app.ProviderResource{
			RunID:                req.RunID,
			AttemptID:            req.AttemptID,
			Provider:             "lambda",
			Kind:                 app.ProviderResourceKindInstance,
			ExternalID:           "i-fake",
			ProviderRef:          ref,
			Region:               "us-west-1",
			State:                app.ProviderResourceStateRunning,
			CreatedBySwitchboard: true,
			CleanupPolicy:        app.ProviderResourceCleanupAlways,
		}); err != nil {
			return app.SubmitResult{}, err
		}
	}
	if req.OnStarted != nil {
		if err := req.OnStarted(app.ProviderJobRef{ID: ref}); err != nil {
			return app.SubmitResult{}, err
		}
	}
	if req.OnResourceUpdated != nil {
		if err := req.OnResourceUpdated(app.ProviderResource{
			RunID:                req.RunID,
			AttemptID:            req.AttemptID,
			Provider:             "lambda",
			Kind:                 app.ProviderResourceKindInstance,
			ExternalID:           "i-fake",
			ProviderRef:          ref,
			Region:               "us-west-1",
			State:                app.ProviderResourceStateTerminating,
			CreatedBySwitchboard: true,
			CleanupPolicy:        app.ProviderResourceCleanupAlways,
		}); err != nil {
			return app.SubmitResult{}, err
		}
	}
	return app.SubmitResult{ProviderJobRef: ref, ExitCode: 0, ExitReason: "completed"}, nil
}

func (a *fakeLambdaAdapter) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	if a.status.State != "" || a.status.ResourceState != "" {
		return a.status, nil
	}
	return app.ProviderJobStatus{State: app.AttemptStateSucceeded}, nil
}

func (a *fakeLambdaAdapter) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, errUnsupportedFakeLogs
}

func (a *fakeLambdaAdapter) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	a.cancelRef = ref.ID
	return nil
}

type fakeHyperbolicAdapter struct {
	lastSubmit app.SubmitRequest
	cancelRef  string
	status     app.ProviderJobStatus
}

func (a *fakeHyperbolicAdapter) Name() app.ProviderName {
	return "hyperbolic"
}

func (a *fakeHyperbolicAdapter) ValidateAuth(ctx context.Context) error {
	return nil
}

func (a *fakeHyperbolicAdapter) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	return app.ProviderCapabilities{
		SupportsDockerImage:        true,
		SupportedURISchemes:        []string{"http", "https", "s3", "gs"},
		SupportedCheckpointSchemes: []string{"s3", "gs"},
		HardwareShapes: []app.HardwareShape{{
			ID:                "hyperbolic-ondemand-vm-1g",
			Provider:          "hyperbolic",
			Region:            "global",
			MachineType:       "ondemand-vm",
			AcceleratorType:   "H100-SXM5-80GB",
			AcceleratorCount:  1,
			GPUFamily:         "H100",
			VRAMGBPerGPU:      80,
			TotalVRAMGB:       80,
			OnDemandHourlyUSD: 2.50,
			SupportsOnDemand:  true,
		}},
	}, nil
}

func (a *fakeHyperbolicAdapter) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	if spec.Image == "" {
		return app.SupportReport{Supported: false, Reasons: []string{"hyperbolic provider requires job.image"}}
	}
	return app.SupportReport{Supported: true}
}

func (a *fakeHyperbolicAdapter) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	return app.CostEstimate{HourlyUSD: 2.50, Currency: "USD"}, nil
}

func (a *fakeHyperbolicAdapter) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
	a.lastSubmit = req
	ref := "hyperbolic:123"
	if req.OnResourceCreated != nil {
		if err := req.OnResourceCreated(app.ProviderResource{
			RunID:                req.RunID,
			AttemptID:            req.AttemptID,
			Provider:             "hyperbolic",
			Kind:                 app.ProviderResourceKindInstance,
			ExternalID:           "123",
			ProviderRef:          ref,
			State:                app.ProviderResourceStateRunning,
			CreatedBySwitchboard: true,
			CleanupPolicy:        app.ProviderResourceCleanupAlways,
		}); err != nil {
			return app.SubmitResult{}, err
		}
	}
	if req.OnStarted != nil {
		if err := req.OnStarted(app.ProviderJobRef{ID: ref}); err != nil {
			return app.SubmitResult{}, err
		}
	}
	if req.OnResourceUpdated != nil {
		if err := req.OnResourceUpdated(app.ProviderResource{
			RunID:                req.RunID,
			AttemptID:            req.AttemptID,
			Provider:             "hyperbolic",
			Kind:                 app.ProviderResourceKindInstance,
			ExternalID:           "123",
			ProviderRef:          ref,
			State:                app.ProviderResourceStateTerminating,
			CreatedBySwitchboard: true,
			CleanupPolicy:        app.ProviderResourceCleanupAlways,
		}); err != nil {
			return app.SubmitResult{}, err
		}
	}
	return app.SubmitResult{ProviderJobRef: ref, ExitCode: 0, ExitReason: "completed"}, nil
}

func (a *fakeHyperbolicAdapter) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	if a.status.State != "" || a.status.ResourceState != "" {
		return a.status, nil
	}
	return app.ProviderJobStatus{State: app.AttemptStateSucceeded}, nil
}

func (a *fakeHyperbolicAdapter) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, errUnsupportedFakeLogs
}

func (a *fakeHyperbolicAdapter) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	a.cancelRef = ref.ID
	return nil
}
