# Modal Live Verification

This branch includes an experimental `modal-sandbox` execution provider. Normal tests do not touch Modal. Live tests require an authenticated Modal account and are gated behind `CLOUDTUNE_INTEGRATION=modal`.

## Setup

Create a local Python environment and install Modal:

```sh
python3 -m venv .venv
source .venv/bin/activate
pip install modal
```

Authenticate with either Modal's local token file:

```sh
modal token set
```

Or environment variables:

```sh
export MODAL_TOKEN_ID=...
export MODAL_TOKEN_SECRET=...
```

Modal supports both `modal token set`, which writes a local `.modal.toml`, and `MODAL_TOKEN_ID` / `MODAL_TOKEN_SECRET`, with environment variables taking precedence.

## Doctor

Run:

```sh
go build -o bin/cloudtune ./cmd/switchboard-cli
bin/cloudtune doctor --provider modal-sandbox --config examples/eval.yaml
```

Expected ready checks:

```text
Modal:
  cli: ok
  auth: ok
  python_sdk: ok

Provider:
  registered: ok modal-sandbox
  auth: ok validated
  capabilities: ok ...
```

If this fails, do not run live integration tests yet.

## Manual Acceptance

```sh
CLOUDTUNE_HOME="$(mktemp -d)"

bin/cloudtune --home "$CLOUDTUNE_HOME" run examples/eval.yaml --provider local
bin/cloudtune --home "$CLOUDTUNE_HOME" run examples/eval.yaml --provider modal-sandbox
bin/cloudtune --home "$CLOUDTUNE_HOME" runs
bin/cloudtune --home "$CLOUDTUNE_HOME" logs <modal-run-id>
bin/cloudtune --home "$CLOUDTUNE_HOME" artifacts <modal-run-id>

bin/cloudtune --home "$CLOUDTUNE_HOME" run examples/eval_fail.yaml --provider modal-sandbox
bin/cloudtune --home "$CLOUDTUNE_HOME" logs <failed-modal-run-id>
bin/cloudtune --home "$CLOUDTUNE_HOME" artifacts <failed-modal-run-id>

bin/cloudtune --home "$CLOUDTUNE_HOME" compare <local-run-id> <modal-run-id>
```

Pass conditions:

1. `doctor` reports Modal ready.
2. Remote eval reaches `succeeded`.
3. Remote failure reaches `failed`.
4. Logs are readable after completion.
5. `outputs/eval_result.json` and `outputs/partial_result.json` are fetched into local run storage.
6. `workload.json` includes a `modal-sandbox:<sandbox-id>` provider job ref.
7. `compare` shows matching config and dataset hashes.

## Integration Tests

Run:

```sh
CLOUDTUNE_INTEGRATION=modal go test ./... -run ModalIntegration -count=1
```

These tests run real Modal jobs and may create billable usage. They cover:

1. `doctor` readiness.
2. remote eval success.
3. remote failure artifact preservation.
4. local-vs-Modal compare.
5. remote cancellation with `examples/modal_slow.yaml`.
6. missing auth failure before provider job submission.
