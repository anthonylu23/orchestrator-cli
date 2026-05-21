# Credentials

Switchboard supports an encrypted local credentials store for provider secrets. The store is cross-platform and does not depend on macOS Keychain, Linux Secret Service, Windows Credential Manager, or a hosted secret manager.

## Store Location

By default, credentials are stored at:

```text
~/.switchboard-cli/credentials.enc
```

Use `--home` or `SWITCHBOARD_CLI_HOME` to place the store somewhere else.

The file contains an encrypted JSON document. It is encrypted with AES-256-GCM using a key derived from the credentials passphrase with Argon2id. Switchboard writes the file with mode `0600` and replaces it atomically on updates.

## Passphrase

For automation and tests, provide the passphrase with:

```sh
export SWITCHBOARD_CREDENTIALS_PASSPHRASE=...
```

For interactive commands, Switchboard prompts for the passphrase without echoing it.

Do not commit the passphrase or put it in job config.

## Commands

Initialize the store:

```sh
switchboard-cli credentials init
```

Set a credential from an environment variable:

```sh
switchboard-cli credentials set lambda api-key --from-env LAMBDA_API_KEY
```

Set a credential from stdin:

```sh
printf '%s' "$LAMBDA_API_KEY" | switchboard-cli credentials set lambda api-key --value-stdin
```

List metadata without revealing values:

```sh
switchboard-cli credentials list
switchboard-cli credentials status lambda
```

Check a credential:

```sh
switchboard-cli credentials get lambda api-key
```

Reveal a value only when explicitly requested:

```sh
switchboard-cli credentials get lambda api-key --show
```

Delete a credential:

```sh
switchboard-cli credentials delete lambda api-key
```

## Resolution Order

Providers resolve credentials in this order:

1. Explicit provider environment variables when supported.
2. Encrypted local store.
3. Provider-native auth, where applicable.
4. Actionable auth error.

Lambda resolves API auth only from encrypted `lambda/api_key`.

China VM providers accept environment variables for CI/live smoke and canonical encrypted-store keys for local use:

```txt
alibaba_cloud/access_key_id
alibaba_cloud/access_key_secret
huawei_cloud/access_key_id
huawei_cloud/secret_access_key
tencent_cloud/secret_id
tencent_cloud/secret_key
tianyi_cloud/access_key
tianyi_cloud/secret_key
baidu_ai_cloud/access_key_id
baidu_ai_cloud/secret_access_key
```

GCP continues to use Application Default Credentials.

## Safety Rules

Credential values are not printed by `list`, `status`, or `get` unless `--show` is passed. They are not stored in SQLite, run summaries, events, or logs by the credentials manager. Provider code should continue to use the redaction package before writing any user or provider-controlled content to artifacts.
