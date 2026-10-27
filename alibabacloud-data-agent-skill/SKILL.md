---
name: alibabacloud-data-agent-skill
description: |
  Invoke Alibaba Cloud Apsara Data Agent for Analytics via CLI to perform natural language-driven data analysis on enterprise databases.
  Data Agent for Analytics is an intelligent data analysis agent developed by Alibaba Cloud Database team for enterprise users. It automatically completes requirement analysis, data understanding, analysis insights, and report generation based on natural language descriptions.
  This tool supports: discovering data resources (instances/databases/tables) managed in DMS, initiating query or deep analysis sessions, real-time progress tracking, and retrieving analysis conclusions and generated reports.
  Use this Skill when users need to query databases, analyze data trends, generate data reports, ask questions in natural language, or mention "Data Agent", "data analysis", "database query", "SQL analysis", "data insights".
compatibility: |
  Requires Python 3.10+ (macOS system Python is typically too old — use brew or pyenv to install);
  Requires valid Alibaba Cloud credentials (default credential chain or API_KEY);
  Requires dependencies in requirements.txt to be installed inside a venv;
  Data sources must be managed in Alibaba Cloud Apsara Database or DMS.
domain: AIOps
---
metadata:
  author: DataAgent Team
  version: "2.7.2"
---

# Changelog

One line per version — the detail lives in [CHANGELOG.md](references/CHANGELOG.md).

- **Unreleased**: `runtime release --yes` explicitly releases the caller's runtime for a resource refresh; running sessions become unavailable.
- **v2.7.2**: API_KEY data-plane (stream gateway) endpoint override — `--stream-endpoint` / `DATA_AGENT_STREAM_ENDPOINT`.
- **v2.7.1**: Regression-matrix hardening: six fixes — analysis failures no longer fake success, `attach -q` keeps monitoring after a confirmation, API_KEY business failures render their real code/message, credentials-file AK works for every consumer, API_KEY endpoint routing fixed, API_KEY SSE wire repaired.
- **v2.7.0**: `db` / `file` create the session implicitly via the first SendChatMessage on a shared send path; data sources unify on `DataSources`; `send --session-mode`; new read-only `status` subcommand; session-status normalization.
- **v2.6.1**: `DataSources` database entries always carry `DmsDatabaseId` (`--dms-db-id` required, fail-fast); `--dms-db-id` flag on `send` / `legacy-send`.
- **v2.6.0**: `send` binds custom agents directly via `SessionConfig` under AK/SK; `--title` moved to `legacy-send`.
- **v2.5.0**: Sync with the maintained master — permissions, credentials-file auth, endpoint overrides, custom agents, `cancel` / `upload`, SDK contract update.

---

# Installation

## Python Environment (venv) — MUST READ

> **🚨 Hard Requirement: Python ≥ 3.10**
>
> The macOS system `/usr/bin/python3` is typically 3.8 or 3.9 and **cannot run this project** (it relies on `match/case`, `TypeAlias`, `|` union syntax, and other 3.10+ features).
>
> Verify your version first: `python3 --version`. If below 3.10, install via Homebrew or pyenv:
> ```bash
> # Homebrew
> brew install python@3.12
> # Or pyenv
> pyenv install 3.12.4 && pyenv local 3.12.4
> ```

> **⚠️ You MUST use a venv virtual environment. Never install dependencies globally.** Running `pip install` against the system Python pollutes the environment and may fail due to permission issues.

### Use Existing venv (Recommended)

Work from the skill directory (`alibabacloud-data-agent-skill/`). If a pre-built `venv/` exists (all dependencies pre-installed), use it whenever possible:

```bash
cd alibabacloud-data-agent-skill

# Option A (recommended): activate the venv
source venv/bin/activate
python3 scripts/data_agent_cli.py ls

# Option B: invoke the venv interpreter directly (no activation needed)
venv/bin/python3 scripts/data_agent_cli.py ls
```

### Rebuild venv

If `venv/` is missing or dependencies are corrupted, recreate with a **3.10+** Python:

```bash
python3.12 -m venv venv          # explicitly use a 3.10+ interpreter
source venv/bin/activate
pip install -r scripts/requirements.txt
```

> **Tip**: All examples in this document write `python3 scripts/data_agent_cli.py ...`. When venv is activated, `python3` resolves to the venv interpreter automatically; otherwise prefix with `venv/bin/python3`.

## Configure Credentials

This Skill supports three authentication modes: Alibaba Cloud default credential chain (recommended), API_KEY, or a plain credentials file.

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `DATA_AGENT_API_KEY` | API Key authentication (file analysis scenarios) | — |
| `DATA_AGENT_CREDENTIALS_FILE` | Plain credentials file path (multi-account) | `~/.data-agent-credentials.json` |
| `DATA_AGENT_CREDENTIALS_ENV` | Credentials-file environment selector | `default` |
| `DATA_AGENT_CREDENTIALS_PROFILE` | Credentials-file profile selector | `default` |
| `DATA_AGENT_REGION` | Region ID | `cn-hangzhou` |
| `DATA_AGENT_DMS_UNIT` | DMSUnit override (alias `DATA_AGENT_DMSUNIT`) | auto-resolved |
| `DATA_AGENT_WORKSPACE_ID` | Default workspace ID | auto personal workspace |
| `DATA_AGENT_CUSTOM_AGENT_ID` | Default custom agent ID | — |
| `DATA_AGENT_ENDPOINT` | Custom endpoint (control plane) | auto-generated |
| `DATA_AGENT_STREAM_ENDPOINT` | API_KEY data-plane (stream) endpoint override | `dataagent-stream-{region}.aliyuncs.com/apikey` |
| `DATA_AGENT_DMS_ENTERPRISE_ENDPOINT` | dms-enterprise host for discovery APIs (not covered by `DATA_AGENT_ENDPOINT`); set for VPC-only egress | `dms-enterprise.{region}.aliyuncs.com` |
| `DATA_AGENT_TIMEOUT` | Request timeout (seconds) | `300` |
| `DATA_AGENT_MAX_RETRY` | Max retry attempts | `3` |
| `DATA_AGENT_POLL_INTERVAL` | Poll interval (seconds) | `2` |
| `DATA_AGENT_MAX_POLL_COUNT` | Max poll attempts | `60` |
| `DATA_AGENT_DEBUG_API` | Print API request details | `false` |

All variables can be set in a `.env` file or exported as environment variables. CLI flags (`--workspace-id`, `--custom-agent-id`) override the corresponding environment variable.

### Option 1: Default Credential Chain (Recommended)

The Skill uses Alibaba Cloud SDK's default credential chain to automatically obtain credentials, supporting environment variables, configuration files, instance roles, etc.

See [Alibaba Cloud Credential Chain Documentation](https://help.aliyun.com/document_detail/378659.html)

### Option 2: API_KEY Authentication (File Analysis Only)

```bash
export DATA_AGENT_API_KEY=your-api-key
export DATA_AGENT_REGION=cn-hangzhou
```

Get API_KEY: [Data Agent Console](https://agent.dms.aliyun.com/cn-hangzhou/api-key)

### Option 3: Credentials File (Multiple Accounts / Environments)

```bash
export DATA_AGENT_CREDENTIALS_FILE=/path/to/creds.json
export DATA_AGENT_CREDENTIALS_ENV=pre          # optional environment selector
export DATA_AGENT_CREDENTIALS_PROFILE=main     # optional profile selector
```

The file groups AK/SK pairs by environment and nickname; when both selectors are left unset and the file is unambiguous, the single candidate is auto-selected. Entries may also carry an `api_key` field, which takes precedence for that profile. See [CREDENTIALS.md](references/CREDENTIALS.md) for the full format and resolution rules.

### Permission Requirements

RAM users need `AliyunDMSFullAccess` or `AliyunDMSDataAgentFullAccess` permissions.
See [RAM-POLICIES.md](references/RAM-POLICIES.md) for detailed permission information.

## Debug Mode

```bash
DATA_AGENT_DEBUG_API=1 python3 scripts/data_agent_cli.py file example.csv -q "analyze"
```

## 💡 Getting Started Tips

- Use the built-in demo database `internal_data_employees` (DataAgent's built-in test database containing employee, department, and salary data) for first-time experience
- Or use local file `assets/example_game_data.csv` for file analysis experience


# Data Agent CLI — Unified Command-Line Data Analysis Tool

## Overview

`scripts/data_agent_cli.py` helps users complete the full workflow from **discover data → initiate analysis → track progress → get results**.

### Command Overview

```bash
python3 scripts/data_agent_cli.py <command> [options]
```

| Command | Purpose |
|---------|---------|
| `ls` | List Data Center databases / tables with the IDs the `db` command needs |
| `dms` | DMS instance / database / table lookup (`list-instances`, `search-database`, `list-tables`) |
| `import` | Import DMS database tables into Data Agent Data Center |
| `db` | Start a database analysis session (auto / lite / pro / ultra); prints the session ID for `status` / `attach` follow-ups |
| `file` | Upload a local file (or use `--file-id`) and analyze it; prints the session ID for `status` / `attach` follow-ups |
| `upload` | Upload a local file and print its file ID (no session created) |
| `send` | Send a single message — create a session (ID printed for follow-ups) or reuse one with `--session-id`; supports `--session-mode`, report requests and permission-controlled queries |
| `legacy-send` | Variant of `send` that can set `--title`; avoid unless a session title is really needed |
| `attach` | Re-enter an existing session: follow-ups, plan confirmation, live progress, checkpoint resume |
| `status` | Read-only session status query — status / title / mode; WAIT_INPUT sessions get the attach confirmation guidance |
| `reports` | List and download session-generated reports / charts |
| `cancel` | Cancel a running session |
| `runtime release` | Release only the authenticated caller's runtime; requires `--yes`, makes running sessions unavailable |
| `workspace` | List available workspaces |
| `agent` | List / describe / create / update / publish custom agents |

Common options: `--region` (default `DATA_AGENT_REGION` or `cn-hangzhou`), `--endpoint` (explicit gateway override, defaults derived from region), `--stream-endpoint` (API_KEY stream-gateway override, ignored under AK/SK), `--workspace-id`, `--custom-agent-id`, `--session-mode`, `--plan-mode`, `--output summary|detail|raw`, `--async-run`.

### Core Concepts

> **⚠️ Key Prerequisite**: Data Agent can only analyze databases that have been **imported into Data Agent Data Center**.
>
> - **Data Center**: Data Agent's data center, only databases here can be analyzed
> - **DMS**: Alibaba Cloud Data Management Service, stores metadata of all databases
> - **Relationship**: Databases registered in DMS ≠ Databases in Data Center
>
> **Usage Flow**:
> 1. First use `ls` to check if the target database exists in Data Center
> 2. If **not found**, use `dms` subcommand to search for database info, then use `import` subcommand to import it
> 3. After successful import, you can use `db` subcommand for analysis

> **Data source semantics**: data sources are **session-level** — pass them once with the command that starts the session (database via `--dms-*`, Data Center files via `--data-source` or the `file` upload). Follow-up messages — including `attach` — inherit the session's data scope; passing them again is unnecessary (repeated passes are merged server-side).

---

## Session Modes

- **auto** (default): Backend intelligent decision — automatically selects the best mode based on query complexity
- **lite**: Quick query mode, sub-second response, suitable for quick Q&A and simple SQL queries
- **pro**: Deep analysis, takes 5-40 minutes, requires spawning a sub-agent for async execution or using --async-run parameter
- **ultra**: Most thorough analysis with multi-dimensional insights, follows the same plan-confirmation flow as pro

### End-to-End Regression Reference (v1.8.4 verified)

Both lite and pro modes are regression-tested against `chinook` database with the async + attach flow:

| Mode | Kickoff | Observed Chain | Typical Duration |
|------|---------|----------------|------------------|
| lite | `db --session-mode lite -q "..."` | async worker → live SSE → `result.json={"status":"completed"}` | ~15s |
| pro | `db --session-mode pro -q "..."` | async worker → **Plan** → `WAIT_INPUT` → `attach -q "confirm"` → step-by-step execution → Excel/Chart artifacts → text report → **2nd WAIT_INPUT** (webpage render) | 2-10 min (text); +10 min if rendering webpage |

Key checkpoints to look for in `sessions/<SESSION_ID>/progress.log`:

- `> User Query: ...` — request received
- `### Execution Plan (ID: ...)` — pro/ultra plan generated, use `attach -q "confirm"` to proceed
- `> ⚠️  Plan confirmed, continuing analysis...` — plan approved, execution starts
- `## Step N/M: ...` — per-step progress with artifacts links
- `### Report Render` + `⚠️  Please review the report rendering request.` — optional HTML report render confirmation

> See [ANALYSIS_MODE.md](references/ANALYSIS_MODE.md) for pro/ultra mode details

---

## Workspace (Collaborative Space)

Workspaces are collaborative spaces that enable team-based data analysis with shared sessions, data sources, and access control.

- **List workspaces**: Use `workspace` subcommand to discover available workspaces (personal or shared)
- **Bind session to workspace**: Pass `--workspace-id <ID>` when using `db` or `file` to create a session within a specific workspace context
- **Workspace types**: `MY` (default, personal spaces), `ALL` (all accessible spaces including shared ones)

> **Note**: When a session is created within a workspace, all subsequent API calls (describe, send message, etc.) automatically carry the workspace context.

### Workspace Resolution

The workspace ID is resolved automatically in this order:
1. CLI flag `--workspace-id <id>`
2. Environment variable `DATA_AGENT_WORKSPACE_ID`
3. Auto-create personal workspace via `InitDataAgentPersonalWorkspace`

Both AK/SK and API_KEY authentication modes support this resolution chain.

### DMSUnit Resolution

The DMSUnit is resolved automatically in this order:
1. Environment variable `DATA_AGENT_DMS_UNIT`
2. Dynamic route lookup via `GetActiveRouteUnit`
3. Region fallback from `DATA_AGENT_REGION`

Set `DATA_AGENT_DMS_UNIT` only when the tenant must use a specific DMSUnit. Leave it unset for the automatic route lookup.

`ListTagMetaAsset` data discovery remains region-scoped and uses `DATA_AGENT_REGION` in tags such as `sys::DMS-DA::<region>::space:<workspace>`.

---

## Custom Agent

Custom Agents are user-defined AI agents with specialized instructions, knowledge bases, and data scope configurations.

- **List custom agents**: Use `agent` subcommand to discover available custom agents (RELEASED status by default)
- **View agent details**: Use `agent describe --custom-agent-id <ID>` to see full agent configuration
- **Create an agent**: Use `agent create --workspace-id <WS> --agent-name <NAME> [--description ...] [--instruction text|@path] [--dsl-file report_dsl.json]` — created agents are auto-released for immediate use
- **Update an agent**: Use `agent update --custom-agent-id <ID> [--agent-name ...] [--instruction ...]`
- **Publish an agent**: Use `agent publish --custom-agent-id <ID> --workspace-id <WS>` to promote a draft to RELEASED
- **Bind session to custom agent**: Pass `--custom-agent-id <ID>` when using `db`, `file` or `send` to create a session powered by a specific custom agent (or set `DATA_AGENT_CUSTOM_AGENT_ID` as the default)

> **Note**: When `--custom-agent-id` is specified, data source parameters (`--dms-instance-id`, `--dms-db-id`, `--db-name`, `--tables`) are **not required**. The custom agent already carries its own data scope; if you pass extra data sources anyway, the backend merges them into the analysis scope (and rejects out-of-scope ones when the agent forbids appending), so the best practice is to **not pass them** — only `--workspace-id` and session options (e.g. `--session-mode`, `--plan-mode`) are needed. When you do pass a database data source (with or without a custom agent), `--dms-db-id` is mandatory.

> **Note**: Custom Agent sessions automatically use the `prod` stage. The custom agent's instructions, knowledge, and data scope will be applied to the analysis session.

> **Note**: Custom-agent binding works with plain `send` (`--custom-agent-id`). Use `legacy-send` only when a session `--title` is really required; prefer the server-side default title.

---

## Report Generation Messages

Report controls are accepted by `send`, `db`, `file` and `attach`:

```bash
python3 scripts/data_agent_cli.py send --data-source <FILE_ID> -m "Analyze store sales"
python3 scripts/data_agent_cli.py send -s <SESSION_ID> \
    --message-type report --report-theme default \
    --report-prompt @report_instructions.txt \
    -m "Generate a web report on sales trends"
```

- `--message-type primary|report|diag` — `report` attaches a `TaskConfig.ReportConfig` (WebReport) to the message; `diag` sends a report-diagnosis request where the message text is the complaint about the session's report (backend diagnosis support required).
- `--report-theme <name>` — report visual theme (default `default`).
- `--report-prompt <text|@path>` — extra prompt injected into the report guide; `@path` reads the whole file.

---

## Data Permissions (SQL Access Control)

`send` can restrict the agent's SQL access on a database data source:

```bash
python3 scripts/data_agent_cli.py send --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> \
    --db-name <DB> --tables "orders,customers" \
    --permission-file permission.json --permission-default-action deny \
    -m "Analyze last month's orders" --poll
```

- `--permission-file <PATH>` — JSON with per-table column whitelists/blacklists and required row filters, attached to the database data source.
- `--permission-default-action allow|deny` — session-level default for tables not declared in the permission file.

Full schema, field semantics (empty whitelist = deny-all, whitelist−blacklist, last-wins, OR row-filter lists) and SDK examples: [PERMISSIONS.md](references/PERMISSIONS.md).

---

## Cancel a Running Session

```bash
python3 scripts/data_agent_cli.py cancel --session-id <SESSION_ID>
```

Cancels a running analysis session (agent ID is resolved automatically). Use it to clean up test sessions or stop long-running tasks that are no longer needed.

---

## Refresh Runtime Resources (Destructive)

`runtime release` releases only the runtime instance belonging to the currently authenticated sub-account, not a custom-agent definition. **Running sessions become unavailable.** Use it cautiously when a runtime refresh is necessary to pick up the latest resources.

**Never invoke it as an automatic troubleshooting step or as part of a routine custom-agent update. Explain the disruption and obtain explicit user approval before passing `--yes`.** Check the active credentials and region first; there is no target instance or session ID to narrow the impact.

```bash
python3 scripts/data_agent_cli.py runtime release --yes
```

Without `--yes`, no request is sent. After release, start a **new** analysis with `db`, `file` or `send` to provision the latest runtime resources. Release alone does not restart the runtime, verify readiness or resume interrupted sessions; read-only progress monitoring does not provision a replacement.

---

## Session Reuse via `attach` (⭐ Core Mechanism)

> **Best Practice**: `attach` is the **recommended way** to interact with an ongoing or previously created session. Always prefer `attach` over creating a new session for any follow-up interaction on the same data scope.

### Why Use `attach`

After you call `db` / `file` to start a session, **all subsequent interactions on that session MUST go through `attach --session-id <ID>`**. A single session = a single conversation context on the server side, and `attach` is the only way to safely re-enter it.

| Capability | Command | Scenario |
|------------|---------|----------|
| **Follow-up questions** | `attach --session-id <ID> -q "..."` | Continue the conversation with full context, skip data-understanding overhead |
| **Plan confirmation** | `attach --session-id <ID> -q "confirm"` | Approve the execution plan generated by pro/ultra mode |
| **Plan modification** | `attach --session-id <ID> -q "simplify to 3 steps"` | Refine the plan before execution |
| **Progress monitoring** | `attach --session-id <ID>` (no `-q`) | Tail live SSE progress of a long-running session |
| **Resume after network drop** | `attach --session-id <ID> --checkpoint <N>` | Precise recovery from the Nth event after interruption |
| **Replay full history** | `attach --session-id <ID> --from-start` | Re-stream the entire session from event 0 |
| **Workspace-scoped resume** | `attach --session-id <ID> --workspace-id <WS>` | Re-enter a session bound to a specific workspace |

### Golden Workflow (Async + attach)

The canonical pattern for long-running analyses is **async `db` kickoff → `attach` for everything else**:

```bash
# 1) Kick off async analysis, returns SESSION_ID immediately
python3 scripts/data_agent_cli.py db \
    --dms-instance-id <DMS_INSTANCE_ID> \
    --dms-db-id <dbId> \
    --db-name <schemaName> \
    --tables "employees,departments" \
    --workspace-id <workspace_id> \
    --session-mode pro \
    -q "Analyze salary distribution"
# -> ✅ Async task started. Session ID: abc123xyz

# 2) Watch progress live (Ctrl-C is safe, server keeps running)
python3 scripts/data_agent_cli.py attach --session-id abc123xyz

# 3) Confirm or modify the plan when agent enters WAIT_INPUT
python3 scripts/data_agent_cli.py attach --session-id abc123xyz -q "confirm"

# 4) Ask follow-up questions (reuses context, no re-import, no re-profiling)
python3 scripts/data_agent_cli.py attach --session-id abc123xyz -q "Break down by job level"

# 5) Recover precisely if the stream was cut at event #219
python3 scripts/data_agent_cli.py attach --session-id abc123xyz --checkpoint 219

# 6) Fetch generated reports / charts
python3 scripts/data_agent_cli.py reports --session-id abc123xyz
```

### Benefits of Reusing a Session via `attach`

- **Context preservation** — previous SQL, table profiling, and user intent are kept, answers stay consistent.
- **Cost reduction** — skip re-discovering schema / re-profiling tables on every question.
- **Plan governance** — pro / ultra plans require explicit confirmation; only `attach -q "confirm"` can unblock them.
- **Resilience** — `--checkpoint` / `--from-start` make long-running tasks robust against network drops and client restarts.
- **Team collaboration** — share the Session ID, teammates can `attach` to the same session to review progress and results.

### Rules of Thumb

1. Create session **once** with `db` / `file`; drive everything else with `attach`.
2. Record the `Session ID` printed after kickoff — it is the only handle to the session.
3. For pro / ultra mode, always use `attach` (not a new `db`) to confirm plans; creating a new session loses the plan.
4. Session artifacts (progress log, checkpoint, result, images) are persisted under `sessions/<SESSION_ID>/`.

> See [COMMANDS.md](references/COMMANDS.md) for the full `attach` parameter list and [WORKFLOWS.md](references/WORKFLOWS.md) for end-to-end scenarios.

---

## Quick Start

```bash
# 1. List available databases
python3 scripts/data_agent_cli.py ls
# Example output:
#   chinook [mysql]  dbId=abc123  instanceResourceId=rm-xxx  catalogName=chinook
#   employees [mysql]  dbId=def456  instanceResourceId=rm-yyy  catalogName=employees

# 2. Create a session for initial analysis (record the returned Session ID!)
python3 scripts/data_agent_cli.py db \
    --dms-instance-id <DMS_INSTANCE_ID> \
    --dms-db-id <dbId> \
    --db-name <schemaName> \
    --tables <table1,table2> \
    --workspace-id <workspace_id> \
    -q "Which department has the highest average salary"
# -> ✅ Async task started. Session ID: abc123xyz

# 3. ⭐ Reuse the session — follow-up questions, confirm plans, monitor progress
python3 scripts/data_agent_cli.py attach --session-id abc123xyz -q "Break down by month"
python3 scripts/data_agent_cli.py attach --session-id abc123xyz -q "confirm"     # approve pro/ultra plan
python3 scripts/data_agent_cli.py attach --session-id abc123xyz                  # tail live progress
python3 scripts/data_agent_cli.py attach --session-id abc123xyz --checkpoint 219 # resume after drop

# 4. List workspaces
python3 scripts/data_agent_cli.py workspace

# 5. Query in a specific workspace
python3 scripts/data_agent_cli.py db \
    --dms-instance-id <DMS_INSTANCE_ID> \
    --workspace-id <WORKSPACE_ID> \
    --dms-db-id <dbId> \
    --db-name <schemaName> \
    --tables <table1,table2> -q "Which department has the highest average salary"

# 5b. Quick connectivity check (creates a session; its ID is printed for follow-up attach)
python3 scripts/data_agent_cli.py send -m "ping"

# 6. Upload a file without starting a session (prints the file ID)
python3 scripts/data_agent_cli.py upload assets/example_game_data.csv

# 7. List available custom agents
python3 scripts/data_agent_cli.py agent

# 8. Use a custom agent for analysis (no DB params needed)
python3 scripts/data_agent_cli.py db --custom-agent-id <AGENT_ID> --workspace-id <WS_ID> -q "your question"

# 9. Cancel a running session when it is no longer needed
python3 scripts/data_agent_cli.py cancel --session-id <SESSION_ID>
```

> **Remember**: `db` / `file` create the session **once**; all follow-ups go through `attach --session-id <ID>`.

> 📖 See [WORKFLOWS.md](references/WORKFLOWS.md) and [COMMANDS.md](references/COMMANDS.md) for complete workflows, command reference, and best practices

---

## Project Structure

```
                          # Skill root directory
├── SKILL.md              # This document
├── scripts/              # Source code
│   ├── data_agent/       # SDK module (config / client / session / file_manager / sse_client)
│   ├── cli/              # CLI module (parser / cmd_* / streaming / worker_*)
│   ├── data_agent_cli.py # CLI entry point
│   └── requirements.txt  # Dependencies
├── assets/               # Demo / sample files (example_game_data.csv, LICENSE, HEARTBEAT.md)
├── sessions/             # Session data
├── references/           # Reference documents
└── .env.example          # Environment variable template
```
