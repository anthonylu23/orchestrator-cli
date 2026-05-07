# CloudTune Orchestrator MVP

## Interpretation

CloudTune Orchestrator should become the neutral execution layer for AI workloads. The first implementation must stay narrower: reliable local execution plus deterministic mock-cloud routing, with durable state, logs, artifacts, and workload metadata.

This branch starts that product direction without merging the separate GCP-provider branch.

## Implemented Scope

1. Generic workload metadata under `workload`.
2. `cloudtune run <config>` as the main workload entrypoint.
3. `cloudtune runs` for run history.
4. `cloudtune artifacts <run-id>` for artifact manifest inspection.
5. `CLOUDTUNE_*` runtime environment variables.
6. `outputs.save_to` export into `<save_to>/<run-id>/`.
7. `workload.json` and `artifacts.json` persisted for every run.
8. Backward-compatible `train`, `status`, `logs`, `cancel`, and provider commands.

## Current Provider Reality

The branch supports:

1. `local`: executes real local scripts.
2. `mock-lambda` and `mock-gcp`: deterministic mock providers used to stress routing, retry, checkpoint discovery, and resume flow.

The branch does not support real GCP, RunPod, Modal, OpenAI, Anthropic, AWS, Azure, Lambda Labs, or HPC execution yet. The mock providers are test infrastructure and demo infrastructure, not production cloud adapters.

## Workload Contract

Example:

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

job:
  script: examples/eval.py

routing:
  provider: local
  max_attempts: 1

outputs:
  save_to: ./artifacts
```

`workload` is product metadata. `job` is the executable surface. `routing` selects the provider and retry policy. `outputs` controls artifact export.

## Runtime Environment

CloudTune injects the following variables:

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

Scripts should write result files to `CLOUDTUNE_OUTPUT_DIR` and emit structured JSONL metrics/status/checkpoint events to stdout.

## Artifact Contract

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

`artifacts.json` records standard files plus every file under `outputs/` and `checkpoints/`.

## Stress Criteria

Before treating this branch as usable, run:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build -o /tmp/cloudtune ./cmd/switchboard-cli
```

Then exercise:

1. local eval workload through `cloudtune run examples/eval.yaml`.
2. artifact listing with `cloudtune artifacts <run-id>`.
3. run history with `cloudtune runs`.
4. mock failover through `train --provider auto --config examples/failover.yaml`.
5. repeated local runs under a disposable `CLOUDTUNE_HOME`.

## Next Engineering Step

Add one real provider adapter only after this branch is merged cleanly and the local/mock stress path stays stable. The best next adapter for the product wedge is likely a batch/eval-friendly provider before distributed training.
