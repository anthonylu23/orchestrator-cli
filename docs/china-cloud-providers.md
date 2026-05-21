# China Cloud Provider Readiness

This branch adds readiness adapters for five major China cloud providers:

| Provider name | Cloud | Status | What works now |
| --- | --- | --- | --- |
| `alibaba-cloud` | Alibaba Cloud | readiness-only | Credential env validation, public endpoint probe, and strict auth-command validation |
| `huawei-cloud` | Huawei Cloud | readiness-only | Credential env validation, public endpoint probe, and strict auth-command validation |
| `tencent-cloud` | Tencent Cloud | readiness-only | Credential env validation, public endpoint probe, and strict auth-command validation |
| `tianyi-cloud` | China Telecom Tianyi Cloud / eSurfing Cloud | readiness-only | Credential env validation, public endpoint probe, and strict auth-command validation |
| `baidu-ai-cloud` | Baidu AI Cloud | readiness-only | Credential env validation, public endpoint probe, and strict auth-command validation |

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

`providers check` returns nonzero if required credential environment variables are missing or the public endpoint cannot be reached. For China cloud providers, the default success mode is reported as `auth_mode: endpoint_probe` with `authenticated: false`, because an endpoint probe does not prove the credentials can call that cloud API.

For stronger live validation, set a provider-specific auth command. When this variable is present, `providers check` runs the command and requires it to exit successfully instead of using the fallback env-plus-endpoint probe. The command output is not persisted by Switchboard.

Examples:

```sh
export SWITCHBOARD_ALIBABA_CLOUD_AUTH_COMMAND='aliyun ecs DescribeRegions --RegionId cn-hangzhou'
export SWITCHBOARD_TENCENT_CLOUD_AUTH_COMMAND='tccli cvm DescribeRegions --region ap-guangzhou'
```

Require authenticated validation and fail closed if no auth command is configured:

```sh
switchboard-cli providers check alibaba-cloud --strict-auth
switchboard-cli providers check tencent-cloud --strict-auth --json
```

Use official CLIs or a small internal smoke script for providers where SDK-backed auth has not been implemented yet.

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

1. sign provider API requests unless a user-supplied auth command does so; use `providers check --strict-auth` to require that mode.
2. submit managed training jobs.
3. fetch provider logs.
4. cancel provider jobs.
5. stage local datasets into provider object storage.
6. report live pricing, inventory, or quota.

The next real implementation step should pick one China cloud provider, likely Alibaba Cloud or Huawei Cloud, and build a true image-based compute adapter with the same lifecycle expectations as GCP: auth, submit, poll status, logs, cancel, artifacts, and provider-specific error mapping.
