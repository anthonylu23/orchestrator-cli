# GCP Packaging Decision

## Status

Implemented for the first GCP script packaging milestone. The Docker path now performs local preflight for context directory and platform format, and build/push failures include daemon, platform, registry-auth, and Artifact Registry guidance.

## Decision

Switchboard keeps GCP v1 container-image-first at the provider boundary. The CLI can now run Switchboard-managed Docker build and push to Artifact Registry, then submit the produced image through the same Vertex AI CustomJob path. Vertex AI source package execution remains deferred.

## Rationale

The provider already submits Vertex AI CustomJobs from `job.image`, so the least risky packaging milestone was to produce an image before submit and leave provider submission, logging, cancelation, and error handling unchanged.

Switchboard-managed build/push is the better follow-up because it matches the eventual user workflow: start from local training code, produce a reproducible image, push it to a provider-readable registry, then submit the same CustomJob shape. It also keeps dependency installation and system packages explicit in Dockerfiles.

Vertex AI source packaging remains deferred. It may still be useful later, but adding it now would create a parallel execution path before image-based jobs, logs, data, and checkpoint behavior are fully stable.

## Implementation Impact

1. Keep GCP provider submit image-first.
2. Let `packaging` turn local script jobs into pushed images before submit.
3. Use `examples/gcp/iris/Dockerfile` as the first realistic GCP training image.
4. Treat GCS data access as container responsibility in GCP v1; Switchboard validates `gs://` inputs but does not mount them into the container.
5. Use remote-safe container paths for GCP runtime env values such as `SWITCHBOARD_CHECKPOINT_DIR`.

## Next Steps

1. Add cleanup/pruning options for generated images after the first provider expansion.
2. Revisit source packaging only if image build/push proves too heavy for target users.
