package routing

import (
	"context"
	"testing"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
	"github.com/anthonylu23/orchestrator-cli/internal/provider"
	"github.com/anthonylu23/orchestrator-cli/internal/provider/mock"
)

func TestSelectChoosesCheapestEligibleProvider(t *testing.T) {
	registry := provider.NewRegistry(
		mock.New(mock.Config{Name: "mock-expensive", HourlyCost: 3}, nil, nil),
		mock.New(mock.Config{Name: "mock-cheap", HourlyCost: 1}, nil, nil),
	)
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{Objective: "min_cost"})
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if decision.SelectedProvider != "mock-cheap" {
		t.Fatalf("selected = %q", decision.SelectedProvider)
	}
	if len(decision.EligibleProviders) != 2 {
		t.Fatalf("eligible = %#v", decision.EligibleProviders)
	}
}

func TestSelectRecordsRejectedProvider(t *testing.T) {
	registry := provider.NewRegistry(mock.New(mock.Config{Name: "mock-cheap", HourlyCost: 1}, nil, nil))
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{
		Objective: "min_cost",
		Exclude:   map[string]bool{"mock-cheap": true},
	})
	if err == nil {
		t.Fatal("expected no eligible providers")
	}
	if len(decision.RejectedProviders) != 1 {
		t.Fatalf("rejected = %#v", decision.RejectedProviders)
	}
}
