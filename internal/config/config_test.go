package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTrainDefaults(t *testing.T) {
	t.Setenv("SWITCHBOARD_CLI_HOME", filepath.Join(t.TempDir(), "home"))

	got, err := LoadTrain(TrainFlags{Script: "examples/train.py"})
	if err != nil {
		t.Fatalf("LoadTrain returned error: %v", err)
	}
	if got.Provider != "local" {
		t.Fatalf("provider = %q, want local", got.Provider)
	}
	if got.Job.Name != "train.py" {
		t.Fatalf("job name = %q, want train.py", got.Job.Name)
	}
	if got.BundleMaxSizeBytes != DefaultBundleMaxSizeMB*1024*1024 {
		t.Fatalf("bundle max = %d", got.BundleMaxSizeBytes)
	}
	if !got.RequireOverrideAboveLimit {
		t.Fatal("expected large bundle override to be required by default")
	}
}

func TestLoadTrainSwitchboardHomePrecedesLegacyHome(t *testing.T) {
	currentHome := filepath.Join(t.TempDir(), "switchboard-home")
	legacyHome := filepath.Join(t.TempDir(), "orchestrator-home")
	t.Setenv("SWITCHBOARD_CLI_HOME", currentHome)
	t.Setenv("ORCHESTRATOR_CLI_HOME", legacyHome)

	got, err := LoadTrain(TrainFlags{Script: "examples/train.py"})
	if err != nil {
		t.Fatalf("LoadTrain returned error: %v", err)
	}
	if got.SwitchboardHome != currentHome {
		t.Fatalf("home = %q, want %q", got.SwitchboardHome, currentHome)
	}
}

func TestLoadTrainUsesLegacyHomeEnvFallback(t *testing.T) {
	legacyHome := filepath.Join(t.TempDir(), "orchestrator-home")
	t.Setenv("SWITCHBOARD_CLI_HOME", "")
	t.Setenv("ORCHESTRATOR_CLI_HOME", legacyHome)

	got, err := LoadTrain(TrainFlags{Script: "examples/train.py"})
	if err != nil {
		t.Fatalf("LoadTrain returned error: %v", err)
	}
	if got.SwitchboardHome != legacyHome {
		t.Fatalf("home = %q, want %q", got.SwitchboardHome, legacyHome)
	}
}

func TestLoadTrainUsesExistingLegacyHomeBeforeDefault(t *testing.T) {
	userHome := t.TempDir()
	legacyHome := filepath.Join(userHome, ".orchestrator-cli")
	if err := os.MkdirAll(legacyHome, 0o755); err != nil {
		t.Fatalf("create legacy home: %v", err)
	}
	t.Setenv("SWITCHBOARD_CLI_HOME", "")
	t.Setenv("ORCHESTRATOR_CLI_HOME", "")
	t.Setenv("HOME", userHome)

	got, err := LoadTrain(TrainFlags{Script: "examples/train.py"})
	if err != nil {
		t.Fatalf("LoadTrain returned error: %v", err)
	}
	if got.SwitchboardHome != legacyHome {
		t.Fatalf("home = %q, want %q", got.SwitchboardHome, legacyHome)
	}
}

func TestLoadTrainFlagsOverrideYAML(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "switchboard.yaml")
	content := []byte(`
job:
  name: yaml-name
  script: yaml.py
  args: ["--from-yaml"]
  env:
    FOO: bar
data:
  bundle:
    max_size_mb: 1
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := LoadTrain(TrainFlags{
		ConfigPath:      configPath,
		Provider:        "local",
		Script:          "flag.py",
		Args:            []string{"--from-flag"},
		SwitchboardHome: filepath.Join(dir, "home"),
	})
	if err != nil {
		t.Fatalf("LoadTrain returned error: %v", err)
	}
	if got.Job.Script != "flag.py" {
		t.Fatalf("script = %q", got.Job.Script)
	}
	if len(got.Job.Args) != 1 || got.Job.Args[0] != "--from-flag" {
		t.Fatalf("args = %#v", got.Job.Args)
	}
	if got.Job.Env["FOO"] != "bar" {
		t.Fatalf("env not loaded: %#v", got.Job.Env)
	}
	if got.BundleMaxSizeBytes != 1024*1024 {
		t.Fatalf("bundle max = %d", got.BundleMaxSizeBytes)
	}
}

func TestLoadTrainAcceptsContainerImageJob(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "switchboard.yaml")
	content := []byte(`
job:
  name: gcp-container
  image: us-docker.pkg.dev/project/repo/train:latest
  command: ["python", "-m", "trainer"]
  args: ["--epochs", "2"]
gcp:
  project_id: test-project
  location: us-central1
  output_uri_prefix: gs://bucket/outputs
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := LoadTrain(TrainFlags{
		ConfigPath:      configPath,
		Provider:        "gcp",
		SwitchboardHome: filepath.Join(dir, "home"),
	})
	if err != nil {
		t.Fatalf("LoadTrain returned error: %v", err)
	}
	if got.Job.Script != "" || got.Job.Image != "us-docker.pkg.dev/project/repo/train:latest" {
		t.Fatalf("job = %#v", got.Job)
	}
	if got.GCP.MachineType != "n1-standard-4" || got.GCP.BootDiskSizeGB != 100 || got.GCP.PollIntervalSeconds != 30 {
		t.Fatalf("gcp defaults = %#v", got.GCP)
	}
}

func TestLoadTrainRequiresGCPImageAndFields(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "switchboard.yaml")
	content := []byte(`
job:
  script: train.py
gcp:
  project_id: test-project
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadTrain(TrainFlags{
		ConfigPath:      configPath,
		Provider:        "gcp",
		SwitchboardHome: filepath.Join(dir, "home"),
	})
	if err == nil {
		t.Fatal("expected gcp validation error")
	}
	if err.Error() != "gcp provider requires job.image" {
		t.Fatalf("error = %v", err)
	}
}
