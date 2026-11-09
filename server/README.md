# data-agent-mcp-server

Go MCP Server exposing **Alibaba Cloud Apsara Data Agent for Analytics** as 19
native `data_agent_*` tools: 19 with the gateway disabled, 28 with its nine base
metadata tools, or 30 with P2b's two read-only control-plane data-source summary
tools also enabled (11 gateway tools, before host-side filtering).
Historical P2a/P2b automated and cloud checks passed for the tested identities
and 19/28/30 configurations **before security remediation**. The security
remediation implementation is complete locally. Final native binaries passed
real-cloud metadata checks across three transports and all three configurations,
plus protocol-boundary, dacli, and same-role cross-user local-history checks.
A two-turn analysis completed, but its strict follow-up marker assertion failed.
Deployment, real JWT-platform, and differential-role release gates remain
**unverified**; these bounded checks are not production validation. Follow the
[release checklist](../alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md#post-remediation-release-gate--not-yet-verified).
This directory is a standalone Go project; the
distributable skill wrapper lives in
[`../alibabacloud-data-agent-mcp-skill/`](../alibabacloud-data-agent-mcp-skill/)
and ships **without** this source — deployments build the binary here.

## Prerequisites

- **Go 1.26.10+** (https://go.dev/dl/)
- Keep the pinned dependencies, including the security update to `golang.org/x/net v0.62.0`.

## Build

All targets run from this directory and output to `bin/` (gitignored;
binaries are never committed).

```bash
make build       # -> bin/data-agent-mcp-server (current platform)
make build-all   # -> bin/{data-agent-mcp-server,dacli}-linux-{amd64,arm64}
make dacli       # -> bin/dacli (manual verification client)
make test        # go test ./...
make clean       # remove bin/
```

Equivalents without make:

```bash
go build -trimpath -ldflags "-s -w" -o bin/data-agent-mcp-server .
go build -trimpath -ldflags "-s -w" -o bin/dacli ./cmd/dacli
```

The version string embedded in the binary comes from `git describe`
(`-X main.version=...`); outside a git checkout it falls back to `dev`.
Verify the actual compiler is **Go 1.26.10 or newer, supported and rescanned**;
rebuild Server, dacli and every target-platform artifact. Inspect `go version -m`
and run source and binary govulncheck, full tests/race/vet/mod verification and
bounded fuzzing as listed in the [release gate](../alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md#post-remediation-release-gate--not-yet-verified).
Changing a version declaration does not fix an old binary. Rerun checks for the
exact artifacts being deployed and retain the results as release evidence.

## Configure

Use [config.yaml.example](config.yaml.example) for non-secret settings and
[.env.example](.env.example) only as a public placeholder reference. Prefer managed
environment injection for secrets. Do not copy placeholder credentials over an
intended SDK workload-role provider.

Actual secret-bearing YAML/JSON/**dotenv** files must be process-account-owned
regular files in trusted directories, POSIX **0600/0400**, with no group/other
access. Non-secret/public-placeholder files may be 0644. The loader validates
the opened file; final symlinks are rejected in the current implementation.
Projected secrets require a supported regular-file or environment path; Windows
file secrets require equivalent verified ACLs or are blocked. No automatic
chmod/chown, secret rotation or migration occurs. Real permissions, secrets and
deployment changes require separate administrator authorization; see
[secret-file requirements](../alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md#secret-files).

Priority: **env vars > `.env` > `config.yaml` > defaults**. Config file lookup:
`$DATA_AGENT_CONFIG` > `./config.yaml` > `~/.data-agent/config.yaml`.

Startup uses the **credentials-go default chain**, retaining the provider for
subsequent credential retrieval and RAM AssumeRole source chaining. Configure
SDK-supported environment credentials (AK/SK and, for temporary credentials,
`ALIBABA_CLOUD_SECURITY_TOKEN`) or a supported instance/workload-role provider.
The server does not hand-read `~/.aliyun/config.json`; do not assume Alibaba
Cloud CLI profiles automatically work. Static STS environment values do not
renew themselves; dynamic refresh depends on the selected provider.

### API Key migration (all Go Server deployments)

API Key runtime mode is removed from ordinary API calls and SSE, regardless
of `agent_gateway.enabled`. Remove the old YAML keys `api_key`,
`api_key_endpoint`, `api_key_stream_endpoint` and environment variables
`DATA_AGENT_API_KEY`, `DATA_AGENT_API_KEY_ENDPOINT`,
`DATA_AGENT_API_KEY_STREAM_ENDPOINT` from the deployment.

Presence of any of those YAML keys (even empty) or any nonempty old environment
variable causes an **explicit, sanitized startup migration error**, even with
the gateway disabled or valid SDK credentials also present. No secret value is
printed and no silent credential fallback occurs. After cleanup, startup still
fails without usable SDK credentials. This migration does **not** remove
`identity.auth_token`, JWT, dacli `--token`, SDK STS SecurityToken, or OSS upload
signatures. The independent Python CLI skill is unchanged.

### Remote identity and history migration

The remediation contract requires **all** HTTP/SSE deployments, gateway on or
off, to set `identity.enabled=true`, `require_identity=true`, nonempty
`auth_token` (prefer `IDENTITY_AUTH_TOKEN`) and `namespace`. Namespace is a stable
administrator-chosen trust domain, 1–64 characters `[a-z0-9][a-z0-9_-]*`.
Every initialize, tools/list, call, notification and GET/POST/DELETE authenticates
before body reading/dispatch. There is no legacy Header/global-AK fallback for
any remote tool or protocol method. stdio remains local-only, one SDK process
identity, never per-chat-user authorization.

JWT mode requires valid HS256/exp, nonempty `tenant_id`/`agent_id`, and user ID
or email; each group and any default must declare exact tenant/agent scopes.
Default applies only to unmatched users in its exact scope, not across domains.
Multiple matching groups reject even with the same role. Explicit Header mode
requires empty tenant/agent scopes and a trusted proxy that overwrites identity
headers and keeps the shared token from end users. All configured JWT keys have
equivalent signing authority within **one trusted domain**; unrelated issuers
need separate instances/configurations/storage roots.

Remote history uses `sessions_dir/identity-v2/<principal_id>/`, hashing namespace,
auth mode, tenant/agent and typed user ID/email. `owner.json` must match the
principal; nonempty unmarked directories are not automatically claimed. No old
`identity/<sanitized-key>` read fallback, deletion or automatic migration exists.
Offline import requires separately authorized administrator proof of unique
ownership, backup and validation of IDs/files/permissions; ambiguous histories
remain offline. Local ownership markers are not cloud session ACLs or row
isolation. Same-role users normally share cloud permissions; RoleSessionName is
attribution only. See the [identity and migration reference](../alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md#multi-tenant-identity-mode-required-remotely).

### HTTP limits and protocol sessions

[config.yaml.example](config.yaml.example) lists every initial HTTP limit; the
[limits table and lifecycle](../alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md#remote-http-limits-and-protocol-lifecycle)
define their exact scope. Explicit zero/negative values are rejected; only
omission selects defaults. `identity.max_tenants=128` bounds **cached plus
initializing Tenants**, not connections; closing protocol sessions does not free
Tenant capacity. No automatic Tenant eviction is provided.

All ID-bearing GET/POST/DELETE requests must use server-issued, principal-owned,
live protocol IDs, including same-role users. Unknown/expired/deleted/other-owner
IDs receive safe 404s; no rebinding or weak compatibility. Active streams expire
at the earlier of JWT exp and the 3600-second connection lease (Header mode:
lease only). Renew credentials and reconnect/reinitialize as needed. SSE
disconnect revokes its ID; HTTP notification GET disconnect alone does not delete
the session, while DELETE/idle reclamation/lease expiry/shutdown do. Only one
notification GET is allowed per HTTP session. HTTP 202 does not release an
asynchronous task's in-flight slot. Local cancellation is not proof that remote
SQL/analysis stopped; never automatically replay an unknown send.

### Optional metadata gateway

```yaml
region: cn-hangzhou
agent_gateway:
  enabled: false
  data_sources_enabled: false
  endpoint: dms.cn-hangzhou.aliyuncs.com
  request_timeout_seconds: 30
  max_response_bytes: 4194304
```

Both flags default to `false`. `data_sources_enabled=true` requires `enabled=true`;
the combination with `enabled=false` is invalid. Counts are 19 with both off,
28 with only `enabled=true`, and 30 with both on (9 / 11 gateway tools).
Enable P2b only if the deployer accepts the upstream principal's control-plane
summary visibility scope; otherwise leave it disabled.

Set `enabled: true` only after configuring the intended identity and egress.
The endpoint must be explicit and exactly the public `dms.<region>.aliyuncs.com`
host matching `region`; no scheme, port, path, query, fragment, userinfo, VPC
host, or arbitrary target. HTTPS is used and redirects are rejected. The 30s
budget is for outbound HTTP, not total Resolver/STS time; 4 MiB limits decoded
responses, including errors. Tenant initialization has a separate 8-task /
15-second budget, with STS connect/request budgets of 5000/10000 ms. SDK
GetCredential is not context-cancelable: blocked work retains its slot until it
actually exits. Chain steps and provider locking are not an end-to-end 30s promise.
Disabled mode registers no gateway tools and performs no gateway requests;
startup does not probe the gateway network.

The [mandatory remote identity contract](#remote-identity-and-history-migration)
applies equally to old Data Agent tools and gateway tools. Gateway credential
refresh failures additionally reject stale snapshots; do not infer this strict
refresh guarantee for the old analysis path. Same-role sessions do not establish
independent cloud data permissions.

P2a adds three tools under the existing `agent_gateway.enabled` switch, without
its own switch; the original six tools' contracts are unchanged. P2b alone adds
`agent_gateway.data_sources_enabled` for the final two rows below.

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

P2a maps to `GetAgenticCatalog(CatalogUuid)`,
`SearchAgenticDatabase(SearchKey, CatalogUuid?, DbType?, EnvType?, MaxResults, NextToken?)`
and `GetAgenticDatabase(DatabaseUuid)`. Required strings and supplied optional
strings must be nonempty. Database search requires `search_key`; `db_type` uses
official uppercase types without automatic case conversion; `env_type` accepts
only `product/pre/dev/test/sit/uat/pet/stag`. Omitting `catalog_uuid` searches the
service-authorized scope across Catalogs; a filter is not authorization.
Root Catalog search still supports name/alias/address prefixes.

The five base list/search tools, plus P2b List when enabled, paginate one page
per call: `page_size` default 50, range 1..100. Pass returned `next_token` unchanged
for the same identity, target and filters. Database search returns an upstream
`Data` array using the existing cursor format. Catalog/database details return a
`Data` object, never a singleton array, with no pagination. Their `Data:null`
semantics remain unconfirmed and incompatible, not successful empty results.
Columns/indexes remain unpaginated. Use returned CatalogUuid → DatabaseUuid →
QualifiedName verbatim; see the [discovery workflow](../alibabacloud-data-agent-mcp-skill/SKILL.md#compact-discovery-chain).

#### P2b read-only control-plane summaries

- `data_gateway_list_data_sources` calls `ListAgenticDataSources`. Its only
  parameters are optional uppercase-format `db_type` (no automatic case conversion),
  `page_size` (default 50, range 1..100), and `next_token`. No `search_key`, `Host`,
  `State`, or other filter is exposed.
- `data_gateway_get_data_source` calls `GetAgenticDataSource` with only required
  `data_source_uuid`, passed unchanged. It has no pagination.
- The summary allowlist is eight string fields: `DataSourceUuid`, `Alias`, `DbType`,
  `EnvType`, `InstanceSource`, `RegionId`, `CatalogUuid`, `CatalogName`; plus `State`
  as a signed int32 (-2147483648..2147483647). Preserve explicit optional-field
  nulls. Nonempty entities require `DataSourceUuid`; detail identity must match
  the requested UUID. Remove all authentication, network, permission and unknown
  fields, and `TotalCount`; do not expose raw upstream objects.
- Current Get accepts only a valid `Data` object. Missing `Data`, `{}` (including
  an empty `Data` object), `Data:null`, and wrong shapes are incompatible. The
  originally planned successful `Data:null` case is deferred this round because
  no real success-null sample is available; optional-field nulls are different.

These tools expose the **current upstream RAM/STS principal's control-plane
scope, not the chat user's database ACL**. Same-role sessions share permissions.
The deployer must accept the summary visibility scope before release; otherwise
do not enable P2b. A successful test does not establish account-wide visibility,
cross-role isolation or least privilege. P2b filtering is not a server-wide Host
confidentiality guarantee: root Catalog address-prefix search and existing Data
Agent discovery remain available.

Unknown parameters are rejected. Each gateway tool returns **one TextContent
containing one complete JSON object**: `status` (`OK`/`ERROR`), `data`, `request_id`,
and `upstream_request_id` only when received. Only paginated tools have
`pagination` (`has_more`, plus `next_token` when nonempty); errors use
`isError=true` and `error: {code, message, retryable}` with sanitized messages.

#### Verification status and deferred scope

**Historical pre-remediation evidence (2026-10-29):** P2a automated tests and all
nine base Actions' real-cloud discovery passed in Hangzhou using stdio and a
configured default role over HTTP/SSE, including search pagination and empty
results. Initial stdio timeouts were not reproduced on the unchanged binary;
their cause remains unconfirmed.

P2b direct `ListAgenticDataSources` / `GetAgenticDataSource` calls succeeded with
the configured test role's STS. Raw detail contained 23 fields including `Host`
and `AuthPassword`; their values were not recorded. A format-preserving
nonexistent ID returned HTTP 404, `Code=DATA_SOURCE_NOT_FOUND`, no `Data`, with
an upstream RequestId. This remains `ERROR`, not successful null; the mapping is
`GATEWAY_UPSTREAM_INCOMPATIBLE`, with no new error code. P2b automated tests,
race and vet passed. Independent-binary checks passed all nine startup scenarios
(stdio/HTTP/SSE × 19/28/30 tools). Each transport ran real source pagination,
DbType filtering, nine-field sanitized details and missing-source rejection,
plus the original nine Actions. Remote calls used configured-role STS and stdio
used the process SDK identity. Historical RequestIds are not example inputs.
None of these checks verifies the remediated binary or its release gates.

This is not production validation. Minimum RAM authorization (including the
missing `GetAgenticDatabase` definition), the P2b machine-readable/help-page
authorization discrepancy, cross-role ACLs, JWT platform integration and other
P0 acceptance remain pending. See [RAM evidence and validation requirements](../alibabacloud-data-agent-mcp-skill/references/ram-policies.md).

P2c/P3 and Wiki/ACL remain unimplemented. No gateway SQL history/execution,
data-source writes, connection tests, Token, Wiki or ACL/self-permission tools
are exposed; no placeholder `sql_history_enabled` switch is added. Gateway
UUIDs/QualifiedName cannot be used as Data Agent `database_id`: session creation
still requires fresh `data_agent_list_workspace_databases` metadata. **The old
Data Agent SQL path does not traverse these tools and gains no new gateway
row/column protection.**

## Run

Remote examples assume mandatory identity, secrets and limits are already
configured and the deployment/validation action is authorized. They do not grant
release approval; without that configuration remote startup must reject.

```bash
# stdio (local only; an MCP client launches the single-identity process)
./bin/data-agent-mcp-server

# Streamable HTTP (recommended for standalone deployments; endpoint /mcp)
MCP_TRANSPORT=streamable-http MCP_PORT=61026 ./bin/data-agent-mcp-server

# SSE-only clients
MCP_TRANSPORT=sse MCP_PORT=61026 ./bin/data-agent-mcp-server
```

Key environment variables:

| Variable | Meaning |
|---|---|
| `MCP_TRANSPORT` | `stdio` (default) / `streamable-http` / `sse` |
| `MCP_PORT` | Listen port; required for the HTTP transports |
| `DATA_AGENT_CONFIG` | Explicit config.yaml path |
| `DATA_AGENT_WORKSPACE_ID` | Default workspace (empty = auto-discovery) |
| `DATA_AGENT_WAIT_CAP` | Blocking-wait ceiling in seconds (default 55, chosen to stay under nginx's default 60s `proxy_read_timeout`) |
| `DATA_AGENT_LOG_REQUESTS` | Per-call logging: `basic` / `full` / `off` |
| `DATA_AGENT_PERF_LOG` | `1`/`true` enables the periodic performance sampler (see below) |
| `DATA_AGENT_PERF_LOG_FILE` | Perf log path (default `~/.data-agent/perf.log`) |
| `DATA_AGENT_PERF_LOG_INTERVAL` | Sampling period in seconds (default 30) |

### Performance log

Performance sampling is **disabled by default**. Enable it using either the
configuration file or environment variables, then restart the server.

**Option 1 — configuration file:** merge `perf` into the existing `log` section
of your config.yaml (do not add a second `log` key):

```yaml
log:
  perf:
    enabled: true
    file: ~/.data-agent/perf.log
    interval_seconds: 10
    max_bytes: 67108864
```

**Option 2 — environment variables:** these override the configuration file:

```bash
DATA_AGENT_PERF_LOG=1 \
DATA_AGENT_PERF_LOG_FILE="$HOME/.data-agent/perf.log" \
DATA_AGENT_PERF_LOG_INTERVAL=10 \
./bin/data-agent-mcp-server
```

Keep your existing transport, credentials and identity configuration. For remote
deployments, `MCP_TRANSPORT` and `MCP_PORT` must still be set as described above;
enabling perf does not enable HTTP or change authentication. With systemd or a
container, set these variables in the service environment and restart it.

**Confirm collection:** startup stderr contains `perf log enabled`, and the
configured file receives JSON lines starting with an `"event":"start"` record:

```bash
tail -n 1 "$HOME/.data-agent/perf.log"
```

Use your custom path if `file` was overridden. A new line should appear every
configured interval (default 30 seconds); 10–30 seconds is suitable for routine
collection, while 1 second is useful for short diagnostic runs. Ensure the server
account can write to the log directory. To disable, set `enabled: false` or
`DATA_AGENT_PERF_LOG=0` and restart; an environment override takes precedence.

The sampler appends one JSON object per line to a dedicated file, separate from
stderr, so a production stall can be reconstructed afterwards. Each record carries:

| Section | Contents |
|---|---|
| `runtime` | goroutines, GOMAXPROCS, heap/stack bytes, GC count/pauses (`gc_pause_p99_ms`, `gc_last_pause_ms`), scheduler latency (`sched_latency_p50/p99/max_ms`), cumulative mutex wait |
| `process` | pid, user/sys CPU, `cpu_percent` since the previous sample; on Linux also RSS, swap, threads, open/max fds, context switches |
| `host` | Linux only: load averages, runnable tasks, host CPU %/iowait/steal, memory used %, swap, PSI (`psi_cpu/mem/io_some_avg10`); all platforms: free/total bytes of the sessions-dir filesystem |
| `mcp` | tool-call counters per tool (calls, errors, tool_errors, inflight, avg/max/window-max ms), in-flight blocking waits, wait cap; on HTTP transports also protocol-session occupancy (total/ready/initializing/closing/streams/principals/inflight vs limits) |
| `tenants` | remote only: cached tenants vs `max_tenants`, in-progress initializations, restore queue depth, SSE watcher counts by session status |
| `sessions` | stdio only: SSE watcher counts by session status |

The first record is tagged `"event":"start"`, the last `"event":"shutdown"`. The
file rotates once to `<file>.1` when it exceeds `log.perf.max_bytes` (default
64 MiB); it is created `0600`. Counters are cumulative — diff consecutive lines
(e.g. with `jq`) to obtain rates. The sampler never logs arguments, identities or
credentials.

## Verify

```bash
make dacli
# Provision AILY_TOKEN and, in JWT mode, AILY_JWT through trusted env injection.
./bin/dacli --url http://localhost:61026/mcp tools   # 19 off / 28 base / 30 with P2b
./bin/dacli ... dbs                               # workspace databases
./bin/dacli ... ask <db> "<tables>" "<question>"  # end-to-end analysis
```

dacli accepts `--jwt` / `AILY_JWT` and `--token` / `AILY_TOKEN`; prefer environment
injection so secrets do not appear in command history/process arguments. Header
mode instead uses `--user`/`--email` (or `AILY_USER`/`AILY_EMAIL`) with the platform
token from a trusted test context. `--user` does not replace required JWT claims.
dacli authenticates initialization, tool requests, notifications and DELETE,
and closes its protocol session on exit. It remains a **Streamable HTTP client**;
parsing event-framed HTTP responses is not support for the separate SSE transport.
Use an SSE-capable host for SSE lifecycle/renewal acceptance.

With an authorized test identity, use
`call data_gateway_list_catalogs '{"page_size":50}'` to begin discovery, then pass
returned identifiers onward. Tool visibility or a locally signed JWT fixture is
not real-platform or differential-role evidence; the external gates remain open.

## More documentation

- Full setup reference (credentials, RAM policies, YAML + .env, multi-tenant
  identity mode): [`../alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md`](../alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md)
- Deployment guide (systemd unit, nginx reverse-proxy timeouts, dacli
  walkthrough): [`../alibabacloud-data-agent-mcp-skill/README.md`](../alibabacloud-data-agent-mcp-skill/README.md)
- Agent-facing tool reference and workflows: [`../alibabacloud-data-agent-mcp-skill/SKILL.md`](../alibabacloud-data-agent-mcp-skill/SKILL.md)

## Layout

```
├── main.go              # entry point (config load, transport selection)
├── cmd/dacli/           # manual verification CLI
├── internal/
│   ├── mcp/             # 19 Data Agent + 9 base / 11 with P2b gateway tools + wait cap
│   ├── session/         # session manager, SSE watcher, housekeeping
│   ├── dataagent/       # Alibaba Cloud API client (V3 signing, SSE, paging)
│   ├── tenant/          # identity → RAM role AssumeRole registry (JWT/token)
│   ├── config/          # YAML + .env loader
│   └── event/           # SSE event parser
├── config.yaml.example  # config template
└── .env.example         # secrets template
```
