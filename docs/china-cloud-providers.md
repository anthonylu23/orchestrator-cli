# China Cloud Provider Readiness

This branch adds readiness adapters for five major China cloud providers:

| Provider name | Cloud | Status | What works now |
| --- | --- | --- | --- |
| `alibaba-cloud` | Alibaba Cloud | readiness-only | Built-in signed ECS `DescribeRegions` probe, credential env validation, public endpoint probe, and strict auth-command validation |
| `huawei-cloud` | Huawei Cloud | readiness-only | Credential env validation, public endpoint probe, and strict auth-command validation |
| `tencent-cloud` | Tencent Cloud | readiness-only | Built-in TC3-signed CVM `DescribeRegions` probe, credential env validation, public endpoint probe, and strict auth-command validation |
| `tianyi-cloud` | China Telecom Tianyi Cloud / eSurfing Cloud | readiness-only | Credential env validation, public endpoint probe, and strict auth-command validation |
| `baidu-ai-cloud` | Baidu AI Cloud | readiness-only | Built-in BCE-signed BOS probe, credential env validation, public endpoint probe, and strict auth-command validation |

These are not training providers yet. They intentionally reject `train` submission until each cloud has a real compute adapter comparable to the GCP Vertex AI provider.

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

`providers check` returns nonzero if required credential environment variables are missing or the public endpoint cannot be reached. Alibaba Cloud, Tencent Cloud, and Baidu AI Cloud use built-in signed API probes when their credential environment variables are present. Huawei Cloud and Tianyi Cloud currently fall back to endpoint readiness unless a strict auth command is configured.

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

Use official CLIs or a small internal smoke script for providers where SDK-backed auth has not been implemented yet. This is currently required for strict Huawei Cloud and Tianyi Cloud validation.

## Manual GitHub Smoke

The repository includes a manual-only GitHub Actions workflow at `.github/workflows/china-cloud-integration.yml`. It does not run on pull requests or pushes because these checks depend on live cloud credentials and optional provider CLI/SDK setup.

Configure the relevant repository secrets, then run **China Cloud Strict Auth Smoke** from GitHub Actions. Alibaba Cloud, Tencent Cloud, and Baidu AI Cloud can run with their credential secrets alone because Switchboard has built-in signed probes for those providers. Huawei Cloud and Tianyi Cloud need provider-specific auth command secrets until their built-in signers are added:

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

This readiness layer does not:

1. sign Huawei Cloud or Tianyi Cloud API requests unless a user-supplied auth command does so; use `providers check --strict-auth` to require authenticated validation.
2. submit managed training jobs.
3. fetch provider logs.
4. cancel provider jobs.
5. stage local datasets into provider object storage.
6. report live pricing, inventory, or quota.

The next real implementation step should pick one China cloud provider, likely Alibaba Cloud or Huawei Cloud, and build a true image-based compute adapter with the same lifecycle expectations as GCP: auth, submit, poll status, logs, cancel, artifacts, and provider-specific error mapping.
