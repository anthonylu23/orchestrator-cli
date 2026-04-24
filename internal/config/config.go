package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
	"gopkg.in/yaml.v3"
)

const DefaultBundleMaxSizeMB = 512

type Config struct {
	Job  JobConfig  `yaml:"job"`
	Data DataConfig `yaml:"data"`
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

type TrainFlags struct {
	ConfigPath           string
	Provider             string
	Script               string
	Args                 []string
	AllowLargeDataBundle bool
	OrchestratorHome     string
}

type ResolvedTrainConfig struct {
	Provider                  string
	Job                       app.JobSpec
	BundleMaxSizeBytes        int64
	RequireOverrideAboveLimit bool
	AllowLargeDataBundle      bool
	OrchestratorHome          string
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
	if job.Script == "" {
		return ResolvedTrainConfig{}, errors.New("script is required")
	}
	if job.Name == "" {
		job.Name = filepath.Base(job.Script)
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

	home := flags.OrchestratorHome
	if home == "" {
		home = os.Getenv("ORCHESTRATOR_CLI_HOME")
	}
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return ResolvedTrainConfig{}, fmt.Errorf("resolve user home: %w", err)
		}
		home = filepath.Join(userHome, ".orchestrator-cli")
	}

	return ResolvedTrainConfig{
		Provider:                  provider,
		Job:                       job,
		BundleMaxSizeBytes:        int64(maxSizeMB) * 1024 * 1024,
		RequireOverrideAboveLimit: requireOverride,
		AllowLargeDataBundle:      flags.AllowLargeDataBundle,
		OrchestratorHome:          home,
	}, nil
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
