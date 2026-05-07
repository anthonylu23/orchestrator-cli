package home

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	CloudTuneEnvName = "CLOUDTUNE_HOME"
	EnvName          = "SWITCHBOARD_CLI_HOME"
	LegacyEnvName    = "ORCHESTRATOR_CLI_HOME"
	DirName          = ".switchboard-cli"
	LegacyDirName    = ".orchestrator-cli"
)

func Resolve(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if env := os.Getenv(CloudTuneEnvName); env != "" {
		return env, nil
	}
	if env := os.Getenv(EnvName); env != "" {
		return env, nil
	}
	if env := os.Getenv(LegacyEnvName); env != "" {
		return env, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	legacyHome := filepath.Join(userHome, LegacyDirName)
	if _, err := os.Stat(legacyHome); err == nil {
		return legacyHome, nil
	}
	return filepath.Join(userHome, DirName), nil
}
