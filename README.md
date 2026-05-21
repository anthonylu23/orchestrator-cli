# Switchboard CLI

Switchboard CLI is a fault-tolerant ML job orchestrator with provider adapters, local durable run state, structured telemetry, and cost-aware scheduling.

The project is designed as a systems engineering and ML infrastructure tool: the orchestration core owns lifecycle, retries, routing, failover, state, telemetry, resume policy, and eventually hardware selection, while providers stay behind a small adapter contract.

## Status

Switchboard now has a local orchestration vertical slice, deterministic mock-provider failover, a first real GCP provider, and an initial Lambda Cloud adapter milestone. The current implementation can run a local Python training script, persist SQLite run state, capture mixed logs and structured JSONL events, materialize local data bundles, cancel active local runs, route across mock providers, resume from the latest checkpoint after a simulated provider failure, submit container-image jobs to Vertex AI CustomJob, and launch image-first smoke jobs on Lambda Cloud instances.

Run artifacts redact secret-like keys and known secret environment values before persistence. Attempt history also records resume checkpoint provenance and provider cost estimates so failover decisions remain explainable after completion.

GCP v1 is still image-submit-first at the provider boundary, but the CLI can now build and push a Docker image before submit when a GCP job uses `job.script` with `packaging` config. Live auth, a billable CPU-only Vertex AI CustomJob smoke test, a non-billable pricing/capacity smoke, and a PyTorch Iris Vertex container run have passed on the `switchboard-496606` project. `examples/gcp/iris` contains the first realistic PyTorch Iris container workflow with optional GCS checkpoint upload/resume. GCP capabilities can use live Cloud Billing pricing, Compute Engine machine/accelerator inventory, and regional quota facts with static fallback. Lambda v1 launches one on-demand instance, runs a prebuilt Docker image through cloud-init, collects logs/events over SSH, and terminates the instance by default. China cloud provider readiness adapters are available for `alibaba-cloud`, `huawei-cloud`, `tencent-cloud`, `tianyi-cloud`, and `baidu-ai-cloud`; these validate credential/env readiness, public endpoint reachability, and strict user-supplied auth commands, but intentionally do not submit jobs yet. GCP data staging beyond `gs://`, Lambda private-registry/staging support, China cloud compute submission, additional providers, and richer QoL workflows remain roadmap work.

## Quick Start

Build the CLI:

```sh
go build -o bin/switchboard-cli ./cmd/switchboard-cli
```

Run the example training script:

```sh
./bin/switchboard-cli train --provider local --script examples/train.py
```

Use a disposable Switchboard home while developing:

```sh
SWITCHBOARD_CLI_HOME="$(mktemp -d)" ./bin/switchboard-cli train --provider local --script examples/train.py
```

Inspect a run:

```sh
./bin/switchboard-cli status <run-id>
./bin/switchboard-cli logs <run-id>
./bin/switchboard-cli cancel <run-id>
./bin/switchboard-cli providers list --json
./bin/switchboard-cli providers inspect alibaba-cloud
./bin/switchboard-cli providers check alibaba-cloud
./bin/switchboard-cli providers check alibaba-cloud --strict-auth
```

Run the mock failover demo:

```sh
./bin/switchboard-cli train --provider auto --config examples/failover.yaml
```

Expected output includes:

```text
Selected mock-lambda
Found checkpoint: step 800
Selected mock-gcp
Run <run-id> succeeded
```

Run tests:

```sh
go test ./...
go vet ./...
```

Run the PyTorch Iris demo:

```sh
SWITCHBOARD_CLI_HOME="$(mktemp -d)" ./bin/switchboard-cli train --provider local --config examples/iris-pytorch.yaml
```

This demo trains a tiny PyTorch MLP on Kaggle's [Iris Species dataset](https://www.kaggle.com/datasets/uciml/iris), published as CC0/Public Domain and vendored at `examples/data/iris/Iris.csv` for deterministic local runs. It requires `python3` with `torch` installed and exercises bundled data materialization, metric events, checkpoint events, `summary.json`, and local checkpoint files.

Provider adapters should also pass the shared contract checks:

```sh
go test ./internal/provider/...
```

Run a Vertex AI CustomJob from a prebuilt container image:

```sh
./bin/switchboard-cli train --provider gcp --config examples/gcp-container.yaml
```

Build and run the containerized PyTorch Iris GCP demo:

```sh
gcloud storage cp examples/data/iris/Iris.csv gs://<bucket>/switchboard-demo/iris/Iris.csv
gcloud auth configure-docker us-central1-docker.pkg.dev
docker build -f examples/gcp/iris/Dockerfile \
  -t us-central1-docker.pkg.dev/<project>/switchboard/iris-pytorch:latest .
docker push us-central1-docker.pkg.dev/<project>/switchboard/iris-pytorch:latest

SWITCHBOARD_CLI_HOME="$(mktemp -d)" ./bin/switchboard-cli train --provider gcp --config examples/gcp-iris.yaml
```

Run a local script on GCP by letting Switchboard build and push an image first:

```yaml
job:
  script: examples/train.py
packaging:
  dockerfile: Dockerfile
  context: .
  platform: linux/amd64
gcp:
  project_id: my-project
  location: us-central1
  output_uri_prefix: gs://my-bucket/switchboard-outputs
  artifact_registry_repository: switchboard
```

The packaging layer runs `docker build` and `docker push`, derives an Artifact Registry image when `packaging.image` is omitted, and submits the resulting image to the existing GCP provider path. Configure Docker auth first with `gcloud auth configure-docker <location>-docker.pkg.dev`.

See `examples/gcp-script-packaging.yaml` for a GCP Iris config that builds from the checked-in Dockerfile before submitting.

Run a Lambda Cloud smoke job from a prebuilt container image:

```sh
SWITCHBOARD_CREDENTIALS_PASSPHRASE=... \
SWITCHBOARD_CLI_HOME="$(mktemp -d)" \
./bin/switchboard-cli train --provider lambda --config examples/lambda-smoke.yaml
```

The Lambda adapter requires encrypted `lambda/api_key`, plus `lambda.ssh_key_name` and `lambda.ssh_private_key` so Switchboard can collect remote logs, events, and the exit marker from the launched instance. See [Lambda Cloud Provider](docs/lambda-provider.md).

Provider API keys can be stored in Switchboard's encrypted local credentials store:

```sh
SWITCHBOARD_CREDENTIALS_PASSPHRASE=... \
./bin/switchboard-cli credentials set lambda api-key --from-env LAMBDA_API_KEY

./bin/switchboard-cli credentials list
```

See [Credentials](docs/credentials.md).

## Product Wedge

Given a training script, data inputs, sizing profile, and budget, Switchboard should choose compatible execution infrastructure, run the job, persist telemetry, and resume from the latest checkpoint if a provider fails.

The first impressive demo is intentionally narrower than full multi-cloud support:

1. Run an example ML training script through the `local` provider.
2. Stream mixed logs and structured metric events.
3. Persist run state, `events.jsonl`, and `summary.json`.
4. Simulate a cloud provider failure with the `mock` provider.
5. Discover the latest checkpoint and resume on another adapter without changing orchestration code.

The broader product direction extends `provider=auto` into auto hardware routing. Users should be able to choose between `full_auto` provider plus GPU selection, `auto_provider` with user-selected hardware, and fully manual provider/hardware configuration.

## Target Commands

```sh
switchboard-cli train --provider local --script examples/train.py
switchboard-cli train --provider auto --config examples/failover.yaml
switchboard-cli train --provider gcp --config examples/gcp-container.yaml
switchboard-cli train --provider lambda --config examples/lambda-smoke.yaml
switchboard-cli status <run-id>
switchboard-cli logs <run-id> --follow
switchboard-cli cancel <run-id>
switchboard-cli providers list --json
```

Planned commands not implemented yet include explicit `resume`. Planned provider work now focuses on live Lambda smoke validation, additional cloud adapters, and richer provider-specific staging/checkpoint integrations.

## Data Inputs

Switchboard treats training and test data as declared job inputs. Local files or directories can be bundled with the job, while remote sources such as HTTP, S3, and GCS URIs are resolved at runtime. In both cases, training code reads from stable workspace paths.

```yaml
job:
  script: "train.py"
  args: ["--train-data", "/workspace/data/train", "--test-data", "/workspace/data/test.csv"]

data:
  inputs:
    - name: "train"
      source: "./data/train"
      mount: "/workspace/data/train"
      mode: "bundle"

    - name: "test"
      source: "https://example.com/test.csv"
      mount: "/workspace/data/test.csv"
      mode: "uri"
  bundle:
    max_size_mb: 512
    require_override_above_limit: true
```

Local paths default to bundled inputs. URI sources default to runtime-resolved inputs. Oversized local bundles fail preflight unless the user passes an explicit override such as `--allow-large-data-bundle`.
When a YAML config is loaded with `--config`, relative local paths in `job.script`, `job.work_dir`, bundled data `source` values, and `packaging` paths are resolved relative to the config file's directory. Bundled data inputs must not contain symlinks.

For local runs, bundled files and directories are copied into each run workspace under `runs/<run-id>/workspace`. Mounts must be under `/workspace`; for example, `/workspace/data/train` maps to `runs/<run-id>/workspace/data/train`. Job arguments and environment values that reference declared mounts are rewritten to host paths before the local process starts.

For GCP runs, v1 accepts `job.image` directly or can package `job.script` into a Docker image before submit. Data inputs must still be `gs://` URI inputs. Bundled local data and non-GCS URI inputs are rejected before submit. GCP containers receive `SWITCHBOARD_CHECKPOINT_URI_PREFIX=gs://<output-prefix>/<run-id>/checkpoints`; training code can upload checkpoint files there and emit those shared `gs://` URIs for resume/failover. See [GCP Provider](docs/gcp-provider.md).

For Lambda runs, v1 accepts `job.image` directly and runs it on a launched Lambda instance with Docker. Bundled local data and `job.script` are rejected before submit. URI data inputs are passed through for the container to handle itself. Lambda containers receive remote-safe Switchboard paths under `/tmp/switchboard`; portable resume requires emitted `s3://` or `gs://` checkpoint URIs. See [Lambda Cloud Provider](docs/lambda-provider.md).

For China cloud readiness checks, `providers check <provider>` validates required credential variables and endpoint reachability. This default mode reports `authenticated: false` for China clouds because an endpoint probe does not prove the account can call the provider API. Use `providers check <provider> --strict-auth` with the provider-specific auth command environment variable to require an authenticated official CLI or SDK smoke command before reporting ready. See [China Cloud Provider Readiness](docs/china-cloud-providers.md).

Example local data run:

```yaml
job:
  script: "examples/read_data.py"
  args: ["/workspace/data/train.txt"]

data:
  inputs:
    - name: "train"
      source: "./data/train.txt"
      mount: "/workspace/data/train.txt"
      mode: "bundle"
```

## Architecture Flow

```text
CLI command
  -> config loader
  -> data preparation
  -> orchestration service
  -> routing engine
  -> provider registry
  -> provider adapter
  -> run state + events + artifacts
```

The core user-facing object is a run. Each provider execution is an attempt. This lets one run fail on one provider and resume on another provider while preserving a single user-facing run history.

For `provider=auto`, routing checks provider capabilities before ranking candidates. Providers that cannot satisfy bundled data inputs or declared URI schemes are rejected with persisted reasons.

Auto hardware routing now supports single-node provider and hardware-shape selection for `full_auto`, `auto_provider`, and `manual` modes using provider-reported shape facts, sizing hints, budget limits, and persisted rejection reasons. GCP reports a configured shape plus a catalog enriched by live pricing, inventory, and regional quota facts when available. See [Auto Hardware Routing](docs/auto-hardware-routing.md).

## Artifacts

By default Switchboard writes to `~/.switchboard-cli`. Set `SWITCHBOARD_CLI_HOME` or pass `--home` to isolate runs.

```text
~/.switchboard-cli/switchboard.db
~/.switchboard-cli/runs/<run-id>/events.jsonl
~/.switchboard-cli/runs/<run-id>/logs.txt
~/.switchboard-cli/runs/<run-id>/summary.json
~/.switchboard-cli/runs/<run-id>/checkpoints/
~/.switchboard-cli/runs/<run-id>/workspace/
```

`summary.json` includes final metrics and direction-aware `best_metrics`: common loss/error/perplexity/latency/duration metrics are minimized, while other metrics are maximized. Provider attempts include resume checkpoint and estimate fields when available. Auto-routed runs include the persisted routing decision, selected hardware, estimated VRAM/runtime/cost, confidence, and provider/hardware rejection reasons.

## Roadmap Summary

1. Spec and scaffold.
2. Local orchestration vertical slice.
3. Mock cloud provider and failure simulation.
4. Provider extensibility hardening.
5. GCP as the first real provider: container-image CustomJob support, managed Docker build/push, live pricing/capacity enrichment, and GCS checkpoint resume are implemented; richer data staging remains.
6. Auto hardware routing for fastest compatible single-node GPU selection within a max run cost is implemented with provider-reported facts, including live GCP pricing/inventory/quota enrichment when available.
7. China cloud readiness adapters for Alibaba Cloud, Huawei Cloud, Tencent Cloud, Tianyi Cloud, and Baidu AI Cloud are implemented as non-submitting connectivity checks.
8. Later: Lambda, Hyperbolic, China cloud compute adapters, shared checkpoint backends, fan-out sweeps, richer terminal UI, and optional hosted control plane.

## Docs

- [Overview](docs/overview.md)
- [Architecture](docs/architecture.md)
- [PyTorch Iris Demo](docs/pytorch-iris-demo.md)
- [GCP Provider](docs/gcp-provider.md)
- [GCP Live Smoke Test](docs/gcp-live-smoke.md)
- [GCP Packaging Decision](docs/gcp-packaging-decision.md)
- [Credentials](docs/credentials.md)
- [China Cloud Provider Readiness](docs/china-cloud-providers.md)
- [Auto Hardware Routing](docs/auto-hardware-routing.md)
- [Roadmap](docs/roadmap.md)
