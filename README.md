# switchboard-cli

> Status: pre-alpha.
>
> `switchboard-cli` is an experimental CLI for CloudTune-style AI/ML workload orchestration. Local and mock execution work today. `modal-sandbox` is implemented but experimental and still requires live verification against a configured Modal account. Do not use this for production workloads or production credentials yet.

`switchboard-cli` runs eval-style AI/ML workloads from YAML, captures reproducibility evidence, preserves logs/artifacts, validates provider capabilities, and compares runs. The long-term CloudTune direction is a provider-agnostic execution and evidence layer across compute and model providers, but this repo is currently a local/mock-first pre-alpha.

This branch intentionally does **not** include the separate GCP-provider branch. The current implementation can run real local Python workloads, simulate provider failover through deterministic mock providers, persist SQLite run state, collect logs/events/summaries, export workload outputs, and list run artifacts.

## What Works Today

Today `switchboard-cli` can:

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
13. runtime diagnostics through `cloudtune doctor`.
14. experimental `modal-sandbox` provider adapter, gated by Modal SDK/auth readiness.

## What Does Not Work Yet

This branch does not provide:

1. real GCP, RunPod, OpenAI, Anthropic, AWS, or Azure adapters.
2. local script packaging into container images.
3. hosted dashboard, SSO, approval workflow, or enterprise policy engine.
4. real eval-gate enforcement beyond persisted workload/artifact metadata.
5. live-verified Modal execution in this local environment; `modal-sandbox` is implemented but requires Modal CLI/Python SDK/auth.
6. a stable YAML schema.
7. production-grade sandboxing for local workload execution.

## Quick Start

Build:

```sh
go build -o bin/cloudtune ./cmd/switchboard-cli
```

Run the local eval MVP:

```sh
export CLOUDTUNE_HOME="$(mktemp -d)"
./bin/cloudtune run examples/eval.yaml --provider local
```

Inspect the run:

```sh
./bin/cloudtune --home "$CLOUDTUNE_HOME" runs
./bin/cloudtune --home "$CLOUDTUNE_HOME" status <run-id>
./bin/cloudtune --home "$CLOUDTUNE_HOME" logs <run-id>
./bin/cloudtune --home "$CLOUDTUNE_HOME" artifacts <run-id>
./bin/cloudtune --home "$CLOUDTUNE_HOME" compare <run-id> <run-id>
```

The repository and Go module are still named `switchboard-cli`. The built binary is named `cloudtune` in examples because the CLI command surface uses the CloudTune workload language.

Run the mock failover demo:

```sh
export CLOUDTUNE_HOME="$(mktemp -d)"
./bin/cloudtune train --provider auto --config examples/failover.yaml
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
./bin/cloudtune providers inspect modal-sandbox
```

Run local readiness checks:

```sh
./bin/cloudtune doctor --provider local --config examples/eval.yaml
./bin/cloudtune doctor --provider modal-sandbox
```

`modal-sandbox` is expected to report not ready until Modal CLI/SDK/auth are configured.

Modal setup for live verification:

```sh
python3 -m venv .venv
source .venv/bin/activate
pip install modal
modal token set
```

Or use environment-based auth:

```sh
export MODAL_TOKEN_ID=...
export MODAL_TOKEN_SECRET=...
```

Run the controlled failure demo:

```sh
export CLOUDTUNE_HOME="$(mktemp -d)"
./bin/cloudtune run examples/eval_fail.yaml
```

Expected behavior is a failed run with preserved logs, summary, `workload.json`, artifact manifest, and partial output artifact.

Run checks:

```sh
go test ./... -count=1
go vet ./...
go test -race ./... -count=1
git diff --check
```

Live Modal integration tests are opt-in:

```sh
CLOUDTUNE_INTEGRATION=modal go test ./... -run ModalIntegration -count=1
```

These tests run real Modal jobs and may create billable usage.

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

`modal-sandbox` is intentionally experimental. It uses an embedded Python runner around Modal Sandboxes, uploads the workload script plus local dataset path, runs the command remotely, stores the Modal sandbox ID as the provider job ref, preserves stdout/stderr in `logs.txt`, and fetches `outputs/` and `checkpoints/` back as a tar archive. It does not claim cost estimates, GPU routing, URI dataset fetching, or checkpoint resume.

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

- [Provider status](docs/provider-status.md)
- [CloudTune MVP](docs/cloudtune-orchestrator-mvp.md)
- [Modal live verification](docs/modal-live-verification.md)
- [Overview](docs/overview.md)
- [Architecture](docs/architecture.md)
- [Roadmap](docs/roadmap.md)

## Community And Security

- [License](LICENSE)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Code of conduct](CODE_OF_CONDUCT.md)
