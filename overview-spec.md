# Caboose CLI - Project Overview and Specs

## Project Overview

### Vision
Caboose CLI is a unified command-line orchestration layer for ML and deep learning training across cloud GPU providers.

### Core Promise
Run training jobs on multiple providers with one interface, without learning each provider stack or managing provider-specific SSH workflows.

### Target Users
1. Primary (now): Indie researchers.
2. Secondary (later): AI startup engineering teams.

### Product Wedge
"Given my script and hardware constraints, run on the cheapest available compatible provider and resume if a provider fails."

### Differentiation
1. Cost-aware routing with user-fixed hardware constraints.
2. Cross-provider failover resume from checkpoints.
3. Agent-friendly automation contract (JSON events + stable exit codes).
4. Minimal default config with optional advanced YAML settings.

### Scope (v1)
1. Providers: GCP, Lambda, Hyperbolic.
2. Workload: Custom script training packaged into containers.
3. Data ingest: Local files, HTTP pull, `s3://` and `gs://` URIs.
4. Checkpointing: Configurable backends (`local`, `gcs`, `s3`).
5. Parallel execution: Independent fan-out jobs/sweeps across providers.
6. Runtime model: Local orchestrator CLI with BYO provider credentials.

### Non-Goals (v1)
1. Unified billing or single Caboose API key.
2. Enterprise governance/compliance stack.
3. Distributed single-job multi-cloud training cluster.
4. Full ETL/data engineering platform.

## Technical Specs

### CLI Surface
1. `caboose train --script train.py --gpu "A100:1" --provider auto`
2. `caboose train --config caboose.yaml`
3. `caboose status <run-id> [--json]`
4. `caboose logs <run-id> [--follow] [--json]`
5. `caboose attach <run-id>`
6. `caboose attach <run-id> --metrics-only`
7. `caboose resume <run-id>`
8. `caboose cancel <run-id>`
9. `caboose providers list --json`
10. `caboose estimate --gpu "H100:1" --hours 4`

### Job Config Model
Flags should be first-class and override YAML values.

```yaml
job:
  name: "resnet50-exp1"
  script: "train.py"
  args: ["--epochs", "30"]
  env:
    WANDB_PROJECT: "vision"
  resources:
    gpu: "A100:1"
    cpu: 8
    memory_gb: 64
  data:
    - source: "./data/train.csv"
      mount: "/workspace/data/train.csv"
    - source: "https://example.com/dataset.csv"
      mount: "/workspace/data/dataset.csv"

checkpoint:
  enabled: true
  interval_minutes: 10
  path: "/workspace/checkpoints"
  backend:
    type: "gcs" # local|gcs|s3
    uri: "gs://my-bucket/checkpoints/resnet50-exp1"

routing:
  provider: "auto" # auto|gcp|lambda|hyperbolic
  objective: "min_cost"
  max_hourly_cost_usd: 12
  failover:
    enabled: true
    max_attempts: 2

parallel:
  enabled: false
  matrix: {}

output:
  json_events: true
```

### Routing and Recovery Behavior
1. If `routing.provider` is explicit, dispatch directly to that provider.
2. If `routing.provider=auto`, filter providers by resource and capability match.
3. Rank eligible providers by estimated cost under user constraints and select the cheapest.
4. On provider failure, locate the latest valid checkpoint.
5. Exclude failed provider if outage/capacity error threshold is met.
6. Resubmit on next eligible provider with resume metadata.

### Telemetry and Logging Spec
Caboose uses three layers:
1. Structured metric/status events from job runtime.
2. Live terminal streaming and attach views.
3. Durable run artifacts for post-job analysis and automation.

#### Event Ingestion Contract
Training scripts should emit structured JSON lines to stdout for metrics.

```json
{"type":"metric","run_id":"r_123","ts":"2026-02-19T21:00:00Z","step":1200,"epoch":3,"metrics":{"loss":0.431,"accuracy":0.882},"split":"train"}
{"type":"metric","run_id":"r_123","ts":"2026-02-19T21:00:05Z","step":1200,"metrics":{"val_loss":0.512,"val_accuracy":0.861},"split":"val"}
{"type":"checkpoint","run_id":"r_123","ts":"2026-02-19T21:00:08Z","checkpoint_uri":"gs://bucket/ckpt-1200"}
{"type":"status","run_id":"r_123","ts":"2026-02-19T21:00:10Z","state":"running","provider":"lambda"}
```

Plain logs remain supported; parser should handle mixed stdout safely.

#### Live UX
1. `caboose attach <run-id>` streams logs and rolling metric table.
2. `caboose attach <run-id> --metrics-only` shows compact metric-focused terminal output.
3. `caboose logs <run-id> --json` provides raw machine-readable stream.

#### Saved Artifacts
1. `~/.caboose/runs/<run-id>/events.jsonl`
2. `~/.caboose/runs/<run-id>/summary.json`

`summary.json` should include:
1. `final_metrics`
2. `best_metrics`
3. `best_step`
4. `runtime_sec`
5. `provider_attempts`
6. `checkpoint_count`
7. `resume_count`
8. `exit_reason`

### Provider Adapter Contract
```go
type ProviderAdapter interface {
  ValidateAuth(ctx context.Context) error
  Estimate(ctx context.Context, spec JobSpec) (CostEstimate, error)
  Submit(ctx context.Context, spec JobSpec) (ProviderJobRef, error)
  Status(ctx context.Context, ref ProviderJobRef) (JobStatus, error)
  Logs(ctx context.Context, ref ProviderJobRef, cursor string) (LogChunk, error)
  Cancel(ctx context.Context, ref ProviderJobRef) error
  Supports(spec JobSpec) SupportReport
}
```

### Automation Contract
JSON event mode should emit line-delimited lifecycle events and stable exit codes:
1. `0`: Success.
2. `10`: Config/user input error.
3. `20`: Provider auth error.
4. `30`: Routing or capacity failure.
5. `40`: Runtime failure with no resumable checkpoint.
6. `50`: Internal CLI error.

### Security Defaults
1. BYO provider authentication only for early phases.
2. Never persist raw secrets in run metadata.
3. Redact known secret values from logs and JSON events.

### Spec-Level Acceptance Criteria
1. Same workload can run on GCP, Lambda, and Hyperbolic through one CLI.
2. Auto-routing picks the cheapest valid provider under declared constraints.
3. A forced provider failure can resume from latest checkpoint on another provider.
4. Users can watch loss/accuracy in terminal during run.
5. `events.jsonl` and `summary.json` are generated for success and failure cases.
