package sizing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/anthonylu23/switchboard-cli/internal/config"
)

type Profile struct {
	RequiredVRAMGB            float64 `json:"required_vram_gb"`
	PeakVRAMGB                float64 `json:"peak_vram_gb"`
	ModelParametersB          float64 `json:"model_parameters_b"`
	ModelArtifactGB           float64 `json:"model_artifact_gb"`
	BatchSize                 int     `json:"batch_size"`
	GradientAccumulationSteps int     `json:"gradient_accumulation_steps"`
	Precision                 string  `json:"precision"`
	Optimizer                 string  `json:"optimizer"`
	ExpectedSteps             int     `json:"expected_steps"`
}

func RunProbe(ctx context.Context, probe config.SizingProbeConfig, stdout io.Writer, stderr io.Writer) (Profile, error) {
	if probe.Output == "" {
		return Profile{}, fmt.Errorf("sizing.probe.output is required when sizing.probe.command is set")
	}
	if len(probe.Command) > 0 {
		cmd := exec.CommandContext(ctx, probe.Command[0], probe.Command[1:]...)
		cmd.Stdout = stdoutOrDiscard(stdout)
		cmd.Stderr = stdoutOrDiscard(stderr)
		if err := cmd.Run(); err != nil {
			return Profile{}, fmt.Errorf("sizing probe command failed: %w", err)
		}
	}
	content, err := os.ReadFile(probe.Output)
	if err != nil {
		return Profile{}, fmt.Errorf("read sizing probe output %q: %w", probe.Output, err)
	}
	var profile Profile
	if err := json.Unmarshal(content, &profile); err != nil {
		return Profile{}, fmt.Errorf("parse sizing probe output %q: %w", probe.Output, err)
	}
	return profile, nil
}

func ApplyProfile(hints config.SizingHintsConfig, profile Profile) config.SizingHintsConfig {
	if profile.RequiredVRAMGB > 0 {
		hints.RequiredVRAMGB = profile.RequiredVRAMGB
	} else if profile.PeakVRAMGB > 0 {
		hints.RequiredVRAMGB = profile.PeakVRAMGB
	}
	if profile.ModelParametersB > 0 {
		hints.ModelParametersB = profile.ModelParametersB
	}
	if profile.ModelArtifactGB > 0 {
		hints.ModelArtifactGB = profile.ModelArtifactGB
	}
	if profile.BatchSize > 0 {
		hints.BatchSize = profile.BatchSize
	}
	if profile.GradientAccumulationSteps > 0 {
		hints.GradientAccumulationSteps = profile.GradientAccumulationSteps
	}
	if profile.Precision != "" {
		hints.Precision = profile.Precision
	}
	if profile.Optimizer != "" {
		hints.Optimizer = profile.Optimizer
	}
	if profile.ExpectedSteps > 0 {
		hints.ExpectedSteps = profile.ExpectedSteps
	}
	return hints
}

func stdoutOrDiscard(w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return io.Discard
}
