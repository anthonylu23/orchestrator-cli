# PyTorch Iris Demo

## Purpose

The PyTorch Iris demo is a small local deep learning workload for exercising Switchboard with a real dataset, framework import, bundled data mount, structured metrics, checkpoints, and summary generation.

It uses Kaggle's [Iris Species dataset](https://www.kaggle.com/datasets/uciml/iris), which is published as CC0/Public Domain. The CSV is checked into `examples/data/iris/Iris.csv` so tests and demos do not require Kaggle credentials or network access.

## Run Locally

Build the CLI:

```sh
go build -o bin/switchboard-cli ./cmd/switchboard-cli
```

Run the demo with an isolated Switchboard home:

```sh
SWITCHBOARD_CLI_HOME="$(mktemp -d)" ./bin/switchboard-cli train --provider local --config examples/iris-pytorch.yaml
```

The script requires `python3` and `torch` in the local Python environment. It trains a deterministic MLP with `Linear(4 -> 16)`, ReLU, and `Linear(16 -> 3)` layers against the mounted CSV at `/workspace/data/iris/Iris.csv`.

## Artifacts

The run writes the same artifacts as other local jobs:

```text
<home>/runs/<run-id>/events.jsonl
<home>/runs/<run-id>/logs.txt
<home>/runs/<run-id>/summary.json
<home>/runs/<run-id>/checkpoints/iris-epoch-*.pt
<home>/runs/<run-id>/workspace/data/iris/Iris.csv
```

Metric events include train and validation loss/accuracy. Checkpoint events point at local `.pt` files under `SWITCHBOARD_CHECKPOINT_DIR`. If a later attempt receives a local file checkpoint through `SWITCHBOARD_RESUME_FROM`, the script restores model and optimizer state before continuing. The script also accepts the legacy `ORCHESTRATOR_*` runtime variables for compatibility.

## Test Coverage

`internal/cli` includes a PyTorch Iris integration test. It skips when `python3` or `torch` is unavailable, then verifies local execution, metric ingestion, summary metrics, checkpoint artifacts, and bundled data materialization.

## Next Steps

1. Keep this demo local-first until GCP supports source packaging or image build/push.
2. Reuse the demo as the first containerized training workload once the GCP container workflow can build or consume a PyTorch image.
3. Consider adding an optional resume-focused version after explicit user-facing `resume` support exists.
