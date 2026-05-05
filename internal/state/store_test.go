package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
	_ "modernc.org/sqlite"
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
	resumeStep := int64(7)
	hourlyUSD := 1.25
	attempt := app.Attempt{
		ID:                 "a_1",
		RunID:              "r_1",
		Provider:           "local",
		State:              app.AttemptStateRunning,
		StartedAt:          started,
		ResumeFromURI:      "file:///ckpt-7",
		ResumeFromStep:     &resumeStep,
		EstimatedHourlyUSD: &hourlyUSD,
		EstimateCurrency:   "USD",
	}
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
	if attempts[0].ResumeFromURI != "file:///ckpt-7" || attempts[0].ResumeFromStep == nil || *attempts[0].ResumeFromStep != 7 {
		t.Fatalf("resume provenance = %#v", attempts[0])
	}
	if attempts[0].EstimatedHourlyUSD == nil || *attempts[0].EstimatedHourlyUSD != 1.25 || attempts[0].EstimateCurrency != "USD" {
		t.Fatalf("estimate provenance = %#v", attempts[0])
	}
}

func TestRoutingDecisionLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()

	started := time.Now().UTC()
	run := app.Run{ID: "r_1", JobName: "train", Script: "train.py", Provider: "auto", State: app.RunStateRunning, StartedAt: started}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun returned error: %v", err)
	}
	decision := app.RoutingDecision{
		RunID:            "r_1",
		SelectedProvider: "mock-gcp",
		Objective:        "min_cost",
		SelectionReason:  "selected mock-gcp by min_cost",
		EligibleProviders: []app.RoutingCandidate{{
			Provider: "mock-gcp",
			Score:    1.3,
		}},
		RejectedProviders: []app.RoutingCandidate{{
			Provider: "local",
			Reasons:  []string{"local provider does not fetch URI data inputs"},
		}},
	}
	if err := store.SaveRoutingDecision(ctx, decision); err != nil {
		t.Fatalf("SaveRoutingDecision returned error: %v", err)
	}
	got, err := store.GetRoutingDecision(ctx, "r_1")
	if err != nil {
		t.Fatalf("GetRoutingDecision returned error: %v", err)
	}
	if got.SelectedProvider != "mock-gcp" || len(got.RejectedProviders) != 1 {
		t.Fatalf("decision = %#v", got)
	}
}

func TestOpenMigratesOldAttemptSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orchestrator.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	_, err = db.Exec(`
CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  job_name TEXT NOT NULL,
  script TEXT NOT NULL,
  provider TEXT NOT NULL,
  state TEXT NOT NULL,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  exit_code INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE attempts (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  state TEXT NOT NULL,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  exit_code INTEGER NOT NULL DEFAULT 0,
  exit_reason TEXT NOT NULL DEFAULT '',
  provider_ref TEXT NOT NULL DEFAULT '',
  FOREIGN KEY(run_id) REFERENCES runs(id)
);
`)
	if err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated db returned error: %v", err)
	}
	defer store.Close()

	started := time.Now().UTC()
	run := app.Run{ID: "r_1", JobName: "train", Script: "train.py", Provider: "local", State: app.RunStateRunning, StartedAt: started}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("CreateRun returned error: %v", err)
	}
	hourlyUSD := 2.5
	attempt := app.Attempt{
		ID:                 "a_1",
		RunID:              "r_1",
		Provider:           "local",
		State:              app.AttemptStateRunning,
		StartedAt:          started,
		EstimatedHourlyUSD: &hourlyUSD,
		EstimateCurrency:   "USD",
	}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatalf("CreateAttempt returned error: %v", err)
	}
	attempts, err := store.AttemptsByRun(context.Background(), "r_1")
	if err != nil {
		t.Fatalf("AttemptsByRun returned error: %v", err)
	}
	if len(attempts) != 1 || attempts[0].EstimatedHourlyUSD == nil || *attempts[0].EstimatedHourlyUSD != 2.5 {
		t.Fatalf("attempts = %#v", attempts)
	}
}
