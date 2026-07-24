# Alibaba Cloud Data Agent Skills

AI Agent Skills that let AI assistants (Claude Code, Qoder, Codex, and other agent runtimes) invoke **Alibaba Cloud Apsara Data Agent for Analytics** — an intelligent data analysis agent that turns natural language questions into SQL, insights, and full analysis reports on your enterprise databases and files.

Ask questions like *"Which department has the highest average salary?"* or *"Analyze the sales trend of the last quarter and generate a report"*, and the agent handles requirement analysis, data understanding, SQL execution, insight generation, and report rendering.

## Two Independent Skills

This repository contains **two standalone skills** for the same backend service. Choose the one that fits your agent runtime — they are independent and must not be mixed on the same session.

| | [alibabacloud-data-agent-skill](SKILL.md) (CLI, repo root) | [alibabacloud-data-agent-mcp-skill](alibabacloud-data-agent-mcp-skill/SKILL.md) (MCP Server) |
|---|---|---|
| Integration | Python CLI — the agent runs shell commands | Go MCP Server — the agent calls native `data_agent_*` MCP tools |
| Runtime requirement | Python 3.10+ with venv | Go 1.23+ (server auto-built from source on first launch) |
| Session monitoring | Async worker + `sessions/<ID>/` progress files + `attach` | Built-in Session Daemon with background SSE monitoring |
| Plan confirmation (deep analysis) | Manual via `attach -q "confirm"` | Automatic with `auto_confirm=true` |
| Chart images | Saved under `sessions/<ID>/images/` | Returned inline as MCP ImageContent (base64) |
| Best for | Any environment that can run Python; scripting; log tailing | MCP-capable runtimes (Claude Code, Qoder, hosted runtimes) |

## Feature Highlights

- **Data discovery** — list instances, databases, and tables managed in DMS and the Data Agent Data Center
- **Data import** — import DMS databases/tables into the Data Center for analysis
- **Natural language analysis** — quick Q&A (sub-second) or deep multi-step analysis with generated reports
- **File analysis** — upload and analyze local CSV / XLSX / JSON / TXT files
- **Session reuse** — multi-turn conversations with full context preservation
- **Workspaces & custom agents** — team collaboration spaces and user-defined specialized agents

## Prerequisites (Both Skills)

1. An Alibaba Cloud account with data sources managed in [DMS](https://dms.aliyun.com/) (or use the built-in demo database `internal_data_employees`)
2. Credentials via the Alibaba Cloud default credential chain (env vars `ALIBABA_CLOUD_ACCESS_KEY_ID` / `ALIBABA_CLOUD_ACCESS_KEY_SECRET`, `~/.aliyun/config.json`, or ECS instance role), or a `DATA_AGENT_API_KEY` from the [Data Agent Console](https://agent.dms.aliyun.com/cn-hangzhou/api-key)
3. RAM permission: `AliyunDMSFullAccess` or `AliyunDMSDataAgentFullAccess` (see [references/RAM-POLICIES.md](references/RAM-POLICIES.md))

## Quick Start — CLI Skill

```bash
# 1. Set up the Python environment (Python 3.10+ required)
python3 -m venv venv
source venv/bin/activate
pip install -r scripts/requirements.txt

# 2. Configure credentials
export ALIBABA_CLOUD_ACCESS_KEY_ID=your-ak
export ALIBABA_CLOUD_ACCESS_KEY_SECRET=your-sk
export DATA_AGENT_REGION=cn-hangzhou

# 3. Discover data and start analyzing
python3 scripts/data_agent_cli.py ls
python3 scripts/data_agent_cli.py db \
    --dms-instance-id <INSTANCE_ID> --dms-db-id <DB_ID> \
    --db-name <SCHEMA> --tables "t1,t2" \
    -q "Which department has the highest average salary?"
# -> ✅ Async task started. Session ID: abc123xyz

# 4. Follow up on the same session
python3 scripts/data_agent_cli.py attach --session-id abc123xyz -q "Break down by month"
```

Full documentation: [SKILL.md](SKILL.md) · [references/COMMANDS.md](references/COMMANDS.md) · [references/WORKFLOWS.md](references/WORKFLOWS.md) · [references/ANALYSIS_MODE.md](references/ANALYSIS_MODE.md)

## Quick Start — MCP Skill

```bash
# Requires Go 1.23+ (https://go.dev/dl/) — the launcher builds the server on first run.
```

Register the server in your MCP client (stdio mode shown; Streamable HTTP is also supported):

```json
{
  "mcpServers": {
    "data-agent": {
      "command": "bash",
      "args": ["/absolute/path/to/data-agent-skill/alibabacloud-data-agent-mcp-skill/scripts/select-binary.sh"],
      "env": {
        "DATA_AGENT_WORKSPACE_ID": "your-workspace-id"
      }
    }
  }
}
```

Restart the client and verify the `data-agent` server is connected — then the agent can call tools like `data_agent_list_workspace_databases`, `data_agent_create_session`, `data_agent_wait_result`, and `data_agent_result` directly.

Full documentation: [alibabacloud-data-agent-mcp-skill/README.md](alibabacloud-data-agent-mcp-skill/README.md) (deployment guide: stdio / Streamable HTTP / Aily multi-tenant) · [alibabacloud-data-agent-mcp-skill/SKILL.md](alibabacloud-data-agent-mcp-skill/SKILL.md) (tool reference & workflows)

> **Note**: server binaries are not committed to this repository. `scripts/select-binary.sh` builds one from source on first launch and caches it under `assets/server/bin/` (gitignored).

## Repository Layout

```
├── SKILL.md                              # CLI skill (repo root)
├── scripts/                              # CLI source (SDK + CLI modules + entry point)
├── references/                           # CLI skill reference docs
├── assets/                               # Example data (example_game_data.csv)
├── sessions/                             # CLI session data (gitignored)
├── tests/                                # CLI test suite
├── evals/                                # Eval scenarios
└── alibabacloud-data-agent-mcp-skill/    # Standalone MCP skill
    ├── SKILL.md                          # MCP skill instructions
    ├── README.md                         # Deployment guide (stdio / HTTP / Aily)
    ├── scripts/select-binary.sh          # Server launcher (auto-builds with Go)
    ├── assets/server/                    # Go MCP Server source
    └── references/ram-policies.md        # RAM permission requirements
```

## License

Apache License 2.0 — see [assets/LICENSE](assets/LICENSE).
