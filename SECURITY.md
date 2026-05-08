# Security Policy

`switchboard-cli` is pre-alpha software. Do not use it with production credentials, sensitive datasets, or production cloud accounts yet.

## Supported Versions

No stable versions are supported yet. Security fixes are handled on the active development branch until the project publishes a stable release policy.

## Reporting Vulnerabilities

Do not open a public issue with exploit details, credentials, private provider job IDs, dataset contents, or reproduction steps that expose secrets.

Preferred reporting path:

1. Use GitHub private vulnerability reporting if it is enabled for the repository.
2. If private reporting is not enabled, open a minimal public issue asking for a private disclosure channel and include no sensitive details.

## Scope

Security-sensitive areas include:

- workload command execution.
- path traversal and symlink handling around artifacts and data bundles.
- credential redaction in logs, summaries, and run evidence.
- provider authentication checks.
- remote artifact fetch and archive extraction.
- generated package contents for remote execution.

## Current Limitations

- `modal-sandbox` is experimental and not live verified in this repo state.
- The project does not yet implement package manifests or secret scanning for remote bundles.
- The project does not yet provide sandbox isolation for local workloads.

Treat workload YAML and scripts as trusted input for now.
