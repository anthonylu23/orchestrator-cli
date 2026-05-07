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

func TestLoadTrainCloudTuneHomePrecedesSwitchboardHome(t *testing.T) {
	cloudTuneHome := filepath.Join(t.TempDir(), "cloudtune-home")
	switchboardHome := filepath.Join(t.TempDir(), "switchboard-home")
	t.Setenv("CLOUDTUNE_HOME", cloudTuneHome)
	t.Setenv("SWITCHBOARD_CLI_HOME", switchboardHome)

	got, err := LoadTrain(TrainFlags{Script: "examples/train.py"})
	if err != nil {
		t.Fatalf("LoadTrain returned error: %v", err)
	}
	if got.SwitchboardHome != cloudTuneHome {
		t.Fatalf("home = %q, want %q", got.SwitchboardHome, cloudTuneHome)
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

func TestLoadTrainSupportsCloudTuneWorkloadConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "cloudtune.yaml")
	content := []byte(`
workload:
  name: rag-eval-v1
  type: evaluation
  model:
    provider: openai
    name: gpt-4.1-mini
  dataset:
    name: support-eval
    path: ./evals/customer_support.jsonl
job:
  script: eval.py
routing:
  provider: auto
  max_attempts: 3
outputs:
  save_to: ./artifacts
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := LoadTrain(TrainFlags{
		ConfigPath:      configPath,
		SwitchboardHome: filepath.Join(dir, "home"),
	})
	if err != nil {
		t.Fatalf("LoadTrain returned error: %v", err)
	}
	if got.Provider != "auto" || got.Routing.MaxAttempts != 3 {
		t.Fatalf("routing = provider %q config %#v", got.Provider, got.Routing)
	}
	if got.Job.Name != "rag-eval-v1" || got.Job.Workload.Type != "evaluation" {
		t.Fatalf("workload = %#v job=%#v", got.Job.Workload, got.Job)
	}
	wantDatasetPath, err := filepath.Abs("./evals/customer_support.jsonl")
	if err != nil {
		t.Fatalf("abs dataset path: %v", err)
	}
	if got.Job.Workload.Model.Name != "gpt-4.1-mini" || got.Job.Workload.Dataset.Path != wantDatasetPath {
		t.Fatalf("workload refs = %#v", got.Job.Workload)
	}
	if got.Job.Outputs.SaveTo != "./artifacts" {
		t.Fatalf("outputs = %#v", got.Job.Outputs)
	}
	if got.ConfigPath == "" || got.ConfigPath != configPath {
		t.Fatalf("config path = %q, want %q", got.ConfigPath, configPath)
	}
	if got.ConfigHash == "" {
		t.Fatal("expected config hash")
	}
}
