# Orchestrator CLI

Orchestrator CLI is a fault-tolerant ML job orchestrator with provider adapters, local durable run state, structured telemetry, and cost-aware scheduling.

The project is designed as a systems engineering and ML infrastructure tool: the orchestration core owns lifecycle, retries, routing, failover, state, telemetry, and resume policy, while providers stay behind a small adapter contract.

## Status

Orchestrator now has an initial local orchestration vertical slice. The current implementation can run a local Python training script, persist SQLite run state, capture mixed logs and structured JSONL events, and produce durable run artifacts.

Mock cloud failover, routing, resume, and real cloud providers remain roadmap work.

## Quick Start

Build the CLI:

```sh
go build -o bin/orchestrator-cli ./cmd/orchestrator-cli
```

Run the example training script:

```sh
./bin/orchestrator-cli train --provider local --script examples/train.py
```

Use a disposable Orchestrator home while developing:

```sh
ORCHESTRATOR_CLI_HOME="$(mktemp -d)" ./bin/orchestrator-cli train --provider local --script examples/train.py
```

Inspect a run:

```sh
./bin/orchestrator-cli status <run-id>
./bin/orchestrator-cli logs <run-id>
./bin/orchestrator-cli providers list --json
```

Run tests:

```sh
go test ./...
go vet ./...
```

## Product Wedge

Given a training script and hardware constraints, Orchestrator should choose a compatible provider, run the job, persist telemetry, and resume from the latest checkpoint if a provider fails.

The first impressive demo is intentionally narrower than full multi-cloud support:

1. Run an example ML training script through the `local` provider.
2. Stream mixed logs and structured metric events.
3. Persist run state, `events.jsonl`, and `summary.json`.
4. Simulate a cloud provider failure with the `mock` provider.
5. Discover the latest checkpoint and resume on another adapter without changing orchestration code.

## Target Commands

```sh
orchestrator-cli train --provider local --script examples/train.py
orchestrator-cli status <run-id>
orchestrator-cli logs <run-id> --follow
orchestrator-cli providers list --json
```

Planned commands not implemented yet include `provider=auto`, `resume`, `cancel`, mock cloud failover, and real provider adapters.

## Data Inputs

Orchestrator treats training and test data as declared job inputs. Local files or directories can be bundled with the job, while remote sources such as HTTP, S3, and GCS URIs are resolved at runtime. In both cases, training code reads from stable workspace paths.

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
  bundle:
    max_size_mb: 512
    require_override_above_limit: true
```

Local paths default to bundled inputs. URI sources default to runtime-resolved inputs. Oversized local bundles fail preflight unless the user passes an explicit override such as `--allow-large-data-bundle`.

## Architecture Flow

```text
CLI command
  -> config loader
  -> data preparation
  -> orchestration service
  -> routing engine
  -> provider registry
  -> provider adapter
  -> run state + events + artifacts
```

The core user-facing object is a run. Each provider execution is an attempt. This lets one run fail on one provider and resume on another provider while preserving a single user-facing run history.

## Artifacts

By default Orchestrator writes to `~/.orchestrator-cli`. Set `ORCHESTRATOR_CLI_HOME` or pass `--home` to isolate runs.

```text
~/.orchestrator-cli/orchestrator.db
~/.orchestrator-cli/runs/<run-id>/events.jsonl
~/.orchestrator-cli/runs/<run-id>/logs.txt
~/.orchestrator-cli/runs/<run-id>/summary.json
~/.orchestrator-cli/runs/<run-id>/checkpoints/
```

## Roadmap Summary

1. Spec and scaffold.
2. Local orchestration vertical slice.
3. Mock cloud provider and failure simulation.
4. Provider extensibility hardening.
5. GCP as the first real provider.
6. Later: Lambda, Hyperbolic, container packaging, shared checkpoint backends, fan-out sweeps, richer terminal UI, and optional hosted control plane.

## Docs

- [Overview](docs/overview.md)
- [Architecture](docs/architecture.md)
- [Roadmap](docs/roadmap.md)
