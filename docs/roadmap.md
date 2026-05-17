# Switchboard CLI Roadmap

## Strategy

Build the orchestration system before chasing broad cloud coverage. Local and mock providers come first because they prove lifecycle, telemetry, routing, failure handling, and resume behavior quickly and repeatably.

GCP is the first real provider target after the architecture is proven because it is well documented, conventional for ML infrastructure, and strong for systems credibility. Auto hardware routing should build on real provider capability and pricing data once the provider boundary is stable.

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

Status: substantially complete. The local provider can execute real scripts from a per-run workspace, materialize bundled local data under stable `/workspace` mounts, persist SQLite run and attempt state, parse structured JSONL events from mixed output, follow logs for active runs with a final drain on completion/cancelation, cancel active local processes, redact secrets before persistence, and write `events.jsonl`, `logs.txt`, and `summary.json`. The PyTorch Iris demo now provides a small framework-backed local deep learning workload using a checked-in CC0 Kaggle CSV fixture.

Next steps:

1. Harden diagnostics and exit codes around provider and data preparation failures.
2. Add broader provider adapter contract tests before the first real cloud adapter.
3. Containerize or package the PyTorch Iris demo after the GCP container-image path is validated.

Goals:

1. Implement CLI scaffold and config loading.
2. Implement SQLite-backed run and attempt state.
3. Implement the `local` provider for real local script execution.
4. Add local bundled data inputs with preflight path validation and bundle size checks.
5. Ingest mixed stdout with JSONL metric, checkpoint, status, and log events.
6. Persist `events.jsonl`, `summary.json`, and logs.

Target commands:

```sh
switchboard-cli train --provider local --script examples/train.py
switchboard-cli status <run-id>
switchboard-cli logs <run-id> --follow
switchboard-cli cancel <run-id>
```

Exit criteria:

1. A user can run an example ML script locally through Switchboard.
2. Switchboard stores run state and attempts in SQLite.
3. Local training/test data is materialized at stable workspace paths.
4. Oversized local data bundles require an explicit override.
5. Logs and structured metrics are visible after the run.
6. Success and failure both produce durable artifacts.

## Phase 2 - Mock Cloud and Failure Simulation

Status: substantially complete. The mock providers support configurable costs, scripted events, retryable failure modes, `provider=auto` routing, persisted routing decisions, checkpoint discovery from `events.jsonl`, and resume into a second attempt under one run with persisted resume and estimate provenance.

Goals:

1. Implement a `mock` provider with configurable costs, capabilities, logs, and failure modes.
2. Add routing over `local` and mock provider variants.
3. Simulate capacity errors and runtime failures.
4. Simulate URI data fetch success, auth failure, and unavailable remote data.
5. Discover the latest checkpoint and resume a new attempt with `SubmitRequest.ResumeFrom`.

Target demo:

```sh
switchboard-cli train --provider auto --config examples/failover.yaml
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

Status: in progress. A shared provider contract harness now covers local, mock, and fake-backed GCP adapter identity, auth validation, capabilities, job validation, estimates, submit/status behavior, log streaming expectations, and cancel behavior. Routing tests cover capability and support-report rejection paths, and CLI tests pin key diagnostic exit categories.

Goals:

1. Expand provider adapter contract tests as new provider capabilities are introduced.
2. Keep provider error categories and retryability normalized through `ProviderError`.
3. Keep adapter contract checks for bundled data and supported URI schemes current.
4. Continue hardening capability matching and support reports after core routing rejection.
5. Improve diagnostics for auth, invalid spec, data preparation, capacity, quota, network, runtime, and internal failures.

Exit criteria:

1. Adding a new provider does not require core orchestration changes.
2. Adapter tests verify submit/status/logs/cancel behavior, data input handling, capability reporting, and error mapping.
3. Routing, resume, and failure decisions are explainable from persisted state.

## Phase 4 - First Real Provider: GCP

Status: in progress. The first GCP adapter submits prebuilt container images to Vertex AI CustomJob through the Google Cloud Go client, polls job status, records provider refs and estimates, reads Cloud Logging payloads into run artifacts, supports best-effort cancel, and rejects unsupported local bundles or non-GCS URI inputs before submit. Live auth and a billable CPU-only Vertex AI CustomJob smoke test passed on 2026-05-17 for project `switchboard-496606`; packaging workflows are still pending.

Goals:

1. Validate GCP auth through Application Default Credentials.
2. Submit, status, logs, and cancel a Vertex AI CustomJob from a prebuilt container image.
3. Add basic static GCP cost estimation and capability reporting.
4. Reject unsupported bundled data and non-GCS URI-backed data clearly.
5. Document a GCP container-image example.

Exit criteria:

1. A single command can launch and track a real GCP training run from a prebuilt container image.
2. GCP can satisfy the documented data input contract or reject unsupported data modes clearly.
3. GCP behavior passes the same adapter contract expectations as local/mock where applicable.
4. Provider-specific errors are normalized before reaching orchestration code.

Next steps:

1. Keep the GCP live smoke path repeatable against `switchboard-496606`.
2. Decide whether local script packaging should use Switchboard-managed image build/push or a Vertex AI source package path.
3. Containerize the PyTorch Iris demo as the first realistic GCP training workload.
4. Add richer GCP pricing/capacity facts as part of auto hardware routing.

## Phase 5 - Auto Hardware Routing

Goals:

1. Add a concrete config schema for `full_auto`, `auto_provider`, and `manual` routing modes.
2. Add a probe-first sizing profile contract for memory and runtime estimation.
3. Extend provider capabilities to report concrete GPU shapes, VRAM, supported single-node GPU counts, regions, quota/capacity facts, and pricing.
4. Route by fastest compatible hardware within a max estimated run cost.
5. Persist selected provider, selected hardware, estimated VRAM, estimated runtime, estimated total cost, confidence, and rejection reasons.

Exit criteria:

1. Full-auto can select provider, GPU shape, and 1-N GPUs on one node from mock hardware catalogs.
2. Auto-provider can honor user-selected hardware constraints while choosing the provider.
3. Manual mode bypasses automatic provider/hardware choice but still validates compatibility.
4. Low-confidence sizing fails before submit with clear guidance instead of overprovisioning.
5. The design is documented in [Auto Hardware Routing](auto-hardware-routing.md).

## Later Phases

1. Add Lambda and Hyperbolic adapters.
2. Add explicit Docker image build/package workflow.
3. Add GCS and S3 checkpoint backends.
4. Add multi-node and distributed training topology after single-node auto hardware proves useful.
5. Add basic experiment fan-out.
6. Add richer terminal attach views.
7. Explore optional hosted run history and team visibility after CLI adoption.

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
5. Routing: cheapest eligible provider today; planned fastest-within-budget provider and hardware selection under declared sizing, budget, and data requirements.
6. Failure handling: retryable provider failures trigger alternate attempts.
7. Resume: latest checkpoint is passed through `SubmitRequest.ResumeFrom`.
8. Provider contract: local, mock, and future real providers satisfy the same core expectations.
