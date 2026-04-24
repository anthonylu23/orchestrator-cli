# Orchestrator CLI Overview

## Vision

Orchestrator CLI is a local-first orchestration layer for ML and deep learning jobs. It should make training runs portable across execution backends without forcing users to learn each provider's job lifecycle, logging model, failure behavior, and resume workflow.

## Core Promise

Run a training job through one CLI, materialize its data inputs consistently, observe it consistently, persist durable telemetry, and resume from the latest checkpoint when an execution backend fails.

## Target Users

1. Indie researchers who want cheaper or more reliable access to GPU execution without building provider-specific workflows.
2. ML and systems engineers who want scriptable training orchestration with predictable state, logs, metrics, and failure behavior.
3. AI startup teams later, after the open-source CLI proves repeat usage.

## Product Wedge

"Given my script and hardware constraints, run on the cheapest available compatible provider and resume if a provider fails."

The first implementation should prove this through local and mock providers before integrating real cloud APIs. That keeps the early work focused on orchestration quality instead of provider setup friction.

## Differentiation

1. Provider adapter architecture that makes new backends straightforward to add.
2. Run/attempt lifecycle model built for failover and checkpoint resume.
3. Cost-aware routing with explicit provider rejection reasons.
4. Structured JSONL events and stable exit codes for automation.
5. Minimal config for simple runs with optional YAML for advanced workflows.

## V1 Scope

1. Go CLI orchestration core.
2. `local` provider for executing real local scripts.
3. `mock` provider for simulated cloud behavior, costs, logs, capacity failures, runtime failures, and checkpoint resume.
4. SQLite as canonical local run and attempt state.
5. JSONL event ingestion from mixed stdout.
6. First-class data inputs for bundled local files/directories and runtime-resolved URI sources.
7. Durable artifacts under `~/.orchestrator-cli/runs/<run-id>/`.
8. Provider adapter contract tests.
9. GCP as the first real provider after local/mock behavior is proven.

## Deferred Scope

1. Three real providers at once.
2. Automatic container packaging.
3. Hosted control plane.
4. Unified billing or a single Orchestrator API key.
5. Kubernetes.
6. Rich terminal UI.
7. Distributed multi-node training.
8. Complex sweeps or fan-out beyond basic future design notes.
9. Enterprise governance and compliance features.
10. Typed API connector framework for arbitrary dataset APIs.

## Early Runtime Assumptions

Early Orchestrator versions accept either a local script path or an explicit image and command. Automatic Docker image construction can come later.

Training and test data are declared as job inputs. Local files and directories may be bundled with the job. Remote sources use URI inputs such as `http://`, `https://`, `s3://`, and `gs://`. In both cases, Orchestrator materializes data under stable workspace paths so training scripts do not need provider-specific fetch logic.

Large bundled datasets are allowed only with an explicit override. Orchestrator should compute bundle size during preflight, fail when a configured limit is exceeded, and recommend URI/object-store sources for large datasets.

Private data access uses BYO environment authentication in early versions. Orchestrator may pass through selected environment variables, but it must not persist raw data credentials in SQLite, run metadata, logs, `events.jsonl`, or `summary.json`. Provider-native identity can be added later for real cloud providers.

```yaml
job:
  script: "train.py"
  args: ["--train-data", "/workspace/data/train", "--test-data", "/workspace/data/test.csv"]

data:
  inputs:
    - name: "train"
      source: "./data/train"
      mount: "/workspace/data/train"
      mode: "bundle"

    - name: "test"
      source: "https://example.com/test.csv"
      mount: "/workspace/data/test.csv"
      mode: "uri"

    - name: "imagenet"
      source: "s3://my-bucket/datasets/imagenet"
      mount: "/workspace/data/imagenet"
      mode: "uri"
  bundle:
    max_size_mb: 512
    require_override_above_limit: true
```

Defaults:

1. Local paths default to `mode: bundle`.
2. `http://`, `https://`, `s3://`, and `gs://` sources default to `mode: uri`.
3. If `mount` is omitted, Orchestrator assigns `/workspace/data/<name>`.
4. Oversized local bundles require an explicit override such as `--allow-large-data-bundle`.

Orchestrator injects runtime metadata into the job environment:

```text
ORCHESTRATOR_RUN_ID
ORCHESTRATOR_ATTEMPT_ID
ORCHESTRATOR_CHECKPOINT_DIR
ORCHESTRATOR_RESUME_FROM
ORCHESTRATOR_EVENTS_PATH
```

Training scripts may emit JSON lines to stdout for structured events. Plain logs remain valid and must be handled safely.

```json
{"type":"metric","step":1200,"metrics":{"loss":0.431,"accuracy":0.882},"split":"train"}
{"type":"checkpoint","step":1200,"checkpoint_uri":"file:///tmp/orchestrator-cli/runs/r_123/ckpt-1200"}
{"type":"status","state":"running"}
```

## Acceptance Criteria

The first implementation milestone is successful when a user can run an example ML script locally, stream logs and metrics, persist artifacts, simulate provider failure, discover a checkpoint, and resume on another adapter without changing orchestration code.
