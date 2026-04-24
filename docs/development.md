# Orchestrator Development

## Package Layout

The implementation is a Go CLI with a small internal package split:

1. `cmd/orchestrator-cli`: binary entrypoint.
2. `internal/cli`: Cobra command wiring and orchestration flow.
3. `internal/app`: shared contracts for jobs, runs, attempts, events, summaries, providers, and normalized errors.
4. `internal/config`: YAML and flag resolution.
5. `internal/data`: data input preflight, mode inference, path validation, and bundle size checks.
6. `internal/state`: SQLite run and attempt persistence.
7. `internal/provider`: provider registry and adapters.
8. `internal/provider/local`: local script execution provider.
9. `internal/event`: mixed stdout parsing and JSONL helpers.
10. `internal/artifact` and `internal/summary`: durable artifact paths and summary generation.

## Local Workflow

Build:

```sh
go build -o bin/orchestrator-cli ./cmd/orchestrator-cli
```

Run the demo:

```sh
ORCHESTRATOR_CLI_HOME="$(mktemp -d)" ./bin/orchestrator-cli train --provider local --script examples/train.py
```

Inspect artifacts:

```sh
./bin/orchestrator-cli --home "$ORCHESTRATOR_CLI_HOME" status <run-id>
./bin/orchestrator-cli --home "$ORCHESTRATOR_CLI_HOME" logs <run-id>
./bin/orchestrator-cli --home "$ORCHESTRATOR_CLI_HOME" cancel <run-id>
```

Run checks:

```sh
go test ./...
go vet ./...
```

## Current Limits

The `local` provider and deterministic mock providers are implemented. URI data inputs are validated for supported schemes and accepted by mock providers, but real runtime fetching is deferred to cloud provider phases. `logs --follow` follows active run artifacts until the run reaches a terminal state. Explicit `resume` and real cloud providers are still roadmap items.

## Runtime Workspace

Each run gets a workspace at:

```text
<home>/runs/<run-id>/workspace
```

Bundled local data inputs are copied into that workspace. Mounts must be under `/workspace`; `/workspace/data/train` becomes `<home>/runs/<run-id>/workspace/data/train`. The local runtime rewrites job arguments and job environment values that reference declared mounts to their host paths before executing the script.

The local provider stores a `local:<pid>` provider reference on the running attempt. `orchestrator-cli cancel <run-id>` uses that reference to interrupt the process, then marks the run and attempt as `canceled` and rewrites `summary.json`.
