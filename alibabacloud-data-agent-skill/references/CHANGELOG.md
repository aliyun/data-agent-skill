# Changelog

Full version history of the Data Agent CLI skill. The latest entry is mirrored in short form in [SKILL.md](../SKILL.md).

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
