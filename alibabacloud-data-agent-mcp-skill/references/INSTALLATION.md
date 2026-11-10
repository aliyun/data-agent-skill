# Installation & Configuration — Data Agent MCP Server

> **IMPORTANT: After installing this Skill, you MUST also register a separately built or deployed MCP Server in the agent runtime.**
> Without this step, the skill may be loaded but the AI agent will not have the `data_agent_*` tools.

Human-oriented deployment walkthrough (systemd, dacli verification client): see the skill's [README.md](../README.md). This document is the canonical setup reference for the skill.

**Security remediation is implemented locally, not accepted for production release.** The configuration below describes the remediation contract; historical cloud checks do not satisfy the [post-remediation release gate](#post-remediation-release-gate--not-yet-verified). Do not deploy until the applicable gates pass.

## MCP Server Setup (Required for All Runtimes)

The Skill ships neither server source nor pre-compiled binaries. Obtain a `data-agent-mcp-server` binary first — build it from the repository's top-level `server/` project (`cd server && make build`, Go 1.26.10+, https://go.dev/dl/) or use a release binary. `scripts/select-binary.sh` locates it in this order: `$DATA_AGENT_SERVER_BIN` (explicit path) → the skill's `assets/bin/` → the monorepo's `server/bin/`. Standalone server deployments (systemd, containers) run the binary directly and do not need the launcher.

The server supports stdio, Streamable HTTP and SSE. Use **Streamable HTTP** for an authorized remote deployment whose client carries credentials on every method and supports session renewal; use stdio only for a local, single-process identity. SSE transport needs a compatible authenticated host; dacli is Streamable HTTP only.

### Option 1: Streamable HTTP (recommended)

First configure [mandatory remote identity](#multi-tenant-identity-mode-required-remotely), secrets and limits. These apply even on localhost and with the gateway off. Start the server only as an authorized deployment/validation action; select an available port through `MCP_PORT`, not a fixed assumption.

```bash
DATA_AGENT_MCP_PORT=<agent-selected-free-port>
MCP_TRANSPORT=streamable-http MCP_PORT="$DATA_AGENT_MCP_PORT" \
  DATA_AGENT_WORKSPACE_ID=your-workspace-id \
  DATA_AGENT_DMS_UNIT=cn-hangzhou \
  bash /absolute/path/to/alibabacloud-data-agent-mcp-skill/scripts/select-binary.sh
```

Then register it as an HTTP MCP server named `data-agent` (endpoint path `/mcp`). The fragment below shows routing only: separately provision platform token and JWT (or trusted-proxy identity) on **all** GET/POST/DELETE requests, including initialization and notifications. URL-only registration is insufficient; use protected transport outside loopback and never embed secrets in the URL.

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
2. Provision the built/deployed server with mandatory remote identity, role mappings, secret injection and limits, then start it as an authorized HTTP MCP service in the OpenClaw runtime, sidecar, worker image or internal gateway using `MCP_TRANSPORT=streamable-http`. Register `data-agent` at the reachable `/mcp` URL over protected transport, carrying authentication on every method; URL-only registration is insufficient.
3. Use stdio only if the OpenClaw runtime itself launches local tool processes from the same filesystem that contains this skill. In that case, use the `command` and `args` from "Option 2: stdio" above.
4. Ensure the runtime host can execute `bash` and the bundled `scripts/select-binary.sh` path, and has usable AK/STS through the credentials-go default chain (or a legacy API Key for local stdio only; see Credentials below). Do not assume Alibaba Cloud CLI profiles are imported.
5. If OpenClaw uses a tool allowlist, allow the `data-agent` server or all `data_agent_*` tools. At minimum allow `data_agent_list_workspace_databases`, `data_agent_create_session`, `data_agent_status`, `data_agent_send`, `data_agent_result`, `data_agent_list_sessions`, `data_agent_stop_session`, `data_agent_list_files`, `data_agent_get_upload_signature`, `data_agent_upload_callback`, `data_agent_search_dms_databases`, `data_agent_list_tables`, `data_agent_list_imported_tables`, `data_agent_search_instances`, `data_agent_import_database`, `data_agent_list_workspaces`, and `data_agent_list_agents`.
6. For the optional gateway, allow the nine base `data_gateway_*` tools and, only when P2b is enabled, its two summary tools listed below. Configure remote identity and accept the summary visibility scope before enabling P2b. Restart/reload and verify 19 tools off / 28 base / 30 with P2b (before host filtering). Tool visibility is not authorization validation. If required tools are absent, do not attempt CLI, SDK, direct API or alternate-backend fallbacks.

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

> After saving, restart Claude Code or run `/mcp` to verify the `data-agent` server is connected: 19 tools off, 28 with the base gateway, 30 with P2b too (before host-side filtering).

## Credentials

Without a nonempty API Key, startup uses the **credentials-go default chain** and retains its provider for dynamic credential retrieval and RAM AssumeRole source chaining. The server does not hand-read `~/.aliyun/config.json`; an Alibaba Cloud CLI profile is not automatically an SDK credential source. Configure a provider supported by the deployed credentials-go version in the actual server process/container.

**Explicit environment credentials** (or the server's `.env`):
```bash
export ALIBABA_CLOUD_ACCESS_KEY_ID=your-ak
export ALIBABA_CLOUD_ACCESS_KEY_SECRET=your-sk
# Also required when the pair is temporary STS credentials:
# export ALIBABA_CLOUD_SECURITY_TOKEN=your-sts-security-token
```

Alternatively, configure an SDK-supported instance/workload-role provider, such as an ECS RAM role. Remove placeholder AK/SK entries when using another provider so they do not override it. Refreshable providers must remain live; a startup credential snapshot is not sufficient. Static temporary environment values do not self-renew. In identity mode the retained source provider supplies the credentials for each user's RAM AssumeRole provider; its principal needs `sts:AssumeRole` on the mapped roles.

### API Key compatibility — local stdio only

Legacy API Key runtime authentication is supported for a **local, single-process stdio** deployment using the existing inputs:

- YAML/legacy JSON keys: `api_key`, `api_key_endpoint`, `api_key_stream_endpoint`.
- Environment variables: `DATA_AGENT_API_KEY`, `DATA_AGENT_API_KEY_ENDPOINT`, `DATA_AGENT_API_KEY_STREAM_ENDPOINT`.

Empty/null `api_key` or old endpoint settings alone do not activate API Key mode and do not block AK/STS multi-tenant startup. A nonempty API Key cannot coexist with `identity.enabled=true` or `agent_gateway.enabled=true`, and is not supported for HTTP/SSE. Remote transports still require strong identity on every request, without anonymous or global-key fallback. Without a nonempty API Key, the SDK chain must still provide usable credentials.

Inbound `identity.auth_token`, JWT, dacli `--token`, SDK STS `SecurityToken` and OSS upload signatures are distinct from API Key mode and remain supported. The independent Python CLI skill is unchanged. **Real-cloud authentication for the restored local API Key path remains unverified**; historical SDK/STS results do not validate it.

## Permission Requirements

Configure the least-privilege RAM API actions and DMS data access needed by the deployed tools. Existing Data Agent policies do not establish gateway data authorization; exact Agentic permissions and role mapping require authorized P0 validation. See [RAM policy details](ram-policies.md).

## Configuration

The MCP Server supports configuration via **YAML config file**, **.env file**, and **environment variables**. Priority: env vars > .env file > YAML config > defaults.

Prefer managed environment injection; if using dotenv, follow [secret-file ownership/mode requirements](#secret-files). It loads from `$DATA_AGENT_ENV_FILE`, else `./.env`, without overriding existing environment values. See [public environment example](../../server/.env.example); do not copy credential placeholders over an intended workload-role provider.

**YAML config file** (non-secret settings; resolved from `$DATA_AGENT_CONFIG` > `./config.yaml` > `~/.data-agent/config.yaml` > legacy `~/.data-agent/config.json` — see [full example](../../server/config.yaml.example)). The following is **local stdio only** until mandatory remote identity is added:
```yaml
region: cn-hangzhou
dms_unit: cn-hangzhou
workspace_id: your-workspace-id
custom_agent_id: ""               # default custom agent; empty = backend built-in
sessions_dir: ~/.data-agent/sessions
# Optional: VPC endpoint for the dms-enterprise metadata APIs when the host
# has no public egress. Empty = dms-enterprise.{region}.aliyuncs.com.
dms_enterprise_endpoint: ""
# Optional: host of the AK/STS-signed Data Agent API (sessions + SSE).
# Empty = dms.{region}.aliyuncs.com. Gateway has a separate, public-only target.
data_agent_endpoint: ""
agent_gateway:
  enabled: false
  data_sources_enabled: false       # P2b; requires enabled=true and accepted visibility scope
  endpoint: dms.cn-hangzhou.aliyuncs.com
  request_timeout_seconds: 30
  max_response_bytes: 4194304
```

### Optional Agentic Metadata Gateway

Both `agent_gateway.enabled` and `agent_gateway.data_sources_enabled` default to `false`. The latter requires `enabled=true`; enabling it alone is invalid. Before host filtering, expect **19** tools with both off, **28** with only the base gateway enabled (9 gateway tools), or **30** with P2b summaries also enabled (11 gateway tools). P2a uses the base switch without its own flag; the original six contracts are unchanged. Enable P2b only after the deployer accepts the current upstream principal's control-plane summary visibility scope; otherwise keep it disabled.

The endpoint must be **explicit** when enabled: only the public `dms.<region>.aliyuncs.com` host matching `region` is accepted. No scheme, port, path, query, fragment, userinfo, VPC endpoint or arbitrary host. HTTPS redirects are rejected; settings do not inherit a custom Data Agent endpoint. Defaults are a 30-second outbound HTTP budget and 4 MiB decoded response limit (including errors). Resolver/STS retrieval has a separate lifetime, not an end-to-end 30-second promise. Disabled mode adds no gateway tools or requests; enabling does not probe the network at startup. No extra gateway token, user map, transport or dynamic Action setting is needed.

For **all HTTP/SSE tools and protocol methods**, require `identity.enabled=true`, `require_identity=true` and nonempty `auth_token`, regardless of gateway flags. Omitted YAML `require_identity` defaults to true; explicit false rejects remote startup. Omitted/empty `namespace` defaults to `default`. JWT mode requires verified tenant/agent/user claims and consistently scoped or legacy shared mappings, never unsigned Header/global-key fallback. Explicit Header mode requires empty tenant/agent scopes and a trusted proxy. Follow [identity and trust requirements](#identity-scopes-and-trust). The gateway always uses SDK/STS; stdio represents one local process identity, not per-chat-user authorization.

| Tool | Parameters (`?` = optional) |
|---|---|
| `data_gateway_list_catalogs` | `page_size?`, `next_token?` |
| `data_gateway_search_catalogs` | `search_key`, `page_size?`, `next_token?` |
| `data_gateway_get_catalog` | `catalog_uuid` |
| `data_gateway_list_databases` | `catalog_uuid`, `page_size?`, `next_token?` |
| `data_gateway_search_databases` | `search_key`, `catalog_uuid?`, `db_type?`, `env_type?`, `page_size?`, `next_token?` |
| `data_gateway_get_database` | `database_uuid` |
| `data_gateway_list_tables` | `database_uuid`, `page_size?`, `next_token?` |
| `data_gateway_get_table_columns` | `database_uuid`, `table_qualified_name` |
| `data_gateway_get_table_indexes` | `database_uuid`, `table_qualified_name` |
| `data_gateway_list_data_sources` (P2b) | `db_type?`, `page_size?`, `next_token?` |
| `data_gateway_get_data_source` (P2b) | `data_source_uuid` |

P2a maps to `GetAgenticCatalog`, `SearchAgenticDatabase` and `GetAgenticDatabase`. Required strings and supplied optional strings must be nonempty. Database search requires `search_key`; `db_type` uses official uppercase types without automatic case conversion, and `env_type` accepts only `product/pre/dev/test/sit/uat/pet/stag`. Omitting `catalog_uuid` searches across the service-authorized Catalog scope; filters do not replace authorization. Root Catalog search still accepts name/alias/address prefixes.

The five base list/search tools plus P2b List paginate one page per call (`page_size` default 50, range 1..100); pass returned `next_token` unchanged for the same identity, target and filters. Database search returns a `Data` array using the existing cursor format. Catalog/database details return `Data` objects with no pagination, not singleton arrays; their `Data:null` semantics remain unconfirmed and incompatible, not successful empty results. Columns/indexes remain unpaginated.

#### P2b contract and visibility

P2b implements only two read-only control-plane summary tools:

- `data_gateway_list_data_sources` → `ListAgenticDataSources`: only optional uppercase-format `db_type` (no automatic case conversion), `page_size` (default 50, range 1..100), and `next_token`. Do not pass `search_key`, `Host`, `State` or any other filter.
- `data_gateway_get_data_source` → `GetAgenticDataSource`: only required `data_source_uuid`, passed unchanged; no pagination.
- Output allowlist: eight strings `DataSourceUuid`, `Alias`, `DbType`, `EnvType`, `InstanceSource`, `RegionId`, `CatalogUuid`, `CatalogName`, plus signed int32 `State` (-2147483648..2147483647). Preserve explicit optional-field nulls. Nonempty entities require `DataSourceUuid`, and a detail's UUID must match the request. Drop every authentication, network, permission and unknown field, plus `TotalCount`.
- Current Get accepts only a valid `Data` object. Missing `Data`, `{}` (including empty `Data` objects), `Data:null` and wrong shapes are incompatible. The planned successful `Data:null` case is deferred this round for lack of a real sample; preserving optional-field nulls does not mean success-null details are supported.

The scope is the **current upstream RAM/STS principal's control-plane visibility, not the chat user's database ACL**. Same-role sessions share permissions; release requires deployer acceptance of that summary scope, otherwise leave P2b disabled. A positive test does not prove account-wide visibility, cross-role isolation or least privilege. The P2b allowlist is not server-wide Host confidentiality: root Catalog address-prefix search and existing Data Agent discovery are unchanged.

All gateway tools reject unknown parameters and return a single TextContent containing one complete JSON object: `status` (`OK`/`ERROR`), `data`, `request_id`, optional real `upstream_request_id`; only paginated tools include `pagination` (`has_more`, and `next_token` if nonempty). Errors have `isError=true` and sanitized `error: {code, message, retryable}`. Follow the [discovery chain](../SKILL.md#compact-discovery-chain); use P2b List → Get with a returned `DataSourceUuid` for summaries.

#### Evidence and release gates

**Historical pre-remediation evidence (2026-10-29), not validation of the new binary:** P2a automated tests and real-cloud discovery passed in Hangzhou via stdio and configured-role HTTP/SSE, including detail objects, search arrays, pagination and empty results. P2b automated tests, race, vet and independent-binary stdio/HTTP/SSE checks for all 19/28/30 configurations passed. Each transport verified real source pagination, DbType filtering, nine-field sanitized details and missing-source errors, plus the original nine Actions; remote calls used a configured test role's STS, while stdio used the process SDK identity.

The configured role directly called P2b List/Get successfully in `cn-hangzhou`. Raw detail had 23 fields including `Host` and `AuthPassword`; values were not recorded. A format-preserving nonexistent ID returned HTTP 404 `Code=DATA_SOURCE_NOT_FOUND`, no `Data`, with an upstream RequestId recorded in historical evidence. This remains `ERROR`, not successful null; current classification is `GATEWAY_UPSTREAM_INCOMPATIBLE`, with no added error code. Historical role/resource/request identifiers are not deployment examples.

These checks are **not production validation**. P2b machine-readable RAM metadata lists both Actions at `authLevel=operate`, Resource `*`, `conditions=[]`, while help pages say no authorization information; this discrepancy and minimum permissions require acceptance, not a guessed fine-grained Resource or FullAccess grant. `GetAgenticDatabase` minimum RAM authorization, cross-role data ACLs, real JWT integration and other P0 gates remain unresolved. Test deployment-specific allowed/denied visibility and credential lifecycle; see [RAM evidence and validation requirements](ram-policies.md#agentic-metadata-gateway-p0-authorization-validation-pending).

Preserve returned CatalogUuid / DatabaseUuid / QualifiedName / DataSourceUuid, but do not use them as Data Agent `database_id`: sessions require a fresh `data_agent_list_workspace_databases` row. P2c/P3 and Wiki/ACL remain unimplemented; no gateway SQL history/execution, data-source writes, connection tests, Token, Wiki or ACL/self-permission tools are exposed. No placeholder `sql_history_enabled` switch is added. The old Data Agent SQL path does not traverse these tools and receives **no new row/column protection**.

### Environment Variables

Environment variables override .env and config file:

| Variable | Default | Description |
|----------|---------|-------------|
| `ALIBABA_CLOUD_ACCESS_KEY_ID` | — | Alibaba Cloud Access Key ID |
| `ALIBABA_CLOUD_ACCESS_KEY_SECRET` | — | Alibaba Cloud Access Key Secret |
| `ALIBABA_CLOUD_SECURITY_TOKEN` | — | STS token, read with the AK/SK pair when using temporary credentials |
| `DATA_AGENT_API_KEY` | — | Legacy local-stdio API Key; incompatible with identity/gateway enabled and HTTP/SSE |
| `DATA_AGENT_API_KEY_ENDPOINT` / `DATA_AGENT_API_KEY_STREAM_ENDPOINT` | — | Legacy API Key endpoint overrides; alone do not activate API Key mode |
| `DATA_AGENT_REGION` | `cn-hangzhou` | DMS region |
| `BUDDY_REGION` | — | DataBuddy container region fallback when `DATA_AGENT_REGION` is not set |
| `DATA_AGENT_DMS_UNIT` | auto-resolved | DMS unit override (skip GetActiveRouteUnit) |
| `DATA_AGENT_DMS_ENTERPRISE_ENDPOINT` | `dms-enterprise.{region}.aliyuncs.com` | dms-enterprise host for metadata APIs (discovery, import tagging, GetActiveRouteUnit); overrides `dms_enterprise_endpoint`. Use for VPC-only egress or non-public-cloud |
| `DATA_AGENT_ENDPOINT` | `dms.{region}.aliyuncs.com` | Host of the AK/SK-signed Data Agent API (session create/send/status + SSE); overrides `data_agent_endpoint` |
| `DATA_AGENT_WORKSPACE_ID` | auto-resolved | Workspace ID override (skip auto-resolution) |
| `DATA_AGENT_CUSTOM_AGENT_ID` | — | Default custom agent; overrides `custom_agent_id` (identity group defaults and tool arguments win over it) |
| `DATA_AGENT_SESSIONS_DIR` | `~/.data-agent/sessions` | Session data directory |
| `DATA_AGENT_CONFIG` | auto-discovered | YAML config file path |
| `DATA_AGENT_ENV_FILE` | `./.env` | .env file path |
| `DOTENV_UNSAFE_FILES_ALLOW` | disabled | `1` in process environment only: transitional [secret-file exception](#secret-files), not an unsafe .env self-opt-in |
| `AILY_SHARED_SECRET` / `IDENTITY_SHARED_SECRET` / `IDENTITY_AUTH_TOKEN` | — | Overrides `identity.auth_token` for caller authentication |
| `IDENTITY_JWT_SECRET` | — | Overrides `identity.jwt.secret`, the HS256 key verifying the upstream-signed identity token |
| `IDENTITY_JWT_SECRETS` | — | Additional comma-separated keys with equivalent signing authority in the same trusted domain |
| `MCP_TRANSPORT` / `MCP_PORT` | `stdio` / — | Transport (`stdio` \| `streamable-http` \| `sse`); port and mandatory identity required for HTTP transports |
| `DATA_AGENT_LOG_REQUESTS` | `basic` on HTTP, `off` on stdio | Per-call logging: `basic` \| `full` (adds redacted arguments) \| `off`; overrides `log.requests` |
| `DATA_AGENT_DEBUG_SSE` | — | `1` = bounded SSE diagnostic metadata; no raw payloads or credentials |
| `DATA_AGENT_SESSION_LOOKBACK_DAYS` | `7` | Remote session list lookback window for `data_agent_list_sessions(include_remote=true)` |
| `SKILL_SESSION_ID` | auto-generated by MCP server fallback | 32-character hex observability session ID |

## Multi-Tenant Identity Mode (required remotely)

**Remediation is implemented locally; deployment and real-cloud release gates remain unverified.** All remote HTTP/SSE deployments require `identity.enabled=true`, `require_identity=true` and a nonempty `auth_token`, with the gateway enabled **or disabled**. Omitted YAML `require_identity` defaults to true; explicit false rejects remote startup. Omitted/empty `identity.namespace` defaults to `default`; explicit valid values are unchanged. These requirements apply to every tool and protocol method, not just gateway calls. Anonymous, missing-JWT unsigned-header and global-key fallbacks remain forbidden. stdio is local-only, one SDK or API Key process identity, not per-chat-user authorization.

This non-secret JWT fragment illustrates the preferred **explicitly scoped** mapping mode. Existing shared mappings need no added tenant/agent IDs if all groups/default leave both scopes omitted/empty within the same signing trust domain (see below). Replace scope/role/member placeholders from trusted administrator records; inject secrets through environment variables, not literal YAML values:

```yaml
identity:
  enabled: true
  require_identity: true
  namespace: example-domain         # stable administrator-chosen trusted identity domain
  max_tenants: 128                   # cached Tenants + initialization reservations
  auth_token: ""                    # required: inject IDENTITY_AUTH_TOKEN
  jwt:
    enabled: true
    secret: ""                      # inject IDENTITY_JWT_SECRET
    secrets: []                     # optional IDENTITY_JWT_SECRETS; same trusted domain only
    header: ""                      # default x-aily-jwt; bare token, no Bearer prefix
  session_name_claim: user_id
  #session_name_prefix: ""          # omit = aily-<value>; empty = bare value
  headers: {user: "", email: "", token: ""} # x-aily-user / x-aily-email / x-aily-token
  groups:
    analysts:
      tenant_id: "<trusted-tenant-id>"
      agent_id: "<trusted-agent-id>"
      role_arn: "acs:ram::<account-id>:role/<analyst-role>"
      workspace_id: "<workspace-id>"
      mode: pro
      users: ["<user-id>", "<user-email>"]
  # Optional shared role ONLY for unmatched users in this exact scope:
  #default:
  #  tenant_id: "<trusted-tenant-id>"
  #  agent_id: "<trusted-agent-id>"
  #  role_arn: "acs:ram::<account-id>:role/<default-role>"
  #  mode: lite
```

### Identity scopes and trust

- Omitted/empty `namespace` defaults to `default`; explicit valid values are preserved. Values are 1–64 characters matching `[a-z0-9][a-z0-9_-]*`, chosen administratively, not from headers, unverified claims, keys or roles. **Independent deployments/trust domains sharing a sessions root must use different namespaces.** Changing it creates a new identity/history space.
- JWT verification still requires HS256, `exp`, nonempty **`tenant_id` and `agent_id`**, and a usable `user_id` or `email`. Claims are used only after signature verification; missing/invalid/expired JWTs reject every remote request. The platform token is also validated on every request; unsigned headers cannot replace JWT identity. Do not go live if the platform cannot supply required claims.
- JWT mappings must use one mode across **all groups and any default**: both `tenant_id`/`agent_id` omitted/empty everywhere for legacy shared mappings, or both exact values on every mapping. Mixed modes, a single scope value and `*` wildcards are rejected. Legacy shared mappings match signed `user_id`/`email` across signed tenant/agent values within the administrator's shared signing trust domain; existing customers need not add scope IDs to `groups`/`default`. In explicitly scoped mode, named membership and default apply only within the matching tenant/agent pair; no cross-scope default applies. Default is used only for zero named matches in the selected mode. Multiple groups matching user ID/email reject with `IDENTITY_GROUP_CONFLICT`, even if both map to the same role; two matches within one group count once.
- Header mode is a separate administrator-selected deployment (`jwt.enabled=false`), not a fallback. Group/default `tenant_id` and `agent_id` must be empty. Only a trusted proxy may hold the platform token; it must overwrite self-reported identity headers and ensure identifiers are unique across the configured namespace. Restrict network access to trusted egress.
- `secret` plus `secrets`/`IDENTITY_JWT_SECRETS` have **equivalent signing authority in one trusted domain**, not independent issuer isolation. Use for key rotation or mutually trusted applications, never unrelated organizations/issuers. Independent trust domains require separate instances/configurations and different namespaces if sharing a sessions root.
- Preserve exact identity numbers, case and Unicode; do not equate `user_id` with the same string used as `email`. Each request revalidates mapping; cached authorization context cannot override a changed mapping. Configuration changes require a restart, not hot reload.
- Group workspace/custom-agent/mode values are defaults, not ACLs; explicit session arguments win. `RoleSessionName` (`<prefix>-<claim>`, normally `aily-<user_id>`) is audit attribution, **not cloud row/column isolation**. Same-role users normally share effective cloud permissions. Local ownership and cloud session ACLs are separate boundaries.
- Retain the SDK default source provider and AssumeRole chain. The source principal needs `sts:AssumeRole` and mapped roles must trust it; do not change roles/permissions without authorization. Gateway credential failure rejects without stale snapshots or another identity. This is not a claim that the old analysis path has the gateway's strict refresh guarantee.

### Local history and explicit migration

Remote state moves to `{sessions_dir}/identity-v2/{principal_id}/`. The lowercase SHA-256 identity digest encodes the ordered tuple `["principal-v2", namespace, auth_mode, tenant_id, agent_id, subject_kind, subject_value]`; user ID is preferred, otherwise email. Protocol sessions, tenant caches and v2 disk history remain isolated by this verified principal, including signed tenant/agent and typed user, **even with legacy shared role mappings**. Token expiry/signature-key rotation does not change a stable Principal, but signed tenant/agent, auth mode, namespace or subject kind changes do.

Each tenant directory has an atomically written `owner.json` (format version and principal ID, mode 0600). Existing ownership must match; symlink tenants, wrong owners and nonempty unmarked directories are rejected, not automatically claimed. Session IDs accept 1–128 ASCII letters/digits/`_`/`-`, excluding Windows device names; state IDs must match their directories. Root-confined storage rejects path traversal and unsafe file types/symlinks. New directories use 0700 and state/images/temp files 0600. These are local storage checks, **not cloud data authorization**.

There is **no automatic migration or fallback** to `identity/<sanitized-key>`, and no automatic deletion. stdio retains its separate confined local root; it does not import remote tenant history. Before a separately authorized upgrade, stop writes and back up old state. Start with empty identity-v2 if ownership cannot be proved. Import only after an administrator, using trusted business records, proves unique ownership of every old file and validates IDs, types and permissions. Sanitized names may mix users; neither current role mapping nor successful cloud DescribeSession proves ownership. Never import ambiguous data, share/symlink directories, overwrite a nonempty target or hand-create owner markers to claim history. Offline migration and any permissions/secret/deployment changes require **explicit administrator authorization**; no automatic migration tool is provided. Rollback must not re-expose a vulnerable multi-user endpoint or mix old/new layouts.

### Remote HTTP limits and protocol lifecycle

All values below are local ingress limits (not cloud API limits), applying only to remote transports. Omission uses the default; **explicit zero or negative values are rejected**, not interpreted as unlimited. Overflow is rejected. Per-principal/session caps cannot exceed their corresponding totals.

| Configuration | Default | Meaning |
|---|---|---|
| `http.max_request_bytes` | 1048576 | All MCP POST bodies, including notifications; actual bytes, including chunked/no-length requests |
| `http.max_header_bytes` | 65536 | Go HTTP parser semantics; fixed parser slack means measured boundary must be recorded during validation |
| `http.read_header_timeout_seconds` | 5 | Header read budget |
| `http.read_body_timeout_seconds` | 10 | Body-read phase only; deadline cleared before dispatch |
| `http.idle_timeout_seconds` | 60 | Idle keep-alive, not active SSE lifetime |
| `http.max_sessions` | 4096 | Total protocol sessions including initialization reservations |
| `http.max_sessions_per_principal` | 64 | Per-principal protocol sessions |
| `http.max_inflight_requests` | 256 | Total accepted dispatch until actually finished, including asynchronous SSE work |
| `http.max_inflight_per_session` | 8 | Per-session dispatch; HTTP 202 does not release running work |
| `http.session_idle_timeout_seconds` | 1800 | HTTP session idle reclamation with no connection or in-flight call |
| `http.max_stream_lifetime_seconds` | 3600 | Active SSE/notification connection lease; JWT exp can end it sooner |
| `identity.max_tenants` | 128 | **Cached Tenants plus initializing reservations**, not protocol connections |

Tenant capacity does not reset when a protocol session closes. There is no automatic Tenant eviction; at capacity new principals are rejected while cached principals remain usable. Size for OS descriptor limits; expansion/restart is an operator action. Internal limits are 8 concurrent tenant initializations / 15-second initialization wait budget, 4 recovery tasks, JSON depth 64 and request-log output 8 KiB. SDK GetCredential is not context-cancelable; STS connect/request budgets remain 5000/10000 ms, and blocked SDK work retains its slot until it truly exits. These are not an end-to-end timeout or immediate cancellation promise.

The application authenticates platform token + identity and validates mapping **before bounded body reading/dispatch**, without Tenant/STS creation for rejected protocol requests. This covers initialize, tools/list, tools/call, notifications, SSE GET/message POST, and Streamable HTTP **GET/POST/DELETE**. Reject duplicate/conflicting identity headers and malformed lifecycle messages; no method-specific authentication exemption. HTTP rejects remain HTTP errors (401 unauthenticated, 403 forbidden mapping, 413 oversized body, 408 body timeout, 429 capacity), not fabricated tool envelopes.

Protocol IDs must be **issued and owned**: `(transport, session_id)` binds to a verified principal and lifecycle, not just a valid-looking ID, token or RoleArn. Validate ownership on every ID-bearing GET/POST/DELETE, even for same-role users. Unknown, expired, deleted and other-owner IDs return a safe 404; no rebinding or weak compatibility. Only one notification GET may use an HTTP session at a time. SSE disconnect revokes its ID; reconnect for a new ID. HTTP notification GET disconnect alone does not delete the session; DELETE, idle reclamation, authentication lease expiry or shutdown revoke it. Active streams end at the earlier of JWT expiry and the maximum lease (Header mode uses maximum lease); renew credentials and reconnect/reinitialize as needed. A renewed JWT for the same Principal does not change ownership.

Keep client/proxy budgets above the existing 55-second wait cap; header/body limits must not truncate an entire long wait or stream. DELETE/expiry/disconnect cancel local waits and cancelable requests, **not proven remote SQL/analysis cancellation**. Never automatically replay a message with unknown send outcome. Clients unable to authenticate every method or renew/reinitialize must not connect; do not weaken the server to accommodate them. dacli supports `--jwt` / `AILY_JWT`, authenticates notifications and DELETE, and closes its protocol session; it remains a **Streamable HTTP client, not an SSE transport client**.

### Secret files

Prefer environment/managed secret injection and the SDK chain. Checks cover **actually loaded secret-bearing YAML, legacy JSON and dotenv files**, even if the secret is not in config.yaml. By default, POSIX requires process-account-owned regular files with no group/other access (0600 or 0400) in trusted directories. Public placeholder-only examples or non-secret config may be 0644; `.gitignore` is not access control. Checks use the opened handle, not only a preceding path check.

For transitional compatibility, an existing `export DOTENV_UNSAFE_FILES_ALLOW=1` is supported **only from the process environment or launch command**; the variable is ignored in all `.env` files, including non-secret ones. It allows root- or service-user-owned, non-executable regular files such as 0644, but still rejects group/other-writable files. Parent-directory trust/ownership and no-group/other-write checks remain enforced. Final symlinks and FIFOs remain rejected; use environment injection or an administrator-provisioned regular file instead of unsupported projected secrets. Windows file secrets without equivalent verified ACLs remain blocked. The exception emits a warning without secret values and never changes existing permissions. This is not a recommendation to keep secrets as 0644 long-term.

No automatic chmod, chown, moving, secret rotation or migration occurs. Obtain explicit administrator authorization before fixing real file permissions or changing secrets; permission tightening alone does not prove previous non-exposure. Do not write secrets into commands, logs, docs or repository examples. Config placeholders are illustrative, not a promise of YAML `${VAR}` expansion.

### Post-remediation release gate — not yet verified

The [server README](../../server/README.md) summarizes the implemented local fixes and bounded verification; the release checks below remain mandatory. Historical gateway results below/above are not evidence that the remediated deployment passes acceptance. Keep P2b off by default and real configuration, roles, policies and deployments unchanged unless separately authorized.

Build with **Go 1.26.10 minimum**, or a newer supported and rescanned toolchain. Verify the actual toolchain and rebuild Server, dacli and every target-platform artifact; a changed version declaration or old cached binary is insufficient. From `server/`, the release checks include:

```bash
go version
go test ./...
go test -race ./...
go vet ./...
go mod verify
govulncheck ./...
go version -m bin/data-agent-mcp-server
go version -m bin/dacli
govulncheck -mode=binary bin/data-agent-mcp-server
govulncheck -mode=binary bin/dacli
```

Record actual versions/results, repeat binary checks for each platform artifact, and run bounded path/identity/JSON fuzzing. Distinguish vulnerability findings from scanner execution failure; exit zero alone is not a clean scan. Rerun checks for the exact artifacts being deployed and retain the results as release evidence. Keep the pinned x/net v0.62.0 security update as well as Go 1.26.10 or newer.

External gates remain open: real-cloud authentication of the restored local-stdio API Key path; real JWT-platform credentials on initialize, continuous SSE, notifications and DELETE with renewal; all enabled Actions through stdio/HTTP/SSE with real SDK/STS; differential roles with an explicit **A-allowed/B-denied** resource (both list non-enumeration and denial by A's known ID); same-role local history/protocol isolation separately from cloud scope; and old read-only analysis/follow-up without duplicate sends. No local signed fixture, equal positive role results or passing unit test substitutes for these gates. Shared denials lacking upstream RequestId remain un-attributed. Do not grant permissions to manufacture a passing result or claim production acceptance while gates are incomplete; see [RAM validation](ram-policies.md#validation-still-required).

## Observability

`DATA_AGENT_LOG` and `DATA_AGENT_PERF_LOG` behavior is unchanged. Enabling either does not require adding scopes to legacy shared JWT mappings; retain existing credentials and identity settings. See [server logging](../../server/README.md#server-and-request-logs).

All Alibaba Cloud API calls must use this User-Agent template:

```bash
--user-agent AlibabaCloud-Agent-Skills/alibabacloud-data-agent-skill/{session-id}
```

Session-id unified rule: generate a 32-char hex `session-id` once per skill session and keep it consistent across all API calls. When launching the MCP server, pass it through the `SKILL_SESSION_ID` environment variable. If the host does not provide `SKILL_SESSION_ID`, the server generates one 32-character hex fallback at process startup and uses it consistently for signed API and SSE requests in that process.

Gateway logging excludes search terms, returned data, raw error bodies and credentials even with `log.requests=full`. Correlate sanitized error codes with the local request ID and real upstream RequestId when available; never treat a local ID or RoleSessionName as the upstream identifier. Gateway results remain a single JSON TextContent, without appended logging text.

Remediation requires single-line escaping and budgets for all dynamic log fields, including argument keys, caller and errors; authentication rejection must not log a self-reported caller or body. Old-tool `full` logging can still contain business questions and is for explicit diagnostics only. stdout remains reserved for stdio MCP, with logs on stderr. SSE diagnostics expose bounded metadata, not raw payloads.

All signed Data Agent/DMS API and outbound SSE clients reject redirects, including same-host and HTTPS downgrade, without endpoint retry or leaking Location/signatures. Gateway JSON parsing rejects duplicate decoded keys at every depth, invalid Unicode/surrogate escapes, trailing data and nesting beyond 64 within the decoded 4 MiB limit; preserve legitimate Unicode, NULL and precise numbers. Ambiguous responses fail without partial data (`GATEWAY_UPSTREAM_INCOMPATIBLE`), not successful empty results. These requirements remain subject to the post-remediation validation gate.
