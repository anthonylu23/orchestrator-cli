# Switchboard Development

## Package Layout

The implementation is a Go CLI with a small internal package split:

1. `cmd/switchboard-cli`: binary entrypoint.
2. `internal/cli`: Cobra command wiring and orchestration flow.
3. `internal/app`: shared contracts for jobs, runs, attempts, events, summaries, providers, and normalized errors.
4. `internal/config`: YAML and flag resolution.
5. `internal/data`: data input preflight, mode inference, path validation, and bundle size checks.
6. `internal/state`: SQLite run and attempt persistence.
7. `internal/provider`: provider registry and adapters.
8. `internal/provider/local`: local script execution provider.
9. `internal/event`: mixed stdout parsing and JSONL helpers.
10. `internal/artifact` and `internal/summary`: durable artifact paths and summary generation.
11. `internal/redact`: redaction of secret-like keys and known secret environment values before persistence.
12. `internal/provider/contract`: reusable adapter contract checks for local, mock, and future providers.

## Local Workflow

Build:

```sh
go build -o bin/switchboard-cli ./cmd/switchboard-cli
```

Run the demo:

```sh
SWITCHBOARD_CLI_HOME="$(mktemp -d)" ./bin/switchboard-cli train --provider local --script examples/train.py
```

Inspect artifacts:

```sh
./bin/switchboard-cli --home "$SWITCHBOARD_CLI_HOME" status <run-id>
./bin/switchboard-cli --home "$SWITCHBOARD_CLI_HOME" logs <run-id>
./bin/switchboard-cli --home "$SWITCHBOARD_CLI_HOME" cancel <run-id>
```

Run checks:

```sh
go test ./...
go vet ./...
```

## Provider Contract Tests

Provider implementations should opt into `internal/provider/contract` before being registered as production candidates. The contract validates adapter identity, auth validation, capabilities, job validation, estimates, submit/status behavior, log streaming expectations, and cancel behavior.

When adding a provider:

1. Implement `app.ProviderAdapter`.
2. Add a provider-local `contract_test.go`.
3. Declare intentional behavior differences in the contract subject, such as artifact-backed logs or provider-specific cancel setup.
4. Add provider-specific tests only for behavior that cannot be represented by the shared contract.
5. Run `go test ./internal/provider/...` before broader CLI integration tests.

## Exit Codes

The CLI keeps stable exit categories for automation:

```text
1    internal or unclassified failure
10   invalid job spec or data preparation failure
30   routing or provider selection failure
40   retryable provider failure without a usable resume checkpoint
130  canceled run
```

## Current Limits

The `local` provider and deterministic mock providers are implemented. URI data inputs are validated for supported schemes and accepted by mock providers, but real runtime fetching is deferred to cloud provider phases. `logs --follow` follows active run artifacts until the run reaches a terminal state and drains newly appended logs before returning. Explicit `resume` and real cloud providers are still roadmap items.

## Runtime Workspace

Each run gets a workspace at:

```text
<home>/runs/<run-id>/workspace
```

Bundled local data inputs are copied into that workspace. Mounts must be under `/workspace`; `/workspace/data/train` becomes `<home>/runs/<run-id>/workspace/data/train`. The local runtime rewrites job arguments and job environment values that reference declared mounts to their host paths before executing the script.

The local provider stores a `local:<pid>` provider reference on the running attempt. `switchboard-cli cancel <run-id>` uses that reference to interrupt the process, then marks the run and attempt as `canceled` and rewrites `summary.json`.

Attempt records include optional resume checkpoint and cost estimate provenance. Existing SQLite databases are migrated in place by adding missing attempt columns when opened.

Before writing logs, `events.jsonl`, summaries, or provider failure reasons, providers and orchestration code redact secret-like keys and known secret values from job/runtime/inherited environment variables. Do not add new persistence paths without using the redaction utility.

## Rename Compatibility

Prefer `SWITCHBOARD_CLI_HOME`, `~/.switchboard-cli`, `switchboard.db`, and `SWITCHBOARD_*` runtime metadata in new code, docs, and examples.

Deprecated aliases from the previous project name remain supported for existing users:

```text
ORCHESTRATOR_CLI_HOME
~/.orchestrator-cli
orchestrator.db
ORCHESTRATOR_RUN_ID
ORCHESTRATOR_ATTEMPT_ID
ORCHESTRATOR_CHECKPOINT_DIR
ORCHESTRATOR_RESUME_FROM
ORCHESTRATOR_EVENTS_PATH
```

The selected home resolves in this order: `--home`, `SWITCHBOARD_CLI_HOME`, `ORCHESTRATOR_CLI_HOME`, an existing `~/.orchestrator-cli`, then `~/.switchboard-cli`. State opens `switchboard.db` unless only `orchestrator.db` exists in the selected home. Providers receive both runtime env families with identical values until the legacy names are removed in a later compatibility pass.
