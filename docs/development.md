# CloudTune Development

## Package Layout

1. `cmd/switchboard-cli`: binary entrypoint. Build it as `cloudtune` for this branch.
2. `internal/cli`: Cobra command wiring and orchestration flow.
3. `internal/app`: shared contracts for workloads, jobs, runs, attempts, events, providers, and errors.
4. `internal/config`: YAML and flag resolution.
5. `internal/data`: data input preflight.
6. `internal/state`: SQLite run, attempt, and routing persistence.
7. `internal/provider`: provider registry and adapters.
8. `internal/provider/local`: local script execution provider.
9. `internal/provider/mock`: deterministic mock-cloud providers.
10. `internal/artifact`: run artifact paths, manifests, and output export.
11. `internal/event`: mixed stdout parsing and JSONL helpers.
12. `internal/summary`: summary generation.
13. `internal/redact`: secret redaction before persistence.

## Local Workflow

Build:

```sh
go build -o bin/cloudtune ./cmd/switchboard-cli
```

Run the eval MVP:

```sh
CLOUDTUNE_HOME="$(mktemp -d)" ./bin/cloudtune run examples/eval.yaml
```

Run the mock failover demo:

```sh
CLOUDTUNE_HOME="$(mktemp -d)" ./bin/cloudtune train --provider auto --config examples/failover.yaml
```

Inspect artifacts:

```sh
./bin/cloudtune --home "$CLOUDTUNE_HOME" runs
./bin/cloudtune --home "$CLOUDTUNE_HOME" status <run-id>
./bin/cloudtune --home "$CLOUDTUNE_HOME" logs <run-id>
./bin/cloudtune --home "$CLOUDTUNE_HOME" artifacts <run-id>
```

Run checks:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
```

## Provider Contract Tests

Provider implementations should opt into `internal/provider/contract` before being registered as production candidates. Contract tests should verify adapter identity, auth validation, capabilities, job validation, estimates, submit/status behavior, log streaming expectations, and cancel behavior.

## Compatibility

This branch introduces `CLOUDTUNE_HOME` and `CLOUDTUNE_*` runtime variables. It preserves `SWITCHBOARD_CLI_HOME`, `SWITCHBOARD_*`, deprecated `ORCHESTRATOR_CLI_HOME`, and deprecated `ORCHESTRATOR_*` aliases for existing local state and scripts.
