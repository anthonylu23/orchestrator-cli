package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

func TestRunAttemptLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()

	started := time.Now().UTC()
	run := app.Run{ID: "r_1", JobName: "train", Script: "train.py", Provider: "local", State: app.RunStateRunning, StartedAt: started}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun returned error: %v", err)
	}
	attempt := app.Attempt{ID: "a_1", RunID: "r_1", Provider: "local", State: app.AttemptStateRunning, StartedAt: started}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatalf("CreateAttempt returned error: %v", err)
	}
	ended := started.Add(time.Second)
	if err := store.FinishAttempt(ctx, "a_1", app.AttemptStateSucceeded, 0, "completed", "local:r_1", ended); err != nil {
		t.Fatalf("FinishAttempt returned error: %v", err)
	}
	if err := store.FinishRun(ctx, "r_1", app.RunStateSucceeded, 0, "completed", ended); err != nil {
		t.Fatalf("FinishRun returned error: %v", err)
	}

	gotRun, err := store.GetRun(ctx, "r_1")
	if err != nil {
		t.Fatalf("GetRun returned error: %v", err)
	}
	if gotRun.State != app.RunStateSucceeded || gotRun.ExitCode != 0 {
		t.Fatalf("run = %#v", gotRun)
	}
	attempts, err := store.AttemptsByRun(ctx, "r_1")
	if err != nil {
		t.Fatalf("AttemptsByRun returned error: %v", err)
	}
	if len(attempts) != 1 || attempts[0].State != app.AttemptStateSucceeded {
		t.Fatalf("attempts = %#v", attempts)
	}
}
