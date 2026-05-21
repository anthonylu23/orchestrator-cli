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
3. Keep the PyTorch Iris GCP image build repeatable on `linux/amd64` after the successful Cloud Build and Vertex smoke.

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

Status: in progress. A shared provider contract harness now covers local, mock, fake-backed GCP, and fake-backed Lambda adapter identity, auth validation, capabilities, job validation, estimates, submit/status behavior, log streaming expectations, and cancel behavior. Routing tests cover capability and support-report rejection paths, CLI tests pin key diagnostic exit categories, and provider resource lifecycle records now track cloud resources separately from attempts.

Goals:

1. Expand provider adapter contract tests as new provider capabilities are introduced.
2. Keep provider error categories and retryability normalized through `ProviderError`.
3. Keep adapter contract checks for bundled data and supported URI schemes current.
4. Continue hardening capability matching and support reports after core routing rejection.
5. Improve diagnostics for auth, invalid spec, data preparation, capacity, quota, network, runtime, and internal failures.
6. Keep provider resource tracking lightweight: record execution resources first, and add adoption/reuse only when a provider workflow requires it.

Exit criteria:

1. Adding a new provider does not require core orchestration changes.
2. Adapter tests verify submit/status/logs/cancel behavior, data input handling, capability reporting, and error mapping.
3. Routing, resume, and failure decisions are explainable from persisted state.

## Phase 4 - First Real Provider: GCP

Status: substantially complete for the first provider milestone. The GCP adapter submits container images to Vertex AI CustomJob through the Google Cloud Go client, polls job status, records provider refs, resource records, and estimates, reads Cloud Logging payloads into run artifacts, supports best-effort cancel, and rejects unsupported local bundles or non-GCS URI inputs before submit. Live auth and a billable CPU-only Vertex AI CustomJob smoke test passed on 2026-05-17 for project `switchboard-496606`; live pricing/capacity and PyTorch Iris container smokes passed on 2026-05-20. The PyTorch Iris container example provides the first realistic GCP training workload, the CLI can now build/push Docker images before submit for GCP script jobs, and GCP capability reporting can use live Cloud Billing/Compute pricing, inventory, and regional quota facts with static fallback.

Goals:

1. Validate GCP auth through Application Default Credentials.
2. Submit, status, logs, and cancel a Vertex AI CustomJob from a prebuilt container image.
3. Add GCP cost estimation and capability reporting with live API enrichment and static fallback.
4. Reject unsupported bundled data and non-GCS URI-backed data clearly.
5. Document a GCP container-image example.

Exit criteria:

1. A single command can launch and track a real GCP training run from a prebuilt container image.
2. GCP can satisfy the documented data input contract or reject unsupported data modes clearly.
3. GCP behavior passes the same adapter contract expectations as local/mock where applicable.
4. Provider-specific errors are normalized before reaching orchestration code.

Next steps:

1. Keep the GCP live smoke path repeatable against `switchboard-496606`.
2. Keep the PyTorch Iris image build repeatable on `linux/amd64` and rerun it when GCP provider behavior changes.
3. Keep Switchboard-managed image build/push covered by fake-runner tests and documented Artifact Registry auth guidance.
4. Keep the gated Cloud Billing/Compute pricing and capacity smoke current.

## Phase 5 - Auto Hardware Routing

Status: first single-node implementation complete. The router can select provider and hardware shape for `full_auto`, `auto_provider`, and `manual`, reject shapes for memory/budget/constraint/no-quota reasons, persist selected hardware and estimates, and include the decision in summaries. GCP reports a configured shape plus a catalog enriched by live Cloud Billing and Compute inventory/quota APIs when available.

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

## Phase 6 - Lambda Cloud Adapter

Status: initial implementation complete with live smoke coverage. The adapter can be configured as `provider=lambda`, validates Lambda image-first jobs, discovers instance types through the Lambda Cloud API, launches one on-demand instance with cloud-init, runs a Docker image, collects logs/events over SSH, records run artifacts, resolves Lambda API keys from the encrypted local credential store, persists instance resource lifecycle records, and terminates the instance by default. Gated auth, submit, failure-cleanup, cancel, and public CLI smokes passed against real Lambda Cloud infrastructure on 2026-05-20.

Goals:

1. Validate Lambda API auth with encrypted `lambda/api_key`.
2. Map Lambda instance types, prices, regions, and capacity into provider capabilities.
3. Launch one Lambda instance for a prebuilt container-image job.
4. Collect `/tmp/switchboard/logs.txt`, `/tmp/switchboard/events.jsonl`, and `/tmp/switchboard/exit.json` over SSH.
5. Terminate successful smoke instances automatically and support keeping failed instances for debugging.

Exit criteria:

1. A smoke job can run on real Lambda infrastructure through `switchboard-cli train --provider lambda`.
2. The run produces SQLite state, `logs.txt`, `events.jsonl`, and `summary.json`.
3. The test instance is terminated after successful completion.
4. Lambda instance resources are visible through `switchboard-cli resources list`.
5. Lambda behavior passes fake-backed provider contract checks.
6. Current limits and live-smoke workflow are documented in [Lambda Cloud Provider](lambda-provider.md).

Next steps:

1. Keep gated Lambda live smoke coverage current after lifecycle or cloud-init changes.
2. Add private registry guidance if Docker pull auth is needed.
3. Add S3 checkpoint examples for portable Lambda resume.
4. Decide whether retained Lambda instances should support explicit refresh/adoption.
5. Decide whether Lambda filesystems should become first-class staging or checkpoint backends.

## Later Phases

1. Add Hyperbolic and/or RunPod adapters after the Lambda live smoke is stable.
2. Add GCS and S3 checkpoint backends and provider examples that emit shared checkpoint URIs.
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
5. Routing: cheapest eligible provider by default; fastest-within-budget provider and hardware selection under declared sizing, budget, and data requirements when hardware routing is active.
6. Failure handling: retryable provider failures trigger alternate attempts.
7. Resume: latest checkpoint is passed through `SubmitRequest.ResumeFrom` only when the selected provider supports the checkpoint URI scheme.
8. Provider resources: GCP CustomJobs and Lambda instances are tracked separately from attempts and can be inspected after a run.
9. Provider contract: local, mock, and future real providers satisfy the same core expectations.
