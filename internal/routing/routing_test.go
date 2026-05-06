package routing

import (
	"context"
	"fmt"
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

func TestSelectRejectsProviderWithoutBundleSupport(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name:         "no-bundles",
		capabilities: app.ProviderCapabilities{SupportsLocalScript: true},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{
		Script: "train.py",
		Data: []app.DataInput{{
			Name:   "train",
			Source: "./train.csv",
			Mode:   app.DataInputModeBundle,
		}},
	}, Options{Objective: "min_cost"})
	if err == nil {
		t.Fatal("expected no eligible providers")
	}
	if len(decision.RejectedProviders) != 1 || decision.RejectedProviders[0].Reasons[0] == "" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSelectRejectsUnsupportedURIScheme(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name: "http-only",
		capabilities: app.ProviderCapabilities{
			SupportsLocalScript: true,
			SupportedURISchemes: []string{"http", "https"},
		},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{
		Script: "train.py",
		Data: []app.DataInput{{
			Name:   "train",
			Source: "s3://bucket/train.csv",
			Mode:   app.DataInputModeURI,
		}},
	}, Options{Objective: "min_cost"})
	if err == nil {
		t.Fatal("expected no eligible providers")
	}
	if len(decision.RejectedProviders) != 1 || decision.RejectedProviders[0].Reasons[0] != `provider does not support "s3" URI data input "train"` {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSelectRecordsSupportReportRejection(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name:         "rejecting",
		capabilities: app.ProviderCapabilities{SupportsLocalScript: true},
		validateSet:  true,
		validate: app.SupportReport{
			Supported: false,
			Reasons:   []string{"custom provider rejection"},
		},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{Objective: "min_cost"})
	if err == nil {
		t.Fatal("expected no eligible providers")
	}
	if len(decision.RejectedProviders) != 1 || decision.RejectedProviders[0].Reasons[0] != "custom provider rejection" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSelectAcceptsSupportedURIScheme(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name: "s3-provider",
		capabilities: app.ProviderCapabilities{
			SupportsLocalScript: true,
			SupportedURISchemes: []string{"s3"},
		},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{
		Script: "train.py",
		Data: []app.DataInput{{
			Name:   "train",
			Source: "s3://bucket/train.csv",
			Mode:   app.DataInputModeURI,
		}},
	}, Options{Objective: "min_cost"})
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if decision.SelectedProvider != "s3-provider" {
		t.Fatalf("decision = %#v", decision)
	}
}

type staticProvider struct {
	name         string
	capabilities app.ProviderCapabilities
	validateSet  bool
	validate     app.SupportReport
}

func (p staticProvider) Name() app.ProviderName {
	return app.ProviderName(p.name)
}

func (p staticProvider) ValidateAuth(ctx context.Context) error {
	return nil
}

func (p staticProvider) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	return p.capabilities, nil
}

func (p staticProvider) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	if p.validateSet {
		return p.validate
	}
	return app.SupportReport{Supported: true}
}

func (p staticProvider) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	return app.CostEstimate{HourlyUSD: 1, Currency: "USD"}, nil
}

func (p staticProvider) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
	return app.SubmitResult{}, fmt.Errorf("not implemented")
}

func (p staticProvider) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	return app.ProviderJobStatus{}, fmt.Errorf("not implemented")
}

func (p staticProvider) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, fmt.Errorf("not implemented")
}

func (p staticProvider) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	return fmt.Errorf("not implemented")
}
