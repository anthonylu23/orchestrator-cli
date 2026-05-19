package packaging

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Config struct {
	Dockerfile string
	ContextDir string
	Image      string
	Platform   string
}

type BuildRequest struct {
	Config Config
	RunID  string
}

type BuildResult struct {
	Image string
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, stdout io.Writer, stderr io.Writer) error
}

type DockerBuilder struct {
	Runner CommandRunner
	Stdout io.Writer
	Stderr io.Writer
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args []string, stdout io.Writer, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (b DockerBuilder) BuildAndPush(ctx context.Context, req BuildRequest) (BuildResult, error) {
	cfg := req.Config
	if cfg.Image == "" {
		return BuildResult{}, fmt.Errorf("packaging image is required")
	}
	if cfg.ContextDir == "" {
		cfg.ContextDir = "."
	}
	contextDir, err := filepath.Abs(cfg.ContextDir)
	if err != nil {
		return BuildResult{}, fmt.Errorf("resolve packaging context: %w", err)
	}
	if _, err := os.Stat(contextDir); err != nil {
		return BuildResult{}, fmt.Errorf("packaging context %q is not readable: %w", cfg.ContextDir, err)
	}
	var args []string
	args = append(args, "build")
	if cfg.Platform != "" {
		args = append(args, "--platform", cfg.Platform)
	}
	if cfg.Dockerfile != "" {
		dockerfile := cfg.Dockerfile
		if !filepath.IsAbs(dockerfile) {
			dockerfile = filepath.Join(contextDir, dockerfile)
		}
		if _, err := os.Stat(dockerfile); err != nil {
			return BuildResult{}, fmt.Errorf("packaging dockerfile %q is not readable: %w", cfg.Dockerfile, err)
		}
		args = append(args, "-f", dockerfile)
	}
	args = append(args, "-t", cfg.Image, contextDir)
	if err := b.runner().Run(ctx, "docker", args, b.stdout(), b.stderr()); err != nil {
		return BuildResult{}, fmt.Errorf("docker build failed for %s: %w", cfg.Image, err)
	}
	if err := b.runner().Run(ctx, "docker", []string{"push", cfg.Image}, b.stdout(), b.stderr()); err != nil {
		return BuildResult{}, fmt.Errorf("docker push failed for %s: %w", cfg.Image, err)
	}
	return BuildResult{Image: cfg.Image}, nil
}

func ArtifactRegistryImage(location string, projectID string, repository string, runID string) (string, error) {
	location = strings.TrimSpace(location)
	projectID = strings.TrimSpace(projectID)
	repository = strings.Trim(strings.TrimSpace(repository), "/")
	runID = strings.TrimSpace(runID)
	if location == "" || projectID == "" || repository == "" || runID == "" {
		return "", fmt.Errorf("location, project id, repository, and run id are required to derive Artifact Registry image")
	}
	return fmt.Sprintf("%s-docker.pkg.dev/%s/%s/switchboard-cli-%s:latest", location, projectID, repository, safeImagePart(runID)), nil
}

func (b DockerBuilder) runner() CommandRunner {
	if b.Runner != nil {
		return b.Runner
	}
	return ExecRunner{}
}

func (b DockerBuilder) stdout() io.Writer {
	if b.Stdout != nil {
		return b.Stdout
	}
	return io.Discard
}

func (b DockerBuilder) stderr() io.Writer {
	if b.Stderr != nil {
		return b.Stderr
	}
	return io.Discard
}

func safeImagePart(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), ".-_")
	if out == "" {
		return "run"
	}
	return out
}
