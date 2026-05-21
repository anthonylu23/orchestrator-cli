package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	_ "modernc.org/sqlite"
)

func TestRunAttemptLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "switchboard.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()

	started := time.Now().UTC()
	run := app.Run{ID: "r_1", JobName: "train", Script: "train.py", Image: "image:latest", Provider: "local", State: app.RunStateRunning, StartedAt: started}
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
	if gotRun.State != app.RunStateSucceeded || gotRun.ExitCode != 0 || gotRun.Image != "image:latest" {
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
	store, err := Open(filepath.Join(t.TempDir(), "switchboard.db"))
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
		SelectedHardware: &app.HardwareSelection{
			Provider:        "mock-gcp",
			ShapeID:         "mock-a100-1",
			Region:          "us-central1",
			MachineType:     "a2-highgpu-1g",
			AcceleratorType: "NVIDIA_TESLA_A100",
			TotalVRAMGB:     40,
		},
		EligibleHardware: []app.HardwareCandidate{{
			Provider: "mock-gcp",
			ShapeID:  "mock-a100-1",
			Score:    3600,
		}},
		RejectedHardware: []app.HardwareCandidate{{
			Provider: "mock-gcp",
			ShapeID:  "mock-t4-1",
			Reasons:  []string{"total VRAM 16GB is below required 40GB"},
		}},
		EstimatedRequiredVRAMGB: floatPtr(40),
		EstimatedRuntimeSeconds: floatPtr(3600),
		EstimatedTotalCostUSD:   floatPtr(1.3),
		Confidence:              "hinted",
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
	if got.SelectedHardware == nil || got.SelectedHardware.ShapeID != "mock-a100-1" || got.EstimatedTotalCostUSD == nil || *got.EstimatedTotalCostUSD != 1.3 {
		t.Fatalf("hardware decision = %#v", got)
	}
}

func TestProviderResourceLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "switchboard.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()

	started := time.Now().UTC()
	run := app.Run{ID: "r_1", JobName: "train", Script: "train.py", Provider: "lambda", State: app.RunStateRunning, StartedAt: started}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun returned error: %v", err)
	}
	attempt := app.Attempt{ID: "a_1", RunID: "r_1", Provider: "lambda", State: app.AttemptStateRunning, StartedAt: started}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatalf("CreateAttempt returned error: %v", err)
	}

	observed := started.Add(time.Second)
	resource, err := store.SaveProviderResource(ctx, app.ProviderResource{
		RunID:                "r_1",
		AttemptID:            "a_1",
		Provider:             "lambda",
		Kind:                 app.ProviderResourceKindInstance,
		ExternalID:           "i-123",
		ProviderRef:          "lambda:i-123",
		Region:               "us-west-1",
		State:                app.ProviderResourceStateBooting,
		CreatedBySwitchboard: true,
		CleanupPolicy:        app.ProviderResourceCleanupAlways,
		Metadata:             map[string]string{"instance_type": "gpu_1x_a10"},
		LastObservedAt:       &observed,
	})
	if err != nil {
		t.Fatalf("SaveProviderResource returned error: %v", err)
	}
	if resource.ID == "" {
		t.Fatal("expected generated provider resource ID")
	}

	resource.State = app.ProviderResourceStateRunning
	resource.Metadata["native_status"] = "active"
	if _, err := store.SaveProviderResource(ctx, resource); err != nil {
		t.Fatalf("SaveProviderResource update returned error: %v", err)
	}

	resources, err := store.ProviderResourcesByRun(ctx, "r_1")
	if err != nil {
		t.Fatalf("ProviderResourcesByRun returned error: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("resources = %#v", resources)
	}
	got := resources[0]
	if got.ID != resource.ID || got.State != app.ProviderResourceStateRunning || got.Metadata["instance_type"] != "gpu_1x_a10" || got.Metadata["native_status"] != "active" {
		t.Fatalf("resource = %#v", got)
	}
	if got.LastObservedAt == nil || !got.LastObservedAt.Equal(observed) {
		t.Fatalf("last observed = %#v", got.LastObservedAt)
	}
}

func floatPtr(value float64) *float64 {
	return &value
}

func TestOpenMigratesOldAttemptSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "switchboard.db")
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
	started := time.Now().UTC()
	_, err = db.Exec(`
INSERT INTO runs (id, job_name, script, provider, state, started_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		"r_old", "old", "old.py", "local", app.RunStateRunning, started.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("insert old run: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated db returned error: %v", err)
	}
	defer store.Close()

	oldRun, err := store.GetRun(context.Background(), "r_old")
	if err != nil {
		t.Fatalf("GetRun old row returned error: %v", err)
	}
	if oldRun.Image != "" {
		t.Fatalf("old run image = %q, want empty", oldRun.Image)
	}

	run := app.Run{ID: "r_1", JobName: "train", Script: "train.py", Image: "image:latest", Provider: "local", State: app.RunStateRunning, StartedAt: started}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("CreateRun returned error: %v", err)
	}
	gotRun, err := store.GetRun(context.Background(), "r_1")
	if err != nil {
		t.Fatalf("GetRun returned error: %v", err)
	}
	if gotRun.Image != "image:latest" {
		t.Fatalf("run image = %q", gotRun.Image)
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
	resources, err := store.ProviderResources(context.Background(), ProviderResourceFilter{})
	if err != nil {
		t.Fatalf("ProviderResources after migration returned error: %v", err)
	}
	if len(resources) != 0 {
		t.Fatalf("resources = %#v", resources)
	}
}
