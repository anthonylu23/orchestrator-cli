# Lambda Cloud Provider

## Status

The Lambda Cloud provider is implemented as a single-instance, image-first adapter. It launches a Lambda on-demand instance through the Lambda Cloud API, uses cloud-init to run a Docker container on the instance, supports optional private registry login before `docker pull`, collects remote logs and structured events over SSH, records local Switchboard artifacts, persists a tracked `instance` provider resource, and terminates the instance when the job completes by default. The CLI can package a local `job.script` into a Docker image before submit when `packaging.image` is explicit.

This is a milestone adapter for testing Switchboard jobs on real Lambda infrastructure. It is not yet a full image-build or Lambda-filesystem-staging implementation. Cross-provider resume is supported only when training code emits shared `s3://` or `gs://` checkpoint events and the resumed container can read that URI.

Official references:

1. [Lambda Cloud API](https://cloud.lambda.ai/api/v1/openapi.json)
2. [Creating and managing instances](https://docs.lambda.ai/public-cloud/on-demand/creating-managing-instances/)
3. [Connecting to instances](https://docs.lambda.ai/public-cloud/on-demand/connecting-instance/)
4. [Managing the system environment](https://docs.lambda.ai/public-cloud/on-demand/managing-system-environment/)
5. [Billing](https://docs.lambda.ai/public-cloud/billing/)

## Authentication

Switchboard uses two credential paths for Lambda:

1. Lambda API key: local orchestration credential used to list capacity, launch instances, inspect instance state, and terminate instances. It must be stored as encrypted `lambda/api_key`.
2. `lambda.ssh_key_name` plus `lambda.ssh_private_key`: SSH access used after launch to collect `/tmp/switchboard/logs.txt`, `/tmp/switchboard/events.jsonl`, and `/tmp/switchboard/exit.json`.

The Lambda API key is not injected into the training container unless the user explicitly adds it to `job.env`. See [Credentials](credentials.md) for encrypted local credential storage.

## Config

```yaml
job:
  name: lambda-smoke
  image: ghcr.io/example/switchboard-lambda-smoke:latest
  command: ["python", "/app/train.py"]
  args: ["--epochs", "3"]

lambda:
  region_name: us-west-1
  instance_type_name: gpu_1x_a10
  ssh_key_name: switchboard
  ssh_private_key: ~/.ssh/id_ed25519
  image_family: lambda-stack-24-04
  registry_auth:
    server: ghcr.io
    username_env: GHCR_USERNAME
    password_env: GHCR_TOKEN
  poll_interval_seconds: 30
  terminate_on_completion: true
  keep_instance_on_failure: false
```

Package a local script before Lambda submit by supplying an explicit image tag:

```yaml
job:
  name: lambda-script
  script: examples/train.py

packaging:
  context: .
  dockerfile: Dockerfile
  image: ghcr.io/example/switchboard-lambda-script:latest
  platform: linux/amd64

lambda:
  region_name: us-west-1
  instance_type_name: gpu_1x_a10
  ssh_key_name: switchboard
  ssh_private_key: ~/.ssh/id_ed25519
```

Switchboard runs `docker build` and `docker push`, rewrites the provider-facing job to `job.image`, and then uses the normal Lambda image path. Authenticate Docker to the target registry before running the command.

Store the API key, then run it:

```sh
SWITCHBOARD_CREDENTIALS_PASSPHRASE=... \
switchboard-cli credentials set lambda api-key --from-env LAMBDA_API_KEY

SWITCHBOARD_CREDENTIALS_PASSPHRASE=... \
SWITCHBOARD_CLI_HOME="$(mktemp -d)" \
switchboard-cli train --provider lambda --config examples/lambda-smoke.yaml
```

## Runtime Behavior

The provider submits a Lambda launch request with cloud-init `user_data`. The remote runner:

1. Creates `/tmp/switchboard`.
2. Logs into `lambda.registry_auth.server` when registry auth is configured.
3. Pulls `job.image`.
4. Runs `docker run --rm --gpus all`.
5. Mounts `/tmp/switchboard` into the container.
6. Passes Switchboard runtime environment variables.
7. Writes container stdout/stderr to `/tmp/switchboard/logs.txt`.
8. Writes `/tmp/switchboard/exit.json` with the container exit code.

`lambda.registry_auth` reads the username and password from local environment variables before launch. The password is sent to the instance through cloud-init and piped to `docker login --password-stdin`; it is not added to container env, run summaries, SQLite, resource metadata, or logs. Treat Lambda launch `user_data` as sensitive provider-side metadata and prefer short-lived registry tokens.

Switchboard then connects over SSH to read remote logs, parse structured JSONL metric/checkpoint/status events, and persist local artifacts under `~/.switchboard-cli/runs/<run-id>/`.

The provider writes a durable resource record as soon as Lambda returns an instance ID, updates it when the instance becomes active, and updates it again after job exit or cleanup. Cleanup policy is derived from:

1. `terminate_on_completion: true` and `keep_instance_on_failure: false`: `cleanup_policy=always`.
2. `terminate_on_completion: true` and `keep_instance_on_failure: true`: `cleanup_policy=on_success`.
3. `terminate_on_completion: false` and `keep_instance_on_failure: false`: `cleanup_policy=on_failure`.
4. `terminate_on_completion: false` and `keep_instance_on_failure: true`: `cleanup_policy=never`.

Use `switchboard-cli resources list --run <run-id>` to inspect tracked Lambda instances. Use `switchboard-cli resources refresh --provider lambda` to re-read current instance status into the resource record. Use `switchboard-cli resources cleanup --provider lambda` to request termination for tracked active Switchboard-created instances whose cleanup policy allows cleanup.

## Supported Inputs

Lambda v1 supports prebuilt `job.image` workloads and packageable `job.script` workloads when `packaging.image` is explicit. Local bundled data requires managed `s3://` or `gs://` staging before submit.

URI data inputs using `http://`, `https://`, `s3://`, or `gs://` are allowed only as container-visible references. Switchboard uploads staged S3 data with the local AWS CLI before submit, but it does not download or mount URI inputs on Lambda yet. The training image must read the URI itself using credentials supplied by the user through `job.env` or the image environment.

When `staging` is configured, Lambda containers also receive provider-independent object-storage env vars:

```text
SWITCHBOARD_CHECKPOINT_URI_PREFIX=s3://bucket/prefix/<run-id>/checkpoints
SWITCHBOARD_DATA_URI_PREFIX=s3://bucket/prefix/<run-id>/data
SWITCHBOARD_DATA_<INPUT_NAME>_URI=s3://bucket/datasets/train.csv
```

See [Object Storage Staging](object-storage-staging.md).

## Checkpoints and Resume

Lambda advertises `s3://` and `gs://` checkpoint schemes for cross-provider resume routing. Local file checkpoints written under `/tmp/switchboard/checkpoints` are useful for artifacts and debugging, but they are not portable after the Lambda instance is terminated.

Training code should emit shared checkpoint events such as:

```json
{"type":"checkpoint","step":100,"checkpoint_uri":"s3://bucket/run/checkpoints/step-100.pt"}
```

`examples/lambda-s3-checkpoint.yaml` and `examples/lambda/s3_checkpoint` demonstrate a Lambda image that uploads a checkpoint to `SWITCHBOARD_CHECKPOINT_URI_PREFIX` with the AWS CLI before emitting the checkpoint event.

## Hardware and Estimates

Capabilities are derived from Lambda's instance-types endpoint. Switchboard maps instance type, region capacity, GPU count, price in cents per hour, and best-effort GPU family/VRAM from the Lambda response into `HardwareShape`.

If API access is unavailable, the adapter falls back to the configured `lambda.region_name` and `lambda.instance_type_name` so config validation and provider listing still work locally.

## Live Smoke

Build and push the smoke image:

```sh
docker build -f examples/lambda/smoke/Dockerfile \
  -t ghcr.io/<owner>/switchboard-lambda-smoke:latest \
  examples/lambda/smoke
docker push ghcr.io/<owner>/switchboard-lambda-smoke:latest
```

Update `examples/lambda-smoke.yaml` with the image URI, Lambda region, instance type, SSH key name, and private key path.

Then run:

```sh
SWITCHBOARD_CREDENTIALS_PASSPHRASE=... \
SWITCHBOARD_CLI_HOME="$(mktemp -d)" \
./bin/switchboard-cli train --provider lambda --config examples/lambda-smoke.yaml
```

The same path is covered by gated Go tests:

```sh
SWITCHBOARD_LAMBDA_LIVE=1 \
SWITCHBOARD_CREDENTIALS_PASSPHRASE=... \
go test ./internal/provider/lambda -run TestLiveValidateAuth

SWITCHBOARD_LAMBDA_LIVE_SUBMIT=1 \
SWITCHBOARD_CREDENTIALS_PASSPHRASE=... \
SWITCHBOARD_LAMBDA_IMAGE=ghcr.io/<owner>/switchboard-lambda-smoke:latest \
SWITCHBOARD_LAMBDA_REGION=us-west-1 \
SWITCHBOARD_LAMBDA_INSTANCE_TYPE=gpu_1x_a10 \
SWITCHBOARD_LAMBDA_SSH_KEY_NAME=switchboard \
SWITCHBOARD_LAMBDA_SSH_PRIVATE_KEY=~/.ssh/id_ed25519 \
go test ./internal/provider/lambda -run TestLiveSubmitSmoke -count=1

SWITCHBOARD_LAMBDA_LIVE_FAILURE=1 \
SWITCHBOARD_CREDENTIALS_PASSPHRASE=... \
SWITCHBOARD_LAMBDA_IMAGE=ghcr.io/<owner>/switchboard-lambda-smoke:latest \
SWITCHBOARD_LAMBDA_REGION=us-west-1 \
SWITCHBOARD_LAMBDA_INSTANCE_TYPE=gpu_1x_a10 \
SWITCHBOARD_LAMBDA_SSH_KEY_NAME=switchboard \
SWITCHBOARD_LAMBDA_SSH_PRIVATE_KEY=~/.ssh/id_ed25519 \
go test ./internal/provider/lambda -run TestLiveSubmitFailureCleanup -count=1

SWITCHBOARD_LAMBDA_LIVE_CANCEL=1 \
SWITCHBOARD_CREDENTIALS_PASSPHRASE=... \
SWITCHBOARD_LAMBDA_IMAGE=ghcr.io/<owner>/switchboard-lambda-smoke:latest \
SWITCHBOARD_LAMBDA_REGION=us-west-1 \
SWITCHBOARD_LAMBDA_INSTANCE_TYPE=gpu_1x_a10 \
SWITCHBOARD_LAMBDA_SSH_KEY_NAME=switchboard \
SWITCHBOARD_LAMBDA_SSH_PRIVATE_KEY=~/.ssh/id_ed25519 \
go test ./internal/provider/lambda -run TestLiveCancelSmoke -count=1
```

The live submit test verifies remote Docker execution plus local log, metric, checkpoint, and status artifact ingestion. The failure cleanup test runs the same image with an intentionally failing command and verifies the instance reaches a terminating or terminated state. The cancel smoke launches a long-running command, calls `Cancel`, and verifies `Submit` observes the cancellation.

After each run, confirm no smoke instance is left running in the Lambda dashboard, through the Lambda API, or with `switchboard-cli resources list --active --provider lambda`. The adapter terminates successful jobs by default; failed jobs are also terminated unless `keep_instance_on_failure: true` is set.

Live status: the gated auth, submit, failure cleanup, and cancel tests passed against real Lambda Cloud infrastructure on 2026-05-20 using `gpu_1x_a10` in `us-west-1`, `lambda-stack-24-04`, the encrypted local credential store, and a short-lived public smoke image. The public `train --provider lambda` CLI path also passed with the same setup. A launch request tag-schema bug was found during the first live submit and fixed by using Lambda-compatible tag keys.

## Current Limits

1. Lambda packaging requires an explicit `packaging.image`; Switchboard does not derive a provider-native registry image for Lambda.
2. Private registry auth uses launch-time cloud-init user data; provider-native secret injection is not implemented yet.
3. No Lambda filesystem staging yet.
4. No multi-node Lambda jobs.
5. SSH collection currently shells out to the local `ssh` binary.
6. Resource refresh and cleanup work for tracked instances, but Switchboard does not yet adopt or reuse arbitrary existing Lambda instances.

## Next Steps

1. Add provider-native secret handling for private registry pulls when Lambda exposes a safer path than launch user data.
2. Keep S3 checkpoint example coverage current with the shared staging env contract.
3. Add provider resource refresh/adoption if retained instances become a supported debugging workflow.
4. Decide whether Lambda filesystem mounts should become a first-class data/checkpoint backend.
