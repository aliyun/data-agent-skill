---
name: alibabacloud-data-agent-mcp-skill
description: Alibaba Cloud Data Agent MCP skill (alibabacloud-data-agent-mcp-skill, data-agent MCP) for enterprise database/file analysis. Use when the user asks (in any language, including Chinese) to query/analyze DMS-managed databases, run SQL/data analysis, start quick-query (lite) or deep-analysis (pro/ultra) sessions, generate reports/insights, upload CSV/XLSX/JSON/TXT files, manage Data Agent workspaces/custom agents/sessions/files, or mentions Data Agent, data-agent, DMS, Data Center, data analysis, database query, SQL analysis, report generation, data insight, file analysis, workspace, custom agent, session creation, session query, stop/delete session, or any data_agent_* MCP tool. Always call data_agent_* MCP tools directly; do not answer from memory or use aliyun CLI, Python SDK, curl, or API workarounds.
compatibility: Requires a data-agent-mcp-server binary built from the repository's server/ project (Go 1.23+) or deployed by the operator; select-binary.sh locates it via DATA_AGENT_SERVER_BIN, the skill's assets/bin/, or the monorepo's server/bin/. Requires Alibaba Cloud credentials; data sources must be managed in Alibaba Cloud DMS Data Center.
domain: AIOps
---
metadata:
  author: DataAgent Team
  version: "3.0.0"
---

# Changelog
- **v3.0.0** — Session mode tiers aligned with the platform: `auto` / `lite` / `pro` / `ultra` (defaults: `auto` for database, `pro` for file); legacy `ASK_DATA`/`ANALYSIS`/`INSIGHT` values are auto-mapped to `lite`/`pro`/`ultra` for backward compatibility. New `plan_mode` parameter on `data_agent_create_session` (`force` = always generate an execution plan, `disable` = skip planning), injected as `SessionConfig.PlanMode`. Generic multi-tenant identity mode: `identity` config section (legacy `aily:` alias still parsed) with configurable identity headers (defaults `x-aily-*`, Feishu Aily compatible) and two mapping styles — `identity.default` (global sharing: one RAM role + workspace/agent/mode session defaults for every identified user) and `identity.groups.<name>` (per-group role + session defaults + member list; group wins over default). Per-user tenants (isolated client, session store `sessions_dir/identity/<user>/`, `RoleSessionName = <prefix>-<user_id>` for ActionTrail attribution) via STS AssumeRole with auto-refresh. Follow-up questions on finished sessions: `data_agent_send` revives completed sessions from persisted state. YAML (`config.yaml`) + `.env` configuration replaces JSON-only config. Fixed SSE parsing without `event:` prefix lines and watcher lifetime on HTTP transports. Repackaged as a standalone skill; binaries are built from source by `scripts/select-binary.sh` (Go 1.23+).
- **v2.x** — Tool surface grew to 18 `data_agent_*` tools (discovery/import, workspaces, custom agents, file upload, session lifecycle). Event-driven waiting: `data_agent_wait_result` blocks until the session needs LLM attention; `data_agent_status` gained server-side `wait_timeout`; CRITICAL ANTI-LOOP rules against repetitive polling. `data_agent_result` returns chart images as MCP ImageContent. Clear split between workspace Data Center metadata (`data_agent_list_workspace_databases`, authoritative for `create_session`) and DMS discovery (`data_agent_search_dms_databases`, import only). API Key auth mode and file analysis flows.
- **v2.0.0** — Initial Go MCP Server architecture: Session Daemon with background SSE monitoring, auto-confirm for ANALYSIS mode, core session tools.

---

# Runtime Contract

This skill requires the `data-agent` MCP server to be registered in the host runtime. Installing the skill only gives the agent instructions; it does not automatically make `data_agent_*` tools available in every client.

For any Data Agent task, call the exposed MCP tools such as `data_agent_list_workspace_databases`, `data_agent_create_session`, `data_agent_status`, and `data_agent_result`. Do not answer from cached knowledge, do not run `scripts/select-binary.sh` as a one-off CLI workflow, do not start the bundled server during task execution, and do not call Alibaba Cloud APIs or the MCP HTTP endpoint directly with curl.

If no `data_agent_*` MCP tools are visible, stop immediately and report: "Data Agent MCP server is not registered in this agent runtime. Task aborted." In OpenClaw, AgentHub, or other hosted runtimes, MCP registration must be completed during the runtime setup/install phase; a task-running agent must not self-register, edit runtime config, launch `select-binary.sh`, probe localhost ports, or curl `/mcp` as a workaround.

## Trigger Checklist

Use this skill for any prompt that mentions one of these intents or names:

- Intents (any language, including Chinese): `data analysis`, `database query`, `SQL analysis`, `report generation`, `data insight`, `file analysis`, `upload file for analysis`, `workspace`, `custom Agent`, `session creation`, `session query`, `stop session`, `delete session`
- Names: `Data Agent`, `data-agent`, `DMS`, `Data Center`, `database analysis`, `SQL analysis`, `generate report`, `data insight`, `file analysis`, `workspace`, `custom agent`, `session status`, `stop session`
- Tool names or resources: any `data_agent_*` tool, DMS database/table/import metadata, Data Agent workspace/session/file/report resources

## Mandatory Tool-Use Rules

When this skill is active:

1. Use `data_agent_*` MCP tools as the only execution path.
2. Do not fall back to `aliyun` CLI, Python SDK, direct HTTP/curl, locally cached metadata, or hardcoded database IDs.
3. If `data_agent_*` tools are not available in the runtime, report: "Data Agent MCP server is not registered in this agent runtime. Task aborted." Then stop the task. Do not start the bundled server, do not edit runtime settings, do not probe localhost ports, do not call `/mcp` with curl, and do not attempt CLI/SDK/API fallbacks.
4. For database analysis, **always** call `data_agent_list_workspace_databases()` in the same conversation turn **immediately before** `data_agent_create_session`, and use **only** the returned row for `database_id`, `db_name`, `instance_id`, `instance_name`, and `engine`. This applies in ALL auth modes (AK/SK and API Key). **Never** guess, infer, or reuse database/instance parameters from memory, prior conversations, user-provided IDs, or DMS search results — workspace contents change over time and stale values cause `Specified parameter InstanceId is not valid` or wrong-database analysis.
5. For file analysis, call `data_agent_upload_file()` before `data_agent_create_session(file_id=...)`.
6. For session query/status/stop/delete-style requests, use `data_agent_list_sessions()`, `data_agent_status()`, and `data_agent_stop_session()`. If the user asks for hard deletion, explain that the current MCP surface supports stopping monitoring and cleanup via `data_agent_stop_session`, not permanent remote deletion.
7. Do not invent deprecated tool names or legacy command patterns such as `data_agent_search_databases`, `data_agent_list_databases`, `file` subcommands, `attach --session-id`, `reports --session-id`, `--db-id`, or wildcard table imports.
8. Workspace names are display names, not IDs. If the user gives a workspace name such as `dev-workspace`, call `data_agent_list_workspaces(type="ALL")`, exact-match `name`, and use the matched `workspace_id` for workspace-scoped tools. Do not pass the name itself as `workspace_id` and do not guess an ID.

---

# Installation & Setup

Setup is an **install-time responsibility**, not part of task execution. This skill assumes the `data-agent` MCP server (18 `data_agent_*` tools) is already registered in the host runtime.

- **Full setup reference** (transports & runtime registration for Claude Code / OpenClaw / hosted runtimes, credentials, YAML + .env configuration, multi-tenant identity mode, observability): [references/INSTALLATION.md](references/INSTALLATION.md)
- **Human deployment walkthrough** (systemd, dacli verification client): [README.md](README.md)
- **RAM permissions**: [references/ram-policies.md](references/ram-policies.md)

Facts the agent may need when the user asks setup questions (do not perform setup during analysis tasks):

- The skill ships no server source or binaries. The operator builds `data-agent-mcp-server` from the repository's `server/` project (Go 1.23+) or deploys a release binary; `scripts/select-binary.sh` locates it (`$DATA_AGENT_SERVER_BIN` > skill `assets/bin/` > monorepo `server/bin/`). Transports: stdio (default), Streamable HTTP (`MCP_TRANSPORT=streamable-http` + `MCP_PORT`, endpoint `/mcp`), SSE.
- Credentials come from the Alibaba Cloud default chain (AK/SK env vars, `~/.aliyun/config.json`, ECS role) or `DATA_AGENT_API_KEY`. **API Key mode disables** `data_agent_list_tables`, `data_agent_search_dms_databases`, `data_agent_search_instances`, and `data_agent_import_database`; `data_agent_list_workspace_databases` and `data_agent_list_imported_tables` still work and remain the mandatory metadata source.
- Optional multi-tenant identity mode maps per-user identity headers (defaults `x-aily-*`) to RAM roles via STS AssumeRole; group config can inject `workspace_id` / `custom_agent_id` / `mode` as `data_agent_create_session` defaults — explicit tool arguments always win.

---

# MCP Tools (18)

## Core Analysis

### data_agent_list_workspace_databases
List databases imported into a workspace's Data Agent Data Center. This is the **authoritative source** for database analysis sessions.
```
data_agent_list_workspace_databases(workspace_id?)
→ [{db_id, db_name, db_type, instance_id, instance_resource_id, catalog_name}, ...]
```
- **workspace_id** (optional): target workspace. Defaults to the configured workspace. Resolve names to IDs via `data_agent_list_workspaces`; never pass a workspace name.

Use this tool before `data_agent_create_session` for database analysis. Pass its `db_id`, `db_name`, `instance_id`, `instance_resource_id`, and `db_type` into the session options.

### data_agent_create_session
Create an analysis session with automatic SSE monitoring. Supports **database analysis** (`database_id`) or **file analysis** (`file_id` from `upload_file`). For pro/ultra mode with `auto_confirm=true`, all plan/SQL/report confirmations are handled automatically.
```
data_agent_create_session(
  query,                                          # Required
  database_id?, db_name?, tables?,                # For database analysis (database_id required)
  file_id?, file_name?,                           # For file analysis (file_id required, from upload_file)
  mode="auto|lite|pro|ultra",                     # Default: auto for database, pro for file
                                                  # (legacy ASK_DATA/ANALYSIS/INSIGHT auto-map to lite/pro/ultra)
  plan_mode="force|disable",                      # pro/ultra only: force = always generate an execution plan,
                                                  # disable = skip planning and execute directly; empty = server default
  auto_confirm=true,                              # Auto-confirm plans/SQL/reports
  instance_id, instance_name, engine="mysql",     # Optional (database only)
  workspace_id, custom_agent_id                   # Optional
)
→ {session_id, status, mode, auto_confirm}
```

> **Note**: `database_id` and `file_id` are mutually exclusive — use one or the other. For file analysis, pass the `file_id` and `filename` returned by `upload_file`.

> **Database session gotcha**: `data_agent_search_dms_databases` is only for DMS discovery/import. Its returned `instance_id` may be `0` or otherwise unusable for `create_session`, which can cause `Specified parameter InstanceId is not valid` or a `database_None_<db_name>` data source. Before creating a database session, always call `data_agent_list_workspace_databases()` in the target workspace and use the imported database row from Data Center:
> - `database_id` = `db_id`
> - `db_name` = `db_name`
> - `instance_id` = `instance_id`
> - `instance_name` = `instance_resource_id`
> - `engine` = `db_type`
>
> If the database is not returned by `data_agent_list_workspace_databases()`, use `data_agent_search_dms_databases` → `data_agent_list_tables` → `data_agent_import_database`, then call `data_agent_list_workspace_databases()` again and use that row for `create_session`.

### data_agent_wait_result
Block until the session needs LLM attention: completed, error, canceled, or waiting for manual input. For `auto_confirm=true` sessions this fires only on completion/error, collapsing all intermediate polling into a single blocking call. **Preferred over looping `data_agent_status` where this tool is exposed.**

> **May be unavailable.** Non-blocking deployments (e.g. DataBuddy IM) disable this tool via `tools.exclude` because a long internal block can exceed the outer MCP call timeout. If `data_agent_wait_result` is not in your tool schema, do not call or invent it — fall back to the one-shot status snapshot protocol (create session, take at most one `data_agent_status` snapshot per user turn, end the turn if still running). See the Progress Tracking Protocol below.
```
data_agent_wait_result(session_id, timeout=110)
→ {session_id, status, reason, mode, current_step, total_steps, step_name,
   waiting_for, waiting_detail, checkpoint, conclusions, artifacts, error_message, updated_at}
```
- **timeout** (default and ceiling: server wait cap, 110s unless `DATA_AGENT_WAIT_CAP` overrides it): max seconds to block. The server clamps larger values so the response always beats common MCP client transport timeouts (~120s) — never rely on a longer block.
- **reason**: `"completed"` | `"error"` | `"canceled"` | `"waiting_input"` | `"timeout"` | `"client_canceled"` | `"duplicate_wait"`
- On `reason="timeout"` the session is still running and the response carries `checkpoint_delta`, `new_conclusions`, and `next_action`: briefly report that progress to the user, then call `data_agent_wait_result` again with the same `session_id`. Loop until a terminal reason (cap the loop, e.g. 10 rounds for pro/ultra).
- **Never call in parallel** for the same session: a duplicate concurrent call returns immediately with `reason="duplicate_wait"` and a warning instead of blocking.
- Returns immediately when the SSE watcher fires any terminal or input event — no polling.

### data_agent_status
Get current status snapshot. Use `wait_timeout` when you want incremental step-level progress during a long run.
```
data_agent_status(session_id, wait_timeout=30, poll_hint="check-N")
→ {session_id, status, mode, current_step, total_steps, step_name, waiting_for, conclusions, artifacts, poll_seq, changed, ...}
```
- **wait_timeout** (recommended: 30): seconds the MCP server blocks waiting for the next status change (capped server-side at the wait cap, 110s by default). Returns `changed=true` when checkpoint advances; `changed=false` on timeout. Long-poll calls do not count toward the anti-loop `poll_seq` warning; bare snapshots (`wait_timeout` omitted) do. Never call in parallel for the same session.
- **poll_hint** (optional): incrementing tag (`"check-1"`, `"check-2"`, …) to distinguish consecutive calls in history.

### data_agent_watch_session
Attach the MCP Server to an existing remote Data Agent session (e.g. one created by the Console/Web UI or a previous process) and start background SSE monitoring. Returns immediately after the server-side watcher is registered; then use `data_agent_status` / `data_agent_wait_result` / `data_agent_send` as usual.
```
data_agent_watch_session(session_id, agent_id?, workspace_id?, mode?, auto_confirm=true)
→ {ok, session_id, status}
```

### data_agent_send
Send a message to an active session (confirmations, follow-up questions, human input).
```
data_agent_send(session_id, message)
→ {ok, status}
```

> **Follow-up questions on finished sessions are supported.** If the session has already completed (or the server restarted), `data_agent_send` transparently revives it from persisted state and resumes SSE monitoring from the saved checkpoint — the remote Data Agent session keeps the full conversation context, so pronouns like "那最低的呢？" resolve against earlier turns. After sending, use `data_agent_wait_result` / `data_agent_result` as usual; each turn appends a new entry to `conclusions`.

### data_agent_result
Get the analysis result of a Data Agent session. Returns structured JSON metadata as text, plus any chart images as ImageContent (base64).

```
data_agent_result(session_id)
→ Content[0]: TextContent — {status, conclusions, artifacts, images, confirmations, recommended_questions, error_message}
→ Content[1..N]: ImageContent — chart images (base64, image/png)
```

The `images` field in JSON lists persisted filenames (e.g. `["img_0.png", "img_1.png"]`).
Chart images extracted from SSE `output_conclusion` events are automatically decoded and persisted locally, then returned as MCP ImageContent objects that LLMs with vision can directly interpret.

### data_agent_list_sessions
List sessions managed by the MCP Server. By default returns only active (in-memory) sessions; set `include_history=true` to also include completed/errored sessions from disk.
```
data_agent_list_sessions(include_history=false)
→ [{session_id, status, mode, current_step, total_steps, step_name, waiting_for, updated_at}, ...]
```

> Active sessions appear first. When `include_history=true`, historical sessions are sorted by `updated_at` descending.

### data_agent_stop_session
Stop monitoring a session and clean up resources.
```
data_agent_stop_session(session_id)
→ {ok}
```

## File & Report

### data_agent_list_files
List files and reports generated by a session. Returns download URLs.
```
data_agent_list_files(session_id, category?)
→ [{file_id, filename, file_type, file_size, download_url}, ...]
```

> **File artifacts are not guaranteed.** A plain pro/ultra run may produce only text conclusions and zero files. To reliably get file outputs, state it explicitly in the session query (e.g. "导出明细数据为 Excel 并生成分析报告" / "export the details as an Excel file and generate a report"). A file-producing run typically yields a Markdown report (.md), an HTML report (.html), and data exports (.xlsx) with OSS pre-signed `download_url`s.

> **Prefer omitting `category`.** `category="WebReport"` only matches interactive web-rendered reports (produced by the report-render confirmation flow) and often returns empty even when .md/.html/.xlsx artifacts exist. Call without `category` to get the full list, then filter by `file_type` yourself.

### data_agent_upload_file
Upload a local file for Data Agent analysis. Returns `file_id` (Data Center ID, e.g. `f-xxx`) for use with `create_session`. Supported types: CSV, XLSX, XLS, JSON, TXT.
```
data_agent_upload_file(file_path)
→ {file_id, filename, size}
```

## Resource Discovery

## Database Tool Naming: DMS Search vs Workspace Data Center

| Tool | Meaning | Use for `create_session`? |
|------|---------|---------------------------|
| `data_agent_search_dms_databases` | Search DMS-managed databases globally by schema name. Use it to discover a database before import. | **No.** Its `instance_id` may be `0` or not usable by Data Agent sessions. |
| `data_agent_list_workspace_databases` | List databases already imported into the current workspace Data Center. | **Yes.** This row is the session source of truth. Use `db_id`, `db_name`, `instance_id`, `instance_resource_id`, `db_type`. |

If the user asks to analyze a database, first try `data_agent_list_workspace_databases()`. Use `data_agent_search_dms_databases()` only when the database is missing from the workspace list and must be imported.

### data_agent_list_tables
List all tables in a DMS database. Queries DMS directly — not workspace-scoped. Use for discovering tables before import.
```
data_agent_list_tables(database_id)
→ [{table_name, table_id, engine}, ...]
```

### data_agent_list_imported_tables
List tables already imported into a workspace. Only shows tables tagged for that workspace.
```
data_agent_list_imported_tables(database_id?, workspace_id?)
→ [{table_name, table_id, engine, db_id, db_name}, ...]
```
- **database_id** (optional): DMS database ID. Omit to list imported tables across all databases in the workspace; each row carries `db_id`/`db_name` for attribution.
- **workspace_id** (optional): target workspace. Defaults to the configured workspace.

### data_agent_search_instances
Search DMS-managed database instances.
```
data_agent_search_instances(search_key?, db_type?)
→ [{instance_id, instance_alias, host, port, db_type, env_type, instance_resource_id}, ...]
```

### data_agent_search_dms_databases
Search databases in DMS by schema name.
```
data_agent_search_dms_databases(search_key)
→ [{database_id, schema_name, host, port, instance_id, db_type, env_type}, ...]
```

> Use this tool to discover databases and obtain a DMS `database_id` for table listing/import. Do **not** treat its `instance_id` as authoritative for `create_session`; it may be `0`. For database analysis sessions, use `data_agent_list_workspace_databases()` after import and pass that workspace Data Center row's `instance_id` and `instance_resource_id`.

### data_agent_import_database
Import database tables into a Data Agent workspace. Uses DMS TagMetaAsset to tag tables into the target workspace's data center.

> **Important**: You must pass **specific table names** — wildcard `*` is NOT supported. Use `data_agent_list_tables` first to get the exact table names, then pass them to this tool.

```
data_agent_import_database(
  database_id,                             # Required: DMS database ID
  tables,                                  # Required: comma-separated table names (NO wildcard *)
  workspace_id?                            # Optional: target workspace (default: configured workspace)
)
→ {ok, database_id, tables}
```

**Typical flow**:
```
1. data_agent_search_dms_databases(search_key="employees") # Get DMS database_id
2. data_agent_list_tables(database_id="68897585")         # Get exact table names
3. data_agent_import_database(
     database_id="68897585",
     tables="employees,departments,salaries")             # Import specific tables
```

## Workspace & Agent

### data_agent_list_workspaces
List Data Agent workspaces (collaborative spaces). Use this tool to resolve a workspace display name to the required `workspace_id`.
```
data_agent_list_workspaces(type?)    # MY (default) | ALL
→ [{workspace_id, name, type}, ...]
```

When the user names a workspace, call `data_agent_list_workspaces(type="ALL")` and exact-match the returned `name`. Values like `dev-workspace` are names, not valid `workspace_id` values.

### data_agent_list_agents
List custom Data Agent agents. Agents belong to a specific workspace — to find agents in a shared workspace, pass that workspace's ID.
```
data_agent_list_agents(
  status?,                           # RELEASED (default)
  workspace_id?                      # Default: personal workspace; must be an ID returned by data_agent_list_workspaces, not a name
)
→ [{agent_id, name, description, status}, ...]
```

> **Note**: Custom Agents are scoped to workspaces. Use `data_agent_list_workspaces` first to find workspace IDs, then pass the desired `workspace_id` to `data_agent_list_agents`. If omitted, only agents in the user's personal workspace are returned. Never pass a workspace display name such as `dev-workspace` as `workspace_id`.

---

# Workflows

## Quick Q&A (lite)
```
1. data_agent_list_workspace_databases()          # Discover imported databases in current workspace
2. data_agent_create_session(
     database_id, db_name, tables, query,
     instance_id, instance_name, engine,
     mode="lite", auto_confirm=true)              # or omit mode for "auto" (backend decides)
3. data_agent_wait_result(session_id, timeout=110) # Block until completed/error/waiting_input
                                                    # (if excluded: one-shot data_agent_status snapshot, then end turn if still running)
4. data_agent_result(session_id)                  # Get conclusion (when completed)
5. data_agent_list_files(session_id)              # Get reports/charts; embed images as ![](download_url)
```

> **Follow-up in lite mode**: lite sessions support multi-turn conversation. When the user asks a follow-up question about the same database (e.g. "what about last month?" or "break it down by region"), use `data_agent_send(session_id, message)` on the **existing session** instead of creating a new one. The session retains prior SQL context, making follow-ups faster and more accurate. Only create a new session when the user switches to a different database or starts an unrelated analysis topic.

> For database sessions, prefer `data_agent_list_workspace_databases()` over `data_agent_search_dms_databases()` as the source of `database_id`, `instance_id`, `instance_name`, and `engine`. DMS search can return `instance_id=0`; passing that to `create_session` may fail with `Specified parameter InstanceId is not valid`.

## Deep Analysis (pro/ultra, auto-confirm)
```
1. data_agent_create_session(
     ..., mode="pro", auto_confirm=true)          # All confirmations automatic ("ultra" for the most thorough tier)
2. LOOP data_agent_wait_result(session_id, timeout=110)  # Server-capped block; pro/ultra runs need several rounds
     reason=="timeout" → report checkpoint_delta/new_conclusions to user, loop again (max ~10 rounds)
     reason terminal   → exit loop
                                                    # (if excluded: one-shot data_agent_status snapshot, then end turn if still running)
3. data_agent_result(session_id)                  # Get multi-step conclusions
4. data_agent_list_files(session_id)              # Get artifacts
```

> **When to use `data_agent_status` instead**: use it when you want to push intermediate step progress to the user (plan text, per-step conclusions, chart images) during a long run. Call `data_agent_status(session_id, wait_timeout=30, poll_hint="check-N")` in a loop until `status != running`; see Status Check Protocol below.

## Database Discovery & Import
```
1. data_agent_search_dms_databases(search_key="sales")   # Find in DMS → get database_id
2. data_agent_list_tables(database_id="15553454")        # Discover all tables in DMS (before import)
3. data_agent_import_database(
     database_id="15553454",
     tables="orders,customers,products")                 # Import specific tables (NO wildcard *)
4. data_agent_list_imported_tables(database_id="15553454")  # Verify tables imported to workspace
5. data_agent_list_workspace_databases()                  # Get workspace row with valid instance_id/instance_resource_id
6. data_agent_create_session(
     database_id="<db_id from list_workspace_databases>",
     db_name="<db_name from list_workspace_databases>",
     tables="orders,customers,products",
     instance_id="<instance_id from list_workspace_databases>",
     instance_name="<instance_resource_id from list_workspace_databases>",
     engine="<db_type from list_workspace_databases>",
     query="...")                                        # Use workspace row, not raw search result
```

> **Note**: `list_tables` queries DMS directly (all tables). `list_imported_tables` shows only tables tagged in the active workspace.
> **Important**: When creating a session after import, re-read `data_agent_list_workspace_databases()` and use that returned row. Do not create the session directly from `data_agent_search_dms_databases` output if `instance_id` is `0` or missing.

## File Analysis (Upload + Analyze)
```
1. data_agent_upload_file(file_path="/path/to/sales.csv")
   → {file_id: "f-xxx", filename: "sales.csv", size: 1024}
2. data_agent_create_session(
     file_id="f-xxx", file_name="sales.csv",
     query="analyze sales trends",
     mode="pro", auto_confirm=true)              # File defaults to pro mode
3. LOOP data_agent_wait_result(session_id, timeout=110)  # Loop on reason=timeout, reporting progress each round
                                                    # (if excluded: one-shot data_agent_status snapshot, then end turn if still running)
4. data_agent_result(session_id)                  # Get conclusions
5. data_agent_list_files(session_id)              # Get generated reports
```

> Supported file types: CSV, XLSX, XLS, JSON, TXT.
> File-based sessions default to pro mode.
> Use `file_id` instead of `database_id` — they are mutually exclusive.

## Custom Agent Analysis
```
1. data_agent_list_workspaces(type="ALL")            # Find workspaces
2. Exact-match the returned workspace name, e.g. name == "dev-workspace"
3. data_agent_list_agents(workspace_id="<matched workspace_id>") # Use ID, never the name
4. data_agent_create_session(
     ..., custom_agent_id="ca-xxx",
     workspace_id="<matched workspace_id>")           # Use custom agent in its workspace
```

If no returned workspace has the requested `name`, stop and say the workspace was not found; include the visible workspace names as candidates. Do not call `data_agent_list_agents` with the unresolved name.

## Workspace Name Resolution
```
1. data_agent_list_workspaces(type="ALL")
2. Find the row where name exactly equals the user-provided workspace name
3. Reuse that row's workspace_id for data_agent_list_agents, data_agent_create_session, or imports
4. Report both name and workspace_id in the final answer when the user asked for a named workspace
```

Example:
```
User: list the custom Agents in the "dev-workspace" workspace
1. data_agent_list_workspaces(type="ALL")
2. match row.name == "dev-workspace"
3. data_agent_list_agents(workspace_id=row.workspace_id)
4. answer with row.workspace_id, row.name, row.type, and agent names
```

Never put a workspace display name into `workspace_id` or `DATA_AGENT_WORKSPACE_ID`; both must use a real workspace ID.

## Custom Agent Session
```
1. Resolve the workspace name to workspace_id if the user supplied a name
2. data_agent_list_agents(workspace_id="<matched workspace_id>") # Find custom agent
3. data_agent_create_session(
     ..., custom_agent_id="ca-xxx",
     workspace_id="<matched workspace_id>")
```

## Session Management (List / Status / Stop)
```
1. data_agent_list_sessions(include_history=true)      # Find active and historical sessions
2. data_agent_status(session_id="...")                 # Inspect current progress/state
3. data_agent_stop_session(session_id="...")           # Stop monitoring and clean local resources
```

Use this workflow when the user asks to query sessions, check progress, stop/cancel a running session, or delete/clean up a session. The current MCP surface does not expose permanent remote session deletion; for delete-style requests, call `data_agent_stop_session` for active monitoring cleanup and clearly state that hard deletion is not supported by the current tools.

---

# Analysis Modes

| Mode | Description | Duration | Auto-Confirm |
|------|------------|----------|-------------|
| **auto** (default for database) | Backend decides the tier based on query complexity | Varies | Follows chosen tier |
| **lite** | Quick Q&A, single SQL, supports follow-up via `data_agent_send` | ~15-30s | N/A |
| **pro** (default for file) | Multi-step deep analysis with plan, generates reports | 2-40 min | Supported |
| **ultra** | Most thorough analysis, multi-dimensional insights, covers more than pro | 2-60 min | Supported |

> Legacy mode names are auto-mapped by the server: `ASK_DATA` → lite, `ANALYSIS` → pro, `INSIGHT` → ultra.

> **Mode selection rules — do NOT switch modes on your own:**
> - **auto**: when the user has no explicit preference — let the backend pick.
> - **lite**: simple questions, single SQL. Supports follow-up via `data_agent_send`.
> - **pro**: user asks for a report, deep analysis, or trend summary. This is the standard choice for structured output. Takes 2-40 min — that is normal.
> - **ultra**: broader and **slower** than pro (up to 60 min). It explores more dimensions autonomously. Only use when the user explicitly asks for the most thorough analysis ("insight", "exploration", "全面洞察"). **ultra is NOT a lighter alternative to pro** — it is heavier.
>
> **NEVER** switch from pro to ultra (or vice versa) to "speed things up" or because the current mode "seems slow." Both tiers are slow by design. The user chose the mode (or you chose it based on their intent) — stick with it. Only change modes if the user explicitly requests a different mode.

## Plan Mode (pro/ultra only)

`plan_mode` controls whether a pro/ultra session generates an execution plan before running:

| Value | Behavior | When to use |
|-------|----------|-------------|
| *(empty, default)* | Server default (currently plans) | Normal case — do not set it unless there is a reason |
| `force` | Always generate an execution plan first (`ask_plan` confirmation; auto-approved when `auto_confirm=true`) | User wants a reviewable, structured multi-step process, or will confirm/modify the plan manually (`auto_confirm=false`) |
| `disable` | Skip planning entirely and execute directly — no `ask_plan` stage, typically faster | User wants conclusions quickly without the plan/confirmation overhead, or says "直接出结果/不用计划" |

> `plan_mode` has no effect on lite sessions (they never plan). Do not use `plan_mode=disable` as a way to "speed up" a run the user asked to be thorough — skipping the plan changes the analysis structure, not just the latency.

---

# Session Lifecycle

```
Created → RUNNING (SSE monitoring active)
  ├── lite: RUNNING → COMPLETED (auto)
  ├── pro/ultra (auto_confirm=true):
  │     RUNNING → ask_plan (auto-confirm) → step execution → ask_sql (auto-confirm) → COMPLETED
  ├── pro/ultra (auto_confirm=false):
  │     RUNNING → WAIT_INPUT (ask_plan) → user confirms → RUNNING → ... → COMPLETED
  └── ERROR / CANCELED (on failure)
```

The MCP Server's Session Daemon automatically:
- Monitors SSE streams for all active sessions
- Handles auto-confirmation (plan, SQL, report render)
- Tracks step progress and extracts conclusions
- Cleans up stale sessions (IDLE > 30 min)
- Reconnects on SSE disconnection (exponential backoff, up to 10 retries)
- Restores active sessions on server restart

---

# Progress Tracking Protocol

### CRITICAL ANTI-LOOP RULES (MANDATORY)
1. **NEVER** call `data_agent_status` consecutively with the same arguments
2. **NEVER** call `data_agent_status` more than 2 times total in one conversation
3. Only use `data_agent_status` for ONE-SHOT status checks (e.g., user explicitly asks "what's the progress?") or as the per-turn snapshot when `data_agent_wait_result` is unavailable
4. Never call or invent `data_agent_wait_result` when it is not in your tool schema
5. Violating these rules triggers upstream "Repetitive tool calls detected" errors that abort the entire conversation
6. **NEVER** abandon a running pro/ultra session and create a new one with the same or similar query — this wastes compute and restarts from zero. Stick with the existing session until it completes, errors, or is explicitly canceled by the user
7. **NEVER** issue parallel/duplicate calls to `data_agent_wait_result` or `data_agent_status` for the same session — issue ONE call and wait for its response. The server degrades duplicates to an immediate snapshot with `reason="duplicate_wait"`; a duplicate response means you must stop and wait for the original call

---

## Choosing a waiting strategy

Two supported paths, depending on whether `data_agent_wait_result` is exposed in your tool schema:

- **`data_agent_wait_result` available (default deployment)** — use it. The server blocks internally and wakes up instantly via SSE. Follow the Standard Workflow below.
- **`data_agent_wait_result` excluded (non-blocking deployments)** — do NOT block. Use the one-shot status snapshot: create the session, take at most one `data_agent_status` snapshot per user turn, and if it is still running, tell the user the session id and that analysis is in progress, then END THE TURN. The next user message drives the next check. See One-Shot Status Check below.

## Standard Workflow (when `data_agent_wait_result` is available)

Use `data_agent_wait_result` — the server blocks internally and wakes up instantly via SSE when the session reaches a terminal state or needs input. The block is capped server-side (110s by default) so each call returns before the MCP transport timeout; long pro/ultra runs simply take several rounds of the loop, each producing a progress update for the user.

```
1. data_agent_create_session(..., auto_confirm=true) → session_id
2. data_agent_wait_result(session_id, timeout=110)
   → {status, reason, ...}
   IF reason == "completed":
     → data_agent_result(session_id)        → conclusions + artifacts + chart ImageContent
     → data_agent_list_files(session_id)   # embed images as ![](url)
     → DONE
   IF reason == "error":    → report error_message → DONE
   IF reason == "canceled": → report to user → DONE
   IF reason == "waiting_input":
     → waiting_for == "ask_plan": show plan, ask user → data_agent_send → loop back to wait_result
     → else: show detail, ask user → data_agent_send → loop back to wait_result
   IF reason == "timeout" (or "client_canceled"):
     → session is still running; the response carries checkpoint_delta, new_conclusions, next_action
     → briefly report that progress to the user, then call data_agent_wait_result again
     → keep looping (up to ~10 rounds for pro/ultra); if the cap is reached, report the
       progress snapshot and session_id to the user → DONE
   IF reason == "duplicate_wait":
     → a wait is already in flight — you issued a parallel call by mistake; do NOT retry,
       wait for the original call's response
```

The default workflow is always:
```
create_session → wait_result → (if timeout: report progress) → wait_result → ... → result
```

**NEVER** use this pattern:
```
create_session → status → status → status → ... → result   ← FORBIDDEN
```

## One-Shot Status Check

Use `data_agent_status` in two cases: (a) the user explicitly asks "what's the progress?" or similar, or (b) `data_agent_wait_result` is excluded from your tool schema, in which case this is the default waiting path. Either way it is a ONE-SHOT check, not a polling mechanism.

**Strict constraints:**
- MUST include a UNIQUE `poll_hint` for EVERY call (e.g., "user-asked-1", "user-asked-2")
- MUST NOT call `data_agent_status` more than 2 times in one conversation
- MUST stop immediately when `changed=false`
- If `data_agent_wait_result` is available and you need to wait for completion, switch to it instead of polling status. If it is excluded, do NOT wait in-turn — report progress and end the turn; the next user message drives the next snapshot.
- NEVER loop on `data_agent_status` — one call, report to user, end turn

```
1. data_agent_status(session_id, wait_timeout=30, poll_hint="user-asked-1")
2. Report progress to user:
   IF status == "completed": → data_agent_result → DONE
   IF status == "error":     → report error → DONE
   IF status == "running":   → report step progress → END TURN (do NOT call status again)
   IF status == "waiting_input": → show detail, ask user → DONE
```

Key rule: **one status call per user request, then end the turn.** The session runs server-side independently; the next user message drives the next check.

## Pushing Plans, Conclusions & Images

Do not hold all output until the very end. Surface intermediate value as it appears so the IM user sees progress, not just a final dump.

- **Analysis plan** — when the status snapshot exposes a generated plan (at
  `ask_plan`, whether auto-confirmed or awaiting confirmation), post the plan
  text to the user before execution proceeds. For `auto_confirm=true` this is
  informational ("📋 Analysis plan: ..."); for `auto_confirm=false` it is the
  confirmation prompt.
- **Step conclusions** — the `conclusions` field in the status snapshot grows
  as steps complete. Each time it gains new entries since the last report,
  push only the **newly added** conclusions ("💡 Step conclusion: ..."), instead of
  repeating everything or waiting for `data_agent_result`.
- **Images / charts** — analysis charts are available through two channels:

  1. **`data_agent_result`** — returns chart images directly as MCP ImageContent
     (base64). LLMs with vision capability can interpret these images immediately.
     Use this as the primary source for charts during and after analysis.

  2. **`data_agent_list_files`** — returns file metadata with `download_url` for
     each generated file. Embed image artifacts as **markdown image syntax**:
     ```
     ![{filename}]({download_url})
     ```
     The gateway auto-extracts markdown `![alt](url)` / `<img>` from the reply
     and delivers them as native images on IM platforms (DingTalk, Feishu, etc.).

  For IM delivery, prefer `download_url` markdown embedding — this works across
  all platforms. Push charts as soon as they are generated (don't batch to the
  end), and re-send the full set at completion alongside the conclusions.

  For non-image reports (xlsx, pdf, etc.), give a labeled download link instead.

## Progress Report Format

Each time step_name changes, send a concise progress update. Match the user's
language for the human-readable labels (the examples below use the localized
form the user is speaking); the placeholders come from the status snapshot:

- `Step 1/5: generating analysis plan...`
- `Plan: {plan}`  (push the plan to the user once it is generated)
- `Step 2/5: running SQL query...`
- `Step 3/5: analyzing data...`
- `Conclusion: {newly added conclusions}`  (push when conclusions grow)
- `Step 4/5: generating charts...`
- `![chart.png]({download_url})`  (push charts as they are produced, markdown image syntax)
- `Step 5/5: summarizing...`
- `Done. {conclusions} conclusions` + all images/reports

For IM platforms (DingTalk, Feishu, etc.), prefer a single progress message
updated in place for step changes; but the analysis plan, newly-added
conclusions, and generated images are **content** the user wants to keep, so
deliver those as their own messages rather than overwriting the progress line.

## Timing Guidelines

| Mode | Expected Duration |
|------|------------------|
| lite | 15-30s |
| pro | 2-40 min |
| ultra | 2-60 min |

These are end-to-end durations, not poll intervals. Do NOT translate them into a busy-wait loop. A single pro/ultra run can take tens of minutes — you cannot hold the turn open that long by spinning on `data_agent_status`. Check status once (per the protocol above), report progress, and end the turn if it is still running.

## Waiting Input Handling

When `status == "waiting_input"`:
- `waiting_for == "ask_plan"` → Show the analysis plan and ask user to confirm or modify
- `waiting_for == "ask_sql"` → Show the SQL to execute and ask user to confirm
- `waiting_for == "ask_report"` → Show report format and ask user to confirm
- `waiting_for == "human_input"` → Show the question and ask user for free-text input

After getting the user's response, call `data_agent_send(session_id, message)`. Then check status once more per the protocol — do not resume a tight polling loop.

## Error Recovery

If a single `data_agent_status` call fails (network error, timeout), you may retry it at most a couple of times before reporting the failure to the user. The MCP server maintains session state independently; a temporary status-call failure does not affect the running analysis. Never retry the same failing call in an unbounded loop.

---

# Project Structure

```
├── SKILL.md                     # This document
├── README.md                    # Human-facing deployment guide (stdio / HTTP / Aily)
├── scripts/
│   └── select-binary.sh         # Server binary locator ($DATA_AGENT_SERVER_BIN > assets/bin/ > ../server/bin/)
├── references/
│   ├── INSTALLATION.md          # Setup reference (transports, credentials, config, identity mode)
│   └── ram-policies.md          # RAM permission requirements
└── assets/
    ├── example_game_data.csv    # Example data for evals
    └── bin/                     # Optional: deployer-placed server binaries (gitignored)

../server/                       # Go MCP Server source (standalone project at the repository root)
├── main.go                      # Entry point
├── go.mod / Makefile            # Build config (make build / build-all / dacli / test)
├── config.yaml.example          # YAML config template (region/workspace/identity multi-tenant)
├── .env.example                 # Secrets template (AK/SK, auth token)
├── bin/                         # Build output (gitignored)
├── cmd/dacli/                   # Manual verification client
└── internal/
    ├── mcp/                     # MCP Server + 18 tool handlers
    ├── session/                 # Session Manager + Watcher + Housekeeping
    ├── dataagent/               # Alibaba Cloud API client (V3 signing, SSE)
    ├── config/                  # YAML + .env configuration loader
    ├── tenant/                  # Identity → RAM role AssumeRole registry
    └── event/                   # SSE event parser
```

---

# Troubleshooting

| Problem | Cause | Solution |
|---------|-------|----------|
| MCP tools not available | MCP Server not registered in the agent runtime | Abort the task with "Data Agent MCP server is not registered in this agent runtime. Task aborted." Registration must be fixed in AgentHub/OpenClaw setup, then the runtime must be reloaded. Do not start `select-binary.sh`, edit runtime settings, probe localhost, curl `/mcp`, or fall back to CLI/SDK/API calls during the task |
| `No data-agent-mcp-server binary found` | No binary deployed yet | Build one from the repository's `server/` project (`cd server && make build`, Go 1.23+) or set `DATA_AGENT_SERVER_BIN` / place it in the skill's `assets/bin/` |
| `failed to load credentials` | No AK/SK configured | Set env vars or create `~/.aliyun/config.json` (see Installation) |
| `data_agent_list_workspace_databases` returns empty | Database not imported to Data Center | Use `data_agent_search_dms_databases` → `data_agent_import_database` to import first |
| `Specified parameter InstanceId is not valid` / `database_None_<db>` | Session was created from DMS search output or missing instance metadata | Call `data_agent_list_workspace_databases()` in the target workspace and pass its `instance_id` plus `instance_resource_id` as `instance_name` to `create_session` |
| `create_session` timeout | Session creation slow | Set `DATA_AGENT_DMS_UNIT` to skip auto-resolution |
| `SSE connection failed` | Network/firewall issue | Check connectivity to `dms.{region}.aliyuncs.com:443` |
| `CheckDatabasePermissionFailed` | RAM permission missing | Grant `AliyunDMSFullAccess` to the RAM user |
| Session stays `running` with 0 conclusions | SSE events not parsed | Check `~/.data-agent/sessions/{id}/status.json` for details |
| Workspace/Agent API returns 404 | Wrong API action name | Ensure server binary is up-to-date (`make build-all`) |
