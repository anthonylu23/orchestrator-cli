package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
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
