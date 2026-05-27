package staging

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/config"
	"google.golang.org/api/storage/v1"
)

type Uploader interface {
	UploadFile(ctx context.Context, sourcePath string, destinationURI string) error
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, stdout io.Writer, stderr io.Writer) error
}

type ManagedResult struct {
	Job             app.JobSpec
	Manifest        app.DataManifest
	UploadedObjects []string
}

func StageBundledInputs(ctx context.Context, cfg config.StagingConfig, runID string, job app.JobSpec, manifest app.DataManifest, uploader Uploader) (ManagedResult, error) {
	if cfg.DataURIPrefix == "" {
		return ManagedResult{Job: job, Manifest: manifest}, nil
	}
	if uploader == nil {
		uploader = NewUploader()
	}

	stagedJob := job
	stagedManifest := manifest
	stagedInputs := append([]app.DataInput(nil), job.Data...)
	uploaded := []string{}
	for i, input := range stagedInputs {
		if input.Mode != app.DataInputModeBundle {
			continue
		}
		staged, objects, err := stageInput(ctx, cfg, runID, input, uploader)
		if err != nil {
			return ManagedResult{}, err
		}
		stagedInputs[i] = staged
		uploaded = append(uploaded, objects...)
	}
	stagedJob.Data = stagedInputs
	stagedManifest.Inputs = append([]app.DataInput(nil), stagedInputs...)
	stagedManifest.BundleSizeBytes = 0
	stagedManifest.RequiresLargeBundleOverride = false
	return ManagedResult{Job: stagedJob, Manifest: stagedManifest, UploadedObjects: uploaded}, nil
}

func NewUploader() Uploader {
	return &schemeUploader{
		gcs: &gcsUploader{},
		s3:  &s3Uploader{runner: execRunner{}},
	}
}

type schemeUploader struct {
	gcs Uploader
	s3  Uploader
}

func (u *schemeUploader) UploadFile(ctx context.Context, sourcePath string, destinationURI string) error {
	parsed, err := url.Parse(destinationURI)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("managed staging destination %q must be an object-store URI", destinationURI)
	}
	switch parsed.Scheme {
	case "gs":
		return u.gcs.UploadFile(ctx, sourcePath, destinationURI)
	case "s3":
		return u.s3.UploadFile(ctx, sourcePath, destinationURI)
	default:
		return fmt.Errorf("managed data staging only supports gs:// or s3:// upload prefixes; got %q", parsed.Scheme)
	}
}

type gcsUploader struct {
	client *storage.Service
}

func (u *gcsUploader) UploadFile(ctx context.Context, sourcePath string, destinationURI string) error {
	bucket, object, err := parseGCSURI(destinationURI)
	if err != nil {
		return err
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open staged source %q: %w", sourcePath, err)
	}
	defer file.Close()
	if u.client == nil {
		client, err := storage.NewService(ctx)
		if err != nil {
			return fmt.Errorf("create GCS client: %w", err)
		}
		u.client = client
	}
	if _, err := u.client.Objects.Insert(bucket, &storage.Object{Name: object}).Media(file).Context(ctx).Do(); err != nil {
		return fmt.Errorf("upload %q to %q: %w", sourcePath, destinationURI, err)
	}
	return nil
}

type s3Uploader struct {
	runner CommandRunner
	stdout io.Writer
	stderr io.Writer
}

func (u *s3Uploader) UploadFile(ctx context.Context, sourcePath string, destinationURI string) error {
	if _, _, err := parseS3URI(destinationURI); err != nil {
		return err
	}
	runner := u.runner
	if runner == nil {
		runner = execRunner{}
	}
	if err := runner.Run(ctx, "aws", []string{"s3", "cp", sourcePath, destinationURI}, writerOrDiscard(u.stdout), writerOrDiscard(u.stderr)); err != nil {
		return fmt.Errorf("upload %q to %q with aws s3 cp: %w", sourcePath, destinationURI, err)
	}
	return nil
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, stdout io.Writer, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func stageInput(ctx context.Context, cfg config.StagingConfig, runID string, input app.DataInput, uploader Uploader) (app.DataInput, []string, error) {
	info, err := os.Stat(input.Source)
	if err != nil {
		return app.DataInput{}, nil, fmt.Errorf("stat staged input %q: %w", input.Source, err)
	}
	inputPrefix := JoinURI(DataPrefix(cfg, runID), input.Name)
	if !info.IsDir() {
		destination := JoinURI(inputPrefix, filepath.Base(input.Source))
		if err := uploader.UploadFile(ctx, input.Source, destination); err != nil {
			return app.DataInput{}, nil, err
		}
		staged := input
		staged.Source = destination
		staged.Mode = app.DataInputModeURI
		return staged, []string{destination}, nil
	}

	uploaded := []string{}
	err = filepath.WalkDir(input.Source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("staged input path %q is not a regular file", path)
		}
		rel, err := filepath.Rel(input.Source, path)
		if err != nil {
			return err
		}
		destination := JoinURI(inputPrefix, filepath.ToSlash(rel))
		if err := uploader.UploadFile(ctx, path, destination); err != nil {
			return err
		}
		uploaded = append(uploaded, destination)
		return nil
	})
	if err != nil {
		return app.DataInput{}, nil, fmt.Errorf("stage data input %q: %w", input.Name, err)
	}
	staged := input
	staged.Source = inputPrefix
	staged.Mode = app.DataInputModeURI
	return staged, uploaded, nil
}

func parseGCSURI(value string) (string, string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("managed staging destination %q must be a gs:// URI", value)
	}
	if parsed.Scheme != "gs" {
		return "", "", fmt.Errorf("managed GCS staging destination %q must use gs://", value)
	}
	return parseObjectURIPath(parsed, value)
}

func parseS3URI(value string) (string, string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("managed staging destination %q must be an s3:// URI", value)
	}
	if parsed.Scheme != "s3" {
		return "", "", fmt.Errorf("managed S3 staging destination %q must use s3://", value)
	}
	return parseObjectURIPath(parsed, value)
}

func parseObjectURIPath(parsed *url.URL, value string) (string, string, error) {
	object := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if object == "" {
		return "", "", fmt.Errorf("managed staging destination %q is missing an object path", value)
	}
	unescaped, err := url.PathUnescape(object)
	if err != nil {
		return "", "", fmt.Errorf("decode object-store path %q: %w", object, err)
	}
	return parsed.Host, unescaped, nil
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return io.Discard
}
