package data

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

func TestPrepareInfersModesAndMounts(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "train.txt")
	if err := os.WriteFile(localPath, []byte("abc"), 0o600); err != nil {
		t.Fatalf("write data: %v", err)
	}

	manifest, err := Prepare(app.JobSpec{Data: []app.DataInput{
		{Name: "train", Source: localPath},
		{Name: "test", Source: "https://example.com/test.csv"},
	}}, PreflightOptions{BundleSizeLimitBytes: 100, RequireOverride: true})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if manifest.Inputs[0].Mode != app.DataInputModeBundle {
		t.Fatalf("local mode = %q", manifest.Inputs[0].Mode)
	}
	if manifest.Inputs[1].Mode != app.DataInputModeURI {
		t.Fatalf("uri mode = %q", manifest.Inputs[1].Mode)
	}
	if manifest.Inputs[0].Mount != "/workspace/data/train" {
		t.Fatalf("mount = %q", manifest.Inputs[0].Mount)
	}
	if manifest.BundleSizeBytes != 3 {
		t.Fatalf("bundle size = %d", manifest.BundleSizeBytes)
	}
}

func TestPrepareMissingBundlePath(t *testing.T) {
	_, err := Prepare(app.JobSpec{Data: []app.DataInput{{Name: "missing", Source: filepath.Join(t.TempDir(), "nope")}}}, PreflightOptions{})
	if err == nil {
		t.Fatal("expected missing path error")
	}
}

func TestPrepareLargeBundleRequiresOverride(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "train.txt")
	if err := os.WriteFile(localPath, []byte("abcdef"), 0o600); err != nil {
		t.Fatalf("write data: %v", err)
	}

	_, err := Prepare(app.JobSpec{Data: []app.DataInput{{Name: "train", Source: localPath}}}, PreflightOptions{
		BundleSizeLimitBytes: 3,
		RequireOverride:      true,
	})
	if err == nil {
		t.Fatal("expected size limit error")
	}

	manifest, err := Prepare(app.JobSpec{Data: []app.DataInput{{Name: "train", Source: localPath}}}, PreflightOptions{
		BundleSizeLimitBytes: 3,
		RequireOverride:      true,
		AllowLargeBundle:     true,
	})
	if err != nil {
		t.Fatalf("Prepare with override returned error: %v", err)
	}
	if !manifest.RequiresLargeBundleOverride {
		t.Fatal("expected manifest to record required override")
	}
}
