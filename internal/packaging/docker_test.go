package packaging

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDockerBuilderBuildsAndPushesImage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	runner := &recordingRunner{}
	var stdout, stderr bytes.Buffer
	result, err := (DockerBuilder{Runner: runner, Stdout: &stdout, Stderr: &stderr}).BuildAndPush(context.Background(), BuildRequest{
		Config: Config{
			Dockerfile: "Dockerfile",
			ContextDir: dir,
			Image:      "us-central1-docker.pkg.dev/project/repo/train:latest",
			Platform:   "linux/amd64",
		},
		RunID: "r_123",
	})
	if err != nil {
		t.Fatalf("BuildAndPush returned error: %v", err)
	}
	if result.Image != "us-central1-docker.pkg.dev/project/repo/train:latest" {
		t.Fatalf("image = %q", result.Image)
	}
	want := []recordedCommand{
		{Name: "docker", Args: []string{"build", "--platform", "linux/amd64", "-f", filepath.Join(dir, "Dockerfile"), "-t", "us-central1-docker.pkg.dev/project/repo/train:latest", dir}},
		{Name: "docker", Args: []string{"push", "us-central1-docker.pkg.dev/project/repo/train:latest"}},
	}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestDockerBuilderRejectsMissingDockerfile(t *testing.T) {
	_, err := (DockerBuilder{Runner: &recordingRunner{}}).BuildAndPush(context.Background(), BuildRequest{
		Config: Config{Dockerfile: "Dockerfile", ContextDir: t.TempDir(), Image: "image"},
	})
	if err == nil || !contains(err.Error(), "dockerfile") {
		t.Fatalf("error = %v", err)
	}
}

func TestDockerBuilderRejectsFileContext(t *testing.T) {
	dir := t.TempDir()
	contextFile := filepath.Join(dir, "context.tar")
	if err := os.WriteFile(contextFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write context file: %v", err)
	}
	_, err := (DockerBuilder{Runner: &recordingRunner{}}).BuildAndPush(context.Background(), BuildRequest{
		Config: Config{ContextDir: contextFile, Image: "image"},
	})
	if err == nil || !contains(err.Error(), "must be a directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestDockerBuilderRejectsMalformedPlatform(t *testing.T) {
	_, err := (DockerBuilder{Runner: &recordingRunner{}}).BuildAndPush(context.Background(), BuildRequest{
		Config: Config{ContextDir: t.TempDir(), Image: "image", Platform: "linux amd64"},
	})
	if err == nil || !contains(err.Error(), "platform") {
		t.Fatalf("error = %v", err)
	}
}

func TestDockerBuilderReportsBuildDaemonFailure(t *testing.T) {
	dir := t.TempDir()
	runner := &recordingRunner{failOn: "build", failErr: errors.New("Cannot connect to the Docker daemon")}
	_, err := (DockerBuilder{Runner: runner}).BuildAndPush(context.Background(), BuildRequest{
		Config: Config{ContextDir: dir, Image: "image", Platform: "linux/amd64"},
	})
	if err == nil || !contains(err.Error(), "docker build failed") {
		t.Fatalf("error = %v", err)
	}
	if !contains(err.Error(), "Docker daemon") {
		t.Fatalf("missing daemon guidance: %v", err)
	}
}

func TestDockerBuilderReportsPushFailure(t *testing.T) {
	dir := t.TempDir()
	runner := &recordingRunner{failOn: "push"}
	_, err := (DockerBuilder{Runner: runner}).BuildAndPush(context.Background(), BuildRequest{
		Config: Config{ContextDir: dir, Image: "image"},
	})
	if err == nil || !contains(err.Error(), "docker push failed") {
		t.Fatalf("error = %v", err)
	}
	if !contains(err.Error(), "gcloud auth configure-docker") {
		t.Fatalf("missing auth guidance: %v", err)
	}
}

func TestArtifactRegistryImage(t *testing.T) {
	got, err := ArtifactRegistryImage("us-central1", "test-project", "switchboard", "r_ABC.123")
	if err != nil {
		t.Fatalf("ArtifactRegistryImage returned error: %v", err)
	}
	want := "us-central1-docker.pkg.dev/test-project/switchboard/switchboard-cli-r_abc.123:latest"
	if got != want {
		t.Fatalf("image = %q, want %q", got, want)
	}
}

type recordedCommand struct {
	Name string
	Args []string
}

type recordingRunner struct {
	commands []recordedCommand
	failOn   string
	failErr  error
}

func (r *recordingRunner) Run(ctx context.Context, name string, args []string, stdout io.Writer, stderr io.Writer) error {
	r.commands = append(r.commands, recordedCommand{Name: name, Args: append([]string(nil), args...)})
	if r.failOn != "" && len(args) > 0 && args[0] == r.failOn {
		if r.failErr != nil {
			return r.failErr
		}
		return errors.New("boom")
	}
	return nil
}

func contains(haystack string, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
