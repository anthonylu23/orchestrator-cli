package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
	"github.com/anthonylu23/orchestrator-cli/internal/artifact"
	"github.com/anthonylu23/orchestrator-cli/internal/state"
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
	configPath := filepath.Join(dir, "orchestrator.yaml")
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

func TestProvidersListIncludesMocks(t *testing.T) {
	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"providers", "list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("providers list returned error: %v", err)
	}
	for _, name := range []string{"local", "mock-lambda", "mock-gcp"} {
		if !strings.Contains(stdout.String(), name) {
			t.Fatalf("%q missing from %s", name, stdout.String())
		}
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
