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

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/config"
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

func TestCloudTuneRunCommandPersistsWorkloadArtifactsAndHistory(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	exportRoot := filepath.Join(dir, "exports")
	script := filepath.Join(dir, "eval.py")
	if err := os.WriteFile(script, []byte(`
import json
import os
from pathlib import Path

out = Path(os.environ["CLOUDTUNE_OUTPUT_DIR"])
out.mkdir(parents=True, exist_ok=True)
(out / "eval_result.json").write_text(json.dumps({
    "workload_type": os.environ["CLOUDTUNE_WORKLOAD_TYPE"],
    "model": os.environ["CLOUDTUNE_MODEL_NAME"],
    "dataset": os.environ["CLOUDTUNE_DATASET_PATH"],
    "accuracy": 0.91,
}) + "\n")
print(json.dumps({"type":"metric","step":1,"metrics":{"accuracy":0.91},"split":"eval"}))
print(json.dumps({"type":"status","state":"verified"}))
`), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	dataset := filepath.Join(dir, "eval.jsonl")
	if err := os.WriteFile(dataset, []byte(`{"input":"hello","expected":"hello"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write dataset: %v", err)
	}
	configPath := filepath.Join(dir, "cloudtune.yaml")
	config := `
workload:
  name: rag-eval-v1
  type: evaluation
  model:
    provider: local
    name: deterministic-evaluator
  dataset:
    name: customer-support-eval
    path: "` + dataset + `"
  tags: ["mvp", "eval"]
job:
  script: "` + script + `"
routing:
  provider: local
  max_attempts: 1
outputs:
  save_to: "` + exportRoot + `"
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "run", configPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run returned error: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	runID := extractRunID(t, stdout.String())
	if !strings.Contains(stdout.String(), "Artifacts exported to") {
		t.Fatalf("stdout missing export path: %s", stdout.String())
	}
	paths := artifact.ForRun(home, runID)
	if _, err := os.Stat(filepath.Join(exportRoot, runID, "eval_result.json")); err != nil {
		t.Fatalf("exported result missing: %v", err)
	}
	var manifest artifact.Manifest
	content, err := os.ReadFile(paths.Manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if manifest.WorkloadType != app.WorkloadTypeEvaluation || !manifestHasPath(manifest, "outputs/eval_result.json") {
		t.Fatalf("manifest = %#v", manifest)
	}
	var evidence app.RunEvidence
	content, err = os.ReadFile(paths.WorkloadManifest)
	if err != nil {
		t.Fatalf("read workload evidence: %v", err)
	}
	if err := json.Unmarshal(content, &evidence); err != nil {
		t.Fatalf("parse workload evidence: %v", err)
	}
	if evidence.Workload.Type != app.WorkloadTypeEvaluation || evidence.RequestedProvider != "local" {
		t.Fatalf("evidence workload = %#v", evidence)
	}
	if !strings.HasPrefix(evidence.ConfigHash, "sha256:") || evidence.ConfigPath == "" {
		t.Fatalf("config evidence = %#v", evidence)
	}
	if evidence.Dataset == nil || !strings.HasPrefix(evidence.Dataset.SHA256, "sha256:") || evidence.Dataset.NumRecords != 1 {
		t.Fatalf("dataset evidence = %#v", evidence.Dataset)
	}
	if len(evidence.ProviderJobRefs) != 1 || !strings.HasPrefix(evidence.ProviderJobRefs[0].ProviderJobID, "local:") {
		t.Fatalf("provider refs = %#v", evidence.ProviderJobRefs)
	}

	var status bytes.Buffer
	statusCmd := NewRootCommand(Options{Stdout: &status, Stderr: &bytes.Buffer{}})
	statusCmd.SetArgs([]string{"--home", home, "status", runID, "--json"})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("status returned error: %v", err)
	}
	if !strings.Contains(status.String(), `"workload_type":"evaluation"`) {
		t.Fatalf("status = %s", status.String())
	}

	var artifactsOut bytes.Buffer
	artifactsCmd := NewRootCommand(Options{Stdout: &artifactsOut, Stderr: &bytes.Buffer{}})
	artifactsCmd.SetArgs([]string{"--home", home, "artifacts", runID})
	if err := artifactsCmd.Execute(); err != nil {
		t.Fatalf("artifacts returned error: %v", err)
	}
	if !strings.Contains(artifactsOut.String(), "eval_result.json") {
		t.Fatalf("artifacts output = %s", artifactsOut.String())
	}

	var runsOut bytes.Buffer
	runsCmd := NewRootCommand(Options{Stdout: &runsOut, Stderr: &bytes.Buffer{}})
	runsCmd.SetArgs([]string{"--home", home, "runs", "--json"})
	if err := runsCmd.Execute(); err != nil {
		t.Fatalf("runs returned error: %v", err)
	}
	if !strings.Contains(runsOut.String(), `"job_name":"rag-eval-v1"`) || !strings.Contains(runsOut.String(), `"workload_type":"evaluation"`) {
		t.Fatalf("runs output = %s", runsOut.String())
	}
}

func TestCompareCommandReportsHashAndMetricMatches(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	exportRoot := filepath.Join(dir, "exports")
	script := filepath.Join(dir, "eval.py")
	if err := os.WriteFile(script, []byte(`
import json
import os
from pathlib import Path

Path(os.environ["CLOUDTUNE_OUTPUT_DIR"]).mkdir(parents=True, exist_ok=True)
print(json.dumps({"type":"metric","step":1,"metrics":{"accuracy":0.75},"split":"eval"}))
`), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	dataset := filepath.Join(dir, "eval.jsonl")
	if err := os.WriteFile(dataset, []byte("{\"input\":\"a\"}\n{\"input\":\"b\"}\n"), 0o600); err != nil {
		t.Fatalf("write dataset: %v", err)
	}
	configPath := filepath.Join(dir, "cloudtune.yaml")
	config := `
workload:
  name: compare-eval
  type: evaluation
  dataset:
    path: "` + dataset + `"
job:
  script: "` + script + `"
routing:
  provider: local
  max_attempts: 1
outputs:
  save_to: "` + exportRoot + `"
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runIDs := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		var stdout, stderr bytes.Buffer
		cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
		cmd.SetArgs([]string{"--home", home, "run", configPath})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("run %d returned error: %v\nstdout=%s\nstderr=%s", i, err, stdout.String(), stderr.String())
		}
		runIDs = append(runIDs, extractRunID(t, stdout.String()))
	}

	var compareOut bytes.Buffer
	compareCmd := NewRootCommand(Options{Stdout: &compareOut, Stderr: &bytes.Buffer{}})
	compareCmd.SetArgs([]string{"--home", home, "compare", runIDs[0], runIDs[1], "--json"})
	if err := compareCmd.Execute(); err != nil {
		t.Fatalf("compare returned error: %v", err)
	}
	var report CompareReport
	if err := json.Unmarshal(compareOut.Bytes(), &report); err != nil {
		t.Fatalf("parse compare output: %v\n%s", err, compareOut.String())
	}
	if report.Left.ConfigHash == "" || report.Left.ConfigHash != report.Right.ConfigHash {
		t.Fatalf("config hash mismatch = %#v", report)
	}
	if report.Left.DatasetSHA256 == "" || report.Left.DatasetSHA256 != report.Right.DatasetSHA256 {
		t.Fatalf("dataset hash mismatch = %#v", report)
	}
	if report.Left.Accuracy == nil || *report.Left.Accuracy != 0.75 {
		t.Fatalf("accuracy = %#v", report.Left.Accuracy)
	}
	if !rowMatched(report.Rows, "config_hash") || !rowMatched(report.Rows, "dataset_sha256") || !rowMatched(report.Rows, "eval_accuracy") {
		t.Fatalf("rows = %#v", report.Rows)
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
	waitForFileText(t, artifact.ForRun(home, runID).Logs, "slow start")

	var followStdout lockedBuffer
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

	waitForBufferText(t, &followStdout, "slow start")
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
    ("CLOUDTUNE_RUN_ID", "SWITCHBOARD_RUN_ID"),
    ("CLOUDTUNE_ATTEMPT_ID", "SWITCHBOARD_ATTEMPT_ID"),
    ("CLOUDTUNE_CHECKPOINT_DIR", "SWITCHBOARD_CHECKPOINT_DIR"),
    ("CLOUDTUNE_RESUME_FROM", "SWITCHBOARD_RESUME_FROM"),
    ("CLOUDTUNE_EVENTS_PATH", "SWITCHBOARD_EVENTS_PATH"),
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
assert os.environ["CLOUDTUNE_OUTPUT_DIR"]
assert os.environ["CLOUDTUNE_ARTIFACTS_MANIFEST"]
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
	for _, name := range []string{"local", "mock-lambda", "mock-gcp"} {
		if !strings.Contains(stdout.String(), name) {
			t.Fatalf("%q missing from %s", name, stdout.String())
		}
	}
}

func TestProvidersInspectShowsCapabilities(t *testing.T) {
	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"providers", "inspect", "mock-cloud", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("providers inspect returned error: %v", err)
	}
	var got struct {
		Name         string                   `json:"name"`
		Capabilities app.ProviderCapabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("parse inspect output: %v\n%s", err, stdout.String())
	}
	if got.Name != "mock-cloud" || !got.Capabilities.Remote || !got.Capabilities.SupportsArtifacts {
		t.Fatalf("inspect output = %#v", got)
	}
	if !got.Capabilities.SupportsCheckpointResume {
		t.Fatalf("mock provider should declare checkpoint resume capability: %#v", got.Capabilities)
	}
}

func TestDoctorLocalProviderReadyWithConfig(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("create home: %v", err)
	}
	script := filepath.Join(dir, "eval.py")
	if err := os.WriteFile(script, []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	dataset := filepath.Join(dir, "eval.jsonl")
	if err := os.WriteFile(dataset, []byte("{\"input\":\"a\"}\n"), 0o600); err != nil {
		t.Fatalf("write dataset: %v", err)
	}
	configPath := filepath.Join(dir, "eval.yaml")
	config := `
workload:
  name: doctor-eval
  type: evaluation
  dataset:
    path: "` + dataset + `"
job:
  script: "` + script + `"
routing:
  provider: local
outputs:
  save_to: "` + filepath.Join(dir, "exports") + `"
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"--home", home, "doctor", "--provider", "local", "--config", configPath, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor returned error: %v\n%s", err, stdout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("parse doctor output: %v\n%s", err, stdout.String())
	}
	if !report.Ready || report.Provider != "local" {
		t.Fatalf("report = %#v", report)
	}
	if !doctorHasCheck(report, "Provider", "config_support", doctorOK) {
		t.Fatalf("missing provider config support check: %#v", report.Checks)
	}
	if !doctorHasCheck(report, "Config", "dataset", doctorOK) {
		t.Fatalf("missing dataset check: %#v", report.Checks)
	}
}

func TestDoctorModalSandboxReportsNotImplementedBeforeSubmission(t *testing.T) {
	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"--home", t.TempDir(), "doctor", "--provider", "modal-sandbox", "--json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected modal-sandbox doctor to fail before provider exists")
	}
	var report doctorReport
	if parseErr := json.Unmarshal(stdout.Bytes(), &report); parseErr != nil {
		t.Fatalf("parse doctor output: %v\n%s", parseErr, stdout.String())
	}
	if report.Ready {
		t.Fatalf("report should not be ready: %#v", report)
	}
	if !doctorHasCheck(report, "Provider", "registered", doctorFail) {
		t.Fatalf("missing provider registration failure: %#v", report.Checks)
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

func TestCloudTuneFailurePreservesPartialOutputs(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	exportRoot := filepath.Join(dir, "exports")
	script := filepath.Join(dir, "eval_fail.py")
	if err := os.WriteFile(script, []byte(`
import json
import os
import sys
from pathlib import Path

out = Path(os.environ["CLOUDTUNE_OUTPUT_DIR"])
out.mkdir(parents=True, exist_ok=True)
(out / "partial_result.json").write_text(json.dumps({"status":"partial"}) + "\n")
print(json.dumps({"type":"metric","step":1,"metrics":{"accuracy":0.0},"split":"eval"}))
print("controlled failure", file=sys.stderr)
sys.exit(2)
`), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	dataset := filepath.Join(dir, "eval.jsonl")
	if err := os.WriteFile(dataset, []byte("{\"input\":\"a\"}\n"), 0o600); err != nil {
		t.Fatalf("write dataset: %v", err)
	}
	configPath := filepath.Join(dir, "eval_fail.yaml")
	config := `
workload:
  name: controlled-failure
  type: evaluation
  dataset:
    path: "` + dataset + `"
job:
  script: "` + script + `"
routing:
  provider: local
  max_attempts: 1
outputs:
  save_to: "` + exportRoot + `"
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	cmd.SetArgs([]string{"--home", home, "run", configPath})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected failed eval command")
	}
	runID := extractRunID(t, stdout.String())
	paths := artifact.ForRun(home, runID)
	if _, err := os.Stat(filepath.Join(exportRoot, runID, "partial_result.json")); err != nil {
		t.Fatalf("exported partial output missing: %v", err)
	}
	manifest, err := artifact.ReadManifest(paths.Manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !manifestHasPath(manifest, "outputs/partial_result.json") {
		t.Fatalf("manifest missing partial output: %#v", manifest)
	}
	var summary app.Summary
	content, err := os.ReadFile(paths.Summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if err := json.Unmarshal(content, &summary); err != nil {
		t.Fatalf("parse summary: %v", err)
	}
	if summary.State != app.RunStateFailed || !strings.Contains(summary.ExitReason, "process exited with code 2") {
		t.Fatalf("summary = %#v", summary)
	}
	if !strings.Contains(stderr.String(), "controlled failure") {
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

func manifestHasPath(manifest artifact.Manifest, path string) bool {
	for _, item := range manifest.Artifacts {
		if item.Path == path {
			return true
		}
	}
	return false
}

func rowMatched(rows []CompareRow, field string) bool {
	for _, row := range rows {
		if row.Field == field {
			return row.Match
		}
	}
	return false
}

func doctorHasCheck(report doctorReport, section string, name string, status doctorStatus) bool {
	for _, check := range report.Checks {
		if check.Section == section && check.Name == name && check.Status == status {
			return true
		}
	}
	return false
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
	deadline := time.Now().Add(15 * time.Second)
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

func waitForFileText(t *testing.T, path string, text string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", path, err)
		}
		last = string(content)
		if strings.Contains(last, text) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%q not found in %q", text, last)
}

func waitForBufferText(t *testing.T, buffer *lockedBuffer, text string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = buffer.String()
		if strings.Contains(last, text) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%q not found in follow output %q", text, last)
}
