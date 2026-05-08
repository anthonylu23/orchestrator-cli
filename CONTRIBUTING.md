# Contributing

`switchboard-cli` is pre-alpha infrastructure software. Contributions are welcome, but changes should preserve the current truth boundary: local and mock providers are testable today; `modal-sandbox` is experimental until live verification passes in an authenticated Modal environment.

## Development Setup

Requirements:

- Go matching `go.mod`.
- Python 3 for local example workloads.
- Modal CLI/Python package only when running live Modal tests.

Build and test:

```sh
go build -o bin/cloudtune ./cmd/switchboard-cli
go test ./... -count=1
go vet ./...
go test -race ./... -count=1
git diff --check
```

Run a local smoke test:

```sh
export CLOUDTUNE_HOME="$(mktemp -d)"
bin/cloudtune run examples/eval.yaml --provider local
bin/cloudtune runs
```

## Live Provider Tests

Do not run live cloud/provider tests by default. Modal tests are opt-in and may create billable usage:

```sh
python3 -m venv .venv
source .venv/bin/activate
pip install modal
modal token set
CLOUDTUNE_INTEGRATION=modal go test ./... -run ModalIntegration -count=1
```

Environment-based Modal auth also works:

```sh
export MODAL_TOKEN_ID=...
export MODAL_TOKEN_SECRET=...
```

## Pull Request Expectations

- Keep provider limitations explicit in docs and tests.
- Do not mark a provider stable without live verification evidence.
- Do not add fake providers that return success without exercising real provider behavior.
- Do not merge provider-specific assumptions into the core lifecycle model.
- Add or update tests for behavior changes.
- Keep generated runtime files, credentials, and local artifacts out of commits.

## Security

This project executes user-provided workload commands and will eventually touch provider credentials. Never commit credentials, token files, private keys, or `.env` files. See `SECURITY.md` before reporting vulnerabilities.
