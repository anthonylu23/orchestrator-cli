# GCP Provider

## Status

The GCP provider is implemented as a Vertex AI CustomJob adapter using the Google Cloud Go client. The first version is container-image-first: Switchboard submits a user-provided container image to one Vertex AI worker pool and polls the CustomJob until it reaches a terminal state.

Local script packaging, Docker image builds, source distribution upload, non-GCS data fetching, and multi-worker distributed training are deferred.

The current packaging decision is to keep GCP image-first while the provider stabilizes, then add Switchboard-managed Docker build/push as the next packaging feature. See [GCP Packaging Decision](gcp-packaging-decision.md).

## Authentication

The provider uses Application Default Credentials. Configure ADC before running:

```sh
gcloud auth application-default login
gcloud config set project <project-id>
```

The caller needs permissions to create, read, and cancel Vertex AI CustomJobs and to read Cloud Logging entries for the project. If `gcp.service_account` is set, the caller also needs permission to act as that service account.

Billing must be enabled on the target project. Live auth and a CPU-only Vertex AI CustomJob smoke test passed on 2026-05-17 for project `switchboard-496606`. See [GCP Live Smoke Test](gcp-live-smoke.md).

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
  output_uri_prefix: gs://my-bucket/switchboard-outputs
  machine_type: n1-standard-8
  accelerator_type: NVIDIA_TESLA_T4
  accelerator_count: 1
  boot_disk_type: pd-ssd
  boot_disk_size_gb: 100
  estimate_hourly_usd: 2.5
```

Run it with:

```sh
switchboard-cli train --provider gcp --config gcp.yaml
```

## Supported Inputs

GCP v1 supports `job.image` and optional `job.command`, `job.args`, and `job.env`. It does not package `job.script`.

Data inputs must use `mode: uri` with `gs://` sources. Bundled local data, `s3://`, `http://`, and `https://` inputs are rejected before submit with provider support reasons.

## Runtime Behavior

Switchboard creates one Vertex AI CustomJob with a single worker pool. The provider stores the full CustomJob resource name as the attempt provider ref, writes provider lifecycle messages and Cloud Logging payloads into `logs.txt`, parses structured JSONL metric/checkpoint/status lines into `events.jsonl`, and records the configured hourly estimate on the attempt.

`switchboard-cli cancel <run-id>` can cancel a running GCP attempt by using the stored CustomJob resource name.

GCP containers receive remote-safe runtime environment paths such as `/tmp/switchboard/checkpoints` and `/tmp/switchboard/events.jsonl`. Structured events should still be printed to stdout so Cloud Logging can mirror them back into local Switchboard artifacts. GCP v1 validates `gs://` data inputs but does not mount them into the container; the image should read or download those URIs itself.

## PyTorch Iris Container Demo

The first realistic GCP workload is the same PyTorch Iris model used by the local demo, wrapped in a container entrypoint that downloads the CSV from GCS:

```sh
gcloud storage cp examples/data/iris/Iris.csv gs://<bucket>/switchboard-demo/iris/Iris.csv

gcloud artifacts repositories create switchboard \
  --repository-format=docker \
  --location=us-central1

gcloud auth configure-docker us-central1-docker.pkg.dev
docker build -f examples/gcp/iris/Dockerfile \
  -t us-central1-docker.pkg.dev/<project>/switchboard/iris-pytorch:latest .
docker push us-central1-docker.pkg.dev/<project>/switchboard/iris-pytorch:latest
```

Make sure the Vertex AI runtime service account can read the GCS object and Artifact Registry image. Then update `examples/gcp-iris.yaml` with the project, bucket, and image URI and run:

```sh
SWITCHBOARD_CLI_HOME="$(mktemp -d)" \
switchboard-cli train --provider gcp --config examples/gcp-iris.yaml
```

## Next Steps

1. Keep the live smoke path current with the `switchboard-496606` smoke bucket and a supported Vertex prebuilt image.
2. Run the containerized PyTorch Iris demo as the first realistic GCP training workload.
3. Add Switchboard-managed Docker build/push support for local scripts after the Iris container path is repeatable.
4. Replace static `estimate_hourly_usd` with provider pricing lookup when hardware routing work begins.
5. Expand data staging beyond `gs://` after GCS checkpoint and dataset behavior is stable.
