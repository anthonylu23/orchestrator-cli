package mock

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
)

func TestSubmitEmitsEventsAndRetryableFailure(t *testing.T) {
	home := t.TempDir()
	paths := artifact.ForRun(home, "r_1")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("EnsureRun returned error: %v", err)
	}
	step := int64(8)
	provider := New(Config{
		Name:        "mock-lambda",
		HourlyCost:  1.10,
		FailureMode: FailureCapacity,
		Events: []app.Event{{
			Type:          app.EventTypeCheckpoint,
			Step:          &step,
			CheckpointURI: "file:///ckpt-8",
		}},
	}, &bytes.Buffer{}, &bytes.Buffer{})

	result, err := provider.Submit(context.Background(), app.SubmitRequest{
		RunID:     "r_1",
		AttemptID: "a_1",
		RunDir:    paths.RunDir,
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	var providerErr *app.ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("error = %#v", err)
	}
	if result.ProviderJobRef == "" {
		t.Fatal("expected provider ref")
	}
	content, readErr := os.ReadFile(paths.EventsJSONL)
	if readErr != nil {
		t.Fatalf("read events: %v", readErr)
	}
	if !strings.Contains(string(content), "file:///ckpt-8") {
		t.Fatalf("events = %s", string(content))
	}
}

func TestCapabilitiesIncludeConfiguredHardwareShapes(t *testing.T) {
	provider := New(Config{
		Name:       "mock-a100",
		HourlyCost: 4.25,
		HardwareShapes: []app.HardwareShape{{
			ID:                "mock-a100-1",
			Region:            "us-central1",
			MachineType:       "a2-highgpu-1g",
			GPUFamily:         "nvidia-a100",
			VRAMGBPerGPU:      40,
			TotalVRAMGB:       40,
			OnDemandHourlyUSD: 4.25,
			SupportsOnDemand:  true,
		}},
	}, nil, nil)

	capabilities, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities returned error: %v", err)
	}
	if len(capabilities.HardwareShapes) != 1 {
		t.Fatalf("hardware shapes = %#v", capabilities.HardwareShapes)
	}
	if capabilities.HardwareShapes[0].ID != "mock-a100-1" || capabilities.HardwareShapes[0].TotalVRAMGB != 40 {
		t.Fatalf("shape = %#v", capabilities.HardwareShapes[0])
	}
}
