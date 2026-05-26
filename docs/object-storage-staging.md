# Object Storage Staging

Switchboard now has a small provider-independent object-storage staging contract for image-based jobs. It does not upload local files yet; it gives containers stable runtime environment variables for shared checkpoint prefixes, declared URI data inputs, and future staging backends.

## Config

```yaml
staging:
  checkpoint_uri_prefix: s3://my-bucket/switchboard/checkpoints
  data_uri_prefix: s3://my-bucket/switchboard/data
```

Supported prefixes today are `s3://` and `gs://`. Switchboard appends the run ID and a purpose directory before injecting the values into the job:

```text
SWITCHBOARD_CHECKPOINT_URI_PREFIX=s3://my-bucket/switchboard/checkpoints/<run-id>/checkpoints
SWITCHBOARD_DATA_URI_PREFIX=s3://my-bucket/switchboard/data/<run-id>/data
```

Legacy `ORCHESTRATOR_*` aliases are also injected during the rename window.

## Data Inputs

For each URI data input, Switchboard injects name-scoped env vars. For example:

```yaml
data:
  inputs:
    - name: train-set
      source: s3://my-bucket/datasets/train.csv
      mount: /workspace/data/train.csv
      mode: uri
```

becomes:

```text
SWITCHBOARD_DATA_TRAIN_SET_URI=s3://my-bucket/datasets/train.csv
SWITCHBOARD_DATA_TRAIN_SET_MOUNT=/workspace/data/train.csv
```

Cloud providers still differ in how they make data available. GCP validates `gs://` inputs and leaves reads to the container. Lambda passes URI inputs and staging env vars to the container. Local bundled data still uses the workspace materialization path.

## Checkpoints

Training code should upload checkpoints to `SWITCHBOARD_CHECKPOINT_URI_PREFIX` and emit the uploaded URI as a structured checkpoint event:

```json
{"type":"checkpoint","step":100,"checkpoint_uri":"s3://my-bucket/switchboard/checkpoints/r_123/checkpoints/step-100.pt"}
```

Provider failover can only resume from checkpoint schemes supported by the next provider. Lambda advertises `s3://` and `gs://`; GCP advertises `gs://`.

## Current Limits

1. Switchboard does not yet upload bundled local datasets to object storage.
2. Switchboard does not create buckets, IAM policies, or short-lived object-store credentials.
3. Containers own object-store upload/download behavior for this phase.
4. GCP keeps its provider-owned `SWITCHBOARD_CHECKPOINT_URI_PREFIX` derived from `gcp.output_uri_prefix`.
