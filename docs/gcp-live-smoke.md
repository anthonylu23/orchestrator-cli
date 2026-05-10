# GCP Live Smoke Test

## Current Status

Last checked: 2026-05-10.

The local environment has `gcloud` installed, Application Default Credentials available, and a configured project of `lfp-temporal-vit`. The gated live auth check reached Vertex AI, but failed before any job submit because billing is disabled on that project:

```text
rpc error: code = PermissionDenied desc = This API method requires billing to be enabled.
```

This is classified as project/environment setup, not a provider implementation regression. No Vertex AI CustomJob submit was attempted.

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
   - `iam.serviceAccounts.actAs` if `ORCHESTRATOR_GCP_SERVICE_ACCOUNT` or `gcp.service_account` is used

## Auth-Only Check

This check lists one Vertex AI CustomJob and does not submit work:

```sh
ORCHESTRATOR_GCP_LIVE=1 \
ORCHESTRATOR_GCP_PROJECT_ID=<project-id> \
go test ./internal/provider/gcp -run TestLiveValidateAuth -count=1
```

If this fails with `BILLING_DISABLED`, enable billing before testing the submit path. If it fails with `PermissionDenied`, check the Vertex AI API and IAM permissions.

## Billable Submit Check

This check submits a real Vertex AI CustomJob and may incur GCP charges:

```sh
ORCHESTRATOR_GCP_LIVE=1 \
ORCHESTRATOR_GCP_LIVE_SUBMIT=1 \
ORCHESTRATOR_GCP_PROJECT_ID=<project-id> \
ORCHESTRATOR_GCP_OUTPUT_URI_PREFIX=gs://<bucket>/orchestrator-smoke \
ORCHESTRATOR_GCP_IMAGE=<region>-docker.pkg.dev/<project>/<repo>/<image>:<tag> \
go test ./internal/provider/gcp -run TestLiveSubmitContainerJob -count=1 -timeout=15m
```

Optional environment variables:

```text
ORCHESTRATOR_GCP_LOCATION=us-central1
ORCHESTRATOR_GCP_MACHINE_TYPE=n1-standard-4
ORCHESTRATOR_GCP_ACCELERATOR_TYPE=NVIDIA_TESLA_T4
ORCHESTRATOR_GCP_ACCELERATOR_COUNT=1
ORCHESTRATOR_GCP_BOOT_DISK_TYPE=pd-ssd
ORCHESTRATOR_GCP_BOOT_DISK_SIZE_GB=100
ORCHESTRATOR_GCP_SERVICE_ACCOUNT=<service-account-email>
ORCHESTRATOR_GCP_NETWORK=<full-vpc-network-resource>
ORCHESTRATOR_GCP_POLL_INTERVAL_SECONDS=15
ORCHESTRATOR_GCP_TIMEOUT=10m
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
  output_uri_prefix: gs://<bucket>/orchestrator-outputs
  machine_type: n1-standard-4
  boot_disk_type: pd-ssd
  boot_disk_size_gb: 100
```

Run:

```sh
ORCHESTRATOR_CLI_HOME="$(mktemp -d)" \
go run ./cmd/orchestrator-cli train --provider gcp --config gcp-smoke.yaml
```

Keep this as a container-image smoke test. Local script packaging remains deferred until this path succeeds against a billing-enabled project.
