# Auto Hardware Routing

## Status

Auto hardware routing is planned scope. The config loader now accepts the concrete `routing`, `sizing`, and `hardware` schema below, and provider capabilities can expose concrete hardware shapes. Mock and GCP providers can report those shape facts, but the orchestration router still makes provider-level decisions only. Memory fit, runtime prediction, total run cost, selected hardware persistence, and manual hardware enforcement remain implementation work.

## Goal

Switchboard should let users choose how much control they want over infrastructure selection:

1. `full_auto`: Switchboard selects the provider, GPU shape, and single-node GPU count.
2. `auto_provider`: The user selects hardware requirements, and Switchboard selects the provider.
3. `manual`: The user selects both provider and hardware.

The default full-auto objective should be fastest compatible hardware within a user-specified max estimated run cost. This keeps the product focused on useful training outcomes instead of always choosing the cheapest hourly machine.

## Conceptual Config

The exact schema is not implemented yet, but the intended shape is:

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

## Sizing Model

The preferred sizing source is a probe or profiling step emitted by the training script or framework. User-provided hints seed or override the probe when the script cannot infer everything.

Useful sizing inputs include:

1. Model parameter count or model artifact size.
2. Precision such as fp32, fp16, bf16, int8, or quantized variants.
3. Batch size, gradient accumulation, optimizer, and activation checkpointing.
4. Sequence length for language models or image/video dimensions for vision workloads.
5. Dataset size and expected epoch/step count.
6. Direct measured values from a probe, such as peak VRAM per sample and estimated throughput.

If full-auto cannot estimate memory fit or run cost with enough confidence, it should fail before submit with clear missing-input or bounds guidance. It should not silently select expensive oversized hardware.

## Routing Decision

Auto hardware routing should extend the existing persisted routing decision. A completed decision should explain:

1. Selected provider.
2. Selected GPU family/model and GPU count.
3. Estimated required VRAM and available VRAM.
4. Estimated runtime and total run cost.
5. Confidence level and the source of the estimate.
6. Rejected providers and rejected hardware shapes with reasons.

The first implementation should stay single-node: choose one machine with 1-N GPUs. Multi-node topology, distributed training setup, network selection, and storage topology are later scope.

## Architecture Direction

The orchestration core should continue to own routing policy. Providers should report available accelerator shapes, pricing, regions, quota/capacity facts, and launch constraints. Providers should not choose the route themselves.

At implementation time, the routing flow should become:

```text
job config
  -> data preparation
  -> sizing probe and user hints
  -> provider and hardware inventory
  -> memory/runtime/cost estimator
  -> route decision
  -> provider submit
```

The same run/attempt model still applies. If an attempt fails for a retryable provider reason, the next attempt may select another provider and hardware shape as long as it satisfies the same sizing, budget, and resume constraints.

## Next Steps

1. Add a sizing profile artifact contract that scripts can emit during probe runs.
2. Add routing tests and implementation for memory fit, fastest-within-budget ranking, confidence failures, and manual override behavior.
3. Persist selected hardware, estimated VRAM, estimated runtime, estimated total cost, confidence, and rejected shape reasons.
4. Replace static GCP hourly estimates with pricing/capacity inventory behind provider capabilities.
5. Add shared checkpoint storage rules before allowing cross-provider resume from GCP file checkpoints.
