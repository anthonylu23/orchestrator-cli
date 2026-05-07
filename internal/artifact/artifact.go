package artifact

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

type Paths struct {
	Home             string
	DB               string
	RunDir           string
	Workspace        string
	EventsJSONL      string
	Logs             string
	Summary          string
	Manifest         string
	WorkloadManifest string
	Checkpoints      string
	Outputs          string
}

func ForRun(home string, runID string) Paths {
	runDir := filepath.Join(home, "runs", runID)
	return Paths{
		Home:             home,
		DB:               DBPath(home),
		RunDir:           runDir,
		Workspace:        filepath.Join(runDir, "workspace"),
		EventsJSONL:      filepath.Join(runDir, "events.jsonl"),
		Logs:             filepath.Join(runDir, "logs.txt"),
		Summary:          filepath.Join(runDir, "summary.json"),
		Manifest:         filepath.Join(runDir, "artifacts.json"),
		WorkloadManifest: filepath.Join(runDir, "workload.json"),
		Checkpoints:      filepath.Join(runDir, "checkpoints"),
		Outputs:          filepath.Join(runDir, "outputs"),
	}
}

func DBPath(home string) string {
	current := filepath.Join(home, "switchboard.db")
	legacy := filepath.Join(home, "orchestrator.db")
	if _, err := os.Stat(current); err == nil {
		return current
	}
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return current
}

func EnsureHome(home string) error {
	return os.MkdirAll(filepath.Join(home, "runs"), 0o755)
}

func EnsureRun(paths Paths) error {
	if err := os.MkdirAll(paths.Checkpoints, 0o755); err != nil {
		return fmt.Errorf("create run directories: %w", err)
	}
	if err := os.MkdirAll(paths.Workspace, 0o755); err != nil {
		return fmt.Errorf("create workspace directory: %w", err)
	}
	if err := os.MkdirAll(paths.Outputs, 0o755); err != nil {
		return fmt.Errorf("create outputs directory: %w", err)
	}
	for _, path := range []string{paths.EventsJSONL, paths.Logs} {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("create artifact %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close artifact %s: %w", path, err)
		}
	}
	return nil
}

type Manifest struct {
	RunID        string           `json:"run_id"`
	WorkloadType app.WorkloadType `json:"workload_type,omitempty"`
	GeneratedAt  time.Time        `json:"generated_at"`
	Artifacts    []ArtifactRecord `json:"artifacts"`
}

type ArtifactRecord struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

func WriteSummary(path string, summary app.Summary) error {
	content, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(path, content, 0o644)
}

func WriteWorkload(path string, workload app.WorkloadSpec) error {
	return writeJSON(path, workload)
}

func WriteRunEvidence(path string, evidence app.RunEvidence) error {
	return writeJSON(path, evidence)
}

func ReadRunEvidence(path string) (app.RunEvidence, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return app.RunEvidence{}, err
	}
	var evidence app.RunEvidence
	if err := json.Unmarshal(content, &evidence); err != nil {
		return app.RunEvidence{}, err
	}
	return evidence, nil
}

func UpdateRunEvidenceProviderRefs(path string, attempts []app.Attempt) error {
	evidence, err := ReadRunEvidence(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	refs := make([]app.ProviderRunRef, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.ProviderRef == "" {
			continue
		}
		refs = append(refs, app.ProviderRunRef{
			AttemptID:     attempt.ID,
			Provider:      attempt.Provider,
			ProviderJobID: attempt.ProviderRef,
			State:         attempt.State,
		})
	}
	evidence.ProviderJobRefs = refs
	return WriteRunEvidence(path, evidence)
}

func WriteManifest(path string, manifest Manifest) error {
	return writeJSON(path, manifest)
}

func ReadManifest(path string) (Manifest, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func BuildManifest(paths Paths, run app.Run, generatedAt time.Time) (Manifest, error) {
	manifest := Manifest{
		RunID:        run.ID,
		WorkloadType: run.WorkloadType,
		GeneratedAt:  generatedAt.UTC(),
	}
	for _, item := range []struct {
		name string
		kind string
		path string
	}{
		{name: "logs.txt", kind: "logs", path: paths.Logs},
		{name: "events.jsonl", kind: "events", path: paths.EventsJSONL},
		{name: "summary.json", kind: "summary", path: paths.Summary},
		{name: "workload.json", kind: "workload", path: paths.WorkloadManifest},
	} {
		record, ok, err := recordFor(paths.RunDir, item.name, item.kind, item.path)
		if err != nil {
			return Manifest{}, err
		}
		if ok {
			manifest.Artifacts = append(manifest.Artifacts, record)
		}
	}
	for _, dir := range []struct {
		root string
		kind string
	}{
		{root: paths.Outputs, kind: "output"},
		{root: paths.Checkpoints, kind: "checkpoint"},
	} {
		records, err := recordsUnder(paths.RunDir, dir.root, dir.kind)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Artifacts = append(manifest.Artifacts, records...)
	}
	return manifest, nil
}

func ExportOutputs(outputsDir string, saveRoot string, runID string) (string, error) {
	if saveRoot == "" {
		return "", nil
	}
	if strings.TrimSpace(runID) == "" {
		return "", fmt.Errorf("run id is required to export outputs")
	}
	if empty, err := dirEmpty(outputsDir); err != nil {
		return "", err
	} else if empty {
		return "", nil
	}
	destination := filepath.Join(saveRoot, runID)
	if empty, err := dirEmpty(destination); err == nil && !empty {
		return "", fmt.Errorf("output export destination %q already exists and is not empty", destination)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := copyDir(outputsDir, destination); err != nil {
		return "", err
	}
	return destination, nil
}

func writeJSON(path string, value interface{}) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(path, content, 0o644)
}

func recordFor(runDir string, name string, kind string, path string) (ArtifactRecord, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ArtifactRecord{}, false, nil
		}
		return ArtifactRecord{}, false, err
	}
	if info.IsDir() {
		return ArtifactRecord{}, false, nil
	}
	rel, err := filepath.Rel(runDir, path)
	if err != nil {
		return ArtifactRecord{}, false, err
	}
	return ArtifactRecord{Name: name, Kind: kind, Path: rel, SizeBytes: info.Size()}, true, nil
}

func recordsUnder(runDir string, root string, kind string) ([]ArtifactRecord, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var records []ArtifactRecord
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(runDir, path)
		if err != nil {
			return err
		}
		records = append(records, ArtifactRecord{
			Name:      filepath.Base(path),
			Kind:      kind,
			Path:      rel,
			SizeBytes: info.Size(),
		})
		return nil
	})
	return records, err
}

func dirEmpty(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	_, err = file.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func copyDir(source string, dest string) error {
	return filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(source string, dest string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
