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
	"github.com/anthonylu23/switchboard-cli/internal/state"
)

func TestModalIntegrationEvalLifecycle(t *testing.T) {
	repo := repoRoot(t)
	requireModalIntegration(t)
	home := filepath.Join(t.TempDir(), "home")

	localID := runCloudTune(t, home, "run", filepath.Join(repo, "examples", "eval.yaml"), "--provider", "local")
	modalID := runCloudTune(t, home, "run", filepath.Join(repo, "examples", "eval.yaml"), "--provider", "modal-sandbox")

	assertRunState(t, home, modalID, app.RunStateSucceeded)
	assertArtifactsContain(t, home, modalID, "outputs/eval_result.json")
	assertLogsContain(t, home, modalID, "evaluation complete")

	evidence := readEvidence(t, home, modalID)
	if len(evidence.ProviderJobRefs) != 1 || !strings.HasPrefix(evidence.ProviderJobRefs[0].ProviderJobID, "modal-sandbox:") {
		t.Fatalf("modal provider job ref not persisted: %#v", evidence.ProviderJobRefs)
	}

	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"--home", home, "compare", localID, modalID, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compare returned error: %v", err)
	}
	var report CompareReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("parse compare: %v\n%s", err, stdout.String())
	}
	if !rowMatched(report.Rows, "config_hash") || !rowMatched(report.Rows, "dataset_sha256") {
		t.Fatalf("compare did not prove config/dataset parity: %#v", report.Rows)
	}
}

func TestModalIntegrationFailurePreservesArtifacts(t *testing.T) {
	repo := repoRoot(t)
	requireModalIntegration(t)
	home := filepath.Join(t.TempDir(), "home")

	runID, stdout, stderr, err := executeCloudTune(home, "run", filepath.Join(repo, "examples", "eval_fail.yaml"), "--provider", "modal-sandbox")
	if err == nil {
		t.Fatalf("expected remote failure\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if runID == "" {
		t.Fatalf("run id missing\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	assertRunState(t, home, runID, app.RunStateFailed)
	assertArtifactsContain(t, home, runID, "outputs/partial_result.json")
	assertLogsContain(t, home, runID, "controlled eval failure")
}

func TestModalIntegrationCancelLongRunningRun(t *testing.T) {
	repo := repoRoot(t)
	requireModalIntegration(t)
	home := filepath.Join(t.TempDir(), "home")

	var runStdout, runStderr bytes.Buffer
	runCmd := NewRootCommand(Options{Stdout: &runStdout, Stderr: &runStderr})
	runCmd.SetArgs([]string{"--home", home, "run", filepath.Join(repo, "examples", "modal_slow.yaml"), "--provider", "modal-sandbox"})
	var wg sync.WaitGroup
	wg.Add(1)
	var runErr error
	go func() {
		defer wg.Done()
		runErr = runCmd.Execute()
	}()

	runID := waitForRunID(t, home)
	providerRef := waitForProviderRef(t, home, runID)
	if !strings.HasPrefix(providerRef, "modal-sandbox:") {
		t.Fatalf("provider ref = %q", providerRef)
	}

	var cancelStdout bytes.Buffer
	cancelCmd := NewRootCommand(Options{Stdout: &cancelStdout, Stderr: &bytes.Buffer{}})
	cancelCmd.SetArgs([]string{"--home", home, "cancel", runID})
	if err := cancelCmd.Execute(); err != nil {
		t.Fatalf("cancel returned error: %v", err)
	}
	wg.Wait()
	if runErr == nil {
		t.Fatal("expected running command to return cancellation exit error")
	}
	assertRunState(t, home, runID, app.RunStateCanceled)
}

func TestModalIntegrationMissingAuthFailsBeforeProviderSubmission(t *testing.T) {
	repo := repoRoot(t)
	requireModalIntegration(t)
	originalHome := os.Getenv("HOME")
	originalTokenID := os.Getenv("MODAL_TOKEN_ID")
	originalTokenSecret := os.Getenv("MODAL_TOKEN_SECRET")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", originalHome)
		restoreEnv("MODAL_TOKEN_ID", originalTokenID)
		restoreEnv("MODAL_TOKEN_SECRET", originalTokenSecret)
	})
	_ = os.Setenv("HOME", t.TempDir())
	_ = os.Unsetenv("MODAL_TOKEN_ID")
	_ = os.Unsetenv("MODAL_TOKEN_SECRET")

	home := filepath.Join(t.TempDir(), "home")
	runID, stdout, stderr, err := executeCloudTune(home, "run", filepath.Join(repo, "examples", "eval.yaml"), "--provider", "modal-sandbox")
	if err == nil {
		t.Fatalf("expected missing auth failure\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if runID == "" {
		runID = waitForRunID(t, home)
	}
	store, err := state.Open(artifact.DBPath(home))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer store.Close()
	attempts, err := store.AttemptsByRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("AttemptsByRun returned error: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %#v", attempts)
	}
	if attempts[0].ProviderRef != "" {
		t.Fatalf("expected no remote provider ref on missing auth: %#v", attempts[0])
	}
}

func requireModalIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("CLOUDTUNE_INTEGRATION") != "modal" {
		t.Skip("set CLOUDTUNE_INTEGRATION=modal to run live Modal integration tests")
	}
	var stdout bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"--home", filepath.Join(t.TempDir(), "home"), "doctor", "--provider", "modal-sandbox", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("modal integration requested but doctor is not ready: %v\n%s", err, stdout.String())
	}
}

func runCloudTune(t *testing.T, home string, args ...string) string {
	t.Helper()
	runID, stdout, stderr, err := executeCloudTune(home, args...)
	if err != nil {
		t.Fatalf("cloudtune %v returned error: %v\nstdout=%s\nstderr=%s", args, err, stdout, stderr)
	}
	if runID == "" {
		t.Fatalf("run id missing from stdout=%s stderr=%s", stdout, stderr)
	}
	return runID
}

func executeCloudTune(home string, args ...string) (string, string, string, error) {
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand(Options{Stdout: &stdout, Stderr: &stderr})
	fullArgs := append([]string{"--home", home}, args...)
	cmd.SetArgs(fullArgs)
	err := cmd.Execute()
	runID := ""
	for _, field := range strings.Fields(stdout.String()) {
		if strings.HasPrefix(field, "r_") {
			runID = field
		}
	}
	return runID, stdout.String(), stderr.String(), err
}

func assertRunState(t *testing.T, home string, runID string, want app.RunState) {
	t.Helper()
	store, err := state.Open(artifact.DBPath(home))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer store.Close()
	run, err := store.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun returned error: %v", err)
	}
	if run.State != want {
		t.Fatalf("run state = %s, want %s, run=%#v", run.State, want, run)
	}
}

func assertArtifactsContain(t *testing.T, home string, runID string, path string) {
	t.Helper()
	manifest, err := artifact.ReadManifest(artifact.ForRun(home, runID).Manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !manifestHasPath(manifest, path) {
		t.Fatalf("manifest missing %q: %#v", path, manifest)
	}
}

func assertLogsContain(t *testing.T, home string, runID string, text string) {
	t.Helper()
	content, err := os.ReadFile(artifact.ForRun(home, runID).Logs)
	if err != nil {
		t.Fatalf("read logs: %v", err)
	}
	if !strings.Contains(string(content), text) {
		t.Fatalf("logs missing %q:\n%s", text, string(content))
	}
}

func readEvidence(t *testing.T, home string, runID string) app.RunEvidence {
	t.Helper()
	evidence, err := artifact.ReadRunEvidence(artifact.ForRun(home, runID).WorkloadManifest)
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	return evidence
}

func waitForProviderRef(t *testing.T, home string, runID string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	var last []app.Attempt
	for time.Now().Before(deadline) {
		store, err := state.Open(artifact.DBPath(home))
		if err == nil {
			attempts, attemptsErr := store.AttemptsByRun(context.Background(), runID)
			_ = store.Close()
			if attemptsErr == nil {
				last = attempts
				for _, attempt := range attempts {
					if attempt.ProviderRef != "" {
						return attempt.ProviderRef
					}
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("provider ref not recorded for run %s, attempts=%#v", runID, last)
	return ""
}

func restoreEnv(key string, value string) {
	if value == "" {
		_ = os.Unsetenv(key)
		return
	}
	_ = os.Setenv(key, value)
}
