# GCP Provider

## Status

The GCP provider is implemented as a Vertex AI CustomJob adapter using the Google Cloud Go client. The provider boundary is image-first: Switchboard submits a container image to one Vertex AI worker pool and polls the CustomJob until it reaches a terminal state.

The CLI now has a pre-submit packaging layer for GCP script jobs. When `job.script` is configured with `packaging`, Switchboard runs `docker build`, pushes the image to Artifact Registry or `packaging.image`, rewrites the attempt to `job.image`, and then uses the same GCP provider path.

GCP capabilities are backed by live Google APIs when credentials and project permissions allow it: Cloud Billing Catalog public SKUs for on-demand pricing, Compute Engine aggregated machine/accelerator listings for regional inventory, and Compute Engine regional quotas for capacity signals. If those API calls are unavailable, Switchboard keeps the static catalog as a fallback and marks shapes with availability reasons.

GCP checkpoint/resume support is implemented through shared `gs://` checkpoint URIs emitted by training code. Switchboard provides a `SWITCHBOARD_CHECKPOINT_URI_PREFIX` under the job output prefix, parses checkpoint events from Cloud Logging into `events.jsonl`, and passes the latest compatible `gs://` checkpoint back as `SWITCHBOARD_RESUME_FROM` on a later attempt. When `staging.data_uri_prefix` is `gs://...`, bundled local data inputs are uploaded before submit and rewritten to GCS URI inputs. The GCP Iris example now uses the shared container data materializer to download declared `gs://` data inputs to their `/workspace` mounts inside the container.

Source distribution upload, non-GCS provider-facing data, and multi-worker distributed training are deferred.

See [GCP Packaging Decision](gcp-packaging-decision.md) for the packaging direction.

## Authentication

The provider uses Application Default Credentials. Configure ADC before running:

```sh
gcloud auth application-default login
gcloud config set project <project-id>
```

The caller needs permissions to create, read, and cancel Vertex AI CustomJobs and to read Cloud Logging entries for the project. If `gcp.service_account` is set, the caller also needs permission to act as that service account.

Billing must be enabled on the target project. Live auth and a CPU-only Vertex AI CustomJob smoke test passed on 2026-05-17 for project `switchboard-496606`. A non-billable live pricing/capacity smoke and a PyTorch Iris Vertex container run passed on 2026-05-20. See [GCP Live Smoke Test](gcp-live-smoke.md).

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

Managed image build/push config for a local script:

```yaml
job:
  name: gcp-script-demo
  script: examples/train.py
  args: ["--epochs", "3"]

packaging:
  dockerfile: Dockerfile
  context: .
  platform: linux/amd64
  # Optional. If omitted, Switchboard derives:
  # <location>-docker.pkg.dev/<project>/<repository>/switchboard-cli-<run-id>:latest
  image: us-central1-docker.pkg.dev/my-project/switchboard/train:latest

gcp:
  project_id: my-project
  location: us-central1
  output_uri_prefix: gs://my-bucket/switchboard-outputs
  artifact_registry_repository: switchboard
```

The Dockerfile is responsible for copying the script and dependencies into the image. If `job.command` is omitted, Switchboard runs Python with the script path relative to `packaging.context`. Packaging diagnostics validate the context directory and Docker platform before invoking Docker, and build/push failures include daemon, platform, registry-auth, and Artifact Registry guidance.

## Supported Inputs

GCP v1 supports `job.image` and optional `job.command`, `job.args`, and `job.env`. `job.script` is supported only through the pre-submit Docker packaging flow described above.

Provider-facing data inputs must use `mode: uri` with `gs://` sources. Bundled local data can be used when `staging.data_uri_prefix` is a `gs://` prefix; Switchboard uploads the files to that prefix before provider validation, rewrites the inputs to URI mode, and injects the staged `SWITCHBOARD_DATA_<NAME>_URI` env vars. Bundled data without GCS staging, `s3://`, `http://`, and `https://` inputs are rejected before submit with provider support reasons.

## Runtime Behavior

Switchboard creates one Vertex AI CustomJob with a single worker pool. The provider stores the full CustomJob resource name as the attempt provider ref, writes provider lifecycle messages and Cloud Logging payloads into `logs.txt`, parses structured JSONL metric/checkpoint/status lines into `events.jsonl`, records the configured, live-priced, or catalog-derived hourly estimate on the attempt, and persists a `custom_job` provider resource record.

The GCP resource record uses the CustomJob resource name as both `external_id` and `provider_ref`, stores the configured project and location, and has `cleanup_policy=never`. Vertex CustomJobs are lifecycle-controlled by canceling active jobs, not by deleting them through Switchboard cleanup.

`switchboard-cli cancel <run-id>` can cancel a running GCP attempt by using the stored CustomJob resource name. `switchboard-cli resources list --run <run-id>` shows the tracked CustomJob resource and its last observed state. `switchboard-cli resources refresh --run <run-id>` re-reads the CustomJob status and updates the resource record.

GCP containers receive remote-safe runtime environment paths such as `/tmp/switchboard/checkpoints` and `/tmp/switchboard/events.jsonl`. They also receive:

```text
SWITCHBOARD_GCS_OUTPUT_DIR=gs://<output-prefix>/<run-id>
SWITCHBOARD_CHECKPOINT_URI_PREFIX=gs://<output-prefix>/<run-id>/checkpoints
```

Structured events should still be printed to stdout so Cloud Logging can mirror them back into local Switchboard artifacts. GCP v1 validates `gs://` data inputs but does not mount them into the container; the image should read or download those URIs itself. This is also true for bundled inputs after managed GCS staging: Switchboard uploads and rewrites the input source, while the container owns download/materialization inside the workload. Images can use `runtime/container/switchboard_materialize_data.py` as their entrypoint wrapper when they include `google-cloud-storage`, `gcloud`, or `gsutil`.

For failover, GCP advertises `gs://` checkpoint resume support. File-local checkpoints from a container path are not considered reusable across providers. Emit shared `gs://` checkpoint URIs when a GCP job is expected to resume elsewhere. `examples/iris_pytorch.py` uploads checkpoints to `SWITCHBOARD_CHECKPOINT_URI_PREFIX` when it is set to a `gs://` prefix, emits those GCS checkpoint events, and can download a `gs://` resume checkpoint from `SWITCHBOARD_RESUME_FROM`. See [Cloud Resume Walkthrough](cloud-resume-walkthrough.md) for the `plan --resume-from` and `resume` flow.

## Hardware and Estimates

GCP capabilities include the configured worker-pool shape and a catalog of common T4, L4, A100, A100 80GB, and H100 shapes for the configured region. When available, the provider enriches those shapes with:

1. On-demand hourly prices from the Cloud Billing Catalog API.
2. Zones from Compute Engine aggregated machine and accelerator listings.
3. Regional quota metric, limit, usage, and available values from Compute Engine region quotas.

Known no-quota and unavailable shapes are rejected during hardware routing before submit. These capacity facts are still inventory/quota signals, not a reservation or a guarantee that Vertex AI will allocate the hardware at submit time; submit-time quota/capacity errors remain normalized as retryable provider failures. `gcp.estimate_hourly_usd` still overrides the configured shape estimate.

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

1. Keep the live smoke path current with the `switchboard-496606` smoke bucket and supported Artifact Registry images.
2. Keep the PyTorch Iris image build repeatable on `linux/amd64` for Vertex CPU jobs.
3. Add provider resource adoption only if long-running CustomJob workflows need to attach to resources that were not created by the current Switchboard run.
4. Add more shared-checkpoint examples as additional cloud providers come online.
