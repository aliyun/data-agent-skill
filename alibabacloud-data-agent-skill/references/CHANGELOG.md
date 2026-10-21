# Changelog

Full version history of the Data Agent CLI skill. The latest entry is mirrored in short form in [SKILL.md](../SKILL.md).

## v2.7.0 (2026-10-20)

`db` / `file` eliminate the CreateDataAgentSession round-trip and converge on the send message path; data sources unify on the session-level `DataSources` list.

### Breaking changes

- `db` no longer pre-creates the session before executing queries (sync and async). The first SendChatMessage creates it implicitly; everything the old CreateDataAgentSession call carried now rides that first message: mode / plan mode / enable search / custom-agent binding via `SessionConfig`. The response's `SessionId` / `AgentId` are parsed the same way `send` does. A rejected first message (business failure) now aborts before any session directory or worker is created, instead of leaving a half-registered async task.
- `file` no longer pre-creates the session either: the local file is uploaded (or `--file-id` taken) and the first message — carrying the file as a `remote_data_center` DataSources entry — creates the session implicitly. The old `file_data_source` pass-through (session-level `DataSource` object reconstructed in the worker from input.json) is gone.
- `db` / `file` no longer send the session-level `DataSource` object at all. **Both `DataSource` and `DataSources` are session-level** (confirmed backend semantics): repeated passes auto-merge server-side with equal priority, and the backend recommends `DataSources` uniformly — the CLI now uses `DataSources` everywhere, carried on the first message of the session; follow-up messages (remaining preset queries) go bare, exactly like `attach` follow-ups. The wire change: one `DataSources` list entry instead of a `DataSource` object plus a duplicate `DataSources` entry on the same message.
- The async worker no longer re-sends the first query: the parent's direct send is already in flight, and the worker consumes its pending response from checkpoint 0 (new `_stream_existing_response` stream-only helper), then sends only the remaining preset queries. A first response that requires plan confirmation stops the batch exactly as before (`status.txt` → `waiting_input`).

### New capabilities

- `send` / `legacy-send` gain `--session-mode` (`auto` / `lite` / `pro` / `ultra`), injected into `SessionConfig.Mode` of the created session — `send` can now express everything `db` / `file` could.
- `db`, `file` and `send` share one message path (`start_session_with_message` in `cmd_send`): direct send with implicit session creation, the API_KEY + custom-agent pre-create fallback, response parsing and result printing all live in one place. `db` / `file` are workflow layers (async worker, presets, streaming consumption) on top of it.
- New `status` subcommand: a read-only session status query (one DescribeDataAgentSession call — no streaming, no worker) that prints status / title / mode / agent, shows the local async worker's `status.txt` when present, and prints the attach confirmation guidance for WAIT_INPUT sessions. Complements `send --poll` (which waits for the message it just sent) instead of replacing it.
- `DescribeDataAgentSession` status parsing is now robust to wire variants: `SessionStatus` matching is case-insensitive and gateway/backend values (`init`, `INITIALIZING`, `RECOVERABLE`, `UNAVAILABLE`, `FINISHED`, `CANCELED`/`CANCELLED`, `ERROR`) are normalized to the CLI enum (`init`/`RECOVERABLE` → `IDLE` — a cold session accepts a new message; `INITIALIZING` → `CREATING`; `UNAVAILABLE`/`FINISHED`/`CANCELED` → `STOPPED`; `ERROR` → `FAILED`). `SessionInfo` gains `title` / `mode` / `raw_status`; `status` shows the server's own value alongside the normalized one, e.g. `IDLE (server: RECOVERABLE)`. Previously these variants all fell through to `CREATING`, which made completed sessions look like they were still starting (and left the `--poll` terminal-state branch dead).

### Compatibility notes

- API_KEY auth + `--custom-agent-id` still pre-creates via CreateDataAgentSession — the backend does not support implicit creation with a custom-agent binding under API_KEY auth yet (same fallback for `send` / `db` / `file`, now handled by the shared path).
- Follow-up interactions remain `attach --session-id <ID>`; `--poll` / status checks remain DescribeDataAgentSession-based. No observable CLI surface changed: flags, outputs and the async kickoff contract (session ID printed immediately) are unchanged.

### SDK contract

- `send_message` (sync and async) accepts `enable_search`, injected into `SessionConfig.EnableSearch` — carried on the first message of an implicitly created session where the CreateDataAgentSession path previously set it at session level.
- `send_message` docstrings corrected: `data_sources` is session-level (merged across messages), not message-level; `DataSources` is the backend-recommended form over the `DataSource` object.

## v2.6.1 (2026-10-20)

Message-level database data sources always carry `DmsDatabaseId`, enforced as a required CLI parameter.

### Breaking changes

- `--dms-db-id` (`DataSources[].DmsDatabaseId`) is now **required whenever any database data source parameter is passed** on `send` / `legacy-send` / `db` message sends (`--dms-instance-id`, `--dms-db-id`, `--db-name`, `--tables`). Previously a database data source without a database ID was sent as-is; the backend resolves table schemas (`GetTableKnowledgeInfo`) and executes SQL (`SubmitAsyncSQLExecuteTask`) by database ID, so such a source always failed server-side (typically as a table permission error). The CLI now fails fast before any API call, with the fix spelled out in the error message. Sessions driven purely by a custom agent or a `--data-source` file are unaffected.

### New capabilities

- `send` / `legacy-send` gain the `--dms-db-id` flag (the `DataSources` database entry previously had no way to express the database ID).

### Fixes

- `_build_data_sources` now fills `DmsDatabaseId` (and `Engine` when available on the subcommand), closing the gap with the session-level `DataSource` path which already carried it.
- SKILL.md / COMMANDS.md custom-agent note corrected: data source parameters alongside `--custom-agent-id` **are technically accepted** (the backend merges them into the analysis scope and rejects out-of-scope sources when the agent forbids appending) — they are still best left unset so the agent's own data scope drives the session; the old wording ("should not be passed") is now explicit about this.

## v2.6.0 (2026-10-20)

`send` drops the CreateDataAgentSession round-trip under AK/SK auth; `legacy-send` keeps it for `--title`.

### Breaking changes

- `send` no longer accepts `--title`: session titles are only expressible through CreateDataAgentSession, which `send` no longer calls under AK/SK auth. Use the new `legacy-send` subcommand when a title is genuinely required; otherwise the session gets a server-side default title.

### New capabilities

- **Custom-agent direct send (AK/SK)**: `send --custom-agent-id <ID>` binds the custom agent via the SendChatMessage `SessionConfig` (`CustomAgentId` / `CustomAgentStage`); the backend creates the session implicitly and the response carries the new `AgentId` / `SessionId` — one API call instead of two. Under API_KEY auth `send` still pre-creates the session (backend limitation).
- **`legacy-send` subcommand**: the pre-CreateDataAgentSession send path, kept for `--title` and the API_KEY custom-agent path.

### SDK contract

- `send_message` (sync and async) accepts `custom_agent_id` / `custom_agent_stage`, injected into `SessionConfig.CustomAgentId` / `CustomAgentStage`.

### Fixes

- SKILL.md quick-start connectivity check: removed the invalid `--no-async-run` flag from the `send` example (`send` is synchronous by design; the flag exists only on `db` / `file` / `attach`).

## v2.5.0 (2026-10-20)

Sync with the maintained master: the CLI skill absorbs the internal master's v1.9.0–v2.5.0 feature set in one pass.

### New capabilities

- **Data permissions**: `send --permission-file` attaches per-table column whitelist/blacklist and row filters to the database data source; `--permission-default-action allow|deny` sets the session-level default for undeclared tables. snake_case JSON is auto-converted to the PascalCase wire format. See [PERMISSIONS.md](PERMISSIONS.md).
- **Credentials-file authentication**: `DATA_AGENT_CREDENTIALS_FILE` / `~/.data-agent-credentials.json` with multi-account environment/profile selectors; unambiguous files (one environment, or one profile under it) auto-select when both selectors are unset, ambiguous files fail with the full environment→profiles listing. Entries may carry `api_key`, which takes precedence and switches the profile to API_KEY mode. See [CREDENTIALS.md](CREDENTIALS.md).
- **`send` subcommand**: session-less direct send — the backend creates the session and the response carries the new `AgentId` / `SessionId` (printed for follow-up `attach` / `--poll`); connectivity checks; permission-controlled requests.
- **`cancel` subcommand**: cancel a running session.
- **`upload` subcommand**: standalone file upload — prints the file ID, creates no session.
- **Report generation messages**: `--message-type primary|report|diag` on `send`, `db`, `file`, `attach`; `report` attaches a `TaskConfig.ReportConfig` (WebReport); `diag` sends a report-diagnosis request; `--report-theme`, `--report-prompt <text|@path>`.
- **Custom agent lifecycle**: `agent create / update / publish`; `DATA_AGENT_CUSTOM_AGENT_ID` env / credentials-file default; `db` data-source parameters (`--dms-instance-id` / `--dms-db-id` / `--db-name` / `--tables`) optional in custom-agent mode.
- **Session management**: `list_sessions` filters (`custom_agent_id` / `mode` / `title` / `is_saved` / `query_type` + workspace auto-resolution); `attach --workspace-id`.
- **Endpoint overrides**: `--endpoint` on all subcommands; `DATA_AGENT_DMS_ENTERPRISE_ENDPOINT` for the 2018-11-01 discovery APIs (VPC-only egress).

### SDK contract

- `agent_id` is optional across `send_message` / `get_chat_content` / `stream_chat_content` / `get_full_response` (sync and async) — sessionId-first is the supported calling style; empty `AgentId` / `SessionId` are omitted from the wire (`GetChatContent` drops `AgentId` when absent).

### Fixes

- Credential-value redaction in debug logs: signed OSS/STS URLs carrying `AccessKeyId` / `SecurityToken` / `Signature` inside string values are now masked, not just key-named fields.
- `ListFileUpload` response dual-shape compatibility: camelCase `fileId` / `fileName` / `downloadLink` alongside PascalCase (sync and async).
- `list_sessions` time parameters documented as epoch milliseconds — the gateway rejects RFC3339 in both auth modes (verified in pre-production and production).

### Dependencies

- `alibabacloud-dms20250414>=2.0.0` (session-level `PermissionConfig` models).

### Removals

- Deprecated `--dms-unit` CLI flag and DMSUnit injection in SSE streaming; DMSUnit remains only as temporary API-KEY RPC compatibility (`DATA_AGENT_DMS_UNIT` env still honored for session APIs).

## v1.8.6

Fix SSE parsing when server omits `event:` prefix line (extract event_type from JSON payload as fallback); add DMSUnit to all SSE streaming methods (GetChatContent); add `--dms-unit` CLI flag to `db`, `file`, `attach` subcommands; add `--workspace-id` CLI flag to `attach` subcommand; DMSUnit resolution uses env/config/CLI override before `GetActiveRouteUnit` and region fallback.

## v1.8.5

Database listing migrated to `ListTagMetaAsset` (dms-enterprise 2018-11-01); workspace auto-resolution (CLI `--workspace-id` > env `DATA_AGENT_WORKSPACE_ID` > `InitDataAgentPersonalWorkspace`); `db` subcommand relaxed `--instance-name` to optional.

## v1.8.4

Document project Python virtualenv (`venv/`) setup and activation; add end-to-end regression notes for ASK_DATA / ANALYSIS (async + attach).

## v1.8.3

`db` and `file` subcommands now accept `--session-mode` with simplified mode tiers.

## v1.8.2

`SendChatMessage` now supports per-message mode override (injected via `SessionConfig.Mode`); dynamic DMSUnit resolution via `GetActiveRouteUnit`.

## v1.8.1

Emphasize `attach`-based session reuse as the core interaction mechanism; add golden workflow, capability matrix, and usage rules.

## v1.8.0

Add workspace (collaborative space) support, add custom agent support.

## v1.7.2

Use Alibaba Cloud default credential chain instead of explicit AK/SK, add User-Agent header, fix RAM policy wildcard issues.

## v1.7.1

Fix CLI `ls` command API response parsing (support case-insensitive field names), optimize SKILL documentation structure, separate pro mode specification document.

## v1.7.0

API_KEY authentication support, native async execution mode, session isolation, enhanced attach mode, optimized log output.
