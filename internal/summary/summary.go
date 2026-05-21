package summary

import (
	"strings"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/redact"
)

func Build(run app.Run, attempts []app.Attempt, events []app.Event, routingDecisions ...app.RoutingDecision) app.Summary {
	attempts = sanitizeAttempts(attempts)
	out := app.Summary{
		RunID:            run.ID,
		State:            run.State,
		ProviderAttempts: attempts,
		ExitReason:       run.Error,
	}
	if len(routingDecisions) > 0 {
		decision := routingDecisions[0]
		out.Routing = &decision
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
				if isBetterMetricValue(k, v, best[k], best) {
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

func sanitizeAttempts(attempts []app.Attempt) []app.Attempt {
	out := make([]app.Attempt, len(attempts))
	for i, attempt := range attempts {
		out[i] = redact.SanitizeAttemptURIs(attempt)
	}
	return out
}

func isBetterMetricValue(name string, candidate float64, current float64, best map[string]float64) bool {
	if _, ok := best[name]; !ok {
		return true
	}
	if lowerIsBetter(name) {
		return candidate < current
	}
	return candidate > current
}

func lowerIsBetter(name string) bool {
	normalized := strings.ToLower(name)
	for _, marker := range []string{"loss", "error", "err", "perplexity", "ppl", "latency", "duration"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
