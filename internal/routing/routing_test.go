package routing

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/provider"
	"github.com/anthonylu23/switchboard-cli/internal/provider/mock"
)

func TestSelectChoosesCheapestEligibleProvider(t *testing.T) {
	registry := provider.NewRegistry(
		mock.New(mock.Config{Name: "mock-expensive", HourlyCost: 3}, nil, nil),
		mock.New(mock.Config{Name: "mock-cheap", HourlyCost: 1}, nil, nil),
	)
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{Objective: "min_cost"})
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if decision.SelectedProvider != "mock-cheap" {
		t.Fatalf("selected = %q", decision.SelectedProvider)
	}
	if len(decision.EligibleProviders) != 2 {
		t.Fatalf("eligible = %#v", decision.EligibleProviders)
	}
}

func TestSelectRecordsRejectedProvider(t *testing.T) {
	registry := provider.NewRegistry(mock.New(mock.Config{Name: "mock-cheap", HourlyCost: 1}, nil, nil))
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{
		Objective: "min_cost",
		Exclude:   map[string]bool{"mock-cheap": true},
	})
	if err == nil {
		t.Fatal("expected no eligible providers")
	}
	if !containsString(err.Error(), "excluded after retryable failure") {
		t.Fatalf("error = %v", err)
	}
	if len(decision.RejectedProviders) != 1 {
		t.Fatalf("rejected = %#v", decision.RejectedProviders)
	}
}

func TestSelectRejectsProviderWithoutBundleSupport(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name:         "no-bundles",
		capabilities: app.ProviderCapabilities{SupportsLocalScript: true},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{
		Script: "train.py",
		Data: []app.DataInput{{
			Name:   "train",
			Source: "./train.csv",
			Mode:   app.DataInputModeBundle,
		}},
	}, Options{Objective: "min_cost"})
	if err == nil {
		t.Fatal("expected no eligible providers")
	}
	if len(decision.RejectedProviders) != 1 || decision.RejectedProviders[0].Reasons[0] == "" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSelectRejectsUnsupportedURIScheme(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name: "http-only",
		capabilities: app.ProviderCapabilities{
			SupportsLocalScript: true,
			SupportedURISchemes: []string{"http", "https"},
		},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{
		Script: "train.py",
		Data: []app.DataInput{{
			Name:   "train",
			Source: "s3://bucket/train.csv",
			Mode:   app.DataInputModeURI,
		}},
	}, Options{Objective: "min_cost"})
	if err == nil {
		t.Fatal("expected no eligible providers")
	}
	if len(decision.RejectedProviders) != 1 || decision.RejectedProviders[0].Reasons[0] != `provider does not support "s3" URI data input "train"` {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSelectRecordsSupportReportRejection(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name:         "rejecting",
		capabilities: app.ProviderCapabilities{SupportsLocalScript: true},
		validateSet:  true,
		validate: app.SupportReport{
			Supported: false,
			Reasons:   []string{"custom provider rejection"},
		},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{Objective: "min_cost"})
	if err == nil {
		t.Fatal("expected no eligible providers")
	}
	if len(decision.RejectedProviders) != 1 || decision.RejectedProviders[0].Reasons[0] != "custom provider rejection" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSelectAcceptsSupportedURIScheme(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name: "s3-provider",
		capabilities: app.ProviderCapabilities{
			SupportsLocalScript: true,
			SupportedURISchemes: []string{"s3"},
		},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{
		Script: "train.py",
		Data: []app.DataInput{{
			Name:   "train",
			Source: "s3://bucket/train.csv",
			Mode:   app.DataInputModeURI,
		}},
	}, Options{Objective: "min_cost"})
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if decision.SelectedProvider != "s3-provider" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSelectRejectsProviderWithoutCheckpointScheme(t *testing.T) {
	registry := provider.NewRegistry(
		staticProvider{
			name: "file-only",
			capabilities: app.ProviderCapabilities{
				SupportsLocalScript:        true,
				SupportedCheckpointSchemes: []string{"file"},
			},
		},
		staticProvider{
			name: "gcs-provider",
			capabilities: app.ProviderCapabilities{
				SupportsLocalScript:        true,
				SupportedCheckpointSchemes: []string{"gs"},
			},
		},
	)
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{
		Objective:  "min_cost",
		ResumeFrom: &app.CheckpointRef{URI: "gs://bucket/ckpt", Step: 10},
	})
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if decision.SelectedProvider != "gcs-provider" {
		t.Fatalf("decision = %#v", decision)
	}
	if len(decision.RejectedProviders) != 1 || !containsString(decision.RejectedProviders[0].Reasons[0], "checkpoint resume") {
		t.Fatalf("rejected = %#v", decision.RejectedProviders)
	}
}

func TestSelectRejectsAllProvidersForUnreachableCheckpoint(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name: "file-only",
		capabilities: app.ProviderCapabilities{
			SupportsLocalScript:        true,
			SupportedCheckpointSchemes: []string{"file"},
		},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{
		Objective:  "min_cost",
		ResumeFrom: &app.CheckpointRef{URI: "gs://bucket/ckpt", Step: 10},
	})
	if err == nil {
		t.Fatal("expected no eligible providers")
	}
	if len(decision.RejectedProviders) != 1 || !containsString(decision.RejectedProviders[0].Reasons[0], "checkpoint resume") {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSelectFullAutoChoosesFastestHardwareWithinBudget(t *testing.T) {
	registry := provider.NewRegistry(
		staticProvider{
			name: "slow-cheap",
			capabilities: app.ProviderCapabilities{SupportsLocalScript: true, HardwareShapes: []app.HardwareShape{{
				ID:                "slow-a100-1",
				Provider:          "slow-cheap",
				Region:            "us-central1",
				MachineType:       "a2-highgpu-1g",
				AcceleratorCount:  1,
				GPUFamily:         "nvidia-a100",
				VRAMGBPerGPU:      80,
				TotalVRAMGB:       80,
				OnDemandHourlyUSD: 1,
				SupportsOnDemand:  true,
			}}},
		},
		staticProvider{
			name: "fast",
			capabilities: app.ProviderCapabilities{SupportsLocalScript: true, HardwareShapes: []app.HardwareShape{{
				ID:                "fast-a100-4",
				Provider:          "fast",
				Region:            "us-central1",
				MachineType:       "a2-highgpu-4g",
				AcceleratorCount:  4,
				GPUFamily:         "nvidia-a100",
				VRAMGBPerGPU:      80,
				TotalVRAMGB:       320,
				OnDemandHourlyUSD: 8,
				SupportsOnDemand:  true,
			}}},
		},
	)
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{
		Mode:                "full_auto",
		Objective:           "fastest_within_budget",
		BudgetMaxRunCostUSD: 3,
		Sizing:              SizingHints{ModelParametersB: 7, Precision: "bf16", Optimizer: "adamw", BatchSize: 1},
		Hardware:            HardwareConstraints{AllowedGPUFamilies: []string{"nvidia-a100"}},
	})
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if decision.SelectedProvider != "fast" || decision.SelectedHardware == nil || decision.SelectedHardware.ShapeID != "fast-a100-4" {
		t.Fatalf("decision = %#v", decision)
	}
	if decision.EstimatedRequiredVRAMGB == nil || *decision.EstimatedRequiredVRAMGB <= 0 {
		t.Fatalf("required vram = %#v", decision.EstimatedRequiredVRAMGB)
	}
}

func TestSelectFullAutoUsesRequiredVRAMFromProbe(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name: "mock",
		capabilities: app.ProviderCapabilities{SupportsLocalScript: true, HardwareShapes: []app.HardwareShape{{
			ID:                "small",
			Provider:          "mock",
			Region:            "us-central1",
			MachineType:       "small",
			AcceleratorCount:  1,
			GPUFamily:         "nvidia-l4",
			VRAMGBPerGPU:      24,
			TotalVRAMGB:       24,
			OnDemandHourlyUSD: 1,
			SupportsOnDemand:  true,
		}, {
			ID:                "large",
			Provider:          "mock",
			Region:            "us-central1",
			MachineType:       "large",
			AcceleratorCount:  1,
			GPUFamily:         "nvidia-a100",
			VRAMGBPerGPU:      80,
			TotalVRAMGB:       80,
			OnDemandHourlyUSD: 2,
			SupportsOnDemand:  true,
		}}},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{
		Mode:      "full_auto",
		Objective: "fastest_within_budget",
		Sizing:    SizingHints{RequiredVRAMGB: 64},
	})
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if decision.SelectedHardware == nil || decision.SelectedHardware.ShapeID != "large" {
		t.Fatalf("decision = %#v", decision)
	}
	if decision.Confidence != "probe" || decision.EstimatedRequiredVRAMGB == nil || *decision.EstimatedRequiredVRAMGB != 64 {
		t.Fatalf("probe sizing not reflected: %#v", decision)
	}
}

func TestSelectFullAutoRejectsOverBudgetHardware(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name: "expensive",
		capabilities: app.ProviderCapabilities{SupportsLocalScript: true, HardwareShapes: []app.HardwareShape{{
			ID:                "expensive-h100-8",
			Provider:          "expensive",
			Region:            "us-central1",
			MachineType:       "a3-highgpu-8g",
			AcceleratorCount:  8,
			GPUFamily:         "nvidia-h100",
			VRAMGBPerGPU:      80,
			TotalVRAMGB:       640,
			OnDemandHourlyUSD: 80,
			SupportsOnDemand:  true,
		}}},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{
		Mode:                "full_auto",
		Objective:           "fastest_within_budget",
		BudgetMaxRunCostUSD: 1,
		Sizing:              SizingHints{ModelParametersB: 7, Precision: "bf16", Optimizer: "adamw"},
	})
	if err == nil {
		t.Fatal("expected no eligible hardware")
	}
	if !containsString(err.Error(), "exceeds max budget") {
		t.Fatalf("error = %v", err)
	}
	if len(decision.RejectedHardware) != 1 || decision.RejectedHardware[0].Reasons[0] == "" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSelectFullAutoRejectsKnownNoQuotaHardware(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name: "gcp",
		capabilities: app.ProviderCapabilities{SupportsDockerImage: true, HardwareShapes: []app.HardwareShape{{
			ID:                 "gcp-t4-no-quota",
			Provider:           "gcp",
			Region:             "us-central1",
			MachineType:        "n1-standard-8",
			AcceleratorType:    "NVIDIA_TESLA_T4",
			AcceleratorCount:   1,
			GPUFamily:          "nvidia-tesla-t4",
			VRAMGBPerGPU:       16,
			TotalVRAMGB:        16,
			OnDemandHourlyUSD:  1,
			SupportsOnDemand:   true,
			AvailabilityHint:   "no_quota",
			AvailabilityReason: "regional quota NVIDIA_T4_GPUS has 0 available, requires 1",
		}}},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{Image: "image"}, Options{
		Mode:      "full_auto",
		Objective: "fastest_within_budget",
		Sizing:    SizingHints{ModelParametersB: 1, Precision: "bf16", Optimizer: "sgd", BatchSize: 1},
	})
	if err == nil {
		t.Fatal("expected no eligible hardware")
	}
	if len(decision.RejectedHardware) != 1 || !containsString(decision.RejectedHardware[0].Reasons[0], "NVIDIA_T4_GPUS") {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSelectFullAutoRequiresSizingConfidence(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name: "mock",
		capabilities: app.ProviderCapabilities{SupportsLocalScript: true, HardwareShapes: []app.HardwareShape{{
			ID:                "mock-a100",
			Provider:          "mock",
			Region:            "us-central1",
			MachineType:       "a2",
			AcceleratorCount:  1,
			GPUFamily:         "nvidia-a100",
			VRAMGBPerGPU:      40,
			TotalVRAMGB:       40,
			OnDemandHourlyUSD: 4,
			SupportsOnDemand:  true,
		}}},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{Script: "train.py"}, Options{Mode: "full_auto", Objective: "fastest_within_budget"})
	if err == nil {
		t.Fatal("expected sizing confidence error")
	}
	if decision.Confidence != "low" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSelectManualHardware(t *testing.T) {
	registry := provider.NewRegistry(staticProvider{
		name: "gcp",
		capabilities: app.ProviderCapabilities{SupportsDockerImage: true, HardwareShapes: []app.HardwareShape{{
			ID:                "gcp-a100",
			Provider:          "gcp",
			Region:            "us-central1",
			MachineType:       "a2-highgpu-1g",
			AcceleratorCount:  1,
			GPUFamily:         "nvidia-a100",
			VRAMGBPerGPU:      40,
			TotalVRAMGB:       40,
			OnDemandHourlyUSD: 4,
			SupportsOnDemand:  true,
		}}},
	})
	decision, err := Select(context.Background(), registry, app.JobSpec{Image: "image"}, Options{
		Mode:           "manual",
		ManualHardware: ManualHardware{Provider: "gcp", ShapeID: "gcp-a100"},
	})
	if err != nil {
		t.Fatalf("Select returned error: %v", err)
	}
	if decision.SelectedProvider != "gcp" || decision.SelectedHardware == nil || decision.SelectedHardware.ShapeID != "gcp-a100" || decision.Confidence != "manual" {
		t.Fatalf("decision = %#v", decision)
	}
}

type staticProvider struct {
	name         string
	capabilities app.ProviderCapabilities
	validateSet  bool
	validate     app.SupportReport
}

func (p staticProvider) Name() app.ProviderName {
	return app.ProviderName(p.name)
}

func (p staticProvider) ValidateAuth(ctx context.Context) error {
	return nil
}

func (p staticProvider) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	return p.capabilities, nil
}

func (p staticProvider) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	if p.validateSet {
		return p.validate
	}
	return app.SupportReport{Supported: true}
}

func (p staticProvider) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	return app.CostEstimate{HourlyUSD: 1, Currency: "USD"}, nil
}

func (p staticProvider) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
	return app.SubmitResult{}, fmt.Errorf("not implemented")
}

func (p staticProvider) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	return app.ProviderJobStatus{}, fmt.Errorf("not implemented")
}

func (p staticProvider) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, fmt.Errorf("not implemented")
}

func (p staticProvider) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	return fmt.Errorf("not implemented")
}

func containsString(haystack string, needle string) bool {
	return strings.Contains(haystack, needle)
}
