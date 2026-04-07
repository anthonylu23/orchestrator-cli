# Caboose CLI - Phases and Roadmap

## Roadmap Summary
Build from an open-source local CLI foundation to stable multi-provider orchestration with checkpoint failover, then optionally add hosted team features after product-market fit.

### Strategic Defaults
1. Distribution now: Open-source CLI.
2. Credentials now: BYO provider keys/accounts.
3. Billing now: Deferred.
4. Billing later: Possible unified billing after PMF validation.

## Phase Plan

## Phase 1 - Foundation (Weeks 1-4)

### Goals
1. Create Go CLI scaffold and core commands.
2. Implement config system (flags + YAML with flag override).
3. Create local run state model and persistence.
4. Add container packaging and preflight validation.

### Deliverables
1. Command surface: `train`, `status`, `logs`, `cancel`, `providers list`.
2. Job spec parser and validation pipeline.
3. Local run metadata store under `~/.caboose/runs`.
4. Initial error taxonomy and exit code handling.

### Exit Criteria
User can package a job, pass preflight checks, and produce a runnable cloud job spec artifact.

## Phase 2 - First Provider End-to-End (Weeks 5-8)

### Goals
1. Implement GCP adapter.
2. Support submit, status, logs, and cancel flow.
3. Integrate first-pass cost estimation.

### Deliverables
1. Provider auth checks.
2. End-to-end run lifecycle on one provider.
3. Stable provider error mapping to CLI error codes.

### Exit Criteria
Single command can launch and track a real GCP training run end-to-end.

## Phase 3 - Multi-Provider and Cost Routing (Weeks 9-12)

### Goals
1. Add Lambda and Hyperbolic adapters.
2. Implement capability/resource filtering.
3. Add `provider=auto` min-cost routing objective.

### Deliverables
1. Unified adapter contract across 3 providers.
2. Routing engine with provider scoring and selection reason output.
3. Fallback selection when top provider is unavailable.

### Exit Criteria
Same workload can run on all three providers, and auto mode chooses the cheapest valid target.

## Phase 4 - Checkpointing and Failover Resume (Weeks 13-16)

### Goals
1. Implement checkpoint backend abstraction (`local`, `gcs`, `s3`).
2. Add resume manager and failover orchestration logic.
3. Add provider failure handling policy and attempt tracking.

### Deliverables
1. Checkpoint discovery and validity checks.
2. Resume metadata injection for restarted jobs.
3. Provider exclusion policy on repeated outage/capacity errors.

### Exit Criteria
When a provider fails mid-run, Caboose resumes from latest checkpoint on an alternate provider.

## Phase 5 - Telemetry and Terminal UX (Weeks 17-19)

### Goals
1. Ingest structured metric JSONL emitted by training runtime.
2. Add attach UX for live log and metric viewing.
3. Persist durable telemetry artifacts per run.

### Deliverables
1. `caboose attach <run-id>` and `--metrics-only` mode.
2. `events.jsonl` (full stream) and `summary.json` (aggregates/outcome).
3. JSON schema for `metric`, `checkpoint`, and `status` events.

### Exit Criteria
Users can monitor loss/accuracy live in terminal and review complete saved telemetry after job completion.

## Phase 6 - Parallel Fan-Out and Hardening (Weeks 20-22)

### Goals
1. Add independent fan-out run groups for experiments/sweeps.
2. Aggregate run-group status, logs, and summaries.
3. Improve retries, diagnostics, and docs for production use.

### Deliverables
1. Matrix/fan-out execution support.
2. Robust retry and error messaging policies.
3. Examples, quickstarts, and troubleshooting guides.

### Exit Criteria
Fan-out workflows run reliably across providers with clear observability and artifact outputs.

## Phase 7 - Optional Hosted Control Plane (Post-PMF)

### Goals
1. Add shared run history, dashboards, alerts, and team policy controls.
2. Preserve full local-orchestrator mode.
3. Keep unified billing out of initial hosted rollout.

### Exit Criteria
Team workflows improve without breaking OSS CLI-first adoption path.

## Milestones
1. M1: First successful real cloud run from Caboose (end Phase 2).
2. M2: Three-provider cost-aware auto routing live (end Phase 3).
3. M3: Checkpoint failover resume proven (end Phase 4).
4. M4: Live terminal metrics + durable telemetry artifacts (end Phase 5).
5. M5: Stable parallel fan-out experimentation workflow (end Phase 6).

## Test and Validation Matrix
1. Submission lifecycle: submit/status/logs/cancel across all providers.
2. Routing behavior: cheapest eligible provider selected under fixed GPU constraints.
3. Failure handling: capacity errors and hard outages trigger expected fallback.
4. Resume logic: checkpoint resume from provider A to provider B succeeds.
5. Telemetry: mixed stdout parsing works and summaries are correct.
6. Fan-out: grouped runs maintain accurate status and artifact links.

## Risks and Mitigations
1. Provider API drift.
Mitigation: Adapter contract tests and scheduled compatibility checks.
2. Cost estimate mismatch with real bills.
Mitigation: Show estimate confidence/range and expose provider quote details.
3. Resume incompatibility across environments.
Mitigation: Runtime/image pinning and resume preflight validation.
4. Scope creep into ETL/platform features.
Mitigation: Hold strict v1 non-goals and route non-core features to backlog.

## PMF Signals and Expansion Triggers
1. Median time-to-first-cloud-run below 15 minutes.
2. Growing weekly active users with repeat run behavior.
3. High usage of auto-routing and failover workflows.
4. Repeated demand for shared visibility and policy controls, triggering hosted phase investment.
