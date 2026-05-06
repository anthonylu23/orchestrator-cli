package local

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
		script := filepath.Join(home, "train.py")
		if err := os.WriteFile(script, []byte("print('{\"type\":\"status\",\"state\":\"ok\"}')\n"), 0o600); err != nil {
			t.Fatalf("write script: %v", err)
		}
		provider := New(&bytes.Buffer{}, &bytes.Buffer{})
		validJob := app.JobSpec{Script: script, WorkDir: paths.Workspace}
		return contract.Subject{
			Name:              string(app.ProviderLocal),
			Adapter:           provider,
			ValidJob:          validJob,
			InvalidJob:        app.JobSpec{},
			SubmitRequest:     app.SubmitRequest{JobSpec: validJob, RunID: "r_contract", AttemptID: "a_contract", RunDir: paths.RunDir},
			ProviderRefPrefix: "local:",
			StreamLogs:        contract.StreamLogsUnsupported,
			AssertCapabilities: func(t *testing.T, capabilities app.ProviderCapabilities) {
				t.Helper()
				if !capabilities.SupportsDataBundle {
					t.Fatalf("local provider must report bundled data support: %#v", capabilities)
				}
			},
			Cancel: func(t *testing.T, adapter app.ProviderAdapter) {
				t.Helper()
				if err := adapter.Cancel(context.Background(), app.ProviderJobRef{ID: "not-local"}); err == nil {
					t.Fatal("expected invalid local ref cancellation to fail")
				}
			},
		}
	})
}
