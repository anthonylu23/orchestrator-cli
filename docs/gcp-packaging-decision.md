# GCP Packaging Decision

## Status

Accepted for the next GCP milestone.

## Decision

Switchboard will keep GCP v1 container-image-first and use user-supplied images for the current realistic demo path. The next packaging implementation should be Switchboard-managed Docker build and push to Artifact Registry, not a Vertex AI source package path.

## Rationale

The current provider already submits Vertex AI CustomJobs from `job.image`, so the least risky next milestone is proving a realistic training container with GCS data. That path keeps provider submission, logging, cancelation, and error handling stable while avoiding a second packaging model.

Switchboard-managed build/push is the better follow-up because it matches the eventual user workflow: start from local training code, produce a reproducible image, push it to a provider-readable registry, then submit the same CustomJob shape. It also keeps dependency installation and system packages explicit in Dockerfiles.

Vertex AI source packaging remains deferred. It may still be useful later, but adding it now would create a parallel execution path before image-based jobs, logs, data, and checkpoint behavior are fully stable.

## Implementation Impact

1. Keep `job.image` required for GCP until build/push support exists.
2. Use `examples/gcp/iris/Dockerfile` as the first realistic GCP training image.
3. Treat GCS data access as container responsibility in GCP v1; Switchboard validates `gs://` inputs but does not mount them into the container.
4. Use remote-safe container paths for GCP runtime env values such as `SWITCHBOARD_CHECKPOINT_DIR`.
5. Add Artifact Registry build/push as a separate feature with its own cleanup, auth, and tests.

## Next Steps

1. Keep the GCP Iris image build manual and documented while the provider path stabilizes.
2. Add Switchboard-managed Docker build/push after the Iris container demo is repeatable.
3. Revisit source packaging only if image build/push proves too heavy for target users.
