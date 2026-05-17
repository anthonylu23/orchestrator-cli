package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/home"
	"gopkg.in/yaml.v3"
)

const DefaultBundleMaxSizeMB = 512

type Config struct {
	Job     JobConfig     `yaml:"job"`
	Data    DataConfig    `yaml:"data"`
	Routing RoutingConfig `yaml:"routing"`
	Mock    MockConfig    `yaml:"mock"`
	GCP     GCPConfig     `yaml:"gcp"`
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

type BundleConfig struct {
	MaxSizeMB                 int  `yaml:"max_size_mb"`
	RequireOverrideAboveLimit bool `yaml:"require_override_above_limit"`
}

type RoutingConfig struct {
	Objective   string `yaml:"objective"`
	MaxAttempts int    `yaml:"max_attempts"`
}

type MockConfig struct {
	Providers []MockProviderConfig `yaml:"providers"`
}

type GCPConfig struct {
	ProjectID           string  `yaml:"project_id"`
	Location            string  `yaml:"location"`
	OutputURIPrefix     string  `yaml:"output_uri_prefix"`
	MachineType         string  `yaml:"machine_type"`
	AcceleratorType     string  `yaml:"accelerator_type"`
	AcceleratorCount    int32   `yaml:"accelerator_count"`
	BootDiskType        string  `yaml:"boot_disk_type"`
	BootDiskSizeGB      int32   `yaml:"boot_disk_size_gb"`
	ServiceAccount      string  `yaml:"service_account"`
	Network             string  `yaml:"network"`
	PollIntervalSeconds int     `yaml:"poll_interval_seconds"`
	EstimateHourlyUSD   float64 `yaml:"estimate_hourly_usd"`
}

type MockProviderConfig struct {
	Name        string            `yaml:"name"`
	HourlyCost  float64           `yaml:"hourly_cost"`
	FailureMode string            `yaml:"failure_mode"`
	Events      []MockEventConfig `yaml:"events"`
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
	Routing                   RoutingConfig
	Mock                      MockConfig
	GCP                       GCPConfig
	BundleMaxSizeBytes        int64
	RequireOverrideAboveLimit bool
	AllowLargeDataBundle      bool
	SwitchboardHome           string
}

func LoadTrain(flags TrainFlags) (ResolvedTrainConfig, error) {
	cfg := Config{}
	if flags.ConfigPath != "" {
		loaded, err := LoadFile(flags.ConfigPath)
		if err != nil {
			return ResolvedTrainConfig{}, err
		}
		cfg = loaded
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
	if flags.Script != "" {
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
		Routing:                   resolveRouting(cfg.Routing),
		Mock:                      cfg.Mock,
		GCP:                       resolveGCP(cfg.GCP),
		BundleMaxSizeBytes:        int64(maxSizeMB) * 1024 * 1024,
		RequireOverrideAboveLimit: requireOverride,
		AllowLargeDataBundle:      flags.AllowLargeDataBundle,
		SwitchboardHome:           resolvedHome,
	}, validateProviderConfig(provider, job, resolveGCP(cfg.GCP))
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

func validateProviderConfig(provider string, job app.JobSpec, gcp GCPConfig) error {
	if provider != "gcp" {
		return nil
	}
	if job.Image == "" {
		return errors.New("gcp provider requires job.image")
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
