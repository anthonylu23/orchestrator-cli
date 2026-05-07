# CloudTune Architecture

## Core Boundary

The orchestration core owns lifecycle, retries, routing, failover, state, telemetry, checkpoint policy, summaries, and artifact records. Provider adapters report capabilities and perform provider-specific operations. They do not own orchestration policy.

## Layers

```text
CLI
  -> config loader
  -> data preflight
  -> orchestration core
  -> routing engine
  -> provider registry
  -> provider adapter
  -> state + telemetry + artifacts
```

## Run Model

A run is the user-facing workload execution. An attempt is one provider execution inside that run.

```text
Run r_123: evaluation
  Attempt a_1: mock-lambda, capacity failure
  Attempt a_2: mock-gcp, resumed from checkpoint, succeeded
```

SQLite is the canonical state store. Files under `<home>/runs/<run-id>/` are durable user-facing artifacts.

## Workload Model

`workload` stores product metadata: workload type, model reference, dataset reference, tags, and metadata. `job` stores the executable surface. This keeps CloudTune-specific provenance separate from script execution mechanics.

## Provider Contract

Providers implement:

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

Resume is passed through `SubmitRequest.ResumeFrom` so the core can decide when to resume and providers only execute.

## Routing

For `provider=auto`, CloudTune filters incompatible providers, ranks eligible providers by objective, persists the decision, and excludes failed providers during retry. Current routing is simple cost-aware routing over local/mock providers. Real provider health, region, GPU shape, privacy, and quota inputs are future work.

## Telemetry

Scripts can emit structured JSON lines for metrics, checkpoints, and statuses. Plain logs are stored as logs. Secret-like values from job/runtime environment are redacted before persistence.

## Artifacts

Each run stores:

```text
events.jsonl
logs.txt
summary.json
workload.json
artifacts.json
outputs/
checkpoints/
workspace/
```

The artifact manifest lists standard run files and files produced under `outputs/` and `checkpoints/`.
