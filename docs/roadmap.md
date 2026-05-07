# CloudTune Orchestrator Roadmap

## Strategy

Build reliable execution before broad provider coverage. The first product should prove that CloudTune can run workloads, track state, preserve artifacts, expose history, retry intelligently, and explain provider choices. Real provider count matters only after those basics hold under stress.

## Phase 1 - Reliable Execution

Status: in progress and usable locally.

Implemented:

1. local runner for Python scripts.
2. SQLite run and attempt state.
3. logs, JSONL events, summaries, workload metadata, artifact manifests.
4. `cloudtune run`, `status`, `logs`, `artifacts`, `runs`, and `cancel`.
5. `CLOUDTUNE_*` runtime environment.
6. output export to `<outputs.save_to>/<run-id>/`.

Exit criteria:

1. local eval workload completes.
2. result artifact is saved and listed.
3. run history survives process restart.
4. failure and cancellation produce durable summaries.

## Phase 2 - Multi-Provider Routing

Status: mock-provider foundation exists.

Implemented:

1. deterministic mock providers with costs and failure modes.
2. `provider=auto` routing.
3. persisted routing decisions.
4. retryable failure handling and provider exclusion.

Next:

1. add one real provider adapter without merging unrelated branch work blindly.
2. add provider health checks.
3. add provider capability details for workload type, data location, GPU shape, and region.
4. keep rejection reasons visible in run state.

## Phase 3 - Checkpoint And Resume

Status: metadata path exists through mock checkpoint events.

Next:

1. introduce a checkpoint registry abstraction.
2. validate provider-compatible checkpoint locations.
3. distinguish same-provider resume from cross-provider migration.
4. require human approval when resume would exceed budget.

## Phase 4 - Eval And Governance

Status: workload/eval metadata is persisted; gate enforcement is not implemented.

Next:

1. define eval result schema.
2. add deployment gate rules.
3. persist dataset/model/config hashes.
4. record approvals and policy decisions.
5. export audit evidence for Axiom or a future governance ledger.

## Phase 5 - Enterprise Control Plane

Future:

1. hosted API and dashboard.
2. teams, RBAC, SSO.
3. cost centers and budget policies.
4. audit exports.
5. provider usage analytics.

## Validation Matrix

1. local lifecycle: run, status, logs, artifacts, history, cancel.
2. event ingestion: metrics, checkpoints, statuses, plain logs.
3. artifacts: summaries, workload metadata, outputs, checkpoints, manifest.
4. state persistence: runs, attempts, routing decisions, exit reasons.
5. routing: cheapest eligible provider under declared constraints.
6. failure handling: retryable provider failures trigger alternate attempts.
7. resume: latest checkpoint is passed through `SubmitRequest.ResumeFrom`.
8. adapter contract: providers satisfy a stable core interface.
