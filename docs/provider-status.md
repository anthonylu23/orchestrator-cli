# Provider Status

This project is pre-alpha. Provider labels describe the current repository state, not a production support promise.

| Provider | Category | Status | Live verified | Notes |
| --- | --- | --- | ---: | --- |
| `local` | Execution | Alpha foundation | Yes | Runs local Python workloads, materializes bundled data, captures logs/events/summaries, and supports cancelation. |
| `mock-gcp` | Test execution | Test-only | Yes | Used for deterministic routing, failover, checkpoint, and summary behavior. Not a real cloud provider. |
| `mock-lambda` | Test execution | Test-only | Yes | Used for deterministic routing, failover, checkpoint, and summary behavior. Not a real cloud provider. |
| `gcp` | Execution | First provider milestone | Yes | Submits Vertex AI CustomJobs from container images, supports managed Docker build/push before submit, reads logs, tracks CustomJob resources, and supports GCS checkpoint URIs. |
| `lambda` | Execution | Initial adapter milestone | Yes | Launches one Lambda Cloud instance, runs an image through cloud-init, collects logs/events over SSH, tracks instance resources, and terminates by default. |
| `alibaba-cloud` | Readiness | Readiness-only | Auth check only | Validates credentials and signed auth probes; job submission is intentionally unsupported. |
| `huawei-cloud` | Readiness | Readiness-only | Auth check only | Validates credentials and signed auth probes; job submission is intentionally unsupported. |
| `tencent-cloud` | Readiness | Readiness-only | Auth check only | Validates credentials and signed auth probes; job submission is intentionally unsupported. |
| `tianyi-cloud` | Readiness | Readiness-only | Auth check only | Validates credentials and signed auth probes; job submission is intentionally unsupported. |
| `baidu-ai-cloud` | Readiness | Readiness-only | Auth check only | Validates credentials and signed auth probes; job submission is intentionally unsupported. |

## Status Rules

- `Alpha foundation`: works locally and is covered by regular tests.
- `Test-only`: exists to exercise core behavior, not to represent real infrastructure.
- `First provider milestone`: executes real cloud jobs for the documented scope, with known v1 limits.
- `Initial adapter milestone`: executes a narrow real-provider workflow with documented lifecycle limits.
- `Readiness-only`: validates provider access or metadata but does not submit jobs.

Do not mark a provider as live verified without a passing transcript, CI run, or documented smoke against the real provider.
