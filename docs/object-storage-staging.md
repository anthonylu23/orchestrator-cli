# Object Storage Staging

Switchboard has a provider-independent object-storage staging contract for image-based jobs. It gives containers stable runtime environment variables for shared checkpoint prefixes, declared URI data inputs, and staged data prefixes. When `staging.data_uri_prefix` is a `gs://` or `s3://` prefix and a non-local run has bundled data inputs, Switchboard uploads those local files/directories before provider validation and rewrites the inputs to `mode: uri`.

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

### Managed Bundled Upload

For cloud runs, local bundled inputs can be staged to object storage by setting `staging.data_uri_prefix`:

```yaml
data:
  inputs:
    - name: train
      source: ./data/train.csv
      mount: /workspace/data/train.csv
      mode: bundle

staging:
  data_uri_prefix: s3://my-bucket/switchboard/data
```

Before submit, Switchboard uploads the file to:

```text
s3://my-bucket/switchboard/data/<run-id>/data/train/train.csv
```

and rewrites the provider-facing input to:

```yaml
name: train
source: s3://my-bucket/switchboard/data/<run-id>/data/train/train.csv
mount: /workspace/data/train.csv
mode: uri
```

For directory inputs, all regular files are uploaded under `.../<input-name>/<relative-path>`, and the provider-facing input source becomes the directory prefix. GCS upload uses the Google Cloud Storage API; S3 upload shells out to `aws s3 cp`, so the AWS CLI and credentials must be available locally. The container is still responsible for reading or downloading the staged URI into the mounted path it expects; Switchboard does not inject provider IAM, create buckets, or mount object storage filesystems.

Before paying for a run, use `switchboard-cli plan --provider <provider> --config <file> --json` to see the exact staged object URIs that would be uploaded. Planning uses the same path normalization and directory walk as managed staging, but it uses a no-op uploader and records `managed data upload` as a suppressed action.

### URI Inputs

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

Image-based jobs can include the shared helper at `runtime/container/switchboard_materialize_data.py`:

```dockerfile
COPY runtime/container/switchboard_materialize_data.py /app/switchboard_materialize_data.py
ENTRYPOINT ["python", "/app/switchboard_materialize_data.py", "--", "python", "/app/train.py"]
```

The helper scans `SWITCHBOARD_DATA_<NAME>_URI` and `SWITCHBOARD_DATA_<NAME>_MOUNT`, downloads each URI to its declared mount, then `exec`s the training command. It supports `file://`, local paths, `http://`, `https://`, `gs://`, and `s3://`. GCS inputs use `google-cloud-storage` when installed, with `gcloud storage cp` or `gsutil cp` fallback. S3 inputs use `boto3` when installed, with `aws s3 cp` fallback. Directory-like object-store inputs are treated as prefixes when the URI or mount does not look like a file path.

## Checkpoints

Training code should upload checkpoints to `SWITCHBOARD_CHECKPOINT_URI_PREFIX` and emit the uploaded URI as a structured checkpoint event:

```json
{"type":"checkpoint","step":100,"checkpoint_uri":"s3://my-bucket/switchboard/checkpoints/r_123/checkpoints/step-100.pt"}
```

Provider failover can only resume from checkpoint schemes supported by the next provider. Lambda advertises `s3://` and `gs://`; GCP advertises `gs://`.

## Current Limits

1. Managed bundled upload supports `gs://` and `s3://` data prefixes only.
2. Switchboard does not create buckets, IAM policies, or short-lived object-store credentials.
3. Containers must still include the SDKs or CLIs needed for the helper to read provider object storage.
4. GCP keeps its provider-owned `SWITCHBOARD_CHECKPOINT_URI_PREFIX` derived from `gcp.output_uri_prefix`.
