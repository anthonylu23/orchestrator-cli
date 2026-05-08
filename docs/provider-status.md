# Provider Status

This project is pre-alpha. Provider labels describe the current repo state, not a production support promise.

| Provider | Category | Status | Live verified | Notes |
| --- | --- | --- | ---: | --- |
| `local` | Execution | Alpha foundation | Yes | Runs local Python eval workloads, captures logs, artifacts, evidence, and run history. |
| `mock-cloud` | Test execution | Test-only | Yes | Used for deterministic conformance, routing, retry, failover, and checkpoint simulation. Not a real cloud provider. |
| `mock-lambda` | Test execution | Test-only | Yes | Failover demo provider used by `examples/failover.yaml`. |
| `mock-gcp` | Test execution | Test-only | Yes | Failover demo provider used by `examples/failover.yaml`. This is not the colleague's real GCP branch. |
| `modal-sandbox` | Execution | Experimental | No | Implemented behind the provider interface. Requires Modal CLI, Python package, and auth. Live verification is gated by `CLOUDTUNE_INTEGRATION=modal`. |
| GCP / Vertex AI | Execution | Not merged | No | Keep out until the core provider lifecycle, packaging, and conformance expectations are stable. |
| RunPod Serverless | Execution | Planned | No | Candidate second real compute provider after Modal is live verified. |
| OpenAI Batch | Model API | Planned | No | Belongs under a future model-provider taxonomy, not the current execution-provider interface. |

## Status Rules

- `Alpha foundation`: works locally and is covered by regular tests.
- `Test-only`: exists to exercise core behavior, not to represent real infrastructure.
- `Experimental`: implemented but not yet proven against a real provider account.
- `Planned`: roadmap item with no supported implementation in this branch.

Do not mark a provider as live verified without a passing transcript or CI run for its opt-in integration test.
