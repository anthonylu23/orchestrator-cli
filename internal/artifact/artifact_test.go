package artifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestBuildManifestIncludesStandardAndOutputArtifacts(t *testing.T) {
	home := t.TempDir()
	paths := ForRun(home, "r_1")
	if err := EnsureRun(paths); err != nil {
		t.Fatalf("EnsureRun returned error: %v", err)
	}
	if err := os.WriteFile(paths.Summary, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	if err := WriteWorkload(paths.WorkloadManifest, app.WorkloadSpec{Type: app.WorkloadTypeEvaluation}); err != nil {
		t.Fatalf("WriteWorkload returned error: %v", err)
	}
	outputPath := filepath.Join(paths.Outputs, "result.json")
	if err := os.WriteFile(outputPath, []byte(`{"ok":true}`+"\n"), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}
	manifest, err := BuildManifest(paths, app.Run{ID: "r_1", WorkloadType: app.WorkloadTypeEvaluation}, time.Now())
	if err != nil {
		t.Fatalf("BuildManifest returned error: %v", err)
	}
	if manifest.WorkloadType != app.WorkloadTypeEvaluation || !recordExists(manifest, "outputs/result.json") {
		t.Fatalf("manifest = %#v", manifest)
	}
	if err := WriteManifest(paths.Manifest, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	content, err := os.ReadFile(paths.Manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var decoded Manifest
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
}

func TestExportOutputsCopiesToRunScopedDirectory(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "outputs")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "result.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	exported, err := ExportOutputs(source, filepath.Join(dir, "exports"), "r_1")
	if err != nil {
		t.Fatalf("ExportOutputs returned error: %v", err)
	}
	if exported != filepath.Join(dir, "exports", "r_1") {
		t.Fatalf("exported = %q", exported)
	}
	if _, err := os.Stat(filepath.Join(exported, "result.json")); err != nil {
		t.Fatalf("export missing result: %v", err)
	}
}

func recordExists(manifest Manifest, path string) bool {
	for _, record := range manifest.Artifacts {
		if record.Path == path {
			return true
		}
	}
	return false
}
