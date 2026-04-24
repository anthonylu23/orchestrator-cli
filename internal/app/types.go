package app

import (
	"context"
	"time"
)

type ProviderName string

const (
	ProviderLocal ProviderName = "local"
	ProviderAuto  ProviderName = "auto"
)

type DataInputMode string

const (
	DataInputModeBundle DataInputMode = "bundle"
	DataInputModeURI    DataInputMode = "uri"
)

type DataInput struct {
	Name   string        `json:"name" yaml:"name"`
	Source string        `json:"source" yaml:"source"`
	Mount  string        `json:"mount" yaml:"mount"`
	Mode   DataInputMode `json:"mode" yaml:"mode"`
}

type DataManifest struct {
	Inputs                       []DataInput `json:"inputs"`
	BundleSizeBytes              int64       `json:"bundle_size_bytes"`
	RequiresLargeBundleOverride  bool        `json:"requires_large_bundle_override"`
	BundleSizeLimitBytes         int64       `json:"bundle_size_limit_bytes"`
	LargeBundleOverridePermitted bool        `json:"large_bundle_override_permitted"`
}

type JobSpec struct {
	Name    string            `json:"name" yaml:"name"`
	Script  string            `json:"script" yaml:"script"`
	Args    []string          `json:"args" yaml:"args"`
	Env     map[string]string `json:"env" yaml:"env"`
	Data    []DataInput       `json:"data" yaml:"data"`
	WorkDir string            `json:"work_dir" yaml:"work_dir"`
}

type RunState string

const (
	RunStateRunning   RunState = "running"
	RunStateSucceeded RunState = "succeeded"
	RunStateFailed    RunState = "failed"
	RunStateCanceled  RunState = "canceled"
)

type AttemptState string

const (
	AttemptStateRunning   AttemptState = "running"
	AttemptStateSucceeded AttemptState = "succeeded"
	AttemptStateFailed    AttemptState = "failed"
	AttemptStateCanceled  AttemptState = "canceled"
)

type Run struct {
	ID        string    `json:"id"`
	JobName   string    `json:"job_name"`
	Script    string    `json:"script"`
	Provider  string    `json:"provider"`
	State     RunState  `json:"state"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	ExitCode  int       `json:"exit_code"`
	Error     string    `json:"error,omitempty"`
}

type Attempt struct {
	ID          string       `json:"id"`
	RunID       string       `json:"run_id"`
	Provider    string       `json:"provider"`
	State       AttemptState `json:"state"`
	StartedAt   time.Time    `json:"started_at"`
	EndedAt     time.Time    `json:"ended_at,omitempty"`
	ExitCode    int          `json:"exit_code"`
	ExitReason  string       `json:"exit_reason,omitempty"`
	ProviderRef string       `json:"provider_ref,omitempty"`
}

type EventType string

const (
	EventTypeMetric     EventType = "metric"
	EventTypeCheckpoint EventType = "checkpoint"
	EventTypeStatus     EventType = "status"
	EventTypeLog        EventType = "log"
)

type Event struct {
	Type          EventType              `json:"type"`
	RunID         string                 `json:"run_id,omitempty"`
	AttemptID     string                 `json:"attempt_id,omitempty"`
	Timestamp     time.Time              `json:"ts,omitempty"`
	Step          *int64                 `json:"step,omitempty"`
	Epoch         *int64                 `json:"epoch,omitempty"`
	Split         string                 `json:"split,omitempty"`
	State         string                 `json:"state,omitempty"`
	CheckpointURI string                 `json:"checkpoint_uri,omitempty"`
	Message       string                 `json:"message,omitempty"`
	Metrics       map[string]float64     `json:"metrics,omitempty"`
	Fields        map[string]interface{} `json:"fields,omitempty"`
}

type Summary struct {
	RunID            string             `json:"run_id"`
	State            RunState           `json:"state"`
	FinalMetrics     map[string]float64 `json:"final_metrics,omitempty"`
	BestMetrics      map[string]float64 `json:"best_metrics,omitempty"`
	BestStep         *int64             `json:"best_step,omitempty"`
	RuntimeSeconds   float64            `json:"runtime_sec"`
	ProviderAttempts []Attempt          `json:"provider_attempts"`
	CheckpointCount  int                `json:"checkpoint_count"`
	ResumeCount      int                `json:"resume_count"`
	ExitReason       string             `json:"exit_reason"`
}

type RoutingCandidate struct {
	Provider string   `json:"provider"`
	Score    float64  `json:"score,omitempty"`
	Reasons  []string `json:"reasons,omitempty"`
}

type RoutingDecision struct {
	RunID             string             `json:"run_id"`
	SelectedProvider  string             `json:"selected_provider"`
	Objective         string             `json:"objective"`
	SelectionReason   string             `json:"selection_reason"`
	EligibleProviders []RoutingCandidate `json:"eligible_providers,omitempty"`
	RejectedProviders []RoutingCandidate `json:"rejected_providers,omitempty"`
}

type ProviderCapabilities struct {
	GPUFamilies             []string `json:"gpu_families"`
	Regions                 []string `json:"regions"`
	SupportsSpot            bool     `json:"supports_spot"`
	SupportsOnDemand        bool     `json:"supports_on_demand"`
	SupportsDockerImage     bool     `json:"supports_docker_image"`
	SupportsLocalScript     bool     `json:"supports_local_script"`
	SupportsDataBundle      bool     `json:"supports_data_bundle"`
	SupportedURISchemes     []string `json:"supported_uri_schemes"`
	SupportsObjectStorePull bool     `json:"supports_object_store_pull"`
	MaxRuntimeHours         *int     `json:"max_runtime_hours,omitempty"`
}

type ProviderErrorKind string

const (
	ProviderErrorAuth        ProviderErrorKind = "auth_error"
	ProviderErrorCapacity    ProviderErrorKind = "capacity_error"
	ProviderErrorQuota       ProviderErrorKind = "quota_error"
	ProviderErrorInvalidSpec ProviderErrorKind = "invalid_spec_error"
	ProviderErrorNetwork     ProviderErrorKind = "network_error"
	ProviderErrorInternal    ProviderErrorKind = "provider_internal_error"
	ProviderErrorRuntime     ProviderErrorKind = "runtime_error"
	ProviderErrorUnknown     ProviderErrorKind = "unknown_provider_error"
)

type ProviderError struct {
	Kind    ProviderErrorKind
	Message string
	Err     error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Kind)
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ProviderError) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case ProviderErrorCapacity, ProviderErrorNetwork, ProviderErrorInternal:
		return true
	default:
		return false
	}
}

type SupportReport struct {
	Supported bool     `json:"supported"`
	Reasons   []string `json:"reasons,omitempty"`
}

type CostEstimate struct {
	HourlyUSD float64 `json:"hourly_usd"`
	Currency  string  `json:"currency"`
}

type CheckpointRef struct {
	URI  string `json:"uri"`
	Step int64  `json:"step"`
}

type SubmitRequest struct {
	JobSpec    JobSpec
	RunID      string
	AttemptID  string
	ResumeFrom *CheckpointRef
	RuntimeEnv map[string]string
	RunDir     string
	OnStarted  func(ref ProviderJobRef) error
}

type SubmitResult struct {
	ProviderJobRef string
	ExitCode       int
	ExitReason     string
}

type ProviderJobRef struct {
	ID string
}

type ProviderJobStatus struct {
	State AttemptState
}

type LogStreamRequest struct {
	Ref ProviderJobRef
}

type LogStream interface {
	Next(ctx context.Context) (string, error)
	Close() error
}

type ProviderAdapter interface {
	Name() ProviderName
	ValidateAuth(ctx context.Context) error
	Capabilities(ctx context.Context) (ProviderCapabilities, error)
	ValidateJob(ctx context.Context, spec JobSpec) SupportReport
	Estimate(ctx context.Context, spec JobSpec) (CostEstimate, error)
	Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error)
	GetStatus(ctx context.Context, ref ProviderJobRef) (ProviderJobStatus, error)
	StreamLogs(ctx context.Context, req LogStreamRequest) (LogStream, error)
	Cancel(ctx context.Context, ref ProviderJobRef) error
}
