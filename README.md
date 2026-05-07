# CloudTune Orchestrator

CloudTune Orchestrator is a provider-agnostic control plane for AI/ML workloads. It manages workload lifecycle, provider routing, retries, checkpoint resume, telemetry, artifacts, and run history behind a small provider adapter contract.

This branch intentionally does **not** include the separate GCP-provider branch. The current implementation is a local + deterministic mock-cloud MVP foundation: it can run real local Python workloads, simulate provider failover through mock providers, persist SQLite run state, collect logs/events/summaries, export workload outputs, and list run artifacts.

## Status

Implemented now:

1. `cloudtune run <config>` for CloudTune workload YAML.
2. `local` provider for real local scripts.
3. deterministic mock providers for routing, retry, failover, and checkpoint-resume demos.
4. SQLite run/attempt/routing state.
5. structured JSONL telemetry from mixed stdout.
6. artifact manifest generation through `artifacts.json`.
7. output export through `outputs.save_to`.
8. run history through `cloudtune runs`.
9. runtime env aliases under `CLOUDTUNE_*`, while preserving `SWITCHBOARD_*` and legacy `ORCHESTRATOR_*`.
10. provider capability inspection through `cloudtune providers inspect <provider>`.
11. run evidence capture in `workload.json`: config hash, git commit/dirty flag, working directory, entrypoint, dataset fingerprint, and provider job refs.
12. run comparison through `cloudtune compare <run-id> <run-id>`.

Not implemented in this branch:

1. real GCP, RunPod, Modal, OpenAI, Anthropic, AWS, or Azure adapters.
2. local script packaging into container images.
3. hosted dashboard, SSO, approval workflow, or enterprise policy engine.
4. real eval-gate enforcement beyond persisted workload/artifact metadata.

## Quick Start

Build:

```sh
go build -o bin/cloudtune ./cmd/switchboard-cli
```

Run the CloudTune eval MVP:

```sh
CLOUDTUNE_HOME="$(mktemp -d)" ./bin/cloudtune run examples/eval.yaml
```

Inspect the run:

```sh
./bin/cloudtune --home "$CLOUDTUNE_HOME" runs
./bin/cloudtune --home "$CLOUDTUNE_HOME" status <run-id>
./bin/cloudtune --home "$CLOUDTUNE_HOME" logs <run-id>
./bin/cloudtune --home "$CLOUDTUNE_HOME" artifacts <run-id>
./bin/cloudtune --home "$CLOUDTUNE_HOME" compare <run-id> <run-id>
```

Run the mock failover demo:

```sh
CLOUDTUNE_HOME="$(mktemp -d)" ./bin/cloudtune train --provider auto --config examples/failover.yaml
```

Expected failover output includes:

```text
Selected mock-lambda
Found checkpoint: step 800
Selected mock-gcp
Run <run-id> succeeded
```

Inspect provider capability declarations:

```sh
./bin/cloudtune providers list
./bin/cloudtune providers inspect local
./bin/cloudtune providers inspect mock-cloud
```

Run the controlled failure demo:

```sh
CLOUDTUNE_HOME="$(mktemp -d)" ./bin/cloudtune run examples/eval_fail.yaml
```

Expected behavior is a failed run with preserved logs, summary, `workload.json`, artifact manifest, and partial output artifact.

Run checks:

```sh
go test ./...
go test -race ./...
go vet ./...
```

## Workload Config

CloudTune workload configs add product-level metadata around the existing executable job contract:

```yaml
workload:
  name: rag-eval-v1
  type: evaluation
  model:
    provider: local
    name: deterministic-evaluator
  dataset:
    name: customer-support-sample
    path: examples/evals/customer_support.jsonl
  tags: ["mvp", "eval"]

job:
  script: examples/eval.py

routing:
  provider: local
  max_attempts: 1

outputs:
  save_to: ./artifacts
```

The local runtime injects:

```text
CLOUDTUNE_RUN_ID
CLOUDTUNE_ATTEMPT_ID
CLOUDTUNE_CHECKPOINT_DIR
CLOUDTUNE_RESUME_FROM
CLOUDTUNE_EVENTS_PATH
CLOUDTUNE_OUTPUT_DIR
CLOUDTUNE_ARTIFACTS_MANIFEST
CLOUDTUNE_WORKLOAD_TYPE
CLOUDTUNE_MODEL_PROVIDER
CLOUDTUNE_MODEL_NAME
CLOUDTUNE_DATASET_NAME
CLOUDTUNE_DATASET_PATH
CLOUDTUNE_DATASET_URI
```

Scripts should write result files to `CLOUDTUNE_OUTPUT_DIR`. CloudTune records those files in `artifacts.json` and, when `outputs.save_to` is set, copies them into `<save_to>/<run-id>/`.

`workload.json` stores reproducibility evidence for each run. Current fields include:

```json
{
  "config_hash": "sha256:...",
  "git_commit": "...",
  "git_dirty": true,
  "working_dir": "...",
  "entrypoint": "examples/eval.py",
  "dataset": {
    "path": "...",
    "sha256": "sha256:...",
    "size_bytes": 123,
    "num_records": 10
  },
  "provider_job_refs": [
    {
      "provider": "local",
      "provider_job_id": "local:12345"
    }
  ]
}
```

## Provider Contract

Providers stay behind this core shape:

```text
ValidateAuth
Capabilities
ValidateJob
Estimate
Submit
GetStatus
StreamLogs
Cancel
```

The orchestration core owns state transitions, retryability, failover, checkpoint discovery, telemetry ingestion, summaries, and artifact records. A provider should report facts and perform provider-specific operations; it should not own orchestration policy.

Providers also declare capabilities before routing or explicit execution. The current schema includes workload types, log mode, artifact support, cancellation support, cost-estimate support, checkpoint-resume support, remote/local classification, URI schemes, and data-bundle support. Unsupported workload/provider combinations are rejected before submission.

## Artifact Layout

By default, state still uses the existing local compatibility path unless `--home` or `CLOUDTUNE_HOME` is set:

```text
<home>/switchboard.db
<home>/runs/<run-id>/events.jsonl
<home>/runs/<run-id>/logs.txt
<home>/runs/<run-id>/summary.json
<home>/runs/<run-id>/workload.json
<home>/runs/<run-id>/artifacts.json
<home>/runs/<run-id>/outputs/
<home>/runs/<run-id>/checkpoints/
<home>/runs/<run-id>/workspace/
```

Home resolution order is `--home`, `CLOUDTUNE_HOME`, `SWITCHBOARD_CLI_HOME`, deprecated `ORCHESTRATOR_CLI_HOME`, an existing `~/.orchestrator-cli`, then `~/.switchboard-cli`.

## Roadmap

The correct near-term wedge is reliable eval and batch workload orchestration across local + one real provider. This branch implements the local/mock foundation. The next real step is to add one production provider adapter, but only after the workload/artifact/run-history path remains stable under stress.

Near-term phases:

1. Reliable execution: local runner, lifecycle, logs, artifacts, run history.
2. Multi-provider routing: one real provider plus health/cost/capability checks.
3. Checkpoint/resume: checkpoint registry and provider migration.
4. Eval/governance: eval gates, lineage, deployment blocking metadata.
5. Enterprise control plane: dashboard, teams, RBAC, cost centers, audit export.

## Docs

- [CloudTune MVP](docs/cloudtune-orchestrator-mvp.md)
- [Overview](docs/overview.md)
- [Architecture](docs/architecture.md)
- [Roadmap](docs/roadmap.md)
