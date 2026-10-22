# Authentication Guide

The Data Agent SDK and CLI support three authentication modes. Exactly one is active per client instance, resolved in this order:

1. **API_KEY** — active when `DATA_AGENT_API_KEY` is set (or the credentials-file entry carries an `api_key`).
2. **Credentials file** — active when a credentials file exists and contains the selected environment/profile with AK/SK.
3. **Default credential chain** — fallback when neither of the above applies.

## Mode 1: Default Credential Chain (recommended for AK/SK users)

Nothing to configure here. The Alibaba Cloud SDK resolves credentials automatically from, in order:

- environment variables (`ALIBABA_CLOUD_ACCESS_KEY_ID` / `ALIBABA_CLOUD_ACCESS_KEY_SECRET`)
- `~/.aliyun/config.json` (written by the `aliyun` CLI)
- ECS instance role / OIDC role (when running inside Alibaba Cloud)

See the [credential chain documentation](https://help.aliyun.com/document_detail/378659.html).

## Mode 2: API_KEY

```bash
export DATA_AGENT_API_KEY=<your-api-key>
export DATA_AGENT_REGION=cn-hangzhou
```

Get a key from the [Data Agent console](https://agent.dms.aliyun.com/cn-hangzhou/api-key). In this mode requests go to the `dataagent-<region>.aliyuncs.com/apikey` endpoints with the `x-api-key` header; no AK/SK signature is performed.

## Mode 3: Credentials File

For managing multiple accounts or environments without touching global credential state (e.g. on a machine where the default chain already points at another identity).

### Location

- `DATA_AGENT_CREDENTIALS_FILE=/path/to/creds.json` (explicit), or
- `~/.data-agent-credentials.json` (implicit default; silently ignored when absent)

### Format

```json
{
  "default": {
    "default": {
      "access_key_id": "LTAI5t...",
      "access_key_secret": "..."
    }
  },
  "pre-release": {
    "sandbox": {
      "access_key_id": "LTAI5t...",
      "access_key_secret": "...",
      "api_key": "da-..."
    }
  }
}
```

Top level = **environment**; second level = **profile** (a nickname you choose). Each entry holds `access_key_id` + `access_key_secret`, and may additionally carry `api_key` — when present, the `api_key` wins and that profile runs in API_KEY mode.

### Selection

| Variable | Meaning | Default |
|----------|---------|---------|
| `DATA_AGENT_CREDENTIALS_FILE` | Path to the JSON file | `~/.data-agent-credentials.json` |
| `DATA_AGENT_CREDENTIALS_ENV` | Environment key | `default` |
| `DATA_AGENT_CREDENTIALS_PROFILE` | Profile key | `default` |

```bash
export DATA_AGENT_CREDENTIALS_FILE=~/.data-agent-credentials.json
export DATA_AGENT_CREDENTIALS_ENV=pre-release
export DATA_AGENT_CREDENTIALS_PROFILE=sandbox
```

When both selector variables are left unset (`default`), a file offering exactly one environment auto-selects it, and likewise a single profile under the selected environment; explicitly set keys are never auto-corrected (typos fail loudly).

### Error semantics

- Explicit `DATA_AGENT_CREDENTIALS_FILE` pointing at a missing file → startup error (fail loud).
- File exists but lacks the selected environment/profile → startup error listing every environment with its profiles (or the profiles under the selected environment) in a single message, so the full selector recipe is visible in one shot (fail loud, so you never silently run under the wrong identity).
- Implicit default file absent → silent fallback to the default credential chain.
- A profile must provide either `api_key` or both `access_key_id` and `access_key_secret`; anything else → startup error.

### Security notes

- The file is plain JSON — protect it with file permissions (`chmod 600`) and never commit it to version control.
- Prefer the default credential chain or instance roles on servers you control; the credentials file is best for local multi-account workflows.

## Endpoint derivation

| Mode | Endpoint (when `DATA_AGENT_ENDPOINT` is unset) |
|------|------------------------------------------------|
| API_KEY | `dataagent-<region>.aliyuncs.com/apikey` (control plane) / `dataagent-stream-<region>.aliyuncs.com/apikey` (data plane) |
| Credentials file (AK/SK) or default chain | `dms.<region>.aliyuncs.com` |

`DATA_AGENT_ENDPOINT` overrides the derived control-plane value in all modes; `DATA_AGENT_STREAM_ENDPOINT` overrides the API_KEY data-plane (stream) value. Point both at the non-public gateway hosts when targeting such deployments (e.g. pre-release `pre-dataagent.aliyuncs.com/apikey` / `pre-dataagent-stream.aliyuncs.com/apikey`).

## Verifying which mode is active

```bash
python3 -c "from data_agent.config import DataAgentConfig; print(DataAgentConfig.from_env().to_dict())"
```

The `auth_type` field reports `api_key`, `credential_file`, or `default_credential_chain`. Debug logging (`DATA_AGENT_DEBUG_API=1`) prints the effective endpoint and headers per request with secrets redacted.

## RAM permissions

AK/SK identities need `AliyunDMSFullAccess` or `AliyunDMSDataAgentFullAccess`; see [RAM-POLICIES.md](RAM-POLICIES.md).
