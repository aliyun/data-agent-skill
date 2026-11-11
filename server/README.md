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

By default, actual secret-bearing YAML/JSON/**dotenv** files must be
process-account-owned regular files in trusted directories, POSIX **0600/0400**,
with no group/other access. Non-secret/public-placeholder files may be 0644.
For transitional compatibility, `export DOTENV_UNSAFE_FILES_ALLOW=1` in the
**process environment or launch command** permits root- or service-user-owned,
non-executable regular files such as 0644, but never group/other-writable files.
The variable is ignored in `.env` files, even non-secret ones. Trusted-parent
ownership and no-group/other-write checks still apply; final symlinks and FIFOs
are rejected. Windows file secrets without verified ACLs remain blocked. The
loader validates the opened file and warns without secret values when using the
exception; it does not change permissions. Do not retain secrets as 0644 long-term.
No automatic chmod/chown, secret rotation or migration occurs. Real permissions,
secrets and deployment changes require separate administrator authorization; see
[secret-file requirements](../alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md#secret-files).

Priority: **env vars > `.env` > `config.yaml` > defaults**. Config file lookup:
`$DATA_AGENT_CONFIG` > `./config.yaml` > `~/.data-agent/config.yaml`.

Without a nonempty API Key, startup uses the **credentials-go default chain**,
retaining the provider for subsequent credential retrieval and RAM AssumeRole
source chaining. Configure SDK-supported environment credentials (AK/SK and, for
temporary credentials, `ALIBABA_CLOUD_SECURITY_TOKEN`) or a supported
instance/workload-role provider.
The server does not hand-read `~/.aliyun/config.json`; do not assume Alibaba
Cloud CLI profiles automatically work. Static STS environment values do not
renew themselves; dynamic refresh depends on the selected provider.

### API Key compatibility (local stdio only)

API Key authentication is supported for a **local, single-process stdio** server.
The legacy YAML/JSON keys `api_key`, `api_key_endpoint`, `api_key_stream_endpoint`
and environment variables `DATA_AGENT_API_KEY`, `DATA_AGENT_API_KEY_ENDPOINT`,
`DATA_AGENT_API_KEY_STREAM_ENDPOINT` remain supported. An empty/null `api_key`,
or old endpoint settings alone, does not select API Key mode or block AK/STS
multi-tenant startup. A nonempty API Key cannot be combined with
`identity.enabled=true` or `agent_gateway.enabled=true`, or used for HTTP/SSE.
Without a nonempty API Key, usable SDK credentials are still required.

API Key is distinct from `identity.auth_token`, JWT, dacli `--token`, SDK STS
SecurityToken and OSS upload signatures. **Real-cloud validation of the restored
local API Key path is still pending**; historical SDK/STS checks do not verify it.
The independent Python CLI skill is unchanged.

### Remote identity and history migration

The remediation contract requires **all** HTTP/SSE deployments, gateway on or
off, to use `identity.enabled=true`, `require_identity=true` and nonempty
`auth_token` (prefer `IDENTITY_AUTH_TOKEN`). Omitted YAML `require_identity`
defaults to true; explicit false rejects remote startup. Omitted/empty
`identity.namespace` defaults to `default`; explicit valid values are unchanged.
Namespace is a stable administrator-chosen trust domain, 1–64 characters
`[a-z0-9][a-z0-9_-]*`. Independent deployments/trust domains sharing a sessions
root must use different namespaces.
Every initialize, tools/list, call, notification and GET/POST/DELETE validates the
platform token and identity before body reading/dispatch. Only trusted upstreams
may hold the platform token; never distribute it to end users. There is no anonymous
or global-AK/key fallback for remote methods. stdio remains local-only, one SDK or
API Key process identity, never per-chat-user authorization.

With `identity.jwt.enabled=true`, a **completely absent JWT header** permits trusted
Header `user`/`email` identity, subject to shared mappings below. A present but empty,
invalid or expired JWT is rejected, never downgraded to Header identity. A valid
HS256 JWT with `exp` and usable `user_id` or `email` takes precedence; simultaneous
plain user/email headers are ignored for identity. With `jwt.enabled=false`, trusted
Header identity is used and any JWT header, including an empty one, is rejected.
No new compatibility switch is needed.

Across **all groups and any default**, either both `tenant_id`/`agent_id` mapping
values are omitted/empty (legacy shared mapping), or both are exact nonempty values
on every mapping. Mixed shared/scoped mappings, partial scopes and wildcards are
rejected. Shared mappings accept trusted Header identity and valid HS256/exp JWTs
without `tenant_id` or `agent_id`; matching uses user ID/email within the trusted
domain. Existing shared `groups`/`default` need no added scope IDs. Explicitly scoped
mappings require a JWT with both signed scope claims matching the mapping exactly;
Header identity cannot match them. Default applies only to unmatched users in the
selected mapping mode and, when scoped, within that exact scope. Multiple matching
groups reject even with the same role.

On either Header path, the trusted upstream must authenticate the caller, overwrite
client-supplied identity headers, keep the platform token from end users, and ensure
identifier uniqueness within the namespace. Restrict server access to trusted
upstreams; the token authenticates that upstream, not an end user's self-reported
identity. All configured JWT keys have equivalent signing authority within **one
trusted domain**, not independent issuer isolation. Unrelated issuers need separate
instances/configurations and different namespaces if sharing a sessions root.

For RoleSessionName attribution, a missing `session_name_claim` value on the Header
path falls back to `user_id`, then `email`. On the JWT path, an explicitly selected
non-default claim must exist; only the default `user_id` selection can fall back to
email. This does not change user/email role matching or grant cloud isolation.

Remote history uses `sessions_dir/identity-v2/<principal_id>/`. The `principal-v2`
digest includes namespace, `auth_mode`, tenant/agent scope (including absent scope)
and typed user ID/email. Protocol sessions, tenant caches and disk history retain
this isolation even with shared role mappings. Header and JWT history never merge
automatically, even for the same user. `owner.json` must match the principal;
nonempty unmarked directories are not automatically claimed. No old v1
`identity/<sanitized-key>` read fallback, deletion or automatic migration exists.
Offline import requires separately authorized administrator proof of unique
ownership, backup and validation of IDs/files/permissions; ambiguous histories
remain offline. Local ownership markers are not cloud session ACLs or row
isolation. Same-role users normally share cloud permissions; RoleSessionName is
attribution only. See the [identity and migration reference](../alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md#multi-tenant-identity-mode-required-remotely).

### Upgrading an existing deployment

Existing unscoped `groups`/`default` mappings can stay unchanged within the same
trusted domain: they support trusted Header identity with no JWT header, even when
JWT is enabled, and valid JWTs without tenant/agent claims. A supplied JWT must pass
verification; explicitly scoped mappings still require complete matching JWT scope.
Enabling `DATA_AGENT_LOG` or `DATA_AGENT_PERF_LOG` does not require a mapping upgrade.
Empty legacy API Key settings remain valid with AK/STS; nonempty API Keys remain
local-stdio-only. Namespace omission uses
`default`, while explicit namespaces are preserved. Compatibility does **not**
disable per-request authentication, protocol ownership, tenant-cache or v2 disk
isolation by verified tenant/agent/typed user, nor enable old-directory reads.
Use the secret-file exception above only as a transition. Release gates remain
open, including real-cloud verification of local API Key authentication.

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
| `DATA_AGENT_LOG` | `1`/`true`/`on` enables ordinary file logging; default disabled (stderr) |
| `DATA_AGENT_LOG_FILE` | Optional server/request log path (default `~/.data-agent/server.log`; `~/` supported) |
| `DATA_AGENT_LOG_MAX_BYTES` | Positive byte limit before rotation (default 67108864 = 64 MiB) |
| `DATA_AGENT_LOG_REQUESTS` | Per-call logging: `basic` / `full` / `off` |
| `DATA_AGENT_PERF_LOG` | `1`/`true` enables the periodic performance sampler (see below) |
| `DATA_AGENT_PERF_LOG_FILE` | Perf log path (default `~/.data-agent/perf.log`) |
| `DATA_AGENT_PERF_LOG_INTERVAL` | Sampling period in seconds (default 30) |

### Server and request logs

Ordinary server/runtime and request logs go to **stderr by default**, never
stdout. Enable rotating file output with just one environment variable:

```bash
export DATA_AGENT_LOG=1
# Restart using your existing server command and transport/port configuration.
```

No path or size setting is required: the defaults are `~/.data-agent/server.log`
and **64 MiB** with one `.1` backup. `DATA_AGENT_LOG_FILE` and
`DATA_AGENT_LOG_MAX_BYTES` are optional overrides; setting a path alone does not
enable file logging. Ordinary file logging and performance sampling have separate
switches: set both `DATA_AGENT_LOG=1` and `DATA_AGENT_PERF_LOG=1` to enable both.

Or merge these settings into the existing `log` section of your config.yaml
(do not add a second `log` key; only `enabled: true` is needed):

```yaml
log:
  enabled: true
  file: ~/.data-agent/server.log
  max_bytes: 67108864
```

Environment variables override YAML. Set `DATA_AGENT_LOG=0` (`false`/`off` also
work) or `log.enabled: false` and restart to restore stderr. An empty
`DATA_AGENT_LOG_FILE` resets the path to its default, not the switch. Keep all
existing transport, port, credentials and identity settings: logging does not
enable HTTP or change authentication. For systemd or containers, set the
variables in the service environment and restart it.

The server appends to an existing regular file. At the default **64 MiB** limit,
it moves the log to `<file>.1`, retaining one backup. Set `log.max_bytes` or
`DATA_AGENT_LOG_MAX_BYTES` to a positive byte count to adjust the limit. On POSIX,
new files use `0600` and new directories use `0700`; existing permissions are not changed.
Symlink log files and, on POSIX, group/world-accessible existing logs are rejected.
Use a trusted directory writable by the service account, with one server process
per log file; do not combine this with external file rotation. A single oversized
record is kept intact and can exceed the size limit.

If the configured file cannot be opened at startup, the server reports the error
to stderr and exits. If later writes or rotation fail, ordinary logging falls
back to stderr, never stdout, and retries file output on subsequent writes.
Configuration errors before logging is initialized still go to stderr.

Request detail remains controlled by `log.requests` / `DATA_AGENT_LOG_REQUESTS`:
`basic` by default remotely, `off` by default under stdio. `full` includes redacted
arguments for existing tools but may contain business questions; use it for
short troubleshooting, not routine operation. Existing credential-redaction,
identity-safety and gateway logging restrictions still apply.

Ordinary logs and the performance sampler below are independent. When both use
files, their current paths and `<file>.1` backup paths must not overlap. Startup
checks normalized paths, existing directory symlinks and hard-linked files to
prevent mixed output or one writer rotating the other's data.

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

**Confirm collection:** the server log (stderr or the configured log file)
contains `perf log enabled` at startup, and the performance file receives JSON
lines starting with an `"event":"start"` record:

```bash
tail -n 1 "$HOME/.data-agent/perf.log"
```

Use your custom path if `file` was overridden. A new line should appear every
configured interval (default 30 seconds); 10–30 seconds is suitable for routine
collection, while 1 second is useful for short diagnostic runs. Ensure the server
account can write to the log directory. To disable, set `enabled: false` or
`DATA_AGENT_PERF_LOG=0` and restart; an environment override takes precedence.

The sampler appends one JSON object per line to a dedicated file, separate from
ordinary server/request logs, so a production stall can be reconstructed
afterwards. Each record carries:

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

## Interaction mode

Pass `work_mode: "CHAT"` to `data_agent_create_session` when the user wants more
interaction, clarification and back-and-forth conversation. The other value is
`ANALYSIS`; omitting `work_mode` keeps the upstream default (`ANALYSIS`). This is
independent of `mode` (`auto` / `lite` / `pro` / `ultra`) and `plan_mode`.

The MCP server places it in `SessionConfig.WorkMode` on the initial
[SendChatMessage](https://api.aliyun.com/api/Dms/2025-04-14/SendChatMessage?RegionId=cn-shanghai)
request, normalized to the lowercase `chat` / `analysis` values required by the
live API. The API only applies that session configuration on the first message,
so choose it during creation, not through a later `data_agent_send` call.
`CHAT` does not alter `auto_confirm`; also set `auto_confirm: false` if the user
wants to approve plans and SQL manually. Web-report approval remains required.

## Verify

```bash
make dacli
# In a trusted upstream/operator context, inject AILY_TOKEN and the selected identity.
# JWT identity uses AILY_JWT; Header identity uses AILY_USER/AILY_EMAIL with no JWT.
./bin/dacli --url http://localhost:61026/mcp tools   # 19 off / 28 base / 30 with P2b
./bin/dacli ... dbs                               # workspace databases
./bin/dacli ... ask <db> "<tables>" "<question>"  # end-to-end analysis
```

dacli also accepts `--config` to read `identity.auth_token` from an existing server
configuration file without copying the token into shell history or process arguments.
For an authorized trusted upstream/operator using shared mappings, the 8931 Header
examples below work with JWT either enabled or disabled. Replace `actual-user` with
the actual authenticated, mapped user; never give this token/config file to end users:

```bash
unset AILY_JWT  # or add --jwt '' to each command to omit the JWT header
./bin/dacli --url http://127.0.0.1:8931/mcp --config ./config.yaml --user actual-user tools
./bin/dacli --url http://127.0.0.1:8931/mcp --config ./config.yaml --user actual-user dbs
./bin/dacli --url http://127.0.0.1:8931/mcp --config ./config.yaml --user actual-user sessions
```

Use the actual server port and configuration path. A nonempty `--token` overrides
`AILY_TOKEN`; the selected token takes precedence over `--config`, which is read
only when that token is empty. Without `--config`, no configuration file is read.
Only `identity.auth_token` supplies the client token: server environment overrides
and `.env` files are not loaded. The existing secret-file ownership, permissions
and trusted-directory checks still apply. `--config` reads the token; it does not
issue or sign a JWT. The server's JWT signing secret is not a client JWT. For JWT
identity, supply a valid `--jwt` / `AILY_JWT`; explicitly scoped mappings require
both matching scope claims and cannot use the Header examples above.

Prefer trusted environment injection for `AILY_TOKEN` and `AILY_JWT` so secrets do
not appear in command history/process arguments. Header identity uses `--user`/
`--email` (or `AILY_USER`/`AILY_EMAIL`) with the platform token and no JWT header.
In dacli, `--jwt ''` omits the JWT header; it does not send an empty header (which
the server rejects). Do not automatically remove a rejected JWT to retry as Header.
A valid JWT takes precedence over `--user`/`--email`; these flags cannot supply or
replace required signed claims, including an explicit non-default session-name claim.
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
