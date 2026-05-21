# Contributing

`switchboard-cli` is pre-alpha infrastructure software. Contributions are welcome, but changes should preserve the current truth boundary: local and mock providers are testable, GCP and Lambda have first real-provider milestones, and China cloud providers are readiness-only until compute submission is implemented.

## Development Setup

Requirements:

- Go matching `go.mod`.
- Python 3 for local example workloads.
- Docker and provider credentials only when running opt-in live cloud or packaging tests.

Build and test:

```sh
go build -o bin/switchboard-cli ./cmd/switchboard-cli
go test ./... -count=1
go vet ./...
go test -race ./... -count=1
git diff --check
```

Run a local smoke test:

```sh
SWITCHBOARD_CLI_HOME="$(mktemp -d)" bin/switchboard-cli train --provider local --script examples/train.py
bin/switchboard-cli --home "$SWITCHBOARD_CLI_HOME" status <run-id>
```

## Live Provider Tests

Do not run live cloud/provider tests by default. GCP, Lambda, and China cloud checks are opt-in and may create billable usage or touch real provider accounts. Use the documented environment gates and provider docs before running them.

## Pull Request Expectations

- Keep provider limitations explicit in docs and tests.
- Do not mark a provider stable without live verification evidence.
- Do not add fake providers that return success without exercising real provider behavior.
- Do not merge provider-specific assumptions into the core lifecycle model.
- Add or update tests for behavior changes.
- Keep generated runtime files, credentials, and local artifacts out of commits.

## Security

This project executes user-provided workload commands and touches provider credentials. Never commit credentials, token files, private keys, or `.env` files. See `SECURITY.md` before reporting vulnerabilities.
