package local

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
	"github.com/anthonylu23/orchestrator-cli/internal/artifact"
)

func TestValidateJobRejectsURIInputs(t *testing.T) {
	script := filepath.Join(t.TempDir(), "train.py")
	if err := os.WriteFile(script, []byte("print('ok')"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	provider := New(&bytes.Buffer{}, &bytes.Buffer{})
	report := provider.ValidateJob(context.Background(), app.JobSpec{
		Script: script,
		Data: []app.DataInput{{
			Name:   "remote",
			Source: "https://example.com/data.csv",
			Mount:  "/workspace/data/remote",
			Mode:   app.DataInputModeURI,
		}},
	})
	if report.Supported {
		t.Fatal("expected URI input to be rejected")
	}
}

func TestSubmitSuccessWritesArtifactsAndProviderRef(t *testing.T) {
	dir := t.TempDir()
	paths := artifact.ForRun(dir, "r_1")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("EnsureRun returned error: %v", err)
	}
	script := filepath.Join(dir, "train.py")
	if err := os.WriteFile(script, []byte("print('{\"type\":\"status\",\"state\":\"ok\"}')\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	var started string
	provider := New(&bytes.Buffer{}, &bytes.Buffer{})
	result, err := provider.Submit(context.Background(), app.SubmitRequest{
		JobSpec:   app.JobSpec{Script: script, WorkDir: paths.Workspace},
		RunID:     "r_1",
		AttemptID: "a_1",
		RunDir:    paths.RunDir,
		OnStarted: func(ref app.ProviderJobRef) error {
			started = ref.ID
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("result = %#v", result)
	}
	if !strings.HasPrefix(started, "local:") || result.ProviderJobRef != started {
		t.Fatalf("provider refs started=%q result=%q", started, result.ProviderJobRef)
	}
	events, err := os.ReadFile(paths.EventsJSONL)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(events), `"type":"status"`) {
		t.Fatalf("events = %s", string(events))
	}
}

func TestSubmitFailure(t *testing.T) {
	dir := t.TempDir()
	paths := artifact.ForRun(dir, "r_1")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("EnsureRun returned error: %v", err)
	}
	script := filepath.Join(dir, "fail.py")
	if err := os.WriteFile(script, []byte("import sys\nsys.exit(3)\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	provider := New(&bytes.Buffer{}, &bytes.Buffer{})
	result, err := provider.Submit(context.Background(), app.SubmitRequest{
		JobSpec:   app.JobSpec{Script: script, WorkDir: paths.Workspace},
		RunID:     "r_1",
		AttemptID: "a_1",
		RunDir:    paths.RunDir,
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if result.ExitCode != 3 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
}
