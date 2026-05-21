package artifact

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

type Paths struct {
	Home        string
	DB          string
	RunDir      string
	Workspace   string
	EventsJSONL string
	Logs        string
	Summary     string
	Checkpoints string
}

var validRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func ForRun(home string, runID string) Paths {
	if err := ValidateRunID(runID); err != nil {
		panic(err)
	}
	runDir := filepath.Join(home, "runs", runID)
	paths := Paths{
		Home:        home,
		DB:          DBPath(home),
		RunDir:      runDir,
		Workspace:   filepath.Join(runDir, "workspace"),
		EventsJSONL: filepath.Join(runDir, "events.jsonl"),
		Logs:        filepath.Join(runDir, "logs.txt"),
		Summary:     filepath.Join(runDir, "summary.json"),
		Checkpoints: filepath.Join(runDir, "checkpoints"),
	}
	if err := validatePaths(paths); err != nil {
		panic(err)
	}
	return paths
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

func ValidateRunID(runID string) error {
	if !validRunID.MatchString(runID) {
		return fmt.Errorf("invalid run id %q", runID)
	}
	return nil
}

func EnsureHome(home string) error {
	if home == "" {
		return errors.New("switchboard home is required")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create switchboard home: %w", err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return fmt.Errorf("secure switchboard home: %w", err)
	}
	runs := filepath.Join(home, "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return fmt.Errorf("create runs directory: %w", err)
	}
	return os.Chmod(runs, 0o700)
}

func EnsureRun(paths Paths) error {
	if err := validatePaths(paths); err != nil {
		return err
	}
	for _, dir := range []string{paths.RunDir, paths.Checkpoints, paths.Workspace} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create run directories: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure run directory %s: %w", dir, err)
		}
	}
	for _, path := range []string{paths.EventsJSONL, paths.Logs} {
		if err := ensurePrivateFile(path); err != nil {
			return err
		}
	}
	return nil
}

func StreamLogs(w io.Writer, paths Paths) error {
	if err := validatePaths(paths); err != nil {
		return err
	}
	file, err := os.Open(paths.Logs)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(w, file)
	return err
}

func WriteSummary(path string, summary app.Summary) error {
	content, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return atomicWriteFile(path, content, 0o600)
}

func ensurePrivateFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create artifact %s: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure artifact %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close artifact %s: %w", path, err)
	}
	return nil
}

func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create run directories: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create summary temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace summary: %w", err)
	}
	return os.Chmod(path, mode)
}

func validatePaths(paths Paths) error {
	if err := ValidateRunID(filepath.Base(paths.RunDir)); err != nil {
		return err
	}
	runsDir := filepath.Join(paths.Home, "runs")
	if err := ensureContained(runsDir, paths.RunDir); err != nil {
		return fmt.Errorf("run directory escapes runs root: %w", err)
	}
	for _, path := range []string{paths.Workspace, paths.EventsJSONL, paths.Logs, paths.Summary, paths.Checkpoints} {
		if err := ensureContained(paths.RunDir, path); err != nil {
			return fmt.Errorf("artifact path %q escapes run directory: %w", path, err)
		}
	}
	return nil
}

func ensureContained(base string, child string) error {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	childAbs, err := filepath.Abs(child)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(baseAbs, childAbs)
	if err != nil {
		return err
	}
	if rel == "." || rel == "" {
		return nil
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return fmt.Errorf("%s is outside %s", child, base)
	}
	return nil
}
