# Switchboard CLI Architecture

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
  run state machine, attempt manager, routing, retry/failover, checkpoints, hardware selection

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
2. `Attempt`: provider name, provider job reference, attempt state, resume checkpoint URI/step, cost estimate, and exit reason.
3. `Event`: structured metric, checkpoint, status, and log payloads linked to a run and attempt.
4. `Summary`: derived final metrics, best metrics, runtime, checkpoint count, resume count, provider attempts, routing decision, and exit reason.

SQLite is the canonical state store for runs and attempts. Files under `~/.switchboard-cli/runs/<run-id>/` are durable user-facing artifacts. Local attempts also use a per-run workspace at `runs/<run-id>/workspace`.

## Data Preparation

Switchboard has a provider-independent data preparation layer between config loading and provider submit. It validates declared training/test data inputs, estimates bundled data size, creates a data manifest, and adds provider-ready data instructions to the runtime bundle.

Initial data input modes:

1. `bundle`: package a local file or directory with the job.
2. `uri`: resolve a remote `http://`, `https://`, `s3://`, or `gs://` source at runtime.

Training scripts should consume stable mounted paths, not provider-specific source locations. Local paths default to `bundle`; URI sources default to `uri`; omitted mounts default to `/workspace/data/<name>`. Local execution maps `/workspace/...` mounts into the run workspace before process start.

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

Private data access uses BYO environment authentication early. Switchboard may pass selected environment variables to data fetch steps, but raw credentials must be redacted from logs and omitted from SQLite, run metadata, `events.jsonl`, and `summary.json`. Redaction covers secret-like structured keys and known secret values from job, runtime, and inherited environment variables.

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
  HardwareShapes []HardwareShape
  SupportsSpot bool
  SupportsOnDemand bool
  SupportsDockerImage bool
  SupportsLocalScript bool
  SupportsDataBundle bool
  SupportedURISchemes []string
  SupportedCheckpointSchemes []string
  SupportsObjectStorePull bool
  MaxRuntimeHours *int
}
```

Provider capabilities include concrete hardware shapes for auto hardware routing: GPU model/family, VRAM, supported GPU counts, regions, availability hints, hourly prices, and launch constraints. The orchestration core owns hardware route policy.
Provider capabilities also declare supported checkpoint URI schemes. Failover routing rejects providers that cannot read the latest checkpoint URI.

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

The routing engine consumes job requirements, provider capabilities, support reports, cost estimates, provider health, and user policy. Core routing rejects providers whose capabilities cannot satisfy declared data inputs before cost ranking. Bundled inputs require `SupportsDataBundle`, and URI inputs require a matching supported URI scheme.

It produces a persisted routing decision:

```text
selected_provider
eligible providers with scores
rejected providers with reasons
objective
selection reason
```

Routing decisions are stored in SQLite with JSON snapshots of eligible and rejected providers. This makes the final provider selection explainable after the run completes.

For `provider=auto`, Switchboard filters incompatible providers, ranks eligible providers by objective, and selects the best candidate. When hardware routing is active, the decision includes the selected provider, selected shape, estimated VRAM, estimated runtime, estimated total cost, confidence, and rejected hardware reasons. On resumable provider failure, the core discovers the latest checkpoint, excludes providers according to failure policy, rejects providers that cannot read the checkpoint URI scheme, and submits a new attempt with resume metadata. Attempts persist resume checkpoint and estimate provenance for later inspection through summaries and state.

## Hardware Routing

Auto hardware routing extends provider routing without moving policy into adapters. The first version stays single-node and chooses one machine with 1-N GPUs.

Planned control levels:

1. `full_auto`: route provider and hardware.
2. `auto_provider`: route provider while honoring user-selected hardware requirements.
3. `manual`: use the specified provider and hardware.

The preferred full-auto objective is fastest compatible hardware within a max estimated run cost. Current inputs come from sizing hints and hardware constraints; probe artifacts are future work. If the estimator cannot produce a confident memory and cost estimate, routing rejects full-auto before submit with actionable missing-input or bounds reasons.

The planned routing flow is:

```text
job config
  -> data preparation
  -> sizing probe and user hints
  -> provider and hardware inventory
  -> memory/runtime/cost estimator
  -> persisted routing decision
  -> provider submit
```

Persisted decisions include selected provider, GPU model/family, GPU count, estimated VRAM, estimated runtime, estimated total cost, confidence, and rejected provider/hardware reasons.

## Checkpoints

Checkpoint discovery is provider-independent. Early milestones can start with local checkpoint discovery, then add shared backends later.

```go
type CheckpointResolver interface {
  Latest(ctx context.Context, runID string) (*CheckpointRef, error)
}
```

The current resolver reads structured checkpoint events from `events.jsonl` and returns the highest-step checkpoint. Real cross-provider resume requires shared storage reachable from both providers. Providers report supported checkpoint URI schemes, and routing rejects resume attempts where the next provider cannot read the latest checkpoint. Local/mock tests model the metadata flow; GCP is considered reachable only for `gs://` checkpoint URIs.

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

Local execution may run scripts directly. Cloud execution uses images. For GCP, the CLI can build and push a Docker image before submit when `job.script` and `packaging` are configured, then hand an image job to the provider adapter. For Lambda, the adapter launches an on-demand instance, uses cloud-init to run a prebuilt Docker image, and collects logs/events from remote `/tmp/switchboard` files over SSH.

The local provider materializes bundled data into the workspace and executes the script from that workspace. The mock provider simulates data preparation, URI fetch success, configured logs/events, and retryable failures. Future cloud providers can upload bundled data to staging storage or use provider-native transfer behavior.

The event ingestor accepts mixed stdout. Valid JSON events are persisted as structured records and raw logs remain available after redaction. Secret-like keys and known secret values are replaced with `[REDACTED]` before logs, events, summaries, or provider failure reasons are written.

Summaries keep `best_metrics` as a directional view. Metrics whose names contain `loss`, `error`, `err`, `perplexity`, `ppl`, `latency`, or `duration` are minimized; other metrics are maximized.

Artifacts:

```text
~/.switchboard-cli/switchboard.db
~/.switchboard-cli/runs/<run-id>/events.jsonl
~/.switchboard-cli/runs/<run-id>/summary.json
~/.switchboard-cli/runs/<run-id>/logs.txt
~/.switchboard-cli/runs/<run-id>/workspace/
```

Remote providers use remote-safe runtime paths rather than local artifact paths. GCP and Lambda jobs receive `/tmp/switchboard/checkpoints` and `/tmp/switchboard/events.jsonl`; provider adapters are responsible for mirroring remote logs and structured events back into local run artifacts.

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
6. Reporting concrete hardware shapes and supported checkpoint schemes.
7. Passing the shared provider contract tests, with explicit contract settings for intentional behavior differences such as artifact-backed logs.
8. Registering the adapter.

It should not require changes to CLI commands, the run state machine, retry/failover policy, telemetry parsing, or checkpoint resolution.
