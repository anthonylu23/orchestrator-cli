package checkpoint

import (
	"context"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/event"
	"github.com/anthonylu23/switchboard-cli/internal/redact"
)

type Resolver struct {
	Home string
}

func (r Resolver) Latest(ctx context.Context, runID string) (*app.CheckpointRef, error) {
	events, err := event.ReadJSONL(artifact.ForRun(r.Home, runID).EventsJSONL)
	if err != nil {
		return nil, err
	}
	var latest *app.CheckpointRef
	for _, ev := range events {
		if ev.Type != app.EventTypeCheckpoint || ev.CheckpointURI == "" {
			continue
		}
		step := int64(0)
		if ev.Step != nil {
			step = *ev.Step
		}
		if latest == nil || step >= latest.Step {
			latest = &app.CheckpointRef{URI: redact.SanitizeURI(ev.CheckpointURI), Step: step}
		}
	}
	return latest, nil
}
