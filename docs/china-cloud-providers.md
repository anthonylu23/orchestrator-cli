# China Cloud Provider Readiness

This branch adds readiness adapters for five major China cloud providers and code-ready VM client/runtime seams for the China compute path. Live VM execution is not marked verified yet; it is pending a China colleague smoke run against real accounts.

| Provider name | Cloud | Status | What works now |
| --- | --- | --- | --- |
| `alibaba-cloud` | Alibaba Cloud | code-ready VM provider | Built-in signed ECS `DescribeRegions` probe plus config-gated VM create/describe/terminate runtime |
| `huawei-cloud` | Huawei Cloud | code-ready VM provider | Built-in AK/SK-signed IAM region probe plus config-gated VM create/describe/terminate runtime |
| `tencent-cloud` | Tencent Cloud | code-ready VM provider | Built-in TC3-signed CVM `DescribeRegions` probe plus config-gated VM create/describe/terminate runtime |
| `tianyi-cloud` | China Telecom Tianyi Cloud / eSurfing Cloud | code-ready VM provider | Built-in EOP-signed ECX region-cluster probe plus config-gated VM create/describe/terminate runtime |
| `baidu-ai-cloud` | Baidu AI Cloud | code-ready VM provider | Built-in BCE-signed BOS probe plus config-gated VM create/describe/terminate runtime |

`providers list`, `providers inspect`, and `providers check` expose readiness/auth behavior without needing train config. `switchboard-cli train --provider <china-provider> --config ...` registers the VM runtime when the matching `china_cloud.<provider>` block is configured. This is code-ready but not live verified; use the example configs as integration fixtures, not as a live-verification claim.

## Provider Selection

The first five China targets were selected from current market and roadmap fit:

1. Canalys reported that Alibaba Cloud, Huawei Cloud, and Tencent Cloud led Mainland China's Q1 2025 cloud infrastructure market, with Alibaba at 33%, Huawei at 18%, and Tencent at 10%.
2. Canalys also noted that telco cloud services led by China Telecom are gradually capturing share.
3. Baidu AI Cloud remains a major China cloud and AI-cloud provider in IDC/Canalys-era market reporting.

China Mobile Cloud is a likely follow-up target, especially for telco/government workloads, but this first slice keeps the requested top-five implementation bounded.

Sources:

- https://www.canalys.com/newsroom/china-cloud-market-q1-2025
- https://www.canalys.com/newsroom/mainland-china-cloud-service-q1-2024
- https://canalys.com/static/press_release/2023/263880002china-cloud-market-Q4-2022.pdf

## Commands

List providers:

```sh
switchboard-cli providers list --json
```

Inspect static capabilities without credentials:

```sh
switchboard-cli providers inspect alibaba-cloud
switchboard-cli providers inspect huawei-cloud
switchboard-cli providers inspect tencent-cloud
switchboard-cli providers inspect tianyi-cloud
switchboard-cli providers inspect baidu-ai-cloud
```

Check credentials and endpoint reachability:

```sh
switchboard-cli providers check alibaba-cloud
```

`providers check` returns nonzero if required credential environment variables are missing or the public endpoint cannot be reached. All five China cloud providers use built-in signed API probes when their credential environment variables are present. A provider-specific auth command still takes precedence when configured.

For China cloud providers, endpoint-only success is reported as `auth_mode: endpoint_probe` with `authenticated: false`, because an endpoint probe does not prove the credentials can call that cloud API. Built-in signed checks and auth-command checks report `authenticated: true`.

For stronger live validation, set a provider-specific auth command. When this variable is present, `providers check` runs the command and requires it to exit successfully instead of using the fallback env-plus-endpoint probe. The command output is not persisted by Switchboard.

Examples:

```sh
export SWITCHBOARD_ALIBABA_CLOUD_AUTH_COMMAND='aliyun ecs DescribeRegions --RegionId cn-hangzhou'
export SWITCHBOARD_TENCENT_CLOUD_AUTH_COMMAND='tccli cvm DescribeRegions --region ap-guangzhou'
```

Require authenticated validation and fail closed if neither built-in signed auth nor an auth command is available:

```sh
switchboard-cli providers check alibaba-cloud --strict-auth
switchboard-cli providers check tencent-cloud --strict-auth --json
```

Use official CLIs or a small internal smoke script when you want to override the built-in signed probes with a provider-maintained command.

## VM Runtime Coverage

The China VM runtime shape is image-first and mirrors the Lambda single-instance lifecycle: create a VM, run Docker through cloud-init/user-data, collect `/tmp/switchboard/logs.txt`, `/tmp/switchboard/events.jsonl`, and `/tmp/switchboard/exit.json` over SSH, then terminate according to cleanup policy.

All five providers now have provider-specific VM clients that build signed create, describe, and terminate API requests from the shared `VMCreateRequest` shape. Tests use fake HTTP servers to verify request signing headers, payload fields, user-data propagation, status parsing, and terminate calls without requiring live China credentials.

Live verification remains pending. Do not mark any China provider as live verified until the China colleague smoke in [China Live Smoke Guide](china-live-smoke.md) records a passing transcript against real cloud accounts.

Example VM configs live under `examples/china-*-vm.yaml`. They pass provider object-storage URIs through job args/env for the container to resolve itself because first-class OSS/OBS/COS/OOS/BOS data staging is still roadmap work. They are integration fixtures for the code-ready VM path and should not be used as proof that live `train` execution has been verified.

## Manual GitHub Smoke

The repository includes a manual-only GitHub Actions workflow at `.github/workflows/china-cloud-integration.yml`. It does not run on pull requests or pushes because these checks depend on live cloud credentials and optional provider CLI/SDK setup.

Configure the relevant repository secrets, then run **China Cloud Strict Auth Smoke** from GitHub Actions. All five providers can run with their credential secrets alone because Switchboard has built-in signed probes. Provider-specific auth command secrets can override the built-in probes:

```txt
SWITCHBOARD_ALIBABA_CLOUD_AUTH_COMMAND
SWITCHBOARD_HUAWEI_CLOUD_AUTH_COMMAND
SWITCHBOARD_TENCENT_CLOUD_AUTH_COMMAND
SWITCHBOARD_TIANYI_CLOUD_AUTH_COMMAND
SWITCHBOARD_BAIDU_AI_CLOUD_AUTH_COMMAND
```

Credential secrets such as `ALIBABA_CLOUD_ACCESS_KEY_ID`, `TENCENTCLOUD_SECRET_ID`, and the provider-specific secret keys can also be configured when the auth command needs them or when built-in auth is available. The workflow builds the CLI and runs:

```sh
switchboard-cli providers check <provider> --strict-auth --json
```

If no strict auth command secret or built-in credential pair is configured for the selected provider, the workflow fails instead of reporting a false live validation.

## Credential Environment Variables

### Alibaba Cloud

```sh
export ALIBABA_CLOUD_ACCESS_KEY_ID=...
export ALIBABA_CLOUD_ACCESS_KEY_SECRET=...
```

Default endpoint probe: `https://ecs.cn-hangzhou.aliyuncs.com/`

Override:

```sh
export SWITCHBOARD_ALIBABA_CLOUD_ENDPOINT=https://ecs.cn-shanghai.aliyuncs.com/
```

Optional live-auth command:

```sh
export SWITCHBOARD_ALIBABA_CLOUD_AUTH_COMMAND='aliyun ecs DescribeRegions --RegionId cn-hangzhou'
```

Alibaba Cloud endpoint documentation: https://www.alibabacloud.com/help/doc-detail/2392167.html

### Huawei Cloud

```sh
export HUAWEICLOUD_SDK_AK=...
export HUAWEICLOUD_SDK_SK=...
```

Alternate names accepted:

```sh
export HUAWEI_CLOUD_ACCESS_KEY_ID=...
export HUAWEI_CLOUD_SECRET_ACCESS_KEY=...
```

Default endpoint probe: `https://ecs.cn-north-4.myhuaweicloud.com/`

Override:

```sh
export SWITCHBOARD_HUAWEI_CLOUD_ENDPOINT=https://ecs.cn-south-1.myhuaweicloud.com/
```

Optional live-auth command:

```sh
export SWITCHBOARD_HUAWEI_CLOUD_AUTH_COMMAND='<your huawei cloud cli auth smoke command>'
```

Huawei Cloud AK/SK signing documentation: https://support.huaweicloud.com/intl/en-us/devg-apisign/api-sign-provide01.html

### Tencent Cloud

```sh
export TENCENTCLOUD_SECRET_ID=...
export TENCENTCLOUD_SECRET_KEY=...
```

Default endpoint probe: `https://cvm.tencentcloudapi.com/`

Override:

```sh
export SWITCHBOARD_TENCENT_CLOUD_ENDPOINT=https://cvm.tencentcloudapi.com/
```

Optional live-auth command:

```sh
export SWITCHBOARD_TENCENT_CLOUD_AUTH_COMMAND='tccli cvm DescribeRegions --region ap-guangzhou'
```

Tencent Cloud Go SDK documentation: https://cloud.tencent.com/document/sdk/Go

### Tianyi Cloud / eSurfing Cloud

```sh
export CTYUN_ACCESS_KEY=...
export CTYUN_SECRET_KEY=...
```

Alternate names accepted:

```sh
export TIANYI_CLOUD_ACCESS_KEY=...
export TIANYI_CLOUD_SECRET_KEY=...
```

Default endpoint probe: `https://ecx-global.ctapi.ctyun.cn/`

Override:

```sh
export SWITCHBOARD_TIANYI_CLOUD_ENDPOINT=https://ecx-global.ctapi.ctyun.cn/
```

Optional live-auth command:

```sh
export SWITCHBOARD_TIANYI_CLOUD_AUTH_COMMAND='<your tianyi cloud cli auth smoke command>'
```

Tianyi Cloud AK/SK and endpoint documentation: https://www.ctyun.cn/document/10011497/10629581

### Baidu AI Cloud

```sh
export BAIDU_CLOUD_ACCESS_KEY_ID=...
export BAIDU_CLOUD_SECRET_ACCESS_KEY=...
```

Alternate names accepted:

```sh
export BCE_ACCESS_KEY_ID=...
export BCE_SECRET_ACCESS_KEY=...
```

Default endpoint probe: `https://bj.bcebos.com/`

Override:

```sh
export SWITCHBOARD_BAIDU_AI_CLOUD_ENDPOINT=https://gz.bcebos.com/
```

Optional live-auth command:

```sh
export SWITCHBOARD_BAIDU_AI_CLOUD_AUTH_COMMAND='<your baidu ai cloud cli auth smoke command>'
```

Baidu AI Cloud BOS SDK documentation: https://intl.cloud.baidu.com/en/doc/BOS/s/Tjwvyrw7a-intl-en

## Current Limits

The public readiness provider path does not yet:

1. submit managed training jobs.
2. expose China VM logs through the normal CLI registry path.
3. cancel China VM jobs through the normal CLI registry path.
4. stage local datasets into provider object storage.
5. report live pricing, inventory, or quota.

The next real implementation step is to run the colleague-owned live smoke, then mark only providers that pass create, poll, log/event collection, exit marker parsing, cleanup, and provider-specific error mapping as live verified.
