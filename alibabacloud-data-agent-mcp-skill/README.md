# Alibaba Cloud Data Agent MCP Skill

A standalone Agent Skill that exposes **Alibaba Cloud Apsara Data Agent for Analytics** as a native Go MCP server (`data-agent`): 19 `data_agent_*` tools by default, 28 with the nine base `data_gateway_*` metadata tools, or 30 with P2b's two read-only control-plane data-source summary tools too (11 gateway tools). AI assistants call the tools directly to discover databases, run natural language analysis, track progress via server-side SSE monitoring, and fetch conclusions, charts, and file artifacts (reports / Excel exports).

P2a adds Catalog/database details and database search under the base gateway switch; the original six tools are unchanged. P2b adds `data_gateway_list_data_sources` / `data_gateway_get_data_source` behind `agent_gateway.data_sources_enabled` (default `false`, requires `enabled=true`). **Historical pre-remediation evidence (2026-10-29):** P2a/P2b automated checks and independent-binary stdio/configured-role HTTP/SSE discovery passed, including 19/28/30 registration, pagination, type filtering, sanitized details and missing-source errors in Hangzhou.

**Security remediation is implemented locally. Deployment and real-cloud release gates remain unverified.** Historical successes do not establish the new binary's storage, ingress or isolation guarantees. Real JWT-platform, differential-role and least-privilege acceptance remain open; do not claim production acceptance. See the [release checklist](references/INSTALLATION.md#post-remediation-release-gate--not-yet-verified).

P2b summaries reflect the **upstream RAM/STS principal's control-plane scope, not the chat user's database ACL**; same-role sessions share permissions. Deployers must accept that summary visibility scope before release or leave P2b disabled. Success does not prove account-wide visibility, cross-role isolation or least privilege. P2b removes authentication/network/permission/unknown fields and `TotalCount`, not all Server Host exposure: root Catalog search still accepts address prefixes. Get requires a valid object; successful `Data:null` support is deferred, and the observed 404 stays `ERROR` (`GATEWAY_UPSTREAM_INCOMPATIBLE`). See [contracts and real evidence](references/INSTALLATION.md#optional-agentic-metadata-gateway) and [RAM documentation discrepancy](references/ram-policies.md#p2b-read-only-control-plane-summaries).

P2c/P3 and Wiki/ACL remain unimplemented; no gateway SQL history/execution, data-source writes, connection tests or Token tools are exposed. The metadata gateway adds no SQL protection to the old Data Agent path; gateway UUIDs/QualifiedName are not session `database_id` values.

> Looking for the Python CLI integration instead? See the repository root [README](../README.md) and [SKILL.md](../alibabacloud-data-agent-skill/SKILL.md).

## Requirements

- A **data-agent-mcp-server binary**. This skill ships without server source or binaries — build once from the repository's [`server/`](../server) project (Go 1.26.10+, `cd server && make build`) or place a release binary where the launcher can find it (see Server Binary below)
- Alibaba Cloud SDK AK/STS credentials with RAM API and service-side data permissions for the enabled tools; Data Agent policies do not establish gateway authorization (see [references/ram-policies.md](references/ram-policies.md))
- Data sources managed in Alibaba Cloud DMS / Data Agent Data Center

## Server Binary

`scripts/select-binary.sh` locates the binary in this order:

1. **`$DATA_AGENT_SERVER_BIN`** — explicit path; use this for standalone deployments where the binary lives outside the skill (e.g. `/usr/local/bin/data-agent-mcp-server`)
2. **`assets/bin/`** inside this skill — drop `data-agent-mcp-server` (or the platform-suffixed `data-agent-mcp-server-<os>-<arch>`) here when distributing the skill on its own
3. **`../server/bin/`** — monorepo development build (`cd server && make build`)

## Configuration

Sources and priority (highest wins): **env vars > `.env` file > `config.yaml` > defaults**.

The Go Server uses the **credentials-go default chain**, retaining providers for dynamic retrieval and RAM AssumeRole source chaining. It does not hand-read `~/.aliyun/config.json` or guarantee CLI-profile support. Configure a supported SDK provider; static STS environment values do not renew themselves.

**Breaking migration:** API Key runtime mode is removed from the entire Go Server, including SSE, regardless of `agent_gateway.enabled`. Remove obsolete `api_key`, `api_key_endpoint`, `api_key_stream_endpoint` configuration keys and `DATA_AGENT_API_KEY`, `DATA_AGENT_API_KEY_ENDPOINT`, `DATA_AGENT_API_KEY_STREAM_ENDPOINT` environment variables. Any old YAML key (even empty) or nonempty old variable causes an explicit sanitized startup migration error, even with valid SDK credentials and the gateway disabled; no silent fallback. After cleanup, usable SDK credentials are still required. Keep `identity.auth_token`, JWT, dacli `--token`, STS SecurityToken and OSS upload signatures. The Python CLI skill is unchanged. See [credential migration](references/INSTALLATION.md#credentials).

Use the [non-secret YAML example](../server/config.yaml.example) and
[public environment template](../server/.env.example), preferring managed
secret/environment injection over file secrets. Actually loaded secret-bearing
YAML/JSON/dotenv files must be process-owned regular files in trusted directories,
POSIX **0600/0400**; placeholder-only/non-secret files may be 0644. Windows requires
verified equivalent ACLs or file-secret deployment is blocked. No automatic
chmod/chown, secret rotation or migration occurs; changes to real files,
permissions or deployments require separate administrator authorization. See
[secret-file requirements](references/INSTALLATION.md#secret-files), including
projected/symlink limitations. YAML examples do not interpolate `${VAR}` secrets.

### config.yaml — examples

**Minimal — local stdio only, gateway disabled.** The server acts as one SDK
credential-chain process identity, not a multi-user endpoint. **All HTTP/SSE**
deployments require the mandatory identity configuration below, even on localhost
or with the gateway off.

```yaml
region: cn-hangzhou
sessions_dir: ~/.data-agent/sessions
agent_gateway:
  enabled: false
  data_sources_enabled: false       # P2b; requires enabled=true and accepted visibility scope
  endpoint: dms.cn-hangzhou.aliyuncs.com
  request_timeout_seconds: 30
  max_response_bytes: 4194304
# workspace_id / custom_agent_id optional; empty = auto workspace + built-in agent
```

Enable only after configuring the intended access. Gateway accepts an explicit
public `dms.<region>.aliyuncs.com` host matching `region`, not a scheme, port,
path, query, fragment, userinfo or VPC endpoint. HTTPS redirects are rejected.
The 30s HTTP budget does not cover Resolver/STS time; 4 MiB caps decoded responses.
See the [full gateway contract](references/INSTALLATION.md#optional-agentic-metadata-gateway).

**Remote JWT example — verified claims, explicitly scoped groups and RAM roles.**
Every tool and protocol request must authenticate. This example requires complete,
matching JWT scope; shared mappings may instead omit both tenant/agent mapping
fields and support trusted Header identity or valid JWTs without scope claims.
Same-role users normally share cloud permissions, not independent row/column
scopes. This is an illustrative fragment, not approval to deploy or modify roles.

```yaml
region: cn-hangzhou
sessions_dir: ~/.data-agent/sessions
log:
  requests: basic
identity:
  enabled: true
  require_identity: true
  namespace: example-domain
  max_tenants: 128
  jwt:
    enabled: true
    # Inject IDENTITY_JWT_SECRET; optional IDENTITY_JWT_SECRETS for same trust domain
  auth_token: ""                    # inject nonempty IDENTITY_AUTH_TOKEN
  session_name_claim: user_id
  groups:
    analysts:
      tenant_id: "<trusted-tenant-id>"
      agent_id: "<trusted-agent-id>"
      role_arn: "acs:ram::<account-id>:role/<analyst-role>"
      workspace_id: "<workspace-id>"
      mode: pro
      users: ["<user-id>", "<user-email>"]
  # Optional default for unmatched users ONLY in this exact scope:
  #default:
  #  tenant_id: "<trusted-tenant-id>"
  #  agent_id: "<trusted-agent-id>"
  #  role_arn: "acs:ram::<account-id>:role/<default-role>"
  #  mode: lite
```

Provision `IDENTITY_AUTH_TOKEN` and `IDENTITY_JWT_SECRET` in the server environment
through the operator's trusted secret mechanism. All extra keys in
`IDENTITY_JWT_SECRETS` have equivalent signing authority in **one trusted domain**;
do not combine unrelated organizations/issuers. The SDK default chain is retained;
use AK/SK/STS environment values only when that is the intended provider.

After configuration and applicable validation/authorization, use
`MCP_TRANSPORT=streamable-http MCP_PORT=61026 ./data-agent-mcp-server`.
See [Identity & RAM Role Assumption](#identity--ram-role-assumption-how-角色扮演-works)
for mapping requirements and [all HTTP limits](references/INSTALLATION.md#remote-http-limits-and-protocol-lifecycle).

### config.yaml (full reference)

Lookup order: `$DATA_AGENT_CONFIG` > `./config.yaml` > `~/.data-agent/config.yaml` > legacy `~/.data-agent/config.json` (JSON still parsed).

| Key | Default | Description |
|-----|---------|-------------|
| `region` | `cn-hangzhou` | DMS region ID |
| `dms_unit` | auto-resolved | DMSUnit override (skips `GetActiveRouteUnit` lookup) |
| `workspace_id` | auto personal workspace | Default Data Agent workspace |
| `custom_agent_id` | — | Default custom agent for sessions that omit one. An identity group's `custom_agent_id` wins over this; an explicit tool argument wins over both |
| `sessions_dir` | `~/.data-agent/sessions` | Local session state directory |
| `dms_enterprise_endpoint` | `dms-enterprise.{region}.aliyuncs.com` | Host for the dms-enterprise metadata APIs (database/table/instance discovery, import tagging, `GetActiveRouteUnit`); set a VPC endpoint when public egress is unavailable. Not covered by any Data Agent endpoint setting |
| `data_agent_endpoint` | `dms.{region}.aliyuncs.com` | Host for the AK/SK-signed Data Agent API (session create/send/status and the SSE stream) |
| `agent_gateway.enabled` | `false` | Add nine base Agentic metadata tools including P2a (19 off / 28 base); full P0 least-privilege/cross-role acceptance pending |
| `agent_gateway.data_sources_enabled` | `false` | Add two P2b read-only control-plane summaries (30 total / 11 gateway tools); requires `enabled=true`, otherwise invalid. Enable only after accepting the principal-scoped summary visibility |
| `agent_gateway.endpoint` | — | Required explicitly when enabled: public `dms.<region>.aliyuncs.com` host matching `region` only |
| `agent_gateway.request_timeout_seconds` | `30` | Gateway outbound HTTP budget, not total identity/STS time |
| `agent_gateway.max_response_bytes` | `4194304` | 4 MiB decoded response cap, including errors |
| `log.requests` | `basic` on HTTP transports, `off` on stdio | Per-tool-call logging: `basic` (tool, caller, outcome, duration) \| `full` (adds redacted arguments) \| `off`. A standalone deployment logs by default; stdio stays quiet because the host agent owns the console. The identity token is never logged |
| `sts.endpoint` | `sts.{region}.aliyuncs.com` | STS endpoint used for AssumeRole |
| `sts.session_expiration` | `3600` | Temporary credential lifetime in seconds |
| `identity.enabled` | `false` (local stdio only) | Must be explicitly `true` for every HTTP/SSE deployment, gateway on or off |
| `identity.namespace` | `default` | Omitted/empty selects `default`; explicit valid values are preserved. Stable administrator-chosen domain, 1–64 chars `[a-z0-9][a-z0-9_-]*`; independent trust domains sharing a sessions root need distinct values |
| `identity.max_tenants` | `128` | Cached Tenants plus initializing reservations; positive, no automatic eviction; protocol disconnect does not release this capacity |
| `identity.jwt.enabled` | `false` | False: trusted Header identity, any JWT header rejects. True: completely absent JWT allows trusted Header identity under shared mappings; present empty/invalid/expired JWT rejects without downgrade. Valid HS256/exp plus user_id or email wins over plain headers; scope requirements depend on mappings |
| `identity.jwt.secret` | — | HS256 key used as-is (no base64); prefer `IDENTITY_JWT_SECRET` via managed environment injection |
| `identity.jwt.secrets` | `[]` | Additional equivalent-authority signing keys in the same trusted domain only; env `IDENTITY_JWT_SECRETS` (comma-separated), not independent issuer routing |
| `identity.jwt.header` | `x-aily-jwt` | Header carrying the bare token (no `Bearer ` prefix) |
| `identity.session_name_claim` | `user_id` | STS RoleSessionName field: `user_id` \| `email` \| `enterprise_email` \| `employee_no` \| `tenant_id` \| `agent_id`. Header: missing selected value falls back to user_id, then email. JWT: an explicitly selected non-default claim must exist; only default user_id may fall back to email. Group matching always uses user_id/email |
| `identity.require_identity` | `true` when omitted in YAML | Required for all remote tools and protocol methods; explicit false rejects remote startup |
| `identity.auth_token` | — | Platform authentication token; **required whenever `identity.enabled`**, checked first on every request on both paths. Trusted upstreams only, never end users. Legacy name `shared_secret` still accepted; distinct from outbound API Key mode |
| `identity.session_name_prefix` | `aily` when the key is absent | STS RoleSessionName = `<prefix>-<session_name_claim value>`. Set it to an **empty string** to drop the prefix and use the bare value; omit the key to keep the historical `aily-` form. Env: `IDENTITY_SESSION_NAME_PREFIX` (empty = no prefix) |
| `identity.headers.user/email/token` | `x-aily-user` / `x-aily-email` / `x-aily-token` | Identity header names (rename for non-Aily upstreams) |
| `identity.default` | — | Optional role for authenticated, unmatched users; shared when unscoped, otherwise only within its exact JWT tenant/agent scope |
| `identity.groups.<name>` | — | `role_arn` + session defaults + `users` list, consistently shared or scoped; exactly one named match takes precedence over default; conflicts reject even with identical roles |
| `identity.groups.<name>.tenant_id/agent_id`, `identity.default.tenant_id/agent_id` | — | All pairs omitted/empty: shared Header/JWT mappings, JWT scope claims optional. All pairs exact/nonempty: complete matching JWT scope required, Header cannot match. Mixed/partial/wildcard mapping scopes and cross-scope defaults reject |

All eleven `http.*` defaults, their units and lifecycle meanings are in the
[HTTP limits table](references/INSTALLATION.md#remote-http-limits-and-protocol-lifecycle)
and [full configuration example](../server/config.yaml.example). Omitted numeric
limits use defaults; **explicit zero/negative values and overflow reject**, not
unlimited mode. Per-principal/session caps cannot exceed their corresponding totals.

### .env / environment variables

The `.env` file (path: `$DATA_AGENT_ENV_FILE`, else `./.env`) is loaded into the process environment at startup without overriding already-set variables — so every row below works from either place.

| Variable | Purpose |
|----------|---------|
| `ALIBABA_CLOUD_ACCESS_KEY_ID` / `ALIBABA_CLOUD_ACCESS_KEY_SECRET` | Explicit AK/SK for the credentials-go default chain; otherwise configure a supported SDK provider, such as an instance/workload role. CLI profiles are not automatically imported |
| `ALIBABA_CLOUD_SECURITY_TOKEN` | STS token, read together with the AK/SK pair when running on temporary credentials |
| `DATA_AGENT_REGION` / `BUDDY_REGION` | Region override (`DATA_AGENT_REGION` wins) |
| `DATA_AGENT_DMS_UNIT` / `DATA_AGENT_WORKSPACE_ID` / `DATA_AGENT_CUSTOM_AGENT_ID` / `DATA_AGENT_SESSIONS_DIR` | Override the matching config keys |
| `DATA_AGENT_DMS_ENTERPRISE_ENDPOINT` | dms-enterprise host for the metadata APIs; overrides `dms_enterprise_endpoint`. Default `dms-enterprise.{region}.aliyuncs.com` — set for VPC-only egress or non-public-cloud |
| `DATA_AGENT_ENDPOINT` | Host of the AK/STS-signed Data Agent API (sessions + SSE); overrides `data_agent_endpoint`. Default `dms.{region}.aliyuncs.com`; not a gateway endpoint override |
| `DATA_AGENT_CONFIG` / `DATA_AGENT_ENV_FILE` | Explicit config / .env file paths |
| `AILY_SHARED_SECRET` / `IDENTITY_SHARED_SECRET` / `IDENTITY_AUTH_TOKEN` | Overrides `identity.auth_token` |
| `IDENTITY_JWT_SECRET` | Overrides `identity.jwt.secret` (HS256 key for the upstream-signed identity token) |
| `IDENTITY_JWT_SECRETS` | Comma-separated extra HS256 keys, equivalent signing authority in one trusted domain; not for untrusted issuers |
| `IDENTITY_SESSION_NAME_PREFIX` | Overrides `identity.session_name_prefix`; an **empty** value means no prefix (unset keeps the config/default) |
| `MCP_TRANSPORT` / `MCP_PORT` | `stdio` (default) \| `streamable-http` \| `sse`; port is required for HTTP transports |
| `DATA_AGENT_LOG_REQUESTS` | `basic` \| `full` \| `off`; overrides `log.requests`. Unset = `basic` on HTTP transports, `off` on stdio |
| `DATA_AGENT_DEBUG_SSE` | `1` = bounded SSE diagnostic metadata, not raw payloads or credentials |

## Identity & RAM Role Assumption (how 角色扮演 works)

Every remote request follows platform token → verified Principal → shared or scoped
role mapping → protocol ownership and bounded body → dispatch. Only business tools
then initialize a Tenant/STS provider. Authentication/mapping rejection must not
create a Tenant or call STS. This applies to **all** old/new tools and protocol
methods, independently of gateway flags.

### Identity headers & caller authentication

| Setting | Default header | Purpose |
|---|---|---|
| `identity.headers.token` | `x-aily-token` | Mandatory platform token, checked on every request before mapping; trusted upstreams only |
| `identity.jwt.header` | `x-aily-jwt` | Bare HS256 JWT; present empty/invalid/expired values reject. With JWT disabled, any JWT header rejects |
| `identity.headers.user` | `x-aily-user` | Trusted Header user ID when JWT is completely absent; ignored for identity when a valid JWT is used |
| `identity.headers.email` | `x-aily-email` | Trusted Header email when JWT is completely absent; ignored for identity when a valid JWT is used |

With `jwt.enabled=true`, a **completely absent JWT header** permits trusted Header
user/email under shared mappings. A present empty, invalid or expired JWT rejects
without downgrade. A valid HS256 JWT with `exp` and usable `user_id` or `email`
takes precedence; simultaneous plain user/email identity is ignored. With
`jwt.enabled=false`, use trusted Header identity and reject any JWT header,
including an empty one. The platform token is mandatory on **every protocol
method**, not just tool calls. No new compatibility switch is needed.

Across **all groups and any default**, both tenant/agent mapping fields must be
omitted/empty everywhere (shared mappings) or exact nonempty values on every
mapping (explicitly scoped). Shared mappings accept trusted Header identity and
valid JWTs without `tenant_id` or `agent_id`, matching user ID/email in one trusted
domain. Existing shared mappings need no added IDs. Explicitly scoped mappings
require both signed JWT claims to match exactly; Header identity cannot match,
including a scoped default. Reject mixed shared/scoped, partial and wildcard
mapping scopes. Exactly one named group wins, otherwise default may apply within
the selected mode and, when scoped, only the same scope. Multiple groups matching
user ID/email reject even if their roles are equal.

On either Header path, the trusted upstream must authenticate callers, overwrite
client-supplied identity headers and ensure namespace-wide identifier uniqueness.
**Never distribute the platform token to end users**; restrict server access to
trusted upstreams. The token authenticates the upstream, not a self-reported end
user. Do not remove a rejected JWT to retry automatically as Header identity.
Namespace is administrator-chosen and stable, never a request value. Shared JWT
keys are equivalent signers in one trusted domain; independent issuers require
separate instances/configurations and distinct namespaces if sharing a sessions root.

A missing Header `session_name_claim` value falls back to `user_id`, then `email`.
On the JWT path, an explicitly selected non-default claim must exist; only the
default `user_id` selection may fall back to email. This affects RoleSessionName
attribution, not user/email mapping or cloud authorization.

Retain the SDK source/AssumeRole provider chain. Source `sts:AssumeRole`, role
trust, API permissions and data ACLs are separate prerequisites; do not change
them without authorization or add FullAccess to hide missing evidence. Gateway
credential failures reject stale/global fallback, without promising that the
old analysis path has the same strict refresh semantics. RoleSessionName and
workspace/custom-agent defaults are **not cloud row/column isolation**; same-role
users normally share effective cloud scope. See [RAM requirements](references/ram-policies.md).

### Breaking history and protocol migration

Remote local history uses `sessions_dir/identity-v2/<principal_id>/`, with a
SHA-256 `principal-v2` key including namespace, `auth_mode`, tenant/agent scope
(including absent scope) and typed user ID/email. Protocol sessions, tenant caches
and disk history keep this isolation even with shared mappings. Header and JWT
history do not merge automatically, even for the same user. `owner.json` must match;
wrong-owner and nonempty unmarked folders are not claimed. No old v1
`identity/<sanitized-key>` fallback or automatic migration/deletion exists. Offline
migration requires **explicit administrator authorization**, backup, proof of unique
ownership and file/ID/permission checks;
ambiguous histories remain offline. stdio is a separate confined single-process
root. Local ownership markers are not cloud session ACLs. Follow the
[migration procedure](references/INSTALLATION.md#local-history-and-explicit-migration).

Every initialize, tools/list/call, notification and GET/POST/DELETE authenticates.
ID-bearing requests require a **server-issued, principal-owned, live** protocol
session; same-role identities cannot share IDs. Unknown/expired/deleted/other-owner
IDs receive safe 404s; no weak compatibility or rebinding. Active streams have
leases (default 3600 seconds, shortened by JWT exp); clients renew credentials and
reconnect/reinitialize. SSE disconnect revokes its ID; HTTP notification GET
disconnect alone does not delete a session. DELETE, idle reclamation, lease expiry
and shutdown revoke it. Local cancellation is not remote SQL cancellation and
unknown sends must not be replayed. See [limits and lifecycle](references/INSTALLATION.md#remote-http-limits-and-protocol-lifecycle).

`identity.max_tenants=128` limits cached plus initializing Tenants, not HTTP
connections. Protocol close does not free Tenant capacity; no automatic eviction
exists. All numeric safety limits reject explicit zero/negative values. There
is no anonymous or global-AK fallback for any remote method; trusted Header
compatibility still requires per-request token validation and shared mappings.

## Deployment

### Option 1: stdio (client launches the binary)

For clients that spawn tool processes locally (Claude Code, Qoder, Codex, etc.):

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

Where to put it:

- **Claude Code**: `~/.claude/settings.json` (or project `.claude/settings.json`), then restart / run `/mcp`
- **Qoder**: `~/.qoder/mcp.json`

`select-binary.sh` locates a prebuilt binary; build it separately with the required
toolchain. stdio is local-only and does not separate chat users.

### Option 2: Streamable HTTP (standalone long-running service)

For authorized shared/hosted deployments, first configure mandatory identity,
shared or scoped mappings, secrets and limits above. This includes localhost and
gateway-disabled servers. `MCP_PORT` must be set explicitly; these commands are not
release approval:

```bash
MCP_TRANSPORT=streamable-http MCP_PORT=8931 \
  bash scripts/select-binary.sh
```

Register in the client (endpoint path `/mcp`). This routing-only fragment is not a
complete authentication setup: a trusted upstream must supply the platform token
and JWT, or trusted Header identity with no JWT under shared mappings, for **every**
request, including notifications and DELETE. Never distribute the platform token
to end users. Keep secrets out of URLs, use protected transport outside loopback,
and reject hosts that cannot carry authentication on all required methods:

```json
{
  "mcpServers": {
    "data-agent": { "url": "http://<host>:8931/mcp" }
  }
}
```

For SSE-only clients, start with `MCP_TRANSPORT=sse` and connect to `http://<host>:8931/sse`.

Only after release gates and separate deployment authorization, run under a
supervisor (systemd / launchd / container) using the validated binary directly.
The skill launcher is not needed. This illustrative systemd fragment omits
operator-provisioned identity/secrets and is not a production acceptance claim:

```ini
[Service]
# Config directory: contains config.yaml and .env (or use $DATA_AGENT_CONFIG)
WorkingDirectory=/etc/data-agent-mcp-server
Environment=MCP_TRANSPORT=streamable-http MCP_PORT=8931
ExecStart=/usr/local/bin/data-agent-mcp-server
Restart=always
```

When fronting the server with a reverse proxy (nginx etc.), the proxy read
timeout must exceed the server wait cap (55s by default, see
`DATA_AGENT_WAIT_CAP`), long-poll timeouts must not count as upstream
failures, and response buffering must be off for SSE. This timeout-only snippet
is not an authentication proxy: separately configure trusted identity-header
overwrite, token injection and access restriction. Proxy limits supplement, not
replace, the application's header/body/capacity limits and connection leases:

```nginx
upstream data-agent-mcp {
    server 10.0.0.1:8931 max_fails=0;   # blocking waits hit the cap by design
    keepalive 32;
}
location / {
    proxy_pass http://data-agent-mcp;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_connect_timeout 5s;
    proxy_read_timeout    120s;         # > wait cap (55s)
    proxy_buffering off;
}
```

### Option 3: Multi-tenant deployment (e.g. Feishu Aily)

All remote deployments are identity-enabled. For explicitly scoped mappings, use
the JWT example above and validate complete matching platform claims and renewal.
Existing shared mappings need no added tenant/agent IDs; they accept valid JWTs
without scope claims or trusted Header identity when the JWT header is completely
absent, even with `jwt.enabled=true`. The Header-only example below keeps JWT disabled
and rejects any JWT header. Both Header paths require shared mappings and a trusted
upstream; a present empty/invalid/expired JWT is never downgraded:

```yaml
identity:
  enabled: true
  require_identity: true
  namespace: example-domain
  max_tenants: 128
  auth_token: ""                    # inject IDENTITY_AUTH_TOKEN
  jwt:
    enabled: false                  # trusted Header only; any JWT header rejects
  groups:
    analysts:
      tenant_id: ""                 # MUST be empty in Header mode
      agent_id: ""
      role_arn: "acs:ram::<account-id>:role/<analyst-role>"
      workspace_id: "<workspace-id>"
      mode: pro
      users: ["<user-id>", "<user-email>"]
```

An optional default on either Header path also requires empty tenant/agent scopes
and explicit acceptance of its shared role scope. Register the protected endpoint
and have the trusted upstream supply the platform token (`x-aily-token` by default)
plus identity on all methods. Header names matching a platform convention are not
proof that real JWT/notification/DELETE integration passes acceptance.

> **Security**: keep the token with the trusted proxy, not end users. Require
> network access restriction and overwrite of self-reported headers. Do not
> switch modes to bypass an authentication failure.

## Verify the Deployment

```bash
# stdio handshake
printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}\n' \
  | bash scripts/select-binary.sh
# → serverInfo "data-agent-mcp-server"; tools/list: 19 off / 28 base / 30 with P2b
```

### dacli — manual verification client

`cmd/dacli` is the **Streamable HTTP** manual verification client, not an SSE
transport client or proof of real Aily platform behavior. It supports `--config`
for reading the platform token and `--jwt` / `AILY_JWT` for an issued JWT,
authenticates notifications and DELETE as well as tool requests, and closes its
protocol session on exit. Run it only in an authorized trusted upstream/operator
context; never give end users the platform token or server config. Use a compatible
host for separate SSE transport/renewal acceptance.

```bash
cd server && make dacli          # -> bin/dacli (or: go build -o bin/dacli ./cmd/dacli)

# Prefer trusted env injection: AILY_MCP_URL / AILY_TOKEN / AILY_JWT (JWT identity)
# Header identity: AILY_USER / AILY_EMAIL plus AILY_TOKEN; no JWT, shared mappings only
./bin/dacli --url http://localhost:8931/mcp <command>

# commands
./bin/dacli ... tools                     # 19 off / 28 base / 30 with P2b
./bin/dacli ... workspaces                # visible workspaces under the assumed role
./bin/dacli ... dbs                       # workspace Data Center databases
./bin/dacli ... ask chinook "album,artist,track" "哪个艺术家的专辑数量最多？"
#   ^ golden path in one command: resolve db → create lite session → wait → conclusions
./bin/dacli ... send <session_id> "那最低的呢？"  # follow-up on an existing (even finished) session
./bin/dacli ... status|wait|result|files <session_id> ...
./bin/dacli ... call <tool> '<json-args>' # raw tool call
./bin/dacli ... repl                      # interactive loop
```

For a trusted Header call against shared mappings on port 8931, read the platform
token from the existing config and use the actual authenticated, mapped user:

```bash
unset AILY_JWT  # alternatively add --jwt '' to omit the JWT header for this call
./bin/dacli --url http://127.0.0.1:8931/mcp --config ./config.yaml --user actual-user tools
```

This works with JWT enabled or disabled, but not with explicitly scoped mappings.
In dacli, `--jwt ''` omits the header; it does not send an empty JWT header (which
the server rejects). Do not use this as an automatic retry for a rejected JWT.

`--config` reads only `identity.auth_token`; it neither issues/signs JWTs nor loads
server environment/`.env` overrides. Nonempty `--token` overrides `AILY_TOKEN`, and
the selected token takes precedence over `--config`; the file is read only when
that token is empty. Without `--config`, no config file is read. Existing secret-file
ownership, permission and trusted-directory checks still apply. The server signing
secret is not a client JWT; see the [server verification reference](../server/README.md#verify).

`--token` / `--jwt` flags are available, but trusted environment injection avoids
secrets in command history/process arguments. Wrong tokens, present empty/invalid/
expired JWTs, missing required claims, ambiguous mappings or out-of-scope users
must reject. Valid JWT identity takes precedence over plain `--user`/`--email`;
these flags cannot replace signed claims. Explicitly scoped mappings require a
valid JWT with both matching tenant/agent claims on every request.
Chart images returned by `result` are saved to `./dacli-out/`. With an authorized
identity, use `call data_gateway_list_catalogs '{"page_size":50}'`, then the
[discovery chain](SKILL.md#compact-discovery-chain). Successful tool listing or a
local JWT fixture does not satisfy real JWT-platform/STS/data-ACL release gates.

Then from the AI client: `data_agent_list_workspaces` → `data_agent_list_workspace_databases` should return real data for an authorized test identity. Full tool reference, workflows, and troubleshooting: [SKILL.md](SKILL.md).

## Build & Development

The Go server lives in the repository's top-level [`server/`](../server) project;
builds output to `server/bin/` (gitignored — binaries are never committed).
**Go 1.26.10 minimum**, or a newer supported, rescanned toolchain, is required.
Verify actual `go version`, rebuild Server/dacli/all platform artifacts, inspect
`go version -m` and scan source plus binaries. Follow the
[post-remediation release checks](references/INSTALLATION.md#post-remediation-release-gate--not-yet-verified);
build completion or old cloud evidence is not production acceptance.

```bash
cd server

# MCP server
make build            # -> bin/data-agent-mcp-server (current platform)
make build-all        # -> bin/data-agent-mcp-server-linux-{amd64,arm64} (cross-compile)

# dacli — manual verification client
make dacli            # -> bin/dacli

# equivalents without make
go build -trimpath -ldflags "-s -w" -o bin/data-agent-mcp-server .
go build -trimpath -ldflags "-s -w" -o bin/dacli ./cmd/dacli

# tests & debugging
make test             # go test ./... (config / tenant / mcp / session / dataagent / event)
DATA_AGENT_DEBUG_SSE=1 ...   # bounded diagnostic metadata, never raw SSE payloads
```

## Layout

```
├── SKILL.md                     # Agent instructions (tools, workflows, troubleshooting)
├── README.md                    # This file — human deployment guide
├── scripts/select-binary.sh     # Launcher: locate a deployed/built server binary
├── references/
│   ├── INSTALLATION.md          # Setup reference (transports, credentials, config, identity mode)
│   └── ram-policies.md          # RAM permission requirements
└── assets/                      # Static assets; optional assets/bin/ for a bundled binary

../server/                       # Go MCP Server source (standalone project at the repo root)
├── config.yaml.example          # Config template
├── .env.example                 # Secrets template
├── cmd/dacli/                   # Manual verification client (Aily-style HTTP calls)
└── internal/                    # config / dataagent / mcp / session / tenant / event
```

## License

Apache License 2.0 — see [LICENSE](../alibabacloud-data-agent-skill/assets/LICENSE).
