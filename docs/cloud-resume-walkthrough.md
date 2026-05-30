# Cloud Resume Walkthrough

Switchboard can resume cloud jobs when the latest checkpoint event points at shared object storage that the next provider can read. Local file checkpoints are useful for local artifacts, but they are not portable after a cloud instance or container exits.

## Preflight With Plan

Use `plan --resume-from` before a billable resume attempt:

```sh
switchboard-cli plan \
  --provider gcp \
  --config examples/gcp-iris.yaml \
  --resume-from gs://my-bucket/switchboard-outputs/r_123/checkpoints/iris-epoch-40.pt \
  --resume-step 40 \
  --json
```

The plan command validates the resolved job, provider support, cost estimate, and checkpoint scheme compatibility without submitting a provider job, building or pushing an image, uploading staged data, or writing run state.

## GCP To GCP Resume

GCP supports shared `gs://` checkpoint resume. The training image should upload checkpoints to `SWITCHBOARD_CHECKPOINT_URI_PREFIX` and emit the uploaded URI:

```json
{"type":"checkpoint","step":40,"checkpoint_uri":"gs://my-bucket/switchboard-outputs/r_123/checkpoints/iris-epoch-40.pt"}
```

After a run has such an event, resume it with:

```sh
switchboard-cli resume r_123 --provider gcp --config examples/gcp-iris.yaml
```

Switchboard reads the latest checkpoint from `events.jsonl`, validates that GCP supports the `gs://` scheme, passes the URI as `SWITCHBOARD_RESUME_FROM`, and records the resumed attempt in `summary.json`.

## Lambda and Hyperbolic Resume

Lambda and Hyperbolic advertise `s3://` and `gs://` checkpoint schemes. For portable resume on either provider, the container must be able to download the object referenced by `SWITCHBOARD_RESUME_FROM`. The S3 checkpoint example demonstrates upload and checkpoint event emission:

```sh
switchboard-cli resume r_123 --provider lambda --config examples/lambda-s3-checkpoint.yaml
switchboard-cli plan --provider hyperbolic --config examples/hyperbolic-smoke.yaml --resume-from s3://bucket/checkpoints/epoch-3.pt --json
```

Provider routing rejects resume attempts when the selected provider cannot read the checkpoint scheme. For example, GCP rejects an `s3://` resume checkpoint because GCP v1 only advertises `gs://` checkpoint support.
