package artifact

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

func TestDBPathDefaultsToSwitchboardDB(t *testing.T) {
	home := t.TempDir()
	got := DBPath(home)
	want := filepath.Join(home, "switchboard.db")
	if got != want {
		t.Fatalf("DBPath = %q, want %q", got, want)
	}
}

func TestDBPathFallsBackToLegacyOrchestratorDB(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, "orchestrator.db")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write legacy db: %v", err)
	}
	got := DBPath(home)
	if got != legacy {
		t.Fatalf("DBPath = %q, want %q", got, legacy)
	}
}

func TestDBPathPrefersSwitchboardDBOverLegacyDB(t *testing.T) {
	home := t.TempDir()
	current := filepath.Join(home, "switchboard.db")
	legacy := filepath.Join(home, "orchestrator.db")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write legacy db: %v", err)
	}
	if err := os.WriteFile(current, []byte("current"), 0o600); err != nil {
		t.Fatalf("write current db: %v", err)
	}
	got := DBPath(home)
	if got != current {
		t.Fatalf("DBPath = %q, want %q", got, current)
	}
}

func TestForRunRejectsUnsafeRunID(t *testing.T) {
	for _, runID := range []string{"../escape", "nested/run", "", ".hidden"} {
		t.Run(runID, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("expected ForRun to panic for run id %q", runID)
				}
			}()
			_ = ForRun(t.TempDir(), runID)
		})
	}
}

func TestEnsureRunCreatesPrivateArtifacts(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	paths := ForRun(home, "r_private")
	if err := EnsureHome(home); err != nil {
		t.Fatalf("EnsureHome returned error: %v", err)
	}
	if err := EnsureRun(paths); err != nil {
		t.Fatalf("EnsureRun returned error: %v", err)
	}
	for _, path := range []string{home, filepath.Join(home, "runs"), paths.RunDir, paths.Checkpoints, paths.Workspace} {
		assertMode(t, path, 0o700)
	}
	for _, path := range []string{paths.EventsJSONL, paths.Logs} {
		assertMode(t, path, 0o600)
	}
}

func TestWriteSummaryIsPrivateAndReplacesAtomically(t *testing.T) {
	paths := ForRun(t.TempDir(), "r_summary")
	if err := EnsureRun(paths); err != nil {
		t.Fatalf("EnsureRun returned error: %v", err)
	}
	if err := os.WriteFile(paths.Summary, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	if err := WriteSummary(paths.Summary, app.Summary{RunID: "r_summary", State: app.RunStateSucceeded}); err != nil {
		t.Fatalf("WriteSummary returned error: %v", err)
	}
	content, err := os.ReadFile(paths.Summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !bytes.Contains(content, []byte(`"run_id": "r_summary"`)) || bytes.Contains(content, []byte("old")) {
		t.Fatalf("summary content = %s", string(content))
	}
	assertMode(t, paths.Summary, 0o600)
}

func TestStreamLogsCopiesLogsWithoutFollow(t *testing.T) {
	paths := ForRun(t.TempDir(), "r_logs")
	if err := EnsureRun(paths); err != nil {
		t.Fatalf("EnsureRun returned error: %v", err)
	}
	if err := os.WriteFile(paths.Logs, []byte("line one\nline two\n"), 0o600); err != nil {
		t.Fatalf("write logs: %v", err)
	}
	var out bytes.Buffer
	if err := StreamLogs(&out, paths); err != nil {
		t.Fatalf("StreamLogs returned error: %v", err)
	}
	if out.String() != "line one\nline two\n" {
		t.Fatalf("logs = %q", out.String())
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}
