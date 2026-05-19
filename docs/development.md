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
9. `internal/provider/gcp`: Vertex AI CustomJob provider for prebuilt container images.
10. `internal/packaging`: Docker build/push helpers used before provider submit.
11. `internal/event`: mixed stdout parsing and JSONL helpers.
12. `internal/artifact` and `internal/summary`: durable artifact paths and summary generation.
13. `internal/redact`: redaction of secret-like keys and known secret environment values before persistence.
14. `internal/provider/contract`: reusable adapter contract checks for local, mock, GCP, and future providers.

## Local Workflow

Build:

```sh
go build -o bin/switchboard-cli ./cmd/switchboard-cli
```

Run the demo:

```sh
SWITCHBOARD_CLI_HOME="$(mktemp -d)" ./bin/switchboard-cli train --provider local --script examples/train.py
```

Run the PyTorch Iris demo:

```sh
SWITCHBOARD_CLI_HOME="$(mktemp -d)" ./bin/switchboard-cli train --provider local --config examples/iris-pytorch.yaml
```

The PyTorch Iris demo requires `python3` and `torch`. It uses a checked-in CC0 Kaggle Iris CSV so the local data bundle path is deterministic and does not require Kaggle credentials.

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

The `local` provider, deterministic mock providers, and a GCP Vertex AI CustomJob provider are implemented. GCP v1 accepts a prebuilt `job.image` or a packageable `job.script` that is built and pushed through the Docker packaging layer before submit. GCP data inputs still support only `gs://` URI inputs; source distribution upload, non-GCS cloud data inputs, and multi-worker training are deferred. GCP capabilities use Cloud Billing Catalog pricing, Compute Engine machine/accelerator inventory, and regional quota facts when available, with static estimates as fallback. `logs --follow` follows active run artifacts until the run reaches a terminal state and drains newly appended logs before returning. Explicit user-facing `resume` is still roadmap work, but provider failover passes compatible checkpoint events into later attempts.

## Runtime Workspace

Each run gets a workspace at:

```text
<home>/runs/<run-id>/workspace
```

Bundled local data inputs are copied into that workspace. Mounts must be under `/workspace`; `/workspace/data/train` becomes `<home>/runs/<run-id>/workspace/data/train`. The local runtime rewrites job arguments and job environment values that reference declared mounts to their host paths before executing the script.

The local provider stores a `local:<pid>` provider reference on the running attempt. `switchboard-cli cancel <run-id>` uses that reference to interrupt the process, then marks the run and attempt as `canceled` and rewrites `summary.json`.

The GCP provider stores the full Vertex AI CustomJob resource name as the provider reference. `switchboard-cli cancel <run-id>` uses that reference to issue a best-effort CustomJob cancel request.

Run records include the script path for script jobs and the image URI for container jobs; human-readable `status` output shows whichever target is present. Attempt records include optional resume checkpoint and cost estimate provenance. Routing decisions include selected hardware, estimated VRAM/runtime/cost, confidence, and rejected hardware reasons when hardware routing is active. GCP hardware shapes include live zones and quota fields when Compute Engine APIs are reachable. Existing SQLite databases are migrated in place by adding missing run, attempt, and routing-decision columns when opened.

Before writing logs, `events.jsonl`, summaries, or provider failure reasons, providers and orchestration code redact secret-like keys and known secret values from job/runtime/inherited environment variables. Do not add new persistence paths without using the redaction utility.

## Next Steps

1. Keep the gated billable container submit smoke test current with `SWITCHBOARD_GCP_LIVE=1` and `SWITCHBOARD_GCP_LIVE_SUBMIT=1`.
2. Run the containerized PyTorch Iris demo from `examples/gcp/iris` when a GCS copy of the Iris CSV and an Artifact Registry image are available.
3. Add live smoke coverage for GCP pricing/capacity API permissions.
4. Add more cloud resume examples once additional providers can consume shared checkpoint URIs.
