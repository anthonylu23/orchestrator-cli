package config

import (
	"crypto/sha256"
	"encoding/hex"
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
	Workload app.WorkloadSpec `yaml:"workload"`
	Job      JobConfig        `yaml:"job"`
	Data     DataConfig       `yaml:"data"`
	Routing  RoutingConfig    `yaml:"routing"`
	Mock     MockConfig       `yaml:"mock"`
	Outputs  app.OutputSpec   `yaml:"outputs"`
}

type JobConfig struct {
	Name    string            `yaml:"name"`
	Script  string            `yaml:"script"`
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
	Provider    string `yaml:"provider"`
}

type MockConfig struct {
	Providers []MockProviderConfig `yaml:"providers"`
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
	ConfigPath                string
	ConfigHash                string
	BundleMaxSizeBytes        int64
	RequireOverrideAboveLimit bool
	AllowLargeDataBundle      bool
	SwitchboardHome           string
}

func LoadTrain(flags TrainFlags) (ResolvedTrainConfig, error) {
	cfg := Config{}
	configPath := ""
	configHash := ""
	if flags.ConfigPath != "" {
		loaded, absPath, hash, err := LoadFileWithMetadata(flags.ConfigPath)
		if err != nil {
			return ResolvedTrainConfig{}, err
		}
		cfg = loaded
		configPath = absPath
		configHash = hash
	}

	provider := cfgProviderDefault(flags.Provider)

	job := app.JobSpec{
		Name:     cfg.Job.Name,
		Script:   cfg.Job.Script,
		Args:     append([]string(nil), cfg.Job.Args...),
		Env:      cloneMap(cfg.Job.Env),
		Data:     append([]app.DataInput(nil), cfg.Data.Inputs...),
		WorkDir:  cfg.Job.WorkDir,
		Workload: normalizeWorkload(cfg.Workload),
		Outputs:  cfg.Outputs,
	}
	if flags.Script != "" {
		job.Script = flags.Script
	}
	if len(flags.Args) > 0 {
		job.Args = append([]string(nil), flags.Args...)
	}
	if job.Script == "" {
		return ResolvedTrainConfig{}, errors.New("script is required")
	}
	if job.Name == "" {
		if job.Workload.Name != "" {
			job.Name = job.Workload.Name
		} else {
			job.Name = filepath.Base(job.Script)
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

	routing := resolveRouting(cfg.Routing)
	if provider == "" {
		provider = routing.Provider
	}
	if provider == "" {
		provider = string(app.ProviderLocal)
	}

	return ResolvedTrainConfig{
		Provider:                  provider,
		Job:                       job,
		Routing:                   routing,
		Mock:                      cfg.Mock,
		ConfigPath:                configPath,
		ConfigHash:                configHash,
		BundleMaxSizeBytes:        int64(maxSizeMB) * 1024 * 1024,
		RequireOverrideAboveLimit: requireOverride,
		AllowLargeDataBundle:      flags.AllowLargeDataBundle,
		SwitchboardHome:           resolvedHome,
	}, nil
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

func normalizeWorkload(workload app.WorkloadSpec) app.WorkloadSpec {
	if workload.Type == "" {
		workload.Type = app.WorkloadTypeTraining
	}
	if workload.Dataset.Path != "" && !filepath.IsAbs(workload.Dataset.Path) {
		if abs, err := filepath.Abs(workload.Dataset.Path); err == nil {
			workload.Dataset.Path = abs
		}
	}
	return workload
}

func LoadFile(path string) (Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return parseConfig(content)
}

func LoadFileWithMetadata(path string) (Config, string, string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, "", "", fmt.Errorf("resolve config path: %w", err)
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return Config{}, "", "", fmt.Errorf("read config: %w", err)
	}
	cfg, err := parseConfig(content)
	if err != nil {
		return Config{}, "", "", err
	}
	return cfg, absPath, hashBytes(content), nil
}

func parseConfig(content []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
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
