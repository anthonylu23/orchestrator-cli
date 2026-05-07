package modal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

func TestProviderCapabilitiesAreExplicitlyExperimental(t *testing.T) {
	provider := New(nil, nil)
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities returned error: %v", err)
	}
	if !capabilities.Remote || !capabilities.SupportsArtifacts || !capabilities.SupportsCancel {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if capabilities.SupportsCheckpointResume || capabilities.SupportsCostEstimate {
		t.Fatalf("modal-sandbox should not claim cost/checkpoint support yet: %#v", capabilities)
	}
	if strings.Join(capabilities.WorkloadTypes, ",") != "evaluation,batch_inference" {
		t.Fatalf("workload types = %#v", capabilities.WorkloadTypes)
	}
}

func TestValidateJobRejectsUnreadableScriptAndRemoteDatasetURI(t *testing.T) {
	provider := New(nil, nil)
	report := provider.ValidateJob(context.Background(), app.JobSpec{
		Script: "missing.py",
		Workload: app.WorkloadSpec{
			Type:    app.WorkloadTypeEvaluation,
			Dataset: app.DatasetRef{URI: "s3://bucket/eval.jsonl"},
		},
	})
	if report.Supported {
		t.Fatalf("expected rejection: %#v", report)
	}
	if len(report.Reasons) < 2 {
		t.Fatalf("expected script and URI rejection reasons: %#v", report)
	}
}

func TestValidateJobAcceptsLocalEvalScriptAndDataset(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "eval.py")
	if err := os.WriteFile(script, []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	dataset := filepath.Join(dir, "eval.jsonl")
	if err := os.WriteFile(dataset, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write dataset: %v", err)
	}
	provider := New(nil, nil)
	report := provider.ValidateJob(context.Background(), app.JobSpec{
		Script: script,
		Workload: app.WorkloadSpec{
			Type:    app.WorkloadTypeEvaluation,
			Dataset: app.DatasetRef{Path: dataset},
		},
	})
	if !report.Supported {
		t.Fatalf("expected support: %#v", report)
	}
}
