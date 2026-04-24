# Orchestrator CLI Roadmap

## Strategy

Build the orchestration system before chasing broad cloud coverage. Local and mock providers come first because they prove lifecycle, telemetry, routing, failure handling, and resume behavior quickly and repeatably.

GCP is the first real provider target after the architecture is proven because it is well documented, conventional for ML infrastructure, and strong for systems credibility.

## Phase 0 - Spec and Scaffold

Status: substantially complete. The repo now includes the Go module scaffold, docs, CLI entrypoint, package layout, test conventions, and the initial provider contract.

Goals:

1. Finalize docs, architecture, package layout, CLI command list, and provider contract.
2. Create the Go project scaffold.
3. Establish test conventions and adapter contract test shape.

Exit criteria:

1. Docs clearly explain product scope, architecture, roadmap, and provider extensibility.
2. Implementation can start without re-deciding stack, provider order, run model, or artifact strategy.

## Phase 1 - Local Orchestration Vertical Slice

Status: substantially complete. The local provider can execute real scripts from a per-run workspace, materialize bundled local data under stable `/workspace` mounts, persist SQLite run and attempt state, parse structured JSONL events from mixed output, follow logs for active runs, cancel active local processes, and write `events.jsonl`, `logs.txt`, and `summary.json`.

Next steps:

1. Harden diagnostics and exit codes around provider and data preparation failures.
2. Add broader provider adapter contract tests before the first real cloud adapter.
3. Prepare GCP provider scaffolding.

Goals:

1. Implement CLI scaffold and config loading.
2. Implement SQLite-backed run and attempt state.
3. Implement the `local` provider for real local script execution.
4. Add local bundled data inputs with preflight path validation and bundle size checks.
5. Ingest mixed stdout with JSONL metric, checkpoint, status, and log events.
6. Persist `events.jsonl`, `summary.json`, and logs.

Target commands:

```sh
orchestrator-cli train --provider local --script examples/train.py
orchestrator-cli status <run-id>
orchestrator-cli logs <run-id> --follow
orchestrator-cli cancel <run-id>
```

Exit criteria:

1. A user can run an example ML script locally through Orchestrator.
2. Orchestrator stores run state and attempts in SQLite.
3. Local training/test data is materialized at stable workspace paths.
4. Oversized local data bundles require an explicit override.
5. Logs and structured metrics are visible after the run.
6. Success and failure both produce durable artifacts.

## Phase 2 - Mock Cloud and Failure Simulation

Status: substantially complete. The mock providers support configurable costs, scripted events, retryable failure modes, `provider=auto` routing, persisted routing decisions, checkpoint discovery from `events.jsonl`, and resume into a second attempt under one run.

Goals:

1. Implement a `mock` provider with configurable costs, capabilities, logs, and failure modes.
2. Add routing over `local` and mock provider variants.
3. Simulate capacity errors and runtime failures.
4. Simulate URI data fetch success, auth failure, and unavailable remote data.
5. Discover the latest checkpoint and resume a new attempt with `SubmitRequest.ResumeFrom`.

Target demo:

```sh
orchestrator-cli train --provider auto --config examples/failover.yaml
```

Expected behavior:

```text
Selected mock-lambda: compatible, estimated $1.10/hr
Attempt a_1 failed: capacity interruption
Found checkpoint: step 800
Resuming on mock-gcp
Run completed
```

Exit criteria:

1. A forced provider failure resumes from the latest checkpoint on another adapter.
2. The run history shows multiple attempts under one run.
3. Mock URI data inputs are represented in the data manifest and runtime bundle.
4. Routing decisions include selected, eligible, and rejected providers with reasons.

## Phase 3 - Provider Extensibility Hardening

Goals:

1. Add provider adapter contract tests.
2. Normalize provider error categories and retryability.
3. Add adapter contract checks for bundled data and supported URI schemes.
4. Harden capability matching and support reports.
5. Improve diagnostics for auth, invalid spec, data preparation, capacity, quota, network, runtime, and internal failures.

Exit criteria:

1. Adding a new provider does not require core orchestration changes.
2. Adapter tests verify submit/status/logs/cancel behavior, data input handling, capability reporting, and error mapping.
3. Routing and failure decisions are explainable from persisted state.

## Phase 4 - First Real Provider: GCP

Goals:

1. Implement GCP auth validation.
2. Submit, status, logs, and cancel a real training job.
3. Add basic GCP cost estimation and capability reporting.
4. Define GCP staging behavior for bundled local data and URI-backed data.
5. Document an end-to-end GCP example.

Exit criteria:

1. A single command can launch and track a real GCP training run.
2. GCP can satisfy the documented data input contract or reject unsupported data modes clearly.
3. GCP behavior passes the same adapter contract expectations as local/mock where applicable.
4. Provider-specific errors are normalized before reaching orchestration code.

## Later Phases

1. Add Lambda and Hyperbolic adapters.
2. Add explicit Docker image build/package workflow.
3. Add GCS and S3 checkpoint backends.
4. Add basic experiment fan-out.
5. Add richer terminal attach views.
6. Explore optional hosted run history and team visibility after CLI adoption.

## Two-Week Success Target

The initial two-week milestone is not three-provider cloud coverage. It is a polished systems demo:

1. Run an example ML training script locally.
2. Stream logs and metrics.
3. Materialize bundled local training/test data at stable paths.
4. Persist SQLite state and durable artifacts.
5. Simulate cloud provider failure.
6. Discover a checkpoint.
7. Resume on another adapter.
8. Prove provider extensibility through adapter contract tests.

## Validation Matrix

1. Local lifecycle: train, status, logs, cancel, success, and failure.
2. Event ingestion: mixed stdout, JSONL metrics, checkpoints, statuses, and plain logs.
3. Data handling: bundled files/directories, URI inputs, size limit override, mounted paths, and secret redaction.
4. State persistence: runs, attempts, routing decisions, summaries, and exit reasons.
5. Routing: cheapest eligible provider under fixed constraints and declared data requirements.
6. Failure handling: retryable provider failures trigger alternate attempts.
7. Resume: latest checkpoint is passed through `SubmitRequest.ResumeFrom`.
8. Provider contract: local, mock, and future real providers satisfy the same core expectations.
