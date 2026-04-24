package summary

import (
	"testing"
	"time"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

func TestBuildSummary(t *testing.T) {
	step := int64(7)
	run := app.Run{ID: "r_1", State: app.RunStateSucceeded, StartedAt: time.Unix(0, 0), EndedAt: time.Unix(2, 0)}
	attempts := []app.Attempt{{ID: "a_1", RunID: "r_1", State: app.AttemptStateSucceeded, ExitReason: "completed"}}
	events := []app.Event{
		{Type: app.EventTypeMetric, Step: &step, Metrics: map[string]float64{"accuracy": 0.8}},
		{Type: app.EventTypeCheckpoint, CheckpointURI: "file:///tmp/ckpt"},
	}
	got := Build(run, attempts, events)
	if got.RuntimeSeconds != 2 {
		t.Fatalf("runtime = %f", got.RuntimeSeconds)
	}
	if got.CheckpointCount != 1 {
		t.Fatalf("checkpoint count = %d", got.CheckpointCount)
	}
	if got.FinalMetrics["accuracy"] != 0.8 {
		t.Fatalf("final metrics = %#v", got.FinalMetrics)
	}
	if got.BestStep == nil || *got.BestStep != 7 {
		t.Fatalf("best step = %#v", got.BestStep)
	}
}

func TestBuildSummaryCountsResumes(t *testing.T) {
	run := app.Run{ID: "r_1", State: app.RunStateSucceeded}
	attempts := []app.Attempt{
		{ID: "a_1", RunID: "r_1", State: app.AttemptStateFailed},
		{ID: "a_2", RunID: "r_1", State: app.AttemptStateSucceeded},
	}
	got := Build(run, attempts, nil)
	if got.ResumeCount != 1 {
		t.Fatalf("resume count = %d", got.ResumeCount)
	}
}
