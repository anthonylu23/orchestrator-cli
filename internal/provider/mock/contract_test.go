package mock

import (
	"bytes"
	"context"
	"testing"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
	"github.com/anthonylu23/orchestrator-cli/internal/artifact"
	"github.com/anthonylu23/orchestrator-cli/internal/provider/contract"
)

func TestProviderContract(t *testing.T) {
	contract.Run(t, func(t *testing.T) contract.Subject {
		t.Helper()
		home := t.TempDir()
		paths := artifact.ForRun(home, "r_contract")
		if err := artifact.EnsureRun(paths); err != nil {
			t.Fatalf("EnsureRun returned error: %v", err)
		}
		provider := New(Config{Name: "mock-contract", HourlyCost: 1.25}, &bytes.Buffer{}, &bytes.Buffer{})
		validJob := app.JobSpec{
			Script: "train.py",
			Data: []app.DataInput{{
				Name:   "remote",
				Source: "s3://bucket/train",
				Mount:  "/workspace/data/train",
				Mode:   app.DataInputModeURI,
			}},
		}
		return contract.Subject{
			Name:              "mock-contract",
			Adapter:           provider,
			ValidJob:          validJob,
			InvalidJob:        app.JobSpec{},
			SubmitRequest:     app.SubmitRequest{JobSpec: validJob, RunID: "r_contract", AttemptID: "a_contract", RunDir: paths.RunDir},
			ProviderRefPrefix: "mock:mock-contract:",
			StreamLogs:        contract.StreamLogsUnsupported,
			AssertCapabilities: func(t *testing.T, capabilities app.ProviderCapabilities) {
				t.Helper()
				if !capabilities.SupportsObjectStorePull || len(capabilities.SupportedURISchemes) == 0 {
					t.Fatalf("mock provider must report URI pull support: %#v", capabilities)
				}
			},
			Cancel: func(t *testing.T, adapter app.ProviderAdapter) {
				t.Helper()
				if err := adapter.Cancel(context.Background(), app.ProviderJobRef{ID: "mock:mock-contract:a_contract"}); err != nil {
					t.Fatalf("Cancel returned error: %v", err)
				}
			},
		}
	})
}
