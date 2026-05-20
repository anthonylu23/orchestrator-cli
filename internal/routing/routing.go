package routing

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/provider"
)

type Options struct {
	Mode                string
	Objective           string
	Exclude             map[string]bool
	BudgetMaxRunCostUSD float64
	Sizing              SizingHints
	Hardware            HardwareConstraints
	ManualHardware      ManualHardware
	ResumeFrom          *app.CheckpointRef
}

type SizingHints struct {
	ModelParametersB          float64
	ModelArtifactGB           float64
	BatchSize                 int
	GradientAccumulationSteps int
	Precision                 string
	Optimizer                 string
	ExpectedSteps             int
}

type HardwareConstraints struct {
	MaxGPUs            int
	AllowedGPUFamilies []string
	MinVRAMGBPerGPU    int
	Regions            []string
	AllowSpot          bool
	RequireOnDemand    bool
}

type ManualHardware struct {
	Provider    string
	ShapeID     string
	MachineType string
	Region      string
}

type providerCandidate struct {
	provider     string
	score        float64
	capabilities app.ProviderCapabilities
}

func Select(ctx context.Context, registry *provider.Registry, spec app.JobSpec, opts Options) (app.RoutingDecision, error) {
	objective := opts.Objective
	if objective == "" {
		objective = "min_cost"
	}
	var eligible []providerCandidate
	var rejected []app.RoutingCandidate
	for _, name := range registry.List() {
		nameValue := string(name)
		if opts.Exclude != nil && opts.Exclude[nameValue] {
			rejected = append(rejected, app.RoutingCandidate{Provider: nameValue, Reasons: []string{"excluded after retryable failure"}})
			continue
		}
		adapter, err := registry.Get(nameValue)
		if err != nil {
			rejected = append(rejected, app.RoutingCandidate{Provider: nameValue, Reasons: []string{err.Error()}})
			continue
		}
		capabilities, err := adapter.Capabilities(ctx)
		if err != nil {
			rejected = append(rejected, app.RoutingCandidate{Provider: nameValue, Reasons: []string{err.Error()}})
			continue
		}
		if reasons := capabilityRejections(spec, capabilities, opts.ResumeFrom); len(reasons) > 0 {
			rejected = append(rejected, app.RoutingCandidate{Provider: nameValue, Reasons: reasons})
			continue
		}
		report := adapter.ValidateJob(ctx, spec)
		if !report.Supported {
			rejected = append(rejected, app.RoutingCandidate{Provider: nameValue, Reasons: report.Reasons})
			continue
		}
		estimate, err := adapter.Estimate(ctx, spec)
		if err != nil {
			rejected = append(rejected, app.RoutingCandidate{Provider: nameValue, Reasons: []string{err.Error()}})
			continue
		}
		eligible = append(eligible, providerCandidate{provider: nameValue, score: estimate.HourlyUSD, capabilities: capabilities})
	}
	if len(eligible) == 0 {
		return app.RoutingDecision{Objective: objective, RejectedProviders: rejected}, fmt.Errorf("no eligible providers: %s", formatRejectedProviders(rejected))
	}
	if hardwareRoutingEnabled(opts) {
		return selectHardwareRoute(objective, eligible, rejected, opts)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if objective == "min_cost" {
			return eligible[i].score < eligible[j].score
		}
		return eligible[i].provider < eligible[j].provider
	})
	selected := eligible[0]
	return app.RoutingDecision{
		SelectedProvider:  selected.provider,
		Objective:         objective,
		SelectionReason:   fmt.Sprintf("selected %s by %s", selected.provider, objective),
		EligibleProviders: routingCandidates(eligible),
		RejectedProviders: rejected,
	}, nil
}

func hardwareRoutingEnabled(opts Options) bool {
	return opts.Mode == "full_auto" || opts.Mode == "auto_provider" || opts.Mode == "manual" || opts.Objective == "fastest_within_budget"
}

func selectHardwareRoute(objective string, providers []providerCandidate, rejectedProviders []app.RoutingCandidate, opts Options) (app.RoutingDecision, error) {
	if opts.Mode == "manual" {
		return selectManualHardware(objective, providers, rejectedProviders, opts)
	}
	requiredVRAM, confidence, err := estimateRequiredVRAM(opts)
	if err != nil && opts.Mode == "full_auto" {
		return app.RoutingDecision{Objective: objective, EligibleProviders: routingCandidates(providers), RejectedProviders: rejectedProviders, Confidence: "low"}, err
	}
	if err != nil {
		requiredVRAM = float64(opts.Hardware.MinVRAMGBPerGPU)
		confidence = "constraint"
	}
	var eligibleHardware []hardwareScoredCandidate
	var rejectedHardware []app.HardwareCandidate
	for _, providerCandidate := range providers {
		for _, shape := range providerCandidate.capabilities.HardwareShapes {
			if shape.Provider == "" {
				shape.Provider = providerCandidate.provider
			}
			reasons := hardwareRejections(shape, requiredVRAM, opts.Hardware)
			runtimeSeconds := estimateRuntimeSeconds(shape, opts.Sizing)
			totalCostUSD := estimateTotalCostUSD(shape, providerCandidate.score, runtimeSeconds)
			if opts.BudgetMaxRunCostUSD > 0 && totalCostUSD > opts.BudgetMaxRunCostUSD {
				reasons = append(reasons, fmt.Sprintf("estimated total cost $%.2f exceeds max budget $%.2f", totalCostUSD, opts.BudgetMaxRunCostUSD))
			}
			if len(reasons) > 0 {
				rejectedHardware = append(rejectedHardware, app.HardwareCandidate{Provider: providerCandidate.provider, ShapeID: shape.ID, Reasons: reasons})
				continue
			}
			eligibleHardware = append(eligibleHardware, hardwareScoredCandidate{
				provider:       providerCandidate.provider,
				shape:          shape,
				runtimeSeconds: runtimeSeconds,
				totalCostUSD:   totalCostUSD,
			})
		}
	}
	if len(eligibleHardware) == 0 {
		return app.RoutingDecision{
			Objective:         objective,
			EligibleProviders: routingCandidates(providers),
			RejectedProviders: rejectedProviders,
			RejectedHardware:  rejectedHardware,
			Confidence:        confidence,
		}, fmt.Errorf("no eligible hardware shapes: %s", formatRejectedHardware(rejectedHardware))
	}
	sortHardware(eligibleHardware, objective)
	selected := eligibleHardware[0]
	return app.RoutingDecision{
		SelectedProvider:        selected.provider,
		Objective:               objective,
		SelectionReason:         fmt.Sprintf("selected %s on %s by %s", selected.provider, selected.shape.ID, objective),
		EligibleProviders:       routingCandidates(providers),
		RejectedProviders:       rejectedProviders,
		SelectedHardware:        hardwareSelection(selected.provider, selected.shape),
		EligibleHardware:        hardwareCandidates(eligibleHardware),
		RejectedHardware:        rejectedHardware,
		EstimatedRequiredVRAMGB: floatPtr(requiredVRAM),
		EstimatedRuntimeSeconds: floatPtr(selected.runtimeSeconds),
		EstimatedTotalCostUSD:   floatPtr(selected.totalCostUSD),
		Confidence:              confidence,
	}, nil
}

func formatRejectedProviders(rejected []app.RoutingCandidate) string {
	if len(rejected) == 0 {
		return "no providers were registered"
	}
	parts := make([]string, 0, len(rejected))
	for _, candidate := range rejected {
		reason := strings.Join(candidate.Reasons, ", ")
		if reason == "" {
			reason = "no reason reported"
		}
		parts = append(parts, fmt.Sprintf("%s rejected: %s", candidate.Provider, reason))
	}
	return strings.Join(parts, "; ")
}

func formatRejectedHardware(rejected []app.HardwareCandidate) string {
	if len(rejected) == 0 {
		return "no hardware shapes were reported by eligible providers"
	}
	parts := make([]string, 0, len(rejected))
	for _, candidate := range rejected {
		reason := strings.Join(candidate.Reasons, ", ")
		if reason == "" {
			reason = "no reason reported"
		}
		parts = append(parts, fmt.Sprintf("%s/%s rejected: %s", candidate.Provider, candidate.ShapeID, reason))
	}
	return strings.Join(parts, "; ")
}

func selectManualHardware(objective string, providers []providerCandidate, rejectedProviders []app.RoutingCandidate, opts Options) (app.RoutingDecision, error) {
	var rejectedHardware []app.HardwareCandidate
	for _, candidate := range providers {
		if opts.ManualHardware.Provider != "" && candidate.provider != opts.ManualHardware.Provider {
			continue
		}
		for _, shape := range candidate.capabilities.HardwareShapes {
			if shape.Provider == "" {
				shape.Provider = candidate.provider
			}
			if opts.ManualHardware.ShapeID != "" && shape.ID != opts.ManualHardware.ShapeID {
				rejectedHardware = append(rejectedHardware, app.HardwareCandidate{Provider: candidate.provider, ShapeID: shape.ID, Reasons: []string{"manual shape_id did not match"}})
				continue
			}
			if opts.ManualHardware.MachineType != "" && shape.MachineType != opts.ManualHardware.MachineType {
				rejectedHardware = append(rejectedHardware, app.HardwareCandidate{Provider: candidate.provider, ShapeID: shape.ID, Reasons: []string{"manual machine_type did not match"}})
				continue
			}
			if opts.ManualHardware.Region != "" && shape.Region != opts.ManualHardware.Region {
				rejectedHardware = append(rejectedHardware, app.HardwareCandidate{Provider: candidate.provider, ShapeID: shape.ID, Reasons: []string{"manual region did not match"}})
				continue
			}
			runtimeSeconds := estimateRuntimeSeconds(shape, opts.Sizing)
			totalCostUSD := estimateTotalCostUSD(shape, candidate.score, runtimeSeconds)
			return app.RoutingDecision{
				SelectedProvider:        candidate.provider,
				Objective:               objective,
				SelectionReason:         fmt.Sprintf("selected %s on %s by manual hardware", candidate.provider, shape.ID),
				EligibleProviders:       routingCandidates(providers),
				RejectedProviders:       rejectedProviders,
				SelectedHardware:        hardwareSelection(candidate.provider, shape),
				RejectedHardware:        rejectedHardware,
				EstimatedRuntimeSeconds: floatPtr(runtimeSeconds),
				EstimatedTotalCostUSD:   floatPtr(totalCostUSD),
				Confidence:              "manual",
			}, nil
		}
	}
	return app.RoutingDecision{Objective: objective, EligibleProviders: routingCandidates(providers), RejectedProviders: rejectedProviders, RejectedHardware: rejectedHardware, Confidence: "manual"}, fmt.Errorf("manual hardware selection did not match any eligible provider shape")
}

type hardwareScoredCandidate struct {
	provider       string
	shape          app.HardwareShape
	runtimeSeconds float64
	totalCostUSD   float64
}

func sortHardware(candidates []hardwareScoredCandidate, objective string) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if objective == "fastest_within_budget" {
			if candidates[i].runtimeSeconds != candidates[j].runtimeSeconds {
				return candidates[i].runtimeSeconds < candidates[j].runtimeSeconds
			}
			if candidates[i].totalCostUSD != candidates[j].totalCostUSD {
				return candidates[i].totalCostUSD < candidates[j].totalCostUSD
			}
		} else if candidates[i].totalCostUSD != candidates[j].totalCostUSD {
			return candidates[i].totalCostUSD < candidates[j].totalCostUSD
		}
		if candidates[i].provider != candidates[j].provider {
			return candidates[i].provider < candidates[j].provider
		}
		return candidates[i].shape.ID < candidates[j].shape.ID
	})
}

func estimateRequiredVRAM(opts Options) (float64, string, error) {
	if opts.Sizing.ModelParametersB > 0 {
		bytesPerParam := precisionBytes(opts.Sizing.Precision)
		multiplier := optimizerMultiplier(opts.Sizing.Optimizer)
		activationGB := float64(maxInt(opts.Sizing.BatchSize, 1)) * 0.5
		return opts.Sizing.ModelParametersB*bytesPerParam*multiplier + activationGB, "hinted", nil
	}
	if opts.Sizing.ModelArtifactGB > 0 {
		return opts.Sizing.ModelArtifactGB * 2, "hinted", nil
	}
	if opts.Hardware.MinVRAMGBPerGPU > 0 {
		return float64(opts.Hardware.MinVRAMGBPerGPU), "constraint", nil
	}
	return 0, "low", fmt.Errorf("full_auto routing requires sizing hints or min_vram_gb_per_gpu")
}

func precisionBytes(precision string) float64 {
	switch strings.ToLower(precision) {
	case "fp32", "float32":
		return 4
	case "int8", "8bit", "quantized":
		return 1
	default:
		return 2
	}
}

func optimizerMultiplier(optimizer string) float64 {
	switch strings.ToLower(optimizer) {
	case "adam", "adamw":
		return 4
	case "sgd":
		return 2
	default:
		return 3
	}
}

func hardwareRejections(shape app.HardwareShape, requiredVRAM float64, constraints HardwareConstraints) []string {
	var reasons []string
	if shape.ID == "" {
		reasons = append(reasons, "hardware shape is missing id")
	}
	if requiredVRAM > 0 && float64(shape.TotalVRAMGB) < requiredVRAM {
		reasons = append(reasons, fmt.Sprintf("total VRAM %dGB is below required %.1fGB", shape.TotalVRAMGB, requiredVRAM))
	}
	if constraints.MaxGPUs > 0 && shape.AcceleratorCount > constraints.MaxGPUs {
		reasons = append(reasons, fmt.Sprintf("accelerator count %d exceeds max_gpus %d", shape.AcceleratorCount, constraints.MaxGPUs))
	}
	if constraints.MinVRAMGBPerGPU > 0 && shape.VRAMGBPerGPU < constraints.MinVRAMGBPerGPU {
		reasons = append(reasons, fmt.Sprintf("VRAM per GPU %dGB is below minimum %dGB", shape.VRAMGBPerGPU, constraints.MinVRAMGBPerGPU))
	}
	if len(constraints.AllowedGPUFamilies) > 0 && !containsFold(constraints.AllowedGPUFamilies, shape.GPUFamily) {
		reasons = append(reasons, fmt.Sprintf("gpu family %q is not allowed", shape.GPUFamily))
	}
	if len(constraints.Regions) > 0 && !containsFold(constraints.Regions, shape.Region) {
		reasons = append(reasons, fmt.Sprintf("region %q is not allowed", shape.Region))
	}
	if constraints.RequireOnDemand && !shape.SupportsOnDemand {
		reasons = append(reasons, "shape does not support on-demand execution")
	}
	if !constraints.AllowSpot && !shape.SupportsOnDemand && shape.SupportsSpot {
		reasons = append(reasons, "spot-only shape requires allow_spot")
	}
	if shape.OnDemandHourlyUSD <= 0 && shape.SpotHourlyUSD <= 0 {
		reasons = append(reasons, "hardware shape is missing hourly price")
	}
	switch shape.AvailabilityHint {
	case "no_quota", "unavailable":
		reason := shape.AvailabilityReason
		if reason == "" {
			reason = fmt.Sprintf("hardware shape availability is %s", shape.AvailabilityHint)
		}
		reasons = append(reasons, reason)
	}
	return reasons
}

func estimateRuntimeSeconds(shape app.HardwareShape, sizing SizingHints) float64 {
	accelerators := maxInt(shape.AcceleratorCount, 1)
	if sizing.ExpectedSteps > 0 {
		return math.Max(60, float64(sizing.ExpectedSteps*10)/float64(accelerators))
	}
	if shape.AcceleratorCount > 0 {
		return 3600 / float64(accelerators)
	}
	return 7200
}

func estimateTotalCostUSD(shape app.HardwareShape, providerHourly float64, runtimeSeconds float64) float64 {
	hourly := shape.OnDemandHourlyUSD
	if hourly == 0 {
		hourly = shape.SpotHourlyUSD
	}
	if hourly == 0 {
		hourly = providerHourly
	}
	return hourly * runtimeSeconds / 3600
}

func hardwareSelection(provider string, shape app.HardwareShape) *app.HardwareSelection {
	return &app.HardwareSelection{
		Provider:         provider,
		ShapeID:          shape.ID,
		Region:           shape.Region,
		MachineType:      shape.MachineType,
		AcceleratorType:  shape.AcceleratorType,
		AcceleratorCount: shape.AcceleratorCount,
		GPUFamily:        shape.GPUFamily,
		TotalVRAMGB:      shape.TotalVRAMGB,
		HourlyUSD:        selectedHourlyUSD(shape),
	}
}

func selectedHourlyUSD(shape app.HardwareShape) float64 {
	if shape.OnDemandHourlyUSD > 0 {
		return shape.OnDemandHourlyUSD
	}
	return shape.SpotHourlyUSD
}

func routingCandidates(in []providerCandidate) []app.RoutingCandidate {
	out := make([]app.RoutingCandidate, 0, len(in))
	for _, item := range in {
		out = append(out, app.RoutingCandidate{Provider: item.provider, Score: item.score})
	}
	return out
}

func hardwareCandidates(in []hardwareScoredCandidate) []app.HardwareCandidate {
	out := make([]app.HardwareCandidate, 0, len(in))
	for _, item := range in {
		out = append(out, app.HardwareCandidate{Provider: item.provider, ShapeID: item.shape.ID, Score: item.runtimeSeconds})
	}
	return out
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func floatPtr(value float64) *float64 {
	return &value
}

func capabilityRejections(spec app.JobSpec, capabilities app.ProviderCapabilities, resumeFrom *app.CheckpointRef) []string {
	var reasons []string
	supportedURISchemes := map[string]bool{}
	for _, scheme := range capabilities.SupportedURISchemes {
		supportedURISchemes[strings.ToLower(scheme)] = true
	}
	for _, input := range spec.Data {
		switch input.Mode {
		case app.DataInputModeBundle:
			if !capabilities.SupportsDataBundle {
				reasons = append(reasons, fmt.Sprintf("provider does not support bundled data input %q", input.Name))
			}
		case app.DataInputModeURI:
			parsed, err := url.Parse(input.Source)
			scheme := ""
			if err == nil {
				scheme = strings.ToLower(parsed.Scheme)
			}
			if scheme == "" || !supportedURISchemes[scheme] {
				reasons = append(reasons, fmt.Sprintf("provider does not support %q URI data input %q", scheme, input.Name))
			}
		}
	}
	if resumeFrom != nil {
		scheme := checkpointScheme(resumeFrom.URI)
		if !supportedCheckpointScheme(capabilities, scheme) {
			reasons = append(reasons, fmt.Sprintf("provider does not support %q checkpoint resume URI %q", scheme, resumeFrom.URI))
		}
	}
	return reasons
}

func supportedCheckpointScheme(capabilities app.ProviderCapabilities, scheme string) bool {
	for _, supported := range capabilities.SupportedCheckpointSchemes {
		if strings.EqualFold(supported, scheme) {
			return true
		}
	}
	return false
}

func checkpointScheme(uri string) string {
	parsed, err := url.Parse(uri)
	if err == nil && parsed.Scheme != "" {
		return strings.ToLower(parsed.Scheme)
	}
	return "file"
}
