# Hyperbolic Provider

## Status

The Hyperbolic provider is implemented as a code-ready, image-first On-Demand VM adapter. It rents one Hyperbolic virtual machine through the On-Demand Cloud API, waits for SSH, starts a Docker image on the VM, collects remote logs/events/exit markers over SSH, records local Switchboard artifacts, persists a tracked `instance` provider resource, and requests VM termination by default when the job completes.

The implementation is fake-backed and covered by provider contract, client, config, and CLI integration tests. Live Hyperbolic execution is not verified yet, so `provider-status.md` must remain `No` for live verification until a passing smoke transcript is recorded.

Hyperbolic documentation used for this adapter:

1. [On-Demand Cloud API](https://docs.hyperbolic.xyz/docs/on-demand-cloud-api)
2. [On-Demand GPU Overview](https://www.hyperbolic.ai/docs/on-demand/overview)
3. [On-Demand FAQ](https://docs.hyperbolic.xyz/docs/faq)

## Authentication

Switchboard uses two credential paths for Hyperbolic:

1. Hyperbolic API key: local orchestration credential used to list VM options, rent VMs, inspect state, and terminate VMs. Store it as encrypted `hyperbolic/api_key` or set `HYPERBOLIC_API_KEY`.
2. `hyperbolic.ssh_private_key`: SSH access used after launch to collect `/tmp/switchboard/logs.txt`, `/tmp/switchboard/events.jsonl`, and `/tmp/switchboard/exit.json`.

The API key is not injected into the training container unless the user explicitly adds it to `job.env`.

```sh
SWITCHBOARD_CREDENTIALS_PASSPHRASE=... \
switchboard-cli credentials set hyperbolic api-key --from-env HYPERBOLIC_API_KEY
```

## Config

```yaml
job:
  name: hyperbolic-smoke
  image: ghcr.io/example/switchboard-hyperbolic-smoke:latest
  command: ["python", "/app/train.py"]
  args: ["--epochs", "3"]

staging:
  checkpoint_uri_prefix: s3://my-bucket/switchboard/checkpoints
  data_uri_prefix: s3://my-bucket/switchboard/data

hyperbolic:
  vm_config_id: c6fd6253-cbb6-4ea8-a20c-47644b431f1c
  gpu_count: 1
  gpu_type: H100-SXM5-80GB
  ssh_user: ubuntu
  ssh_private_key: ~/.ssh/id_ed25519
  registry_auth:
    server: ghcr.io
    username_env: GHCR_USERNAME
    password_env: GHCR_TOKEN
  poll_interval_seconds: 30
  terminate_on_completion: true
  keep_instance_on_failure: false
```

Package a local script before Hyperbolic submit by supplying an explicit image tag:

```yaml
job:
  name: hyperbolic-script
  script: examples/train.py

packaging:
  context: .
  dockerfile: Dockerfile
  image: ghcr.io/example/switchboard-hyperbolic-script:latest
  platform: linux/amd64

hyperbolic:
  ssh_private_key: ~/.ssh/id_ed25519
```

Switchboard runs `docker build` and `docker push`, rewrites the provider-facing job to `job.image`, and then uses the normal Hyperbolic image path. Authenticate Docker to the target registry before running the command.

## Runtime Behavior

The provider rents an On-Demand VM with `configId` and `gpuCount`, then polls the rental list until the VM is running and exposes a public IP or SSH command. After SSH is ready, Switchboard uploads a runner script and starts it with `nohup`.

The remote runner:

1. Creates `/tmp/switchboard`.
2. Logs into `hyperbolic.registry_auth.server` when registry auth is configured.
3. Pulls `job.image`.
4. Runs `docker run --rm --gpus all`.
5. Mounts `/tmp/switchboard` into the container.
6. Passes Switchboard runtime environment variables.
7. Writes container stdout/stderr to `/tmp/switchboard/logs.txt`.
8. Writes `/tmp/switchboard/exit.json` with the container exit code.

Switchboard connects over SSH to read remote logs, parse structured JSONL metric/checkpoint/status events, and persist local artifacts under `~/.switchboard-cli/runs/<run-id>/`.

Cleanup policy is derived from:

1. `terminate_on_completion: true` and `keep_instance_on_failure: false`: `cleanup_policy=always`.
2. `terminate_on_completion: true` and `keep_instance_on_failure: true`: `cleanup_policy=on_success`.
3. `terminate_on_completion: false` and `keep_instance_on_failure: false`: `cleanup_policy=on_failure`.
4. `terminate_on_completion: false` and `keep_instance_on_failure: true`: `cleanup_policy=never`.

Use `switchboard-cli resources list --run <run-id>` to inspect tracked Hyperbolic VMs. Use `switchboard-cli resources refresh --provider hyperbolic` to re-read current VM status into the resource record. Use `switchboard-cli resources cleanup --provider hyperbolic` to request termination for tracked active Switchboard-created VMs whose cleanup policy allows cleanup.

## Supported Inputs

Hyperbolic v1 supports prebuilt `job.image` workloads and packageable `job.script` workloads when `packaging.image` is explicit. Local bundled data requires managed `s3://` or `gs://` staging before submit.

URI data inputs using `http://`, `https://`, `s3://`, or `gs://` are passed through as container-visible references. Switchboard does not download or mount URI inputs on the VM itself. The training image must read the URI itself using credentials supplied by the user through `job.env` or the image environment. A portable image can run the shared container materializer first; S3 inputs require `boto3` or `aws`, and GCS inputs require `google-cloud-storage`, `gcloud`, or `gsutil`.

When `staging` is configured, Hyperbolic containers receive provider-independent object-storage env vars:

```text
SWITCHBOARD_CHECKPOINT_URI_PREFIX=s3://bucket/prefix/<run-id>/checkpoints
SWITCHBOARD_DATA_URI_PREFIX=s3://bucket/prefix/<run-id>/data
SWITCHBOARD_DATA_<INPUT_NAME>_URI=s3://bucket/datasets/train.csv
```

See [Object Storage Staging](object-storage-staging.md).

## Checkpoints and Resume

Hyperbolic advertises `s3://` and `gs://` checkpoint schemes for cross-provider resume routing. Local file checkpoints written under `/tmp/switchboard/checkpoints` are useful for debugging while the VM exists, but they are not portable after termination.

Training code should emit shared checkpoint events such as:

```json
{"type":"checkpoint","step":100,"checkpoint_uri":"s3://bucket/run/checkpoints/step-100.pt"}
```

Use `switchboard-cli plan --provider hyperbolic --config <file> --resume-from s3://... --json` before a billable resume attempt to verify checkpoint scheme compatibility without renting a VM.

## Hardware and Estimates

Capabilities are derived from `/v2/marketplace/virtual-machine-options` when the API is available. Switchboard maps GPU count and hourly price into `HardwareShape` records with a default GPU type of `H100-SXM5-80GB`. If API access is unavailable, the adapter falls back to configured `hyperbolic.gpu_count`, `hyperbolic.gpu_type`, and optional `hyperbolic.estimate_hourly_usd`.

## Live Smoke

Build and push a smoke image that writes a metric, checkpoint, and status event to the Switchboard event path, then update `examples/hyperbolic-smoke.yaml` with the image URI, SSH private key, and optional staging prefixes.

Run:

```sh
SWITCHBOARD_CREDENTIALS_PASSPHRASE=... \
SWITCHBOARD_CLI_HOME="$(mktemp -d)" \
./bin/switchboard-cli train --provider hyperbolic --config examples/hyperbolic-smoke.yaml
```

After the run, confirm no smoke VM is left running in Hyperbolic and through:

```sh
./bin/switchboard-cli resources list --active --provider hyperbolic
```

## Current Limits

1. Live Hyperbolic submit/cancel/cleanup has not been verified yet.
2. The first implementation targets On-Demand VMs only, not spot marketplace or bare metal rentals.
3. Packaging requires an explicit `packaging.image`; Switchboard does not derive a Hyperbolic-native registry image.
4. Private registry auth uses the uploaded runner script; provider-native secret injection is not implemented.
5. No multi-node or InfiniBand cluster topology support yet.
6. SSH collection shells out to the local `ssh` binary.
7. VM option reporting currently exposes GPU count and price; richer GPU model, region, quota, and capacity fields require additional live API confirmation.

## Next Steps

1. Run a gated live auth and submit smoke against a short-lived Hyperbolic VM.
2. Confirm the live shape of VM metadata, public IP, SSH command, and rental status values.
3. Add a Hyperbolic-specific smoke container example after live execution is confirmed.
4. Add richer hardware/capacity mapping if Hyperbolic exposes stable option metadata beyond GPU count and hourly price.
