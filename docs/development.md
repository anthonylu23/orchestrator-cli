# Switchboard Development

## Package Layout

The implementation is a Go CLI with a small internal package split:

1. `cmd/switchboard-cli`: binary entrypoint.
2. `internal/cli`: Cobra command wiring and orchestration flow.
3. `internal/app`: shared contracts for jobs, runs, attempts, events, summaries, providers, and normalized errors.
4. `internal/config`: YAML and flag resolution.
5. `internal/credentials`: encrypted local provider credential store and resolver.
6. `internal/data`: data input preflight, mode inference, path validation, and bundle size checks.
7. `internal/state`: SQLite run and attempt persistence.
8. `internal/provider`: provider registry and adapters.
9. `internal/provider/local`: local script execution provider.
10. `internal/provider/gcp`: Vertex AI CustomJob provider for prebuilt container images.
11. `internal/provider/lambda`: Lambda Cloud instance provider for image-first jobs with SSH artifact collection.
12. `internal/provider/chinacloud`: readiness-only adapters for Alibaba Cloud, Huawei Cloud, Tencent Cloud, Tianyi Cloud, and Baidu AI Cloud.
13. `internal/packaging`: Docker build/push helpers used before provider submit.
14. `internal/event`: mixed stdout parsing and JSONL helpers.
15. `internal/artifact` and `internal/summary`: durable artifact paths and summary generation.
16. `internal/redact`: redaction of secret-like keys and known secret environment values before persistence.
17. `internal/provider/contract`: reusable adapter contract checks for local, mock, GCP, Lambda, and future providers.

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

The `local` provider, deterministic mock providers, a GCP Vertex AI CustomJob provider, an initial Lambda Cloud instance provider, and China cloud readiness adapters are implemented. GCP v1 accepts a prebuilt `job.image` or a packageable `job.script` that is built and pushed through the Docker packaging layer before submit. Lambda v1 accepts a prebuilt `job.image`, launches one Lambda instance, runs the image through Docker via cloud-init, collects logs/events over SSH, and terminates by default. China cloud adapters currently stop at credential, endpoint, and optional auth-command readiness for Alibaba Cloud, Huawei Cloud, Tencent Cloud, Tianyi Cloud, and Baidu AI Cloud; they do not submit jobs yet. GCP data inputs still support only `gs://` URI inputs; Lambda URI data inputs are container-handled and local bundles are not staged. Source distribution upload, non-GCS cloud data staging, Lambda private registry auth, Lambda filesystem staging, China cloud compute submission, and multi-worker training are deferred. GCP capabilities use Cloud Billing Catalog pricing, Compute Engine machine/accelerator inventory, and regional quota facts when available, with static estimates as fallback. Lambda capabilities use the instance-types API when available, with configured fallback. `logs --follow` follows active run artifacts until the run reaches a terminal state and drains newly appended logs before returning. Explicit user-facing `resume` is still roadmap work, but provider failover passes compatible checkpoint events into later attempts.

## Runtime Workspace

Each run gets a workspace at:

```text
<home>/runs/<run-id>/workspace
```

Bundled local data inputs are copied into that workspace. Mounts must be under `/workspace`; `/workspace/data/train` becomes `<home>/runs/<run-id>/workspace/data/train`. The local runtime rewrites job arguments and job environment values that reference declared mounts to their host paths before executing the script.

The local provider stores a `local:<pid>` provider reference on the running attempt. `switchboard-cli cancel <run-id>` uses that reference to interrupt the process, then marks the run and attempt as `canceled` and rewrites `summary.json`.

The GCP provider stores the full Vertex AI CustomJob resource name as the provider reference. `switchboard-cli cancel <run-id>` uses that reference to issue a best-effort CustomJob cancel request.

The Lambda provider stores `lambda:<instance-id>` as the provider reference. `switchboard-cli cancel <run-id>` uses that instance ID to issue a Lambda terminate request.

Config files are portable across working directories: relative local `job.script`, `job.work_dir`, bundled data `source` values, and `packaging` paths are resolved from the directory containing `--config`. Explicit flag-provided script paths keep normal shell/cwd semantics. Bundled data preflight rejects symlinks, including symlinks nested in bundled directories, so size accounting and materialization operate on the declared tree only.

Run records include the script path for script jobs and the image URI for container jobs; human-readable `status` output shows whichever target is present. Attempt records include optional resume checkpoint and cost estimate provenance. Routing decisions include selected hardware, estimated VRAM/runtime/cost, confidence, and rejected hardware reasons when hardware routing is active. GCP hardware shapes include live zones and quota fields when Compute Engine APIs are reachable. Existing SQLite databases are migrated in place by adding missing run, attempt, and routing-decision columns when opened.

Before writing logs, `events.jsonl`, summaries, or provider failure reasons, providers and orchestration code redact secret-like keys and known secret values from job/runtime/inherited environment variables. Credentials stored in `credentials.enc` are for provider auth resolution only and must not be copied into SQLite, run summaries, events, or logs. Do not add new persistence paths without using the redaction utility.

## Next Steps

1. Run and document the first gated Lambda live smoke against real Lambda Cloud infrastructure.
2. Keep the gated billable GCP container submit smoke test current with `SWITCHBOARD_GCP_LIVE=1` and `SWITCHBOARD_GCP_LIVE_SUBMIT=1`.
3. Keep the PyTorch Iris GCP image build repeatable on `linux/amd64`, especially for Apple Silicon development machines.
4. Promote one China cloud readiness adapter into a real image-based compute adapter only after auth, submit, status, logs, cancel, and artifact semantics are specified.
5. Add more cloud resume examples once additional providers can consume shared checkpoint URIs.
6. Add Lambda private-registry and S3 checkpoint examples if the live smoke path is stable.
