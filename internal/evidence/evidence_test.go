package evidence

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

func TestFingerprintDatasetCountsNonEmptyJSONLLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eval.jsonl")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n\n{\"a\":2}\n"), 0o600); err != nil {
		t.Fatalf("write dataset: %v", err)
	}
	got, err := FingerprintDataset(path)
	if err != nil {
		t.Fatalf("FingerprintDataset returned error: %v", err)
	}
	if got.Path != path || got.NumRecords != 2 || got.SizeBytes == 0 {
		t.Fatalf("fingerprint = %#v", got)
	}
	if !strings.HasPrefix(got.SHA256, "sha256:") {
		t.Fatalf("sha = %q", got.SHA256)
	}
}

func TestBuildFailsWhenDatasetPathIsUnreadable(t *testing.T) {
	_, err := Build(context.Background(), BuildOptions{
		Job: app.JobSpec{
			Workload: app.WorkloadSpec{
				Dataset: app.DatasetRef{Path: filepath.Join(t.TempDir(), "missing.jsonl")},
			},
		},
	})
	if err == nil {
		t.Fatal("expected missing dataset error")
	}
}
