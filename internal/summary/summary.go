package summary

import (
	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

func Build(run app.Run, attempts []app.Attempt, events []app.Event) app.Summary {
	out := app.Summary{
		RunID:            run.ID,
		State:            run.State,
		ProviderAttempts: attempts,
		ExitReason:       run.Error,
	}
	if !run.StartedAt.IsZero() && !run.EndedAt.IsZero() {
		out.RuntimeSeconds = run.EndedAt.Sub(run.StartedAt).Seconds()
	}
	if len(attempts) > 1 {
		out.ResumeCount = len(attempts) - 1
	}
	if out.ExitReason == "" && len(attempts) > 0 {
		out.ExitReason = attempts[len(attempts)-1].ExitReason
	}

	best := map[string]float64{}
	final := map[string]float64{}
	var bestStep *int64
	for _, ev := range events {
		switch ev.Type {
		case app.EventTypeMetric:
			for k, v := range ev.Metrics {
				final[k] = v
				if _, ok := best[k]; !ok || v > best[k] {
					best[k] = v
					if ev.Step != nil {
						step := *ev.Step
						bestStep = &step
					}
				}
			}
		case app.EventTypeCheckpoint:
			out.CheckpointCount++
		}
	}
	if len(final) > 0 {
		out.FinalMetrics = final
	}
	if len(best) > 0 {
		out.BestMetrics = best
		out.BestStep = bestStep
	}
	return out
}
