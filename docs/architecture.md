# Orchestrator CLI Architecture

## Core Principle

The orchestration core owns lifecycle, retries, routing, failover, state, telemetry, and resume policy. Provider adapters report facts and perform provider-specific operations; they do not decide what happens next.

This boundary is the main extensibility mechanism. Adding a provider should not require changes to the run state machine, CLI command behavior, telemetry parser, checkpoint resolver, or retry policy.

## System Layers

```text
CLI
  parses commands, flags, and config

Application Services
  train, status, logs, resume, cancel, providers

Data Preparation
  validate inputs, estimate bundle size, build data manifest, prepare mounts

Orchestration Core
  run state machine, attempt manager, routing, retry/failover, checkpoints

Provider Layer
  provider registry, adapter contract, capabilities, normalized errors

Persistence and Artifacts
  SQLite run state, events.jsonl, summary.json, logs
```

## Run Model

A run is the user-facing training job. An attempt is one execution of that run on one provider.

```text
Run r_123
  Attempt a_1: mock-lambda, failed_capacity
  Attempt a_2: mock-gcp, resumed_from ckpt_800, completed
```

Core entities:

1. `Run`: job spec, desired state, current state, timestamps, and final outcome.
2. `Attempt`: provider name, provider job reference, attempt state, resume checkpoint, cost estimate, and exit reason.
3. `Event`: structured metric, checkpoint, status, and log payloads linked to a run and attempt.
4. `Summary`: derived final metrics, best metrics, runtime, checkpoint count, resume count, provider attempts, and exit reason.

SQLite is the canonical state store for runs and attempts. Files under `~/.orchestrator-cli/runs/<run-id>/` are durable user-facing artifacts.

## Data Preparation

Orchestrator has a provider-independent data preparation layer between config loading and provider submit. It validates declared training/test data inputs, estimates bundled data size, creates a data manifest, and adds provider-ready data instructions to the runtime bundle.

Initial data input modes:

1. `bundle`: package a local file or directory with the job.
2. `uri`: resolve a remote `http://`, `https://`, `s3://`, or `gs://` source at runtime.

Training scripts should consume stable mounted paths, not provider-specific source locations. Local paths default to `bundle`; URI sources default to `uri`; omitted mounts default to `/workspace/data/<name>`.

```go
type DataInput struct {
  Name string
  Source string
  Mount string
  Mode DataInputMode
}

type DataManifest struct {
  Inputs []DataInput
  BundleSizeBytes int64
  RequiresLargeBundleOverride bool
}
```

Bundled local data is guarded by a configured size limit. If the bundle exceeds the limit, preflight fails unless the user passes an explicit override such as `--allow-large-data-bundle`.

Private data access uses BYO environment authentication early. Orchestrator may pass selected environment variables to data fetch steps, but raw credentials must be redacted from logs and omitted from SQLite, run metadata, `events.jsonl`, and `summary.json`.

## Provider Adapter Contract

Providers implement a small interface and normalize their own API behavior before returning it to the core.

```go
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
```

Resume is expressed through `SubmitRequest.ResumeFrom`, not through a provider-specific resume method.

```go
type SubmitRequest struct {
  JobSpec JobSpec
  RunID string
  AttemptID string
  ResumeFrom *CheckpointRef
  RuntimeEnv map[string]string
}
```

## Provider Capabilities and Errors

Capabilities should be explicit and provider-independent so routing can reject providers before submit.

```go
type ProviderCapabilities struct {
  GPUFamilies []GPUFamily
  Regions []Region
  SupportsSpot bool
  SupportsOnDemand bool
  SupportsDockerImage bool
  SupportsLocalScript bool
  SupportsDataBundle bool
  SupportedURISchemes []string
  SupportsObjectStorePull bool
  MaxRuntimeHours *int
}
```

Adapters translate raw provider failures into normalized errors:

```go
type ProviderErrorKind string

const (
  AuthError ProviderErrorKind = "auth_error"
  CapacityError ProviderErrorKind = "capacity_error"
  QuotaError ProviderErrorKind = "quota_error"
  InvalidSpecError ProviderErrorKind = "invalid_spec_error"
  NetworkError ProviderErrorKind = "network_error"
  ProviderInternalError ProviderErrorKind = "provider_internal_error"
  RuntimeError ProviderErrorKind = "runtime_error"
  UnknownProviderError ProviderErrorKind = "unknown_provider_error"
)
```

The core uses these categories to decide whether a failure is retryable, resumable, or terminal.

## Routing and Failover

The routing engine consumes job requirements, provider capabilities, support reports, cost estimates, provider health, and user policy.

It produces a persisted routing decision:

```text
selected_provider
eligible providers with scores
rejected providers with reasons
objective
selection reason
```

For `provider=auto`, Orchestrator filters incompatible providers, ranks eligible providers by objective, and selects the best candidate. On resumable provider failure, the core discovers the latest checkpoint, excludes providers according to failure policy, and submits a new attempt with resume metadata.

## Checkpoints

Checkpoint discovery is provider-independent. Early milestones can start with local checkpoint discovery, then add shared backends later.

```go
type CheckpointResolver interface {
  Latest(ctx context.Context, runID string) (*CheckpointRef, error)
}
```

Real cross-provider resume requires shared storage reachable from both providers. Local/mock tests should model the behavior before adding GCS or S3.

## Runtime and Telemetry

The runtime layer converts a job spec into a provider-ready bundle:

```go
type RuntimeBundle struct {
  Image string
  Command []string
  Env map[string]string
  Mounts []Mount
  DataInputs []DataInput
}
```

Early local execution may run scripts directly. Cloud execution may initially require explicit images rather than automatic packaging.

The local provider materializes bundled data into the workspace. The mock provider simulates data preparation, URI fetch success, and URI fetch failure. Future cloud providers can upload bundled data to staging storage or use provider-native transfer behavior.

The event ingestor accepts mixed stdout. Valid JSON events are persisted as structured records and raw logs remain available.

Artifacts:

```text
~/.orchestrator-cli/orchestrator.db
~/.orchestrator-cli/runs/<run-id>/events.jsonl
~/.orchestrator-cli/runs/<run-id>/summary.json
~/.orchestrator-cli/runs/<run-id>/logs.txt
```

## Data Failure Behavior

Data preparation failures should happen before training starts whenever possible.

1. Missing local path: config/user input error.
2. Unsupported URI scheme: config/user input error.
3. Oversized bundle without override: config/user input error with guidance to use URI/object storage or pass the explicit override.
4. Remote fetch auth failure: data preparation failure with secret redaction.
5. Remote fetch unavailable: retryable or terminal based on normalized error classification.

Routing should reject providers whose capabilities cannot satisfy declared data inputs.

## Adding a Provider

Adding a provider should require:

1. Implementing `ProviderAdapter`.
2. Loading provider-specific authentication.
3. Reporting capabilities.
4. Mapping raw API errors into normalized provider errors.
5. Supporting or explicitly rejecting bundled data and URI schemes.
6. Passing adapter contract tests.
7. Registering the adapter.

It should not require changes to CLI commands, the run state machine, retry/failover policy, telemetry parsing, or checkpoint resolution.
