package artifact

import (
	"os"
	"path/filepath"
	"testing"
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
