package runtimeprep

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

func TestHostPathForMount(t *testing.T) {
	got, err := HostPathForMount("/tmp/run/workspace", "/workspace/data/train")
	if err != nil {
		t.Fatalf("HostPathForMount returned error: %v", err)
	}
	want := filepath.Join("/tmp/run/workspace", "data", "train")
	if got != want {
		t.Fatalf("host path = %q, want %q", got, want)
	}
}

func TestHostPathForMountRejectsUnsafePath(t *testing.T) {
	for _, mount := range []string{"/tmp/data", "relative/data", "/workspace/../tmp"} {
		if _, err := HostPathForMount("/tmp/run/workspace", mount); err == nil {
			t.Fatalf("expected %q to be rejected", mount)
		}
	}
}

func TestPrepareLocalMaterializesFileAndDirectory(t *testing.T) {
	dir := t.TempDir()
	fileSource := filepath.Join(dir, "train.txt")
	if err := os.WriteFile(fileSource, []byte("train"), 0o600); err != nil {
		t.Fatalf("write file source: %v", err)
	}
	dirSource := filepath.Join(dir, "dataset")
	if err := os.MkdirAll(dirSource, 0o755); err != nil {
		t.Fatalf("mkdir dir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirSource, "test.txt"), []byte("test"), 0o600); err != nil {
		t.Fatalf("write dir source: %v", err)
	}
	workspace := filepath.Join(dir, "workspace")
	manifest := app.DataManifest{Inputs: []app.DataInput{
		{Name: "train", Source: fileSource, Mount: "/workspace/data/train.txt", Mode: app.DataInputModeBundle},
		{Name: "test", Source: dirSource, Mount: "/workspace/data/test", Mode: app.DataInputModeBundle},
	}}

	prepared, err := PrepareLocal(app.JobSpec{
		Script:  "examples/train.py",
		Args:    []string{"--train", "/workspace/data/train.txt"},
		Env:     map[string]string{"TEST_DIR": "/workspace/data/test"},
		WorkDir: "/workspace/project",
	}, manifest, workspace)
	if err != nil {
		t.Fatalf("PrepareLocal returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "data", "train.txt")); err != nil {
		t.Fatalf("file not materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "data", "test", "test.txt")); err != nil {
		t.Fatalf("directory not materialized: %v", err)
	}
	if prepared.Job.Args[1] != filepath.Join(workspace, "data", "train.txt") {
		t.Fatalf("args = %#v", prepared.Job.Args)
	}
	if prepared.Job.Env["TEST_DIR"] != filepath.Join(workspace, "data", "test") {
		t.Fatalf("env = %#v", prepared.Job.Env)
	}
	if len(prepared.Job.Data) != len(manifest.Inputs) || prepared.Job.Data[0].Mode != app.DataInputModeBundle {
		t.Fatalf("prepared data = %#v", prepared.Job.Data)
	}
	if prepared.Job.WorkDir != filepath.Join(workspace, "project") {
		t.Fatalf("work dir = %q", prepared.Job.WorkDir)
	}
}

func TestPrepareLocalRejectsSymlinkInsideBundle(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "dataset")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	target := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(source, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := PrepareLocal(app.JobSpec{Script: "train.py"}, app.DataManifest{Inputs: []app.DataInput{{
		Name:   "train",
		Source: source,
		Mount:  "/workspace/data/train",
		Mode:   app.DataInputModeBundle,
	}}}, filepath.Join(dir, "workspace"))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareLocalPreservesExternalWorkDir(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "custom")
	prepared, err := PrepareLocal(app.JobSpec{Script: "train.py", WorkDir: workDir}, app.DataManifest{}, filepath.Join(dir, "workspace"))
	if err != nil {
		t.Fatalf("PrepareLocal returned error: %v", err)
	}
	if prepared.Job.WorkDir != workDir {
		t.Fatalf("work dir = %q, want %q", prepared.Job.WorkDir, workDir)
	}
}

func TestRewriteValueRequiresPathBoundary(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	mounts := map[string]string{"/workspace/data/test": filepath.Join(workspace, "data", "test")}
	got := RewriteValue("/workspace/data/test:/workspace/data/tester:/workspace/data/test/file", mounts)
	want := filepath.Join(workspace, "data", "test") + ":/workspace/data/tester:" + filepath.Join(workspace, "data", "test", "file")
	if got != want {
		t.Fatalf("rewritten = %q, want %q", got, want)
	}
}

func TestPrepareLocalRejectsNonRegularBundledFile(t *testing.T) {
	source := "/dev/null"
	if _, err := os.Stat(source); err != nil {
		t.Skipf("%s unavailable: %v", source, err)
	}
	_, err := PrepareLocal(app.JobSpec{Script: "train.py"}, app.DataManifest{Inputs: []app.DataInput{{
		Name:   "device",
		Source: source,
		Mount:  "/workspace/data/device",
		Mode:   app.DataInputModeBundle,
	}}}, filepath.Join(t.TempDir(), "workspace"))
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v", err)
	}
}
