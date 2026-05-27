package sizing

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/config"
)

func TestRunProbeParsesOutputAndAppliesProfile(t *testing.T) {
	output := filepath.Join(t.TempDir(), "switchboard-sizing.json")
	profile, err := RunProbe(context.Background(), config.SizingProbeConfig{
		Command: []string{"sh", "-c", "printf '%s' '{\"required_vram_gb\":24,\"batch_size\":8,\"precision\":\"bf16\",\"expected_steps\":120}' > \"$1\"", "sh", output},
		Output:  output,
	}, nil, nil)
	if err != nil {
		t.Fatalf("RunProbe returned error: %v", err)
	}
	hints := ApplyProfile(config.SizingHintsConfig{ModelParametersB: 7}, profile)
	if hints.RequiredVRAMGB != 24 || hints.BatchSize != 8 || hints.Precision != "bf16" || hints.ExpectedSteps != 120 || hints.ModelParametersB != 7 {
		t.Fatalf("hints = %#v", hints)
	}
}

func TestRunProbeReadsExistingOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "switchboard-sizing.json")
	if err := os.WriteFile(output, []byte(`{"peak_vram_gb":16}`), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}
	profile, err := RunProbe(context.Background(), config.SizingProbeConfig{Output: output}, nil, nil)
	if err != nil {
		t.Fatalf("RunProbe returned error: %v", err)
	}
	hints := ApplyProfile(config.SizingHintsConfig{}, profile)
	if hints.RequiredVRAMGB != 16 {
		t.Fatalf("hints = %#v", hints)
	}
}
