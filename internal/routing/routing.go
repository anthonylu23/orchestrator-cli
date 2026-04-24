package routing

import (
	"context"
	"fmt"
	"sort"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
	"github.com/anthonylu23/orchestrator-cli/internal/provider"
)

type Options struct {
	Objective string
	Exclude   map[string]bool
}

func Select(ctx context.Context, registry *provider.Registry, spec app.JobSpec, opts Options) (app.RoutingDecision, error) {
	objective := opts.Objective
	if objective == "" {
		objective = "min_cost"
	}
	var eligible []app.RoutingCandidate
	var rejected []app.RoutingCandidate
	for _, name := range registry.List() {
		nameValue := string(name)
		if opts.Exclude != nil && opts.Exclude[nameValue] {
			rejected = append(rejected, app.RoutingCandidate{Provider: nameValue, Reasons: []string{"excluded after retryable failure"}})
			continue
		}
		adapter, err := registry.Get(nameValue)
		if err != nil {
			rejected = append(rejected, app.RoutingCandidate{Provider: nameValue, Reasons: []string{err.Error()}})
			continue
		}
		report := adapter.ValidateJob(ctx, spec)
		if !report.Supported {
			rejected = append(rejected, app.RoutingCandidate{Provider: nameValue, Reasons: report.Reasons})
			continue
		}
		estimate, err := adapter.Estimate(ctx, spec)
		if err != nil {
			rejected = append(rejected, app.RoutingCandidate{Provider: nameValue, Reasons: []string{err.Error()}})
			continue
		}
		eligible = append(eligible, app.RoutingCandidate{Provider: nameValue, Score: estimate.HourlyUSD})
	}
	if len(eligible) == 0 {
		return app.RoutingDecision{Objective: objective, RejectedProviders: rejected}, fmt.Errorf("no eligible providers")
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if objective == "min_cost" {
			return eligible[i].Score < eligible[j].Score
		}
		return eligible[i].Provider < eligible[j].Provider
	})
	selected := eligible[0]
	return app.RoutingDecision{
		SelectedProvider:  selected.Provider,
		Objective:         objective,
		SelectionReason:   fmt.Sprintf("selected %s by %s", selected.Provider, objective),
		EligibleProviders: eligible,
		RejectedProviders: rejected,
	}, nil
}
