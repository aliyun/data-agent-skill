# Installation & Configuration — Data Agent MCP Server

> **IMPORTANT: After installing this Skill, you MUST also register the bundled MCP Server in the agent runtime.**
> Without this step, the skill may be loaded but the AI agent will not have the `data_agent_*` tools.

Human-oriented deployment walkthrough (systemd, dacli verification client): see the skill's [README.md](../README.md). This document is the canonical setup reference for the skill.

## MCP Server Setup (Required for All Runtimes)

The Skill does not ship pre-compiled binaries. On first launch, `scripts/select-binary.sh` builds the server from source — Go 1.23+ must be installed on the host (https://go.dev/dl/). Subsequent launches reuse the cached build in `assets/server/bin/`.

The server supports two transport modes. **Prefer the HTTP (Streamable HTTP) transport** — it runs the server as a standalone process that any MCP client can reach over a URL, and works uniformly across local, remote, and hosted runtimes. Use stdio only when the client must launch the binary itself.

### Option 1: Streamable HTTP (recommended)

Start the server as a long-running process (it selects the right binary for the host). The agent or hosting runtime must choose an available local port and pass it through `MCP_PORT`; do not assume a fixed port.

```bash
DATA_AGENT_MCP_PORT=<agent-selected-free-port>
MCP_TRANSPORT=streamable-http MCP_PORT="$DATA_AGENT_MCP_PORT" \
  DATA_AGENT_WORKSPACE_ID=your-workspace-id \
  DATA_AGENT_DMS_UNIT=cn-hangzhou \
  bash /absolute/path/to/alibabacloud-data-agent-mcp-skill/scripts/select-binary.sh
```

Then register it as an HTTP MCP server named `data-agent` (endpoint path `/mcp`):

```json
{
  "mcpServers": {
    "data-agent": {
      "type": "streamable-http",
      "url": "http://localhost:<agent-selected-free-port>/mcp"
    }
  }
}
```

For SSE-only clients, start the server with `MCP_TRANSPORT=sse` instead and connect to `http://localhost:<agent-selected-free-port>/sse`.

### Option 2: stdio (client launches the binary)

Use this when the runtime starts tool binaries directly (e.g. local Claude Code):

```json
{
  "mcpServers": {
    "data-agent": {
      "command": "bash",
      "args": ["/absolute/path/to/alibabacloud-data-agent-mcp-skill/scripts/select-binary.sh"],
      "env": {
        "DATA_AGENT_WORKSPACE_ID": "your-workspace-id",
        "DATA_AGENT_DMS_UNIT": "cn-hangzhou"
      }
    }
  }
}
```

The absolute path must point to the copy of this skill folder that exists on the same machine or container where the agent runtime launches tools.

### OpenClaw / Hosted Runtime Setup

OpenClaw skill installation and MCP server registration are separate steps. For hosted OpenClaw, the MCP server must run where OpenClaw can reach it, not on the user's laptop.

1. Install or copy this skill folder into a location OpenClaw loads as a skill, such as the workspace skills directory, personal agent skills directory, or managed skills directory.
2. Start the bundled server as a long-running HTTP MCP service in the OpenClaw runtime, sidecar, worker image, or internal gateway using `MCP_TRANSPORT=streamable-http`. Register that service as an MCP server named `data-agent` with URL `http://<reachable-host>:<port>/mcp`.
3. Use stdio only if the OpenClaw runtime itself launches local tool processes from the same filesystem that contains this skill. In that case, use the `command` and `args` from "Option 2: stdio" above.
4. Ensure the runtime host can execute `bash` and the bundled `scripts/select-binary.sh` path, and can read Alibaba Cloud credentials from environment variables, `~/.aliyun/config.json`, instance role, or `DATA_AGENT_API_KEY`.
5. If OpenClaw uses a tool allowlist, allow the `data-agent` server or all `data_agent_*` tools. At minimum allow `data_agent_list_workspace_databases`, `data_agent_create_session`, `data_agent_status`, `data_agent_send`, `data_agent_result`, `data_agent_list_sessions`, `data_agent_stop_session`, `data_agent_list_files`, `data_agent_upload_file`, `data_agent_search_dms_databases`, `data_agent_list_tables`, `data_agent_list_imported_tables`, `data_agent_search_instances`, `data_agent_import_database`, `data_agent_list_workspaces`, and `data_agent_list_agents`.
6. Restart or reload the OpenClaw agent runtime after registering the server, then verify that `data_agent_*` tools are visible before running analysis tasks. If tools are not visible, do not attempt Data Agent work through CLI, SDK, or direct API fallbacks.

Do not rely on this repository's `.claude/settings.json` for OpenClaw. That file is only a Claude Code convenience and is not read by remote OpenClaw deployments. Do not ask the task-running agent to fix registration by launching a local server or editing OpenClaw/Qwen settings during the task; that is an installation responsibility for AgentHub/OpenClaw setup.

### Claude Code Setup

If this Skill is installed as a project dependency (symlinked or cloned into your project), the bundled `.claude/settings.json` auto-configures the MCP Server.

For other setups, add to your user-level `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "data-agent": {
      "command": "bash",
      "args": ["/absolute/path/to/alibabacloud-data-agent-mcp-skill/scripts/select-binary.sh"],
      "env": {
        "DATA_AGENT_WORKSPACE_ID": "your-workspace-id",
        "DATA_AGENT_DMS_UNIT": "cn-hangzhou"
      }
    }
  }
}
```

> After saving, restart Claude Code or run `/mcp` to verify the `data-agent` server is connected and 18 tools are available.

## Credentials

The MCP Server uses Alibaba Cloud default credential chain:

**Option 1: Environment Variables**
```bash
export ALIBABA_CLOUD_ACCESS_KEY_ID=your-ak
export ALIBABA_CLOUD_ACCESS_KEY_SECRET=your-sk
```

**Option 2: Config File** (`~/.aliyun/config.json`)
```json
{
  "current": "default",
  "profiles": [
    {
      "name": "default",
      "mode": "AK",
      "access_key_id": "your-ak",
      "access_key_secret": "your-sk",
      "region_id": "cn-hangzhou"
    }
  ]
}
```

**Option 3: Instance Role** (on ECS, automatic)

**Option 4: API Key** (simplified auth, no AK/SK required)
```bash
export DATA_AGENT_API_KEY=your-api-key
```
Or in `~/.data-agent/config.json`:
```json
{ "api_key": "your-api-key" }
```

> **API Key limitations**: `data_agent_list_tables`, `data_agent_search_dms_databases`, `data_agent_search_instances`, and `data_agent_import_database` are not available — they require DMS Enterprise APIs which only support AK/SK. However, `data_agent_list_workspace_databases` and `data_agent_list_imported_tables` **work normally** in API Key mode and remain the mandatory source of database metadata before creating sessions.

## Permission Requirements

RAM users need `AliyunDMSFullAccess` or `AliyunDMSDataAgentFullAccess` permissions. See [RAM policy details](ram-policies.md).

## Configuration

The MCP Server supports configuration via **YAML config file**, **.env file**, and **environment variables**. Priority: env vars > .env file > YAML config > defaults.

**.env file** (secrets; loaded from `$DATA_AGENT_ENV_FILE`, then `./.env`, without overriding already-set env vars — see `assets/server/.env.example`):
```bash
ALIBABA_CLOUD_ACCESS_KEY_ID=your-ak
ALIBABA_CLOUD_ACCESS_KEY_SECRET=your-sk
```

**YAML config file** (non-secret settings; resolved from `$DATA_AGENT_CONFIG` > `./config.yaml` > `~/.data-agent/config.yaml` > legacy `~/.data-agent/config.json` — see `assets/server/config.yaml.example`):
```yaml
region: cn-hangzhou
dms_unit: cn-hangzhou
workspace_id: your-workspace-id
sessions_dir: ~/.data-agent/sessions
```

**Environment Variables** (override .env and config file):

| Variable | Default | Description |
|----------|---------|-------------|
| `ALIBABA_CLOUD_ACCESS_KEY_ID` | — | Alibaba Cloud Access Key ID |
| `ALIBABA_CLOUD_ACCESS_KEY_SECRET` | — | Alibaba Cloud Access Key Secret |
| `DATA_AGENT_REGION` | `cn-hangzhou` | DMS region |
| `BUDDY_REGION` | — | DataBuddy container region fallback when `DATA_AGENT_REGION` is not set |
| `DATA_AGENT_DMS_UNIT` | auto-resolved | DMS unit override (skip GetActiveRouteUnit) |
| `DATA_AGENT_WORKSPACE_ID` | auto-resolved | Workspace ID override (skip auto-resolution) |
| `DATA_AGENT_API_KEY` | — | API Key (alternative to AK/SK, some tools unavailable) |
| `DATA_AGENT_SESSIONS_DIR` | `~/.data-agent/sessions` | Session data directory |
| `DATA_AGENT_CONFIG` | auto-discovered | YAML config file path |
| `DATA_AGENT_ENV_FILE` | `./.env` | .env file path |
| `AILY_SHARED_SECRET` / `IDENTITY_SHARED_SECRET` / `IDENTITY_AUTH_TOKEN` | — | Overrides `identity.auth_token` for caller authentication |
| `MCP_TRANSPORT` / `MCP_PORT` | `stdio` / — | Transport (`stdio` \| `streamable-http` \| `sse`); port required for HTTP transports |
| `DATA_AGENT_UPLOAD_DIRS` | — | `:`-separated directories `data_agent_upload_file` may read; overrides `upload.allowed_dirs`. HTTP transports refuse uploads while unset |
| `DATA_AGENT_DEBUG_SSE` | — | `1` = log raw SSE traffic (debugging) |
| `DATA_AGENT_SESSION_LOOKBACK_DAYS` | `7` | Remote session list lookback window for `data_agent_list_sessions(include_remote=true)` |
| `SKILL_SESSION_ID` | auto-generated by MCP server fallback | 32-character hex observability session ID |

## Multi-Tenant Identity Mode (optional)

Upstream callers that forward per-user identity headers (Feishu Aily by default — it sends `x-aily-user` / `x-aily-email` on every MCP HTTP request; gateways or portals work the same way) can enable `identity` in `config.yaml`. The server maps each end user to a RAM role, obtains temporary credentials via STS AssumeRole (auto-refreshed before expiry), and executes **all Data Agent calls under the end user's role**. RAM access policies and DMS data permissions attached to each role decide what that user can query — the MCP server performs no data-level authorization itself.

```yaml
identity:                           # legacy section name "aily" still accepted
  enabled: true
  require_identity: true            # fail-closed: reject requests without identity headers
  auth_token: ""                    # caller authentication (legacy yaml name "shared_secret"; IDENTITY_AUTH_TOKEN env)
  session_name_prefix: ""           # RoleSessionName "<prefix>-<user_id>"; default prefix "aily"
  headers: {user: "", email: "", token: ""}  # defaults: x-aily-user / x-aily-email / x-aily-token
  default:                          # style 1: global sharing — catch-all role + session defaults
    role_arn: acs:ram::<account-id>:role/da-default
    workspace_id: ""
    custom_agent_id: ""
    mode: ""                        # auto | lite | pro | ultra (legacy ASK_DATA/ANALYSIS/INSIGHT auto-mapped)
  groups:                           # style 2: per-group role + session defaults + members
    analysts:
      role_arn: acs:ram::<account-id>:role/da-analysts
      workspace_id: ws-analysts
      mode: pro
      users: [ou_xxxxxxxx, bob@example.com]
```

Behavior and isolation guarantees:

- Resolution order: named group membership (user id or email) → `identity.default` → **reject** (fail-closed; never silently falls back to the server identity). A user may belong to exactly one group (validated at startup).
- Group `workspace_id` / `custom_agent_id` / `mode` act as defaults for `data_agent_create_session` when the call omits them; explicit tool arguments always win.
- Every identified user gets its own tenant even on a shared/default role: isolated client, session store under `sessions_dir/identity/<user>/`, and its own `RoleSessionName` — so `data_agent_list_sessions`, status, and results never leak across users.
- `RoleSessionName` is `<session_name_prefix>-<user_id>` (default `aily-<user_id>`), making per-user calls traceable in ActionTrail audit logs.
- Requires AK/SK base credentials with `sts:AssumeRole` permission on every group/default role; API Key auth cannot be combined with this mode.
- stdio transport has no HTTP headers, so identity mode only takes effect on the Streamable HTTP / SSE transports.

> **Security note**: the identity headers are injected by the upstream platform and are spoofable by anyone who can reach the endpoint directly. Deploy the HTTP endpoint so that only the upstream egress can reach it (private network / gateway allowlist), and/or set `identity.auth_token` and configure the same value as a custom token header in the upstream MCP registration.

## Observability

All Alibaba Cloud API calls must use this User-Agent template:

```bash
--user-agent AlibabaCloud-Agent-Skills/alibabacloud-data-agent-skill/{session-id}
```

Session-id unified rule: generate a 32-char hex `session-id` once per skill session and keep it consistent across all API calls. When launching the MCP server, pass it through the `SKILL_SESSION_ID` environment variable. If the host does not provide `SKILL_SESSION_ID`, the bundled server generates one 32-character hex fallback at process startup and uses it consistently for all signed, API Key, and SSE requests in that process.
