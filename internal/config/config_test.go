package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoadTrainResolvesConfigRelativePaths(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "configs")
	if err := os.MkdirAll(filepath.Join(configDir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "switchboard.yaml")
	content := []byte(`
job:
  script: train.py
  work_dir: work
data:
  inputs:
    - name: train
      source: data/train.csv
      mode: bundle
    - name: remote
      source: gs://bucket/data.csv
      mode: uri
packaging:
  dockerfile: Dockerfile
  context: .
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := LoadTrain(TrainFlags{
		ConfigPath:      configPath,
		Provider:        "local",
		SwitchboardHome: filepath.Join(dir, "home"),
	})
	if err != nil {
		t.Fatalf("LoadTrain returned error: %v", err)
	}
	if got.Job.Script != filepath.Join(configDir, "train.py") {
		t.Fatalf("script = %q", got.Job.Script)
	}
	if got.Job.WorkDir != filepath.Join(configDir, "work") {
		t.Fatalf("work dir = %q", got.Job.WorkDir)
	}
	if got.Job.Data[0].Source != filepath.Join(configDir, "data", "train.csv") {
		t.Fatalf("bundle source = %q", got.Job.Data[0].Source)
	}
	if got.Job.Data[1].Source != "gs://bucket/data.csv" {
		t.Fatalf("uri source = %q", got.Job.Data[1].Source)
	}
	if got.Packaging.Context != configDir || got.Packaging.Dockerfile != filepath.Join(configDir, "Dockerfile") {
		t.Fatalf("packaging = %#v", got.Packaging)
	}
}

func TestLoadTrainDoesNotResolveFlagScriptAgainstConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "switchboard.yaml")
	if err := os.WriteFile(configPath, []byte("job:\n  script: yaml.py\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := LoadTrain(TrainFlags{
		ConfigPath:      configPath,
		Provider:        "local",
		Script:          "flag.py",
		SwitchboardHome: filepath.Join(dir, "home"),
	})
	if err != nil {
		t.Fatalf("LoadTrain returned error: %v", err)
	}
	if got.Job.Script != "flag.py" {
		t.Fatalf("script = %q", got.Job.Script)
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
	for _, want := range []string{"job.image or packaging", "gcp.output_uri_prefix is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error: %v", want, err)
		}
	}
}

func TestLoadTrainReportsAllMissingGCPFields(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "switchboard.yaml")
	if err := os.WriteFile(configPath, []byte("job:\n  script: train.py\n"), 0o600); err != nil {
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
	for _, want := range []string{"job.image or packaging", "gcp.project_id is required", "gcp.output_uri_prefix is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "gcp.location is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadTrainAcceptsGCPPackagingConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "switchboard.yaml")
	content := []byte(`
job:
  script: train.py
packaging:
  dockerfile: Dockerfile
  context: .
  platform: linux/amd64
gcp:
  project_id: test-project
  location: us-central1
  output_uri_prefix: gs://bucket/outputs
  artifact_registry_repository: switchboard
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
	if got.Job.Script != filepath.Join(dir, "train.py") || got.Job.Image != "" {
		t.Fatalf("job = %#v", got.Job)
	}
	if got.Packaging.Dockerfile != filepath.Join(dir, "Dockerfile") || got.Packaging.Context != dir || got.Packaging.Platform != "linux/amd64" {
		t.Fatalf("packaging = %#v", got.Packaging)
	}
	if got.GCP.ArtifactRegistryRepository != "switchboard" {
		t.Fatalf("gcp = %#v", got.GCP)
	}
}

func TestLoadTrainParsesHardwareRoutingSchema(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "switchboard.yaml")
	content := []byte(`
job:
  script: train.py
routing:
  mode: full_auto
  objective: fastest_within_budget
  budget:
    max_run_cost_usd: 75
  max_attempts: 3
sizing:
  probe:
    command: ["python", "train.py", "--profile-memory"]
    output: switchboard-sizing.json
  hints:
    dataset_size_gb: 180
    model_parameters_b: 7
    batch_size: 8
    gradient_accumulation_steps: 2
    precision: bf16
    optimizer: adamw
    sequence_length: 4096
hardware:
  constraints:
    max_gpus: 8
    allowed_gpu_families: ["nvidia-a100", "nvidia-h100"]
    min_vram_gb_per_gpu: 40
    regions: ["us-central1", "us-east4"]
    allow_spot: true
  manual:
    provider: gcp
    shape_id: gcp-us-central1-a100-1
mock:
  providers:
    - name: mock-a100
      hourly_cost: 4.25
      hardware_shapes:
        - id: mock-a100-1
          provider: mock-a100
          region: us-central1
          machine_type: a2-highgpu-1g
          accelerator_type: NVIDIA_TESLA_A100
          accelerator_count: 1
          gpu_family: nvidia-a100
          vram_gb_per_gpu: 40
          total_vram_gb: 40
          on_demand_hourly_usd: 4.25
          supports_on_demand: true
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := LoadTrain(TrainFlags{
		ConfigPath:      configPath,
		Provider:        "auto",
		SwitchboardHome: filepath.Join(dir, "home"),
	})
	if err != nil {
		t.Fatalf("LoadTrain returned error: %v", err)
	}
	if got.Routing.Mode != "full_auto" || got.Routing.Objective != "fastest_within_budget" || got.Routing.MaxAttempts != 3 {
		t.Fatalf("routing = %#v", got.Routing)
	}
	if got.Routing.Budget.MaxRunCostUSD != 75 {
		t.Fatalf("budget = %#v", got.Routing.Budget)
	}
	if got.Sizing.Probe.Output != "switchboard-sizing.json" || got.Sizing.Hints.Precision != "bf16" {
		t.Fatalf("sizing = %#v", got.Sizing)
	}
	if got.Hardware.Constraints.MaxGPUs != 8 || len(got.Hardware.Constraints.AllowedGPUFamilies) != 2 {
		t.Fatalf("hardware = %#v", got.Hardware)
	}
	if len(got.Mock.Providers) != 1 || len(got.Mock.Providers[0].HardwareShapes) != 1 {
		t.Fatalf("mock providers = %#v", got.Mock.Providers)
	}
	shape := got.Mock.Providers[0].HardwareShapes[0]
	if shape.ID != "mock-a100-1" || shape.TotalVRAMGB != 40 || !shape.SupportsOnDemand {
		t.Fatalf("shape = %#v", shape)
	}
}
