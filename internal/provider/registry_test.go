package provider

import (
	"context"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

func TestNewRegistryRejectsDuplicateProviderNames(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected duplicate provider panic")
		}
	}()
	_ = NewRegistry(testProvider{name: "duplicate"}, testProvider{name: "duplicate"})
}

type testProvider struct {
	name app.ProviderName
}

func (p testProvider) Name() app.ProviderName {
	return p.name
}

func (p testProvider) ValidateAuth(ctx context.Context) error {
	return nil
}

func (p testProvider) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	return app.ProviderCapabilities{}, nil
}

func (p testProvider) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	return app.SupportReport{Supported: true}
}

func (p testProvider) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	return app.CostEstimate{}, nil
}

func (p testProvider) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
	return app.SubmitResult{}, nil
}

func (p testProvider) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	return app.ProviderJobStatus{}, nil
}

func (p testProvider) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, nil
}

func (p testProvider) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	return nil
}
