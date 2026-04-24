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
```

Run checks:

```sh
go test ./...
go vet ./...
```

## Current Limits

Only the `local` provider is implemented. URI data inputs are validated for supported schemes, but runtime fetching is deferred to the mock/cloud provider phases. `logs --follow` follows active run artifacts until the run reaches a terminal state. `cancel`, `resume`, auto-routing, mock failover, and real cloud providers are still roadmap items.
