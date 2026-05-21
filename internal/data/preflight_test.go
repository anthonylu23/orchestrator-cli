package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
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

func TestPrepareRejectsBundledSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := Prepare(app.JobSpec{Data: []app.DataInput{{Name: "train", Source: link}}}, PreflightOptions{})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareRejectsSymlinkInsideBundledDirectory(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(bundle, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := Prepare(app.JobSpec{Data: []app.DataInput{{Name: "train", Source: bundle}}}, PreflightOptions{})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareRejectsNonRegularBundledFile(t *testing.T) {
	source := "/dev/null"
	if _, err := os.Stat(source); err != nil {
		t.Skipf("%s unavailable: %v", source, err)
	}
	_, err := Prepare(app.JobSpec{Data: []app.DataInput{{Name: "device", Source: source}}}, PreflightOptions{})
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v", err)
	}
}
