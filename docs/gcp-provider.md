# GCP Provider

## Status

The GCP provider is implemented as a Vertex AI CustomJob adapter using the Google Cloud Go client. The first version is container-image-first: Orchestrator submits a user-provided container image to one Vertex AI worker pool and polls the CustomJob until it reaches a terminal state.

Local script packaging, Docker image builds, source distribution upload, non-GCS data fetching, and multi-worker distributed training are deferred.

## Authentication

The provider uses Application Default Credentials. Configure ADC before running:

```sh
gcloud auth application-default login
gcloud config set project <project-id>
```

The caller needs permissions to create, read, and cancel Vertex AI CustomJobs and to read Cloud Logging entries for the project. If `gcp.service_account` is set, the caller also needs permission to act as that service account.

Billing must be enabled on the target project. A live auth check on 2026-05-10 reached Vertex AI for project `lfp-temporal-vit` but failed with `BILLING_DISABLED`, so the current live blocker is project setup rather than an implementation regression. See [GCP Live Smoke Test](gcp-live-smoke.md).

## Config

```yaml
job:
  name: gcp-container-demo
  image: us-docker.pkg.dev/my-project/training/train:latest
  command: ["python", "-m", "trainer"]
  args: ["--train-data", "gs://my-bucket/data/train"]
  env:
    EPOCHS: "3"

data:
  inputs:
    - name: train
      source: gs://my-bucket/data/train
      mode: uri

gcp:
  project_id: my-project
  location: us-central1
  output_uri_prefix: gs://my-bucket/orchestrator-outputs
  machine_type: n1-standard-8
  accelerator_type: NVIDIA_TESLA_T4
  accelerator_count: 1
  boot_disk_type: pd-ssd
  boot_disk_size_gb: 100
  estimate_hourly_usd: 2.5
```

Run it with:

```sh
orchestrator-cli train --provider gcp --config gcp.yaml
```

## Supported Inputs

GCP v1 supports `job.image` and optional `job.command`, `job.args`, and `job.env`. It does not package `job.script`.

Data inputs must use `mode: uri` with `gs://` sources. Bundled local data, `s3://`, `http://`, and `https://` inputs are rejected before submit with provider support reasons.

## Runtime Behavior

Orchestrator creates one Vertex AI CustomJob with a single worker pool. The provider stores the full CustomJob resource name as the attempt provider ref, writes provider lifecycle messages and Cloud Logging payloads into `logs.txt`, parses structured JSONL metric/checkpoint/status lines into `events.jsonl`, and records the configured hourly estimate on the attempt.

`orchestrator-cli cancel <run-id>` can cancel a running GCP attempt by using the stored CustomJob resource name.

## Next Steps

1. Enable billing and run the auth-only live check documented in [GCP Live Smoke Test](gcp-live-smoke.md).
2. Run the gated billable container submit smoke test with a small image and GCS output prefix.
3. Add packaging or build/push support for local scripts after the container-only path is validated.
4. Replace static `estimate_hourly_usd` with provider pricing lookup when hardware routing work begins.
5. Expand data staging beyond `gs://` after GCS checkpoint and dataset behavior is stable.
