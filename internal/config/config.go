package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/home"
	"gopkg.in/yaml.v3"
)

const DefaultBundleMaxSizeMB = 512

type Config struct {
	Job       JobConfig       `yaml:"job"`
	Data      DataConfig      `yaml:"data"`
	Packaging PackagingConfig `yaml:"packaging"`
	Routing   RoutingConfig   `yaml:"routing"`
	Sizing    SizingConfig    `yaml:"sizing"`
	Hardware  HardwareConfig  `yaml:"hardware"`
	Mock      MockConfig      `yaml:"mock"`
	GCP       GCPConfig       `yaml:"gcp"`
}

type JobConfig struct {
	Name    string            `yaml:"name"`
	Script  string            `yaml:"script"`
	Image   string            `yaml:"image"`
	Command []string          `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	WorkDir string            `yaml:"work_dir"`
}

type DataConfig struct {
	Inputs []app.DataInput `yaml:"inputs"`
	Bundle BundleConfig    `yaml:"bundle"`
}

type PackagingConfig struct {
	Dockerfile string `yaml:"dockerfile"`
	Context    string `yaml:"context"`
	Image      string `yaml:"image"`
	Platform   string `yaml:"platform"`
}

type BundleConfig struct {
	MaxSizeMB                 int  `yaml:"max_size_mb"`
	RequireOverrideAboveLimit bool `yaml:"require_override_above_limit"`
}

type RoutingConfig struct {
	Mode        string       `yaml:"mode"`
	Objective   string       `yaml:"objective"`
	Budget      BudgetConfig `yaml:"budget"`
	MaxAttempts int          `yaml:"max_attempts"`
}

type BudgetConfig struct {
	MaxRunCostUSD float64 `yaml:"max_run_cost_usd"`
}

type SizingConfig struct {
	Probe SizingProbeConfig `yaml:"probe"`
	Hints SizingHintsConfig `yaml:"hints"`
}

type SizingProbeConfig struct {
	Command []string `yaml:"command"`
	Output  string   `yaml:"output"`
}

type SizingHintsConfig struct {
	DatasetSizeGB             float64 `yaml:"dataset_size_gb"`
	ModelParametersB          float64 `yaml:"model_parameters_b"`
	ModelArtifactGB           float64 `yaml:"model_artifact_gb"`
	BatchSize                 int     `yaml:"batch_size"`
	GradientAccumulationSteps int     `yaml:"gradient_accumulation_steps"`
	Precision                 string  `yaml:"precision"`
	Optimizer                 string  `yaml:"optimizer"`
	SequenceLength            int     `yaml:"sequence_length"`
	ImageWidth                int     `yaml:"image_width"`
	ImageHeight               int     `yaml:"image_height"`
	ExpectedSteps             int     `yaml:"expected_steps"`
}

type HardwareConfig struct {
	Constraints HardwareConstraintsConfig `yaml:"constraints"`
	Manual      ManualHardwareConfig      `yaml:"manual"`
}

type HardwareConstraintsConfig struct {
	MaxGPUs            int      `yaml:"max_gpus"`
	AllowedGPUFamilies []string `yaml:"allowed_gpu_families"`
	MinVRAMGBPerGPU    int      `yaml:"min_vram_gb_per_gpu"`
	Regions            []string `yaml:"regions"`
	AllowSpot          bool     `yaml:"allow_spot"`
	RequireOnDemand    bool     `yaml:"require_on_demand"`
}

type ManualHardwareConfig struct {
	Provider    string `yaml:"provider"`
	ShapeID     string `yaml:"shape_id"`
	MachineType string `yaml:"machine_type"`
	Region      string `yaml:"region"`
}

type MockConfig struct {
	Providers []MockProviderConfig `yaml:"providers"`
}

type GCPConfig struct {
	ProjectID                  string  `yaml:"project_id"`
	Location                   string  `yaml:"location"`
	OutputURIPrefix            string  `yaml:"output_uri_prefix"`
	MachineType                string  `yaml:"machine_type"`
	AcceleratorType            string  `yaml:"accelerator_type"`
	AcceleratorCount           int32   `yaml:"accelerator_count"`
	BootDiskType               string  `yaml:"boot_disk_type"`
	BootDiskSizeGB             int32   `yaml:"boot_disk_size_gb"`
	ServiceAccount             string  `yaml:"service_account"`
	Network                    string  `yaml:"network"`
	PollIntervalSeconds        int     `yaml:"poll_interval_seconds"`
	EstimateHourlyUSD          float64 `yaml:"estimate_hourly_usd"`
	ArtifactRegistryRepository string  `yaml:"artifact_registry_repository"`
}

type MockProviderConfig struct {
	Name           string              `yaml:"name"`
	HourlyCost     float64             `yaml:"hourly_cost"`
	FailureMode    string              `yaml:"failure_mode"`
	HardwareShapes []app.HardwareShape `yaml:"hardware_shapes"`
	Events         []MockEventConfig   `yaml:"events"`
}

type MockEventConfig struct {
	Type          string             `yaml:"type"`
	Step          *int64             `yaml:"step"`
	Split         string             `yaml:"split"`
	State         string             `yaml:"state"`
	CheckpointURI string             `yaml:"checkpoint_uri"`
	Message       string             `yaml:"message"`
	Metrics       map[string]float64 `yaml:"metrics"`
}

type TrainFlags struct {
	ConfigPath           string
	Provider             string
	Script               string
	Args                 []string
	AllowLargeDataBundle bool
	SwitchboardHome      string
}

type ResolvedTrainConfig struct {
	Provider                  string
	Job                       app.JobSpec
	Packaging                 PackagingConfig
	Routing                   RoutingConfig
	Sizing                    SizingConfig
	Hardware                  HardwareConfig
	Mock                      MockConfig
	GCP                       GCPConfig
	BundleMaxSizeBytes        int64
	RequireOverrideAboveLimit bool
	AllowLargeDataBundle      bool
	SwitchboardHome           string
}

func LoadTrain(flags TrainFlags) (ResolvedTrainConfig, error) {
	cfg := Config{}
	configDir := ""
	if flags.ConfigPath != "" {
		loaded, err := LoadFile(flags.ConfigPath)
		if err != nil {
			return ResolvedTrainConfig{}, err
		}
		cfg = loaded
		configDir = filepath.Dir(flags.ConfigPath)
	}

	provider := cfgProviderDefault(flags.Provider)
	if provider == "" {
		provider = string(app.ProviderLocal)
	}

	job := app.JobSpec{
		Name:    cfg.Job.Name,
		Script:  cfg.Job.Script,
		Image:   cfg.Job.Image,
		Command: append([]string(nil), cfg.Job.Command...),
		Args:    append([]string(nil), cfg.Job.Args...),
		Env:     cloneMap(cfg.Job.Env),
		Data:    append([]app.DataInput(nil), cfg.Data.Inputs...),
		WorkDir: cfg.Job.WorkDir,
	}
	scriptFromFlag := flags.Script != ""
	if scriptFromFlag {
		job.Script = flags.Script
	}
	if len(flags.Args) > 0 {
		job.Args = append([]string(nil), flags.Args...)
	}
	if job.Script == "" && job.Image == "" {
		return ResolvedTrainConfig{}, errors.New("script or image is required")
	}
	if job.Name == "" {
		if job.Script != "" {
			job.Name = filepath.Base(job.Script)
		} else {
			job.Name = filepath.Base(job.Image)
		}
	}
	if job.Env == nil {
		job.Env = map[string]string{}
	}
	if job.WorkDir == "" {
		job.WorkDir = "."
	}
	if configDir != "" {
		if scriptFromFlag {
			originalScript := job.Script
			job = resolveJobPaths(job, configDir)
			job.Script = originalScript
		} else {
			job = resolveJobPaths(job, configDir)
		}
		cfg.Packaging = resolvePackagingPaths(cfg.Packaging, configDir)
	}

	maxSizeMB := cfg.Data.Bundle.MaxSizeMB
	if maxSizeMB == 0 {
		maxSizeMB = DefaultBundleMaxSizeMB
	}
	requireOverride := cfg.Data.Bundle.RequireOverrideAboveLimit
	if flags.ConfigPath == "" || cfg.Data.Bundle.MaxSizeMB == 0 {
		requireOverride = true
	}

	resolvedHome, err := home.Resolve(flags.SwitchboardHome)
	if err != nil {
		return ResolvedTrainConfig{}, err
	}

	return ResolvedTrainConfig{
		Provider:                  provider,
		Job:                       job,
		Packaging:                 resolvePackaging(cfg.Packaging),
		Routing:                   resolveRouting(cfg.Routing),
		Sizing:                    cfg.Sizing,
		Hardware:                  cfg.Hardware,
		Mock:                      cfg.Mock,
		GCP:                       resolveGCP(cfg.GCP),
		BundleMaxSizeBytes:        int64(maxSizeMB) * 1024 * 1024,
		RequireOverrideAboveLimit: requireOverride,
		AllowLargeDataBundle:      flags.AllowLargeDataBundle,
		SwitchboardHome:           resolvedHome,
	}, validateProviderConfig(provider, job, resolvePackaging(cfg.Packaging), resolveGCP(cfg.GCP))
}

func resolvePackaging(packaging PackagingConfig) PackagingConfig {
	if packaging.Context == "" {
		packaging.Context = "."
	}
	return packaging
}

func resolveJobPaths(job app.JobSpec, baseDir string) app.JobSpec {
	job.Script = resolveLocalPath(job.Script, baseDir)
	job.WorkDir = resolveLocalPath(job.WorkDir, baseDir)
	for i := range job.Data {
		if isPathLikeSource(job.Data[i].Source) {
			job.Data[i].Source = resolveLocalPath(job.Data[i].Source, baseDir)
		}
	}
	return job
}

func resolvePackagingPaths(packaging PackagingConfig, baseDir string) PackagingConfig {
	packaging = resolvePackaging(packaging)
	packaging.Context = resolveLocalPath(packaging.Context, baseDir)
	packaging.Dockerfile = resolveLocalPath(packaging.Dockerfile, baseDir)
	return packaging
}

func resolveLocalPath(path string, baseDir string) string {
	if path == "" || filepath.IsAbs(path) || baseDir == "" {
		return path
	}
	return filepath.Clean(filepath.Join(baseDir, path))
}

func isPathLikeSource(source string) bool {
	parsed, err := url.Parse(source)
	if err != nil {
		return true
	}
	return parsed.Scheme == "" || len(parsed.Scheme) == 1 && !strings.Contains(source, "://")
}

func resolveRouting(routing RoutingConfig) RoutingConfig {
	if routing.Objective == "" {
		routing.Objective = "min_cost"
	}
	if routing.MaxAttempts == 0 {
		routing.MaxAttempts = 2
	}
	return routing
}

func resolveGCP(gcp GCPConfig) GCPConfig {
	if gcp.Location == "" {
		gcp.Location = "us-central1"
	}
	if gcp.MachineType == "" {
		gcp.MachineType = "n1-standard-4"
	}
	if gcp.BootDiskType == "" {
		gcp.BootDiskType = "pd-ssd"
	}
	if gcp.BootDiskSizeGB == 0 {
		gcp.BootDiskSizeGB = 100
	}
	if gcp.PollIntervalSeconds == 0 {
		gcp.PollIntervalSeconds = 30
	}
	return gcp
}

func validateProviderConfig(provider string, job app.JobSpec, packaging PackagingConfig, gcp GCPConfig) error {
	if provider != "gcp" {
		return nil
	}
	if job.Image == "" && !canPackageForGCP(job, packaging, gcp) {
		return errors.New("gcp provider requires job.image or packaging config for job.script")
	}
	if gcp.ProjectID == "" {
		return errors.New("gcp.project_id is required")
	}
	if gcp.Location == "" {
		return errors.New("gcp.location is required")
	}
	if gcp.OutputURIPrefix == "" {
		return errors.New("gcp.output_uri_prefix is required")
	}
	return nil
}

func canPackageForGCP(job app.JobSpec, packaging PackagingConfig, gcp GCPConfig) bool {
	if job.Script == "" {
		return false
	}
	if packaging.Image != "" {
		return true
	}
	return gcp.ProjectID != "" && gcp.Location != "" && gcp.ArtifactRegistryRepository != ""
}

func LoadFile(path string) (Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func cfgProviderDefault(flagProvider string) string {
	if flagProvider != "" {
		return flagProvider
	}
	return ""
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
