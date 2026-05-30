package packaging

import (
	"context"
	"errors"
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
	if err := validateDockerPlatform(cfg.Platform); err != nil {
		return BuildResult{}, err
	}
	if cfg.ContextDir == "" {
		cfg.ContextDir = "."
	}
	contextDir, err := filepath.Abs(cfg.ContextDir)
	if err != nil {
		return BuildResult{}, fmt.Errorf("resolve packaging context: %w", err)
	}
	contextInfo, err := os.Stat(contextDir)
	if err != nil {
		return BuildResult{}, fmt.Errorf("packaging context %q is not readable: %w", cfg.ContextDir, err)
	}
	if !contextInfo.IsDir() {
		return BuildResult{}, fmt.Errorf("packaging context %q must be a directory", cfg.ContextDir)
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
		return BuildResult{}, fmt.Errorf("docker build failed for %s: %w; %s", cfg.Image, err, dockerBuildGuidance(err, cfg.Platform))
	}
	if err := b.runner().Run(ctx, "docker", []string{"push", cfg.Image}, b.stdout(), b.stderr()); err != nil {
		return BuildResult{}, fmt.Errorf("docker push failed for %s: %w; %s", cfg.Image, err, dockerPushGuidance(err))
	}
	return BuildResult{Image: cfg.Image}, nil
}

func validateDockerPlatform(platform string) error {
	if strings.TrimSpace(platform) == "" {
		return nil
	}
	if strings.ContainsAny(platform, " \t\r\n") {
		return fmt.Errorf("packaging platform %q must not contain whitespace", platform)
	}
	parts := strings.Split(platform, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("packaging platform %q must use os/arch or os/arch/variant format, such as linux/amd64", platform)
	}
	for _, part := range parts {
		if strings.Trim(part, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") != "" {
			return fmt.Errorf("packaging platform %q contains unsupported characters", platform)
		}
	}
	return nil
}

func dockerBuildGuidance(err error, platform string) string {
	lower := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, exec.ErrNotFound) || strings.Contains(lower, "executable file not found") || strings.Contains(lower, "no such file or directory"):
		return "Docker executable was not found; install Docker or make sure docker is on PATH"
	case strings.Contains(lower, "cannot connect to the docker daemon") || strings.Contains(lower, "docker daemon") || strings.Contains(lower, "is the docker daemon running"):
		return "verify Docker Desktop or the Docker daemon is running"
	case platform != "" && (strings.Contains(lower, "no match for platform") || strings.Contains(lower, "unsupported platform") || strings.Contains(lower, "platform") && strings.Contains(lower, "not found")):
		return fmt.Sprintf("verify platform %s is supported by the Docker builder and base image", platform)
	}
	if platform != "" {
		return fmt.Sprintf("verify Docker is installed, the daemon is running, and platform %s is supported", platform)
	}
	return "verify Docker is installed and the daemon is running"
}

func dockerPushGuidance(err error) string {
	lower := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, exec.ErrNotFound) || strings.Contains(lower, "executable file not found") || strings.Contains(lower, "no such file or directory"):
		return "Docker executable was not found; install Docker or make sure docker is on PATH"
	case strings.Contains(lower, "denied") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "authentication"):
		return "verify registry credentials and repository write access"
	}
	return "verify registry credentials and repository write access; for Artifact Registry, run gcloud auth configure-docker <location>-docker.pkg.dev"
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
