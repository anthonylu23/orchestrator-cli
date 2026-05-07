# CloudTune Orchestrator Overview

## Vision

CloudTune Orchestrator is a provider-agnostic control plane for AI/ML workloads. It should let teams run evaluation, batch inference, fine-tuning, training, embedding, and agent workloads through one lifecycle layer instead of hardcoding each workflow to one cloud, GPU rental provider, model API, local machine, or HPC cluster.

## Core Promise

Submit a workload once, route it to a compatible provider, track state/logs/metrics/cost/artifacts, retry or resume when possible, and persist enough evidence to reproduce and govern the result.

## MVP Wedge

The best first wedge is reliable eval and batch workload orchestration across local + cloud providers. This branch implements the local and mock-cloud foundation:

1. local workload execution.
2. deterministic mock provider failover.
3. durable run and attempt state.
4. logs, structured events, summaries, and artifact manifests.
5. workload metadata for model, dataset, type, and outputs.

Real cloud providers are not part of this branch.

## Target Users

1. AI startups running evals and batch jobs across fragmented providers.
2. ML engineers who need reproducible run history and artifacts.
3. Platform engineers who need provider abstraction, retry policy, and observability.
4. Enterprises that need lineage, approvals, and audit evidence before deployment.

## Differentiation

1. AI-workload state machine rather than generic task DAGs.
2. Provider adapter contract rather than provider-specific workflow code.
3. Checkpoint and artifact concepts built into the run model.
4. Eval and governance metadata as first-class roadmap surfaces.
5. Local-first CLI path before hosted control plane complexity.

## Workload Shape

```yaml
workload:
  name: rag-eval-v1
  type: evaluation
  model:
    provider: local
    name: deterministic-evaluator
  dataset:
    name: customer-support-sample
    path: examples/evals/customer_support.jsonl

job:
  script: examples/eval.py

routing:
  provider: local
  max_attempts: 1

outputs:
  save_to: ./artifacts
```

## Runtime Events

Training and eval scripts can emit JSON lines:

```json
{"type":"metric","step":1,"metrics":{"accuracy":0.91},"split":"eval"}
{"type":"checkpoint","step":800,"checkpoint_uri":"file:///run/checkpoints/ckpt-800"}
{"type":"status","state":"verified"}
```

Plain logs remain valid and are stored in `logs.txt`.

## Acceptance Criteria For This Branch

1. `cloudtune run examples/eval.yaml` completes locally.
2. `cloudtune artifacts <run-id>` lists result artifacts.
3. `cloudtune runs` shows persisted history.
4. mock provider failover still resumes from checkpoint metadata.
5. normal tests, race tests, vet, build, and repeated local stress runs pass.
