# Subcommand Reference

This document contains detailed descriptions and complete parameter lists for all subcommands.

---

## ls Subcommand — Discover Data Resources

Before initiating analysis, use `ls` to understand what databases and tables are available.

### Environment Resolution

CLI commands load `.env` and process environment variables through `DataAgentConfig.from_env()`.

| Variable | Description |
|------|------|
| `DATA_AGENT_REGION` | RegionId for Data Agent and DMS endpoints, default `cn-hangzhou` |
| `DATA_AGENT_DMS_UNIT` | Optional explicit DMSUnit override for Data Agent session APIs (alias `DATA_AGENT_DMSUNIT`); `ListTagMetaAsset` discovery tags still use `DATA_AGENT_REGION` |
| `DATA_AGENT_WORKSPACE_ID` | Optional default workspace ID when `--workspace-id` is omitted |
| `DATA_AGENT_CUSTOM_AGENT_ID` | Optional default custom agent ID when `--custom-agent-id` is omitted; when set, `db` data-source parameters are not required |
| `DATA_AGENT_ENDPOINT` | Explicit gateway override for Data Agent control-plane APIs, defaults derived from region |
| `DATA_AGENT_STREAM_ENDPOINT` | Explicit override for the API_KEY data-plane (stream) gateway, default `dataagent-stream-{region}.aliyuncs.com/apikey`; set alongside `DATA_AGENT_ENDPOINT` when targeting non-public gateways (ignored under AK/SK auth) |
| `DATA_AGENT_DMS_ENTERPRISE_ENDPOINT` | Optional dms-enterprise host for the discovery APIs (`ls`, `dms`, `import`, route lookup), default `dms-enterprise.{region}.aliyuncs.com`; use a VPC endpoint when public egress is unavailable |
| `DATA_AGENT_CREDENTIALS_FILE` | Path to a plain credentials file (default `~/.data-agent-credentials.json`); see [CREDENTIALS.md](CREDENTIALS.md) |
| `DATA_AGENT_CREDENTIALS_ENV` / `DATA_AGENT_CREDENTIALS_PROFILE` | Credentials-file environment / profile selectors (default `default`) |
| `DATA_AGENT_DEBUG_API` | Verbose API request/response logging with credential values redacted |

> **⚠️ Important**: The `ls` command only shows databases that have been **imported into Data Agent Data Center**. If you can't find the database you need, it means the database has not been imported from DMS yet.

### List All Databases

```bash
python3 scripts/data_agent_cli.py ls
```

Output is divided into two groups:
- **Database Connections** (ImportType: RDS/DMS) — Real relational databases, can be used with `db` subcommand
- **File Data Sources** (ImportType: FILE) — Uploaded file datasets

Output displays databases in compact format:
```
chinook [mysql]  dbId=abc123  instanceResourceId=rm-xxx  catalogName=chinook
employees [mysql]  dbId=def456  instanceResourceId=rm-yyy  catalogName=employees
```

> **Tip**: `internal_data_employees` is DataAgent's built-in demo database, containing employee, department, and salary test data, suitable for first-time experience.

### Filter by Keyword

```bash
python3 scripts/data_agent_cli.py ls --search internal_data_employees
```

### List Tables for a Specific Database + Generate Usable Command

```bash
python3 scripts/data_agent_cli.py ls --db-id <AgentDbId>
```

Output includes a `db` command template ready to copy and use.

---

## db Subcommand — Database Analysis

> **⚠️ Important**: The `db` subcommand requires the database to **already exist in Data Agent Data Center**.

Starts a database analysis session and streams the results. The session ID is printed immediately — use `status` to check progress and `attach` to continue the conversation. In async mode (default) the analysis runs in a background worker; progress is written to `sessions/<session_id>/progress.log` and `status.txt`.

### Data Source Parameters

Required unless `--custom-agent-id` (or `DATA_AGENT_CUSTOM_AGENT_ID`) is set.

| Parameter | Description |
|------|------|
| `--dms-instance-id` | DMS Instance ID (numeric) |
| `--dms-db-id` | DMS Database ID (numeric) |
| `--instance-name` | RDS Instance Name |
| `--db-name` | Database Name |
| `--tables` | Table name list, comma-separated |
| `--engine` | Database engine, default `mysql` |

### Session Parameters

| Parameter | Description |
|------|------|
| `--session-mode` | `auto` (default) / `lite` / `pro` / `ultra` |
| `--plan-mode` | `force` (default, always generate execution plan) / `disable` (skip planning) |
| `--output` | `summary` (default) / `detail` / `raw` |
| `--enable-search` | Enable search capability (default `false`) |
| `--workspace-id` | Workspace ID to bind the session to a specific workspace |
| `--custom-agent-id` | Custom Agent ID to use for the analysis session; when set (flag or `DATA_AGENT_CUSTOM_AGENT_ID`), the data source parameters above are not required |

### Query Methods

| Parameter | Description |
|------|------|
| `-q` / `--query` | Single query (runs preset query if not specified) |

---

## file Subcommand — File Analysis

Uploads a local file (or takes an existing Data Center file via `--file-id`) and analyzes it in `auto` mode. The session ID is printed immediately — use `status` to check progress and `attach` to continue. In async mode (default) the analysis runs in a background worker; progress is written to `sessions/<session_id>/progress.log`.

### Parameters

| Parameter | Description |
|------|------|
| `FILE` / `--file-id` | **Required (mutually exclusive)**, local file path to upload (`.csv` / `.xlsx` / `.xls`) or an existing Data Center file ID |
| `--session-mode` | `auto` (default) / `lite` / `pro` / `ultra` |
| `--plan-mode` | `force` (default, always generate execution plan) / `disable` (skip planning) |
| `--output` | `summary` (default) / `detail` / `raw` |
| `--enable-search` | Enable search capability (default `false`) |
| `--workspace-id` | Workspace ID to bind the session to a specific workspace |
| `--custom-agent-id` | Custom Agent ID to use for the analysis session |
| `-q` / `--query` | Custom query question (runs preset analysis questions if not specified) |
| `--list-generated-files` | List agent-generated reports/charts after analysis |

---

## reports Subcommand — Get Generated Results

View and download reports and chart files generated by the session.

### Parameters

| Parameter | Description |
|------|------|
| `--session-id` | **Required**, Session ID |

---

## Session Mode Description

| Mode | Features | Duration | Report | Use Cases |
|------|------|------|------|----------|
| `auto` | Backend intelligent decision — automatically selects the best mode based on query complexity | varies | auto | Default choice, mixed workloads |
| `lite` | Quick query mode, sub-second response, quick Q&A and simple SQL queries | 30 sec - 2 min | ❌ | Simple queries, data validation |
| `pro` | Deep analysis with planning, multi-step reasoning, and report generation | 5-15 min | ✅ | Topic analysis, business reports |
| `ultra` | Most thorough analysis with multi-dimensional insights; same plan-confirmation flow as pro | 20-40 min | ✅ | Strategic analysis, trend research |

> **⚠️ Important**: When you need to generate reports (HTML/Markdown/charts), **pro mode is recommended**.

## dms Subcommand — DMS Tool Integration

Direct access to DMS metadata, used for discovering instances, databases, and tables.

### Tool List

| Tool | Description |
|------|------|
| `list-instances` | Search database instance list in DMS |
| `search-database` | Search databases by schema name |
| `list-tables` | List tables in specified database |

## workspace Subcommand — List Workspaces

List Data Agent collaboration workspaces under the current account.

### Parameters

| Parameter | Description |
|------|------|
| `--workspace-type` | Workspace type: `MY` (default) / `ALL` |
| `--search` / `-s` | Filter workspaces by name |
| `--page-number` | Page number (default: 1) |
| `--page-size` | Page size (default: 50) |

### Examples

```bash
# List my workspaces
python3 scripts/data_agent_cli.py workspace

# List all accessible workspaces
python3 scripts/data_agent_cli.py workspace --workspace-type ALL

# Search by name
python3 scripts/data_agent_cli.py workspace --search myworkspace
```

### `agent` — List and Describe Custom Agents

List available custom agents (default: RELEASED status only) or view agent details.

**Usage**:
```bash
data-agent agent                           # List RELEASED custom agents
data-agent agent list --search keyword     # Search agents by keyword
data-agent agent describe --custom-agent-id <ID>  # View agent details
```

**Parameters**:

| Parameter | Description | Required |
|-----------|-------------|----------|
| `action` | `list` (default) or `describe` | No |
| `--workspace-id` | Filter agents by workspace ID | No |
| `--custom-agent-id` | Custom Agent ID (required for describe) | Conditional |
| `--search` | Search keyword to filter agents | No |
| `--page-number` | Page number (default: 1) | No |
| `--page-size` | Page size (default: 20) | No |

---

## import Subcommand — Import DMS Database

Import DMS database tables into Data Agent Data Center.

### Parameters

| Parameter | Description |
|------|------|
| `--dms-instance-id` | **Required**, DMS Instance ID (numeric) |
| `--dms-db-id` | **Required**, DMS Database ID (numeric) |
| `--instance-name` | **Required**, RDS Instance Name |
| `--db-name` | **Required**, Database Name |
| `--tables` | **Required**, Table name list to import (comma-separated) |
| `--engine` | Database engine type (default: mysql) |
| `--region` | Region ID (default: cn-hangzhou) |
| `--yes`, `-y` | Confirm the import operation without an interactive prompt |

---

## send Subcommand — Single Message

Send a single message, printing the response. Without `--session-id` a new session is created and its ID printed for follow-up `attach`; with `--session-id` the message reuses that session.

> **Data sources are session-level**: pass them once with the message that starts the session (`--data-source` for Data Center files, `--dms-*` for a database, or the `file` upload). Follow-up messages — including `attach` — inherit the session's data scope; there is no need to pass them again.

### Parameters

| Parameter | Description |
|------|------|
| `-m` / `--message` | **Required**, message content |
| `-s` / `--session-id` | Reuse an existing session instead of creating one |
| `-a` / `--agent-id` | Agent ID (with `--session-id`; auto-resolved when omitted) |
| `--workspace-id` | Workspace ID for newly created sessions (or workspace-bound reuse) |
| `--session-mode` | `auto` / `lite` / `pro` / `ultra` for the created session (default: backend intelligent decision) |
| `--custom-agent-id` | Custom Agent ID bound to the session created by this message |
| `--custom-agent-stage` | `prod` (default) / `debug` |
| `--message-type` | `primary` (default) / `report` / `diag` |
| `--report-theme` / `--report-prompt` | Report options (with `--message-type report`) |
| `--data-source` | Data Center file ID to attach to the session (repeatable) |
| `--dms-db-id` | **Required** when any database data source parameter is passed; the backend resolves table schemas and executes SQL by database ID |
| `--dms-instance-id` / `--db-name` / `--tables` | Database data source attached to the session |
| `--permission-file` / `--permission-default-action` | SQL permission control (see [PERMISSIONS.md](PERMISSIONS.md)) |
| `--poll` / `--timeout` | Wait after sending until the analysis completes (timeout in seconds, default 300) |
| `--endpoint` | Explicit gateway endpoint override (control plane) |
| `--stream-endpoint` | Explicit API_KEY data-plane (stream) endpoint override; ignored under AK/SK auth |

### Examples

```bash
# Connectivity check (creates a session; its ID is printed)
python3 scripts/data_agent_cli.py send -m "ping"

# Custom agent session, waiting until the analysis completes
python3 scripts/data_agent_cli.py send --workspace-id <WS_ID> --custom-agent-id <AGENT_ID> --session-mode pro -m "Generate a report" --poll

# Database data source with permissions
python3 scripts/data_agent_cli.py send --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> --db-name <DB> --tables "t1,t2" \
    --permission-file permission.json --permission-default-action deny -m "Analyze orders"
```

> **Note**: `send` does not support session titles. If a title is genuinely required, use `legacy-send`; otherwise the session gets a server-side default title.

---

## legacy-send Subcommand — Pre-created Send (title)

Variant of `send` that can set a session `--title`. Prefer plain `send` unless a title is really required — the server generates a usable default title.

### Parameters

Identical to `send` (see above), plus:

| Parameter | Description |
|------|------|
| `--title` | Title for a newly created session (default: data-agent-session) |

---

## attach Subcommand — Session Reuse

Connect to an existing session to continue conversation, confirm plans, or check progress.

### Parameters

| Parameter | Description |
|------|------|
| `--session-id` | **Required**, Session ID to connect to |
| `-q` / `--query` | Send single query |
| `--from-start` | Replay session history from beginning (equivalent to --checkpoint 0) |
| `--checkpoint` | Specify exact checkpoint to resume from (e.g., `--checkpoint 219`), used for precise recovery after network interruption |
| `--output` | `summary` (default) / `detail` / `raw` |
| `--workspace-id` | Workspace ID override, for sessions bound to a specific workspace |

---

## status Subcommand — Session Status Query

Read-only query of a session's current state — cheap to run anytime, no streaming involved. The server status is authoritative; if the session ran as a local async task, its `sessions/<id>/status.txt` is also shown. For WAIT_INPUT sessions it prints the `attach` commands to confirm the pending plan / SQL. Use it to check any session from anywhere; `send --poll` is the flag for waiting on a message just sent.

### Parameters

| Parameter | Description |
|------|------|
| `--session-id` | **Required**, Session ID to query |
| `--agent-id` / `-a` | Agent ID (optional; the backend resolves it from the session) |
| `--workspace-id` | Workspace ID if the session is workspace-bound |

### Status Values

`CREATING` preparing · `RUNNING` executing · `IDLE` ready for a new message (also shown for reclaimed sessions, e.g. `IDLE (server: RECOVERABLE)`) · `WAIT_INPUT` waiting for plan/SQL confirmation · `STOPPED` / `FAILED` session has ended.

### Example

```bash
python3 scripts/data_agent_cli.py status --session-id <SESSION_ID>
```

---

## runtime release — Refresh Runtime Resources (Destructive)

Release only the runtime instance belonging to the currently authenticated sub-account. This manages runtime resources, not custom-agent definitions. **Running sessions become unavailable.** Use cautiously and obtain explicit approval for the disruption; do not use as an automatic recovery step or after every custom-agent update.

```bash
# Show the warning without sending any request (exit code 2).
python3 scripts/data_agent_cli.py runtime release

# Only after checking the current credentials/region and approving the impact:
python3 scripts/data_agent_cli.py runtime release --yes
```

| Parameter | Description |
|------|------|
| `--yes` / `-y` | Required to send the release request; confirms the disruption to running sessions |
| `--endpoint` | Optional control-plane gateway override; also settable via `DATA_AGENT_ENDPOINT` |

No instance, session, workspace or custom-agent ID is accepted. Credentials and region follow the normal environment configuration (`DATA_AGENT_REGION`, default `cn-hangzhou`); verify the selected credentials-file environment/profile when using multiple accounts.

After release, start a **new** analysis with `db`, `file` or `send` to provision the latest runtime resources. This command does not provision a replacement, verify readiness or resume interrupted sessions. Read-only `status` and progress-only `attach` do not trigger provisioning. A failed or ambiguous release is reported as an error and is not automatically retried.
