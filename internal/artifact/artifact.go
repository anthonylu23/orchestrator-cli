package artifact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

type Paths struct {
	Home        string
	DB          string
	RunDir      string
	EventsJSONL string
	Logs        string
	Summary     string
	Checkpoints string
}

func ForRun(home string, runID string) Paths {
	runDir := filepath.Join(home, "runs", runID)
	return Paths{
		Home:        home,
		DB:          filepath.Join(home, "orchestrator.db"),
		RunDir:      runDir,
		EventsJSONL: filepath.Join(runDir, "events.jsonl"),
		Logs:        filepath.Join(runDir, "logs.txt"),
		Summary:     filepath.Join(runDir, "summary.json"),
		Checkpoints: filepath.Join(runDir, "checkpoints"),
	}
}

func EnsureHome(home string) error {
	return os.MkdirAll(filepath.Join(home, "runs"), 0o755)
}

func EnsureRun(paths Paths) error {
	if err := os.MkdirAll(paths.Checkpoints, 0o755); err != nil {
		return fmt.Errorf("create run directories: %w", err)
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

func WriteSummary(path string, summary app.Summary) error {
	content, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(path, content, 0o644)
}
