package staging

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/config"
)

func TestRuntimeEnvAddsSharedPrefixesAndInputURIs(t *testing.T) {
	env := RuntimeEnv(config.StagingConfig{
		CheckpointURIPrefix: "s3://bucket/switchboard/checkpoints/",
		DataURIPrefix:       "s3://bucket/switchboard/data",
	}, "r_123", []app.DataInput{
		{Name: "train-set", Source: "s3://bucket/raw/train.csv", Mount: "/workspace/data/train.csv", Mode: app.DataInputModeURI},
		{Name: "local", Source: "./data", Mount: "/workspace/data/local", Mode: app.DataInputModeBundle},
	})

	if env["SWITCHBOARD_CHECKPOINT_URI_PREFIX"] != "s3://bucket/switchboard/checkpoints/r_123/checkpoints" {
		t.Fatalf("checkpoint prefix = %q", env["SWITCHBOARD_CHECKPOINT_URI_PREFIX"])
	}
	if env["SWITCHBOARD_DATA_URI_PREFIX"] != "s3://bucket/switchboard/data/r_123/data" {
		t.Fatalf("data prefix = %q", env["SWITCHBOARD_DATA_URI_PREFIX"])
	}
	if env["SWITCHBOARD_DATA_TRAIN_SET_URI"] != "s3://bucket/raw/train.csv" {
		t.Fatalf("train uri = %q", env["SWITCHBOARD_DATA_TRAIN_SET_URI"])
	}
	if env["SWITCHBOARD_DATA_TRAIN_SET_MOUNT"] != "/workspace/data/train.csv" {
		t.Fatalf("train mount = %q", env["SWITCHBOARD_DATA_TRAIN_SET_MOUNT"])
	}
	if _, ok := env["SWITCHBOARD_DATA_LOCAL_URI"]; ok {
		t.Fatalf("bundled input should not get URI env: %#v", env)
	}
}

func TestStageBundledInputsUploadsFilesAndRewritesInputs(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "train.csv")
	if err := os.WriteFile(filePath, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	nestedDir := filepath.Join(dir, "dataset")
	if err := os.MkdirAll(filepath.Join(nestedDir, "nested"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "nested", "test.csv"), []byte("x\n1\n"), 0o600); err != nil {
		t.Fatalf("write nested: %v", err)
	}

	uploader := &fakeUploader{}
	job := app.JobSpec{Data: []app.DataInput{
		{Name: "train", Source: filePath, Mount: "/workspace/data/train.csv", Mode: app.DataInputModeBundle},
		{Name: "eval", Source: nestedDir, Mount: "/workspace/data/eval", Mode: app.DataInputModeBundle},
		{Name: "remote", Source: "gs://bucket/raw.csv", Mount: "/workspace/data/raw.csv", Mode: app.DataInputModeURI},
	}}
	manifest := app.DataManifest{Inputs: append([]app.DataInput(nil), job.Data...), BundleSizeBytes: 100, RequiresLargeBundleOverride: true}

	got, err := StageBundledInputs(context.Background(), config.StagingConfig{DataURIPrefix: "gs://bucket/staged"}, "r_123", job, manifest, uploader)
	if err != nil {
		t.Fatalf("StageBundledInputs returned error: %v", err)
	}
	if got.Manifest.BundleSizeBytes != 0 || got.Manifest.RequiresLargeBundleOverride {
		t.Fatalf("manifest bundle fields = %#v", got.Manifest)
	}
	if got.Job.Data[0].Mode != app.DataInputModeURI || got.Job.Data[0].Source != "gs://bucket/staged/r_123/data/train/train.csv" {
		t.Fatalf("file input = %#v", got.Job.Data[0])
	}
	if got.Job.Data[1].Mode != app.DataInputModeURI || got.Job.Data[1].Source != "gs://bucket/staged/r_123/data/eval" {
		t.Fatalf("dir input = %#v", got.Job.Data[1])
	}
	if got.Job.Data[2].Source != "gs://bucket/raw.csv" {
		t.Fatalf("uri input changed = %#v", got.Job.Data[2])
	}
	wantUploads := []string{
		"gs://bucket/staged/r_123/data/train/train.csv",
		"gs://bucket/staged/r_123/data/eval/nested/test.csv",
	}
	if !reflect.DeepEqual(uploader.destinations, wantUploads) {
		t.Fatalf("uploads = %#v", uploader.destinations)
	}
}

func TestStageBundledInputsUploadsToS3WithAWSCLI(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "train.csv")
	if err := os.WriteFile(filePath, []byte("x\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runner := &fakeCommandRunner{}
	uploader := &schemeUploader{gcs: &fakeUploader{}, s3: &s3Uploader{runner: runner}}
	got, err := StageBundledInputs(context.Background(), config.StagingConfig{DataURIPrefix: "s3://bucket/staged"}, "r_123", app.JobSpec{Data: []app.DataInput{
		{Name: "train", Source: filePath, Mount: "/workspace/data/train.csv", Mode: app.DataInputModeBundle},
	}}, app.DataManifest{Inputs: []app.DataInput{{Name: "train", Source: filePath, Mount: "/workspace/data/train.csv", Mode: app.DataInputModeBundle}}}, uploader)
	if err != nil {
		t.Fatalf("StageBundledInputs returned error: %v", err)
	}
	wantURI := "s3://bucket/staged/r_123/data/train/train.csv"
	if got.Job.Data[0].Source != wantURI || got.Job.Data[0].Mode != app.DataInputModeURI {
		t.Fatalf("staged input = %#v", got.Job.Data[0])
	}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, []string{"s3", "cp", filePath, wantURI}) {
		t.Fatalf("runner calls = %#v", runner.calls)
	}
}

type fakeUploader struct {
	destinations []string
}

func (u *fakeUploader) UploadFile(ctx context.Context, sourcePath string, destinationURI string) error {
	u.destinations = append(u.destinations, destinationURI)
	return nil
}

type fakeCommandRunner struct {
	calls []commandCall
}

type commandCall struct {
	name string
	args []string
}

func (r *fakeCommandRunner) Run(ctx context.Context, name string, args []string, stdout io.Writer, stderr io.Writer) error {
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...)})
	return nil
}
