package checkpoint

import (
	"context"
	"os"
	"testing"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
	"github.com/anthonylu23/orchestrator-cli/internal/artifact"
	"github.com/anthonylu23/orchestrator-cli/internal/event"
)

func TestLatestReturnsHighestStepCheckpoint(t *testing.T) {
	home := t.TempDir()
	paths := artifact.ForRun(home, "r_1")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("EnsureRun returned error: %v", err)
	}
	file, err := os.OpenFile(paths.EventsJSONL, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	step1 := int64(1)
	step8 := int64(8)
	if err := event.WriteJSONL(file, app.Event{Type: app.EventTypeCheckpoint, Step: &step1, CheckpointURI: "file:///ckpt-1"}); err != nil {
		t.Fatalf("write event: %v", err)
	}
	if err := event.WriteJSONL(file, app.Event{Type: app.EventTypeCheckpoint, Step: &step8, CheckpointURI: "file:///ckpt-8"}); err != nil {
		t.Fatalf("write event: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close events: %v", err)
	}

	got, err := (Resolver{Home: home}).Latest(context.Background(), "r_1")
	if err != nil {
		t.Fatalf("Latest returned error: %v", err)
	}
	if got == nil || got.URI != "file:///ckpt-8" || got.Step != 8 {
		t.Fatalf("checkpoint = %#v", got)
	}
}
