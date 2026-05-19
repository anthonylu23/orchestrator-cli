package summary

import (
	"testing"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
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

func TestBuildSummaryUsesMetricDirectionDefaults(t *testing.T) {
	step1 := int64(1)
	step2 := int64(2)
	run := app.Run{ID: "r_1", State: app.RunStateSucceeded}
	events := []app.Event{
		{Type: app.EventTypeMetric, Step: &step1, Metrics: map[string]float64{"loss": 0.9, "accuracy": 0.6}},
		{Type: app.EventTypeMetric, Step: &step2, Metrics: map[string]float64{"loss": 0.3, "accuracy": 0.8}},
	}
	got := Build(run, nil, events)
	if got.BestMetrics["loss"] != 0.3 {
		t.Fatalf("best loss = %f", got.BestMetrics["loss"])
	}
	if got.BestMetrics["accuracy"] != 0.8 {
		t.Fatalf("best accuracy = %f", got.BestMetrics["accuracy"])
	}
}

func TestBuildSummaryIncludesRoutingDecision(t *testing.T) {
	run := app.Run{ID: "r_1", State: app.RunStateSucceeded}
	decision := app.RoutingDecision{
		RunID:            "r_1",
		SelectedProvider: "gcp",
		SelectedHardware: &app.HardwareSelection{Provider: "gcp", ShapeID: "gcp-a100"},
		Confidence:       "hinted",
	}
	got := Build(run, nil, nil, decision)
	if got.Routing == nil || got.Routing.SelectedProvider != "gcp" || got.Routing.SelectedHardware.ShapeID != "gcp-a100" {
		t.Fatalf("summary routing = %#v", got.Routing)
	}
}
