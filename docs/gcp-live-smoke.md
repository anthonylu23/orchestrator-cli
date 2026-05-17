# GCP Live Smoke Test

## Current Status

Last checked: 2026-05-17.

The local environment has `gcloud` installed, Application Default Credentials available, and a configured project of `switchboard-496606`. Billing is enabled, the Vertex AI API is enabled, and the live auth check passes.

A billable CPU-only Vertex AI CustomJob smoke test passed through the Switchboard CLI on 2026-05-17:

```text
Run r_bbf477a5 succeeded
Vertex CustomJob projects/584014035394/locations/us-central1/customJobs/6274133013915762688
state: JOB_STATE_SUCCEEDED
final val_accuracy: 0.91
final val_loss: 0.25
checkpoint_count: 1
```

An earlier Docker Hub image attempt stayed pending during Vertex provisioning and was canceled cleanly:

```text
Run r_436e532f canceled
Vertex CustomJob projects/584014035394/locations/us-central1/customJobs/3673867179062722560
```

## Prerequisites

Before running a live submit smoke test, configure:

1. Billing enabled on the GCP project.
2. Vertex AI API enabled.
3. Application Default Credentials:

```sh
gcloud auth application-default login
gcloud config set project <project-id>
```

4. A container image that exits successfully when run by Vertex AI, preferably a small CPU-only smoke image.
5. A GCS bucket or prefix for Vertex AI outputs.
6. IAM permissions for the ADC principal:
   - `aiplatform.customJobs.create`
   - `aiplatform.customJobs.get`
   - `aiplatform.customJobs.cancel`
   - `aiplatform.customJobs.list`
   - `logging.logEntries.list`
   - read access to the container image
   - write access to the configured GCS output prefix
   - `iam.serviceAccounts.actAs` if `SWITCHBOARD_GCP_SERVICE_ACCOUNT` or `gcp.service_account` is used

Legacy `ORCHESTRATOR_GCP_*` environment variables are still accepted by the live test harness, but new docs and scripts should use `SWITCHBOARD_GCP_*`.

## Auth-Only Check

This check lists one Vertex AI CustomJob and does not submit work:

```sh
SWITCHBOARD_GCP_LIVE=1 \
SWITCHBOARD_GCP_PROJECT_ID=<project-id> \
go test ./internal/provider/gcp -run TestLiveValidateAuth -count=1
```

If this fails with `BILLING_DISABLED`, enable billing before testing the submit path. If it fails with `PermissionDenied`, check the Vertex AI API and IAM permissions.

## Billable Submit Check

This check submits a real Vertex AI CustomJob and may incur GCP charges:

```sh
SWITCHBOARD_GCP_LIVE=1 \
SWITCHBOARD_GCP_LIVE_SUBMIT=1 \
SWITCHBOARD_GCP_PROJECT_ID=<project-id> \
SWITCHBOARD_GCP_OUTPUT_URI_PREFIX=gs://<bucket>/switchboard-smoke \
SWITCHBOARD_GCP_IMAGE=<region>-docker.pkg.dev/<project>/<repo>/<image>:<tag> \
go test ./internal/provider/gcp -run TestLiveSubmitContainerJob -count=1 -timeout=15m
```

Optional environment variables:

```text
SWITCHBOARD_GCP_LOCATION=us-central1
SWITCHBOARD_GCP_MACHINE_TYPE=n1-standard-4
SWITCHBOARD_GCP_ACCELERATOR_TYPE=NVIDIA_TESLA_T4
SWITCHBOARD_GCP_ACCELERATOR_COUNT=1
SWITCHBOARD_GCP_BOOT_DISK_TYPE=pd-ssd
SWITCHBOARD_GCP_BOOT_DISK_SIZE_GB=100
SWITCHBOARD_GCP_SERVICE_ACCOUNT=<service-account-email>
SWITCHBOARD_GCP_NETWORK=<full-vpc-network-resource>
SWITCHBOARD_GCP_POLL_INTERVAL_SECONDS=15
SWITCHBOARD_GCP_TIMEOUT=10m
```

Leave accelerator variables unset for the cheapest CPU-only smoke path.

## CLI Config Shape

After the gated submit test passes, validate the user-facing command with the same image and output prefix:

```yaml
job:
  name: gcp-container-demo
  image: <region>-docker.pkg.dev/<project>/<repo>/<image>:<tag>

gcp:
  project_id: <project-id>
  location: us-central1
  output_uri_prefix: gs://<bucket>/switchboard-outputs
  machine_type: n1-standard-4
  boot_disk_type: pd-ssd
  boot_disk_size_gb: 100
```

Run:

```sh
SWITCHBOARD_CLI_HOME="$(mktemp -d)" \
go run ./cmd/switchboard-cli train --provider gcp --config gcp-smoke.yaml
```

Keep this as a container-image smoke test. Local script packaging remains deferred until this path succeeds against a billing-enabled project.
