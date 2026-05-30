package local

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
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

func TestSubmitCreatesPrivateArtifactsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	paths := artifact.ForRun(dir, "r_private")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("EnsureRun returned error: %v", err)
	}
	if err := os.Remove(paths.Logs); err != nil {
		t.Fatalf("remove logs: %v", err)
	}
	if err := os.Remove(paths.EventsJSONL); err != nil {
		t.Fatalf("remove events: %v", err)
	}
	script := filepath.Join(dir, "train.py")
	if err := os.WriteFile(script, []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}

	provider := New(&bytes.Buffer{}, &bytes.Buffer{})
	result, err := provider.Submit(context.Background(), app.SubmitRequest{
		JobSpec:   app.JobSpec{Script: script, WorkDir: paths.Workspace},
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

func TestSubmitHandlesLongLogLineAndContinuesParsingEvents(t *testing.T) {
	dir := t.TempDir()
	paths := artifact.ForRun(dir, "r_1")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("EnsureRun returned error: %v", err)
	}
	script := filepath.Join(dir, "long.py")
	if err := os.WriteFile(script, []byte(`
print("x" * 70000)
print('{"type":"status","state":"after-long-line"}')
`), 0o600); err != nil {
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
	if result.ExitCode != 0 {
		t.Fatalf("result = %#v", result)
	}
	events, err := os.ReadFile(paths.EventsJSONL)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(events), "after-long-line") {
		t.Fatalf("events = %s", string(events))
	}
}
