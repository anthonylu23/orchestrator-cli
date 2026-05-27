# Auto Hardware Routing

## Status

Auto hardware routing is implemented for the first single-node path. The config loader accepts `routing`, `sizing`, and `hardware`; provider capabilities expose concrete shapes; and the orchestration router can select provider plus hardware shape for `full_auto`, `auto_provider`, and `manual`.

Current limits: sizing can use conservative hints or a local probe artifact with measured required/peak VRAM. GCP pricing and inventory are loaded from Google APIs when available, with static estimates as a fallback; capacity remains quota/inventory-based rather than a hard reservation.

## Goal

Switchboard should let users choose how much control they want over infrastructure selection:

1. `full_auto`: Switchboard selects the provider, GPU shape, and single-node GPU count.
2. `auto_provider`: The user selects hardware requirements, and Switchboard selects the provider.
3. `manual`: The user selects both provider and hardware.

The default full-auto objective should be fastest compatible hardware within a user-specified max estimated run cost. This keeps the product focused on useful training outcomes instead of always choosing the cheapest hourly machine.

## Config

```yaml
routing:
  mode: "full_auto"
  objective: "fastest_within_budget"
  budget:
    max_run_cost_usd: 75
  max_attempts: 2

sizing:
  probe:
    command: ["python", "train.py", "--profile-memory"]
    output: "switchboard-sizing.json"
  hints:
    dataset_size_gb: 180
    model_parameters_b: 7
    batch_size: 8
    precision: "bf16"
    optimizer: "adamw"
    sequence_length: 4096

hardware:
  constraints:
    max_gpus: 8
    allowed_gpu_families: ["nvidia-a100", "nvidia-h100", "nvidia-l40s"]
    min_vram_gb_per_gpu: 40
    regions: ["us-central1", "us-east4"]
```

For `auto_provider`, `hardware.constraints` becomes the user-authored contract and the router only chooses an eligible provider. For `manual`, the config should identify the provider and concrete hardware shape directly.

Manual example:

```yaml
routing:
  mode: "manual"

hardware:
  manual:
    provider: "gcp"
    shape_id: "gcp-us-central1-a2-highgpu-1g-a100-1"
```

## Sizing Model

The preferred sizing source is a probe or profiling step emitted by the training script or framework. Switchboard can run `sizing.probe.command`, read `sizing.probe.output`, and merge the resulting JSON profile into the routing hints before provider/hardware selection.

Probe output is JSON. Supported fields are:

```json
{
  "required_vram_gb": 24,
  "peak_vram_gb": 22.5,
  "model_parameters_b": 7,
  "model_artifact_gb": 14,
  "batch_size": 8,
  "gradient_accumulation_steps": 2,
  "precision": "bf16",
  "optimizer": "adamw",
  "expected_steps": 1200
}
```

`required_vram_gb` takes precedence for fit checks. If it is absent, `peak_vram_gb` is used as the measured required VRAM. Other nonzero fields override matching static hints for routing.

Useful sizing inputs include:

1. Model parameter count or model artifact size.
2. Precision such as fp32, fp16, bf16, int8, or quantized variants.
3. Batch size, gradient accumulation, optimizer, and activation checkpointing.
4. Sequence length for language models or image/video dimensions for vision workloads.
5. Dataset size and expected epoch/step count.
6. Direct measured values from a probe, such as peak VRAM per sample and estimated throughput.

If `full_auto` cannot estimate memory fit or run cost with enough confidence, it fails before submit with clear missing-input or bounds guidance. It does not silently select expensive oversized hardware.

## Routing Decision

Auto hardware routing extends the existing persisted routing decision. A completed decision explains:

1. Selected provider.
2. Selected GPU family/model and GPU count.
3. Estimated required VRAM and available VRAM.
4. Estimated runtime and total run cost.
5. Confidence level and the source of the estimate.
6. Rejected providers and rejected hardware shapes with reasons.

The first implementation stays single-node: choose one machine with 1-N GPUs. Multi-node topology, distributed training setup, network selection, and storage topology are later scope.

## Architecture Direction

The orchestration core should continue to own routing policy. Providers should report available accelerator shapes, pricing, regions, quota/capacity facts, and launch constraints. Providers should not choose the route themselves.

The routing flow is:

```text
job config
  -> data preparation
  -> sizing hints
  -> provider and hardware inventory
  -> memory/runtime/cost estimator
  -> route decision
  -> provider submit
```

The same run/attempt model still applies. If an attempt fails for a retryable provider reason, the next attempt may select another provider and hardware shape as long as it satisfies the same sizing, budget, and checkpoint reachability constraints.

## Next Steps

1. Expand live GCP inventory smoke tests around Cloud Billing and Compute quota permissions.
2. Expand the shape catalog and contract tests as Lambda, Hyperbolic, and other providers are added.
3. Add framework-specific probe examples for common PyTorch and Transformers workloads.
