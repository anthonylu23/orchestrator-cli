# China Live Smoke Guide

This guide is for the China colleague-owned smoke pass. The current implementation is code-ready for integration testing, but no China provider is live verified yet.

## Status Rule

Do not mark `alibaba-cloud`, `huawei-cloud`, `tencent-cloud`, `tianyi-cloud`, or `baidu-ai-cloud` as live verified until a real-account transcript proves:

1. signed auth succeeds against the target account.
2. a VM is created from a configured image.
3. cloud-init/user-data starts the Docker workload.
4. `/tmp/switchboard/logs.txt`, `/tmp/switchboard/events.jsonl`, and `/tmp/switchboard/exit.json` are readable over SSH.
5. the VM is terminated or intentionally retained according to cleanup policy.
6. provider errors are mapped into Switchboard provider error categories.

## Current Branch Scope

The public CLI registry exposes China providers as readiness/auth adapters for `providers` commands. For `train`, a China provider becomes VM-capable when the matching `china_cloud.<provider>` config block is present. The VM clients are unit-tested and code-ready, but the live smoke should still be treated as pending until a China colleague verifies create, SSH log/event collection, exit parsing, and cleanup against real accounts.

Use these configs as smoke inputs:

```txt
examples/china-alibaba-vm.yaml
examples/china-huawei-vm.yaml
examples/china-tencent-vm.yaml
examples/china-tianyi-vm.yaml
examples/china-baidu-vm.yaml
```

## Preflight

Build the CLI and run strict auth first:

```sh
go build -o bin/switchboard-cli ./cmd/switchboard-cli

./bin/switchboard-cli providers check tencent-cloud --strict-auth --json
./bin/switchboard-cli providers check tianyi-cloud --strict-auth --json
./bin/switchboard-cli providers check baidu-ai-cloud --strict-auth --json
```

Run the same strict auth command before any VM smoke for Alibaba and Huawei as well.

## Credentials

Tencent:

```sh
export TENCENTCLOUD_SECRET_ID=...
export TENCENTCLOUD_SECRET_KEY=...
```

Tianyi:

```sh
export CTYUN_ACCESS_KEY=...
export CTYUN_SECRET_KEY=...
```

Baidu:

```sh
export BAIDU_CLOUD_ACCESS_KEY_ID=...
export BAIDU_CLOUD_SECRET_ACCESS_KEY=...
```

Alibaba and Huawei credential names are documented in [China Cloud Provider Readiness](china-cloud-providers.md).

## VM Smoke Checklist

For each provider under test:

1. Replace placeholder IDs in the matching `examples/china-*-vm.yaml` with a real region, zone, image ID, instance type, subnet, security group, SSH key name, and SSH private key.
2. Use an image that has Docker and NVIDIA runtime support when testing GPU workloads.
3. Run only a short deterministic container command first.
4. Run `./bin/switchboard-cli train --provider <provider> --config examples/china-<provider>-vm.yaml`.
5. Confirm the provider console shows exactly one test VM created by Switchboard.
6. Confirm `logs.txt` contains `switchboard china vm job starting`.
7. Confirm `exit.json` contains `exit_code: 0`.
8. Confirm cleanup terminated the VM, or document the retained VM ID and reason.

## Evidence To Record

Capture:

```txt
provider:
config file:
switchboard commit:
strict auth command and result:
VM create request ID/order ID:
VM ID:
observed provider state transitions:
logs artifact path:
events artifact path:
exit marker:
cleanup result:
known provider-specific caveats:
```

Only after that evidence is recorded should `docs/provider-status.md` change from `Pending China smoke` to `Yes` for the verified provider and scope.
