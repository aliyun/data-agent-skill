# Alibaba Cloud Data Agent Skills

AI Agent Skills that let AI assistants (Claude Code, Qoder, Codex, and other agent runtimes) invoke **Alibaba Cloud Apsara Data Agent for Analytics** — an intelligent data analysis agent that turns natural language questions into SQL, insights, and full analysis reports on your enterprise databases and files.

Ask questions like *"Which department has the highest average salary?"* or *"Analyze the sales trend of the last quarter and generate a report"*, and the agent handles requirement analysis, data understanding, SQL execution, insight generation, and report rendering.

Besides invoking the service, the repository also ships **data-agent-arcvis-skill** — a meta-skill that teaches an AI agent how to author its own skill packages for Data Agent (see [Authoring Skill Packages](#authoring-skill-packages--data-agent-arcvis-skill)).

## Invocation Skills: CLI vs MCP

Two standalone skills provide access to the same backend service. Choose the one that fits your agent runtime — they are independent and must not be mixed on the same session.

| | [alibabacloud-data-agent-skill](alibabacloud-data-agent-skill/SKILL.md) (CLI) | [alibabacloud-data-agent-mcp-skill](alibabacloud-data-agent-mcp-skill/SKILL.md) (MCP Server) |
|---|---|---|
| Integration | Python CLI — the agent runs shell commands | Go MCP Server — 19 `data_agent_*` tools; 28 with the base gateway (9 tools), 30 with P2b summaries too (11 gateway tools) |
| Runtime requirement | Python 3.10+ with venv | Go 1.26.10+ to build the server once (or a deployed binary) |
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

## Prerequisites (Both Invocation Skills)

1. An Alibaba Cloud account with data sources managed in [DMS](https://dms.aliyun.com/) (or use the built-in demo database `internal_data_employees`).
2. **MCP Server:** AK/STS via the credentials-go default chain (SDK-supported environment credentials or instance/workload-role provider). Providers are retained for dynamic credential retrieval and RAM AssumeRole source chaining; static STS environment values do not self-renew. The server does not hand-read `~/.aliyun/config.json` or promise CLI-profile compatibility. API Key runtime mode is removed across the Go Server, including SSE. Old YAML keys or nonempty old environment variables cause a sanitized migration error even when the gateway is disabled; see [migration instructions](server/README.md#api-key-migration-all-go-server-deployments). Inbound identity tokens/JWT, dacli `--token`, STS and OSS signatures remain supported.
3. **CLI Skill:** its authentication and setup are unchanged; follow the independent [CLI instructions](alibabacloud-data-agent-skill/SKILL.md). Go Server migration rules do not apply to the Python skill.
4. RAM API permissions and service-side data access must both be configured; see [MCP RAM requirements](alibabacloud-data-agent-mcp-skill/references/ram-policies.md) or [CLI RAM requirements](alibabacloud-data-agent-skill/references/RAM-POLICIES.md).

## Quick Start — CLI Skill

```bash
cd alibabacloud-data-agent-skill

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

Full documentation: [SKILL.md](alibabacloud-data-agent-skill/SKILL.md) · [references/COMMANDS.md](alibabacloud-data-agent-skill/references/COMMANDS.md) · [references/WORKFLOWS.md](alibabacloud-data-agent-skill/references/WORKFLOWS.md) · [references/ANALYSIS_MODE.md](alibabacloud-data-agent-skill/references/ANALYSIS_MODE.md)

## Quick Start — MCP Skill

**Security remediation is implemented locally, not accepted for production release.** Build with Go **1.26.10 minimum** (or a newer supported, rescanned version) and verify/rebuild Server, dacli and platform artifacts. Historical gateway cloud results below do not verify the remediated binary. See the [post-remediation release gates](alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md#post-remediation-release-gate--not-yet-verified); real JWT-platform and differential-role gates remain open.

```bash
# 1. Build the server once (Go 1.26.10+, https://go.dev/dl/)
cd server && make build   # -> server/bin/data-agent-mcp-server
```

Register the server in your MCP client (**local-only stdio**, one SDK process identity, shown below). For HTTP/SSE, configure mandatory `identity.enabled=true`, `require_identity=true`, nonempty `auth_token` and a stable `namespace` for **every** tool/protocol request, gateway on or off. Shared group/default mappings accept trusted Header identity when JWT is absent, including with JWT enabled, or valid JWTs without tenant/agent claims. Explicitly scoped mappings still require complete matching JWT scope. Present empty, invalid or expired JWTs never fall back to headers, and valid JWT identity takes precedence over plain headers. Keep the platform token with trusted upstreams, never end users; no anonymous/global-identity fallback or automatic Header/JWT history merging is permitted. Follow the [identity, storage migration, secret-file and HTTP-limit reference](alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md#multi-tenant-identity-mode-required-remotely):

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

**Optional MCP metadata gateway:** `agent_gateway.enabled` defaults to `false` (19 tools); enabling it adds nine Catalog/database/table metadata tools (28 total), including P2a under the same switch. The original six tools are unchanged. P2b adds two read-only control-plane data-source summary tools, `data_gateway_list_data_sources` / `data_gateway_get_data_source`, behind `agent_gateway.data_sources_enabled` (default `false`, requires `enabled=true`; 30 total / 11 gateway tools with both flags enabled). Configure an explicit public `dms.<region>.aliyuncs.com` endpoint matching `region`; examples use Hangzhou, a 30-second HTTP budget and a 4 MiB response limit. The mandatory remote authentication contract applies to initialization, notifications, GET/POST/DELETE and all tools, not only gateway calls. Protocol IDs are server-issued, principal-owned and leased; clients must renew credentials and reconnect, not use weak compatibility.

**P2b visibility is the current upstream RAM/STS principal's control-plane scope, not the chat user's database ACL.** Same-role sessions share permissions. Deployers must accept the summary visibility scope before release; otherwise leave P2b disabled. P2b strips authentication, network, permission and unknown fields, plus `TotalCount`; this is not server-wide Host confidentiality: root Catalog search still accepts address prefixes and existing Data Agent discovery is unchanged.

**Historical pre-remediation evidence (2026-10-29):** P2a automation and real-cloud discovery in stdio and configured-role HTTP/SSE passed. P2b automated tests, race and vet passed, as did independent-binary stdio/HTTP/SSE checks for all 19/28/30 configurations. Real calls verified source pagination, type filtering, sanitized details and missing-source errors; remote calls used a configured test role's STS in `cn-hangzhou`. These checks must not be treated as post-remediation acceptance. A format-preserving nonexistent ID returned HTTP 404 `DATA_SOURCE_NOT_FOUND`, not successful `Data:null`; current Get requires an object and keeps this response as `ERROR` (`GATEWAY_UPSTREAM_INCOMPATIBLE`). Success-null support is deferred for lack of a real sample. These results do not prove account-wide visibility, cross-role isolation, least privilege or production readiness. RAM machine-readable definitions and help-page authorization descriptions differ; see [RAM evidence and pending acceptance](alibabacloud-data-agent-mcp-skill/references/ram-policies.md#p2b-read-only-control-plane-summaries).

P2c/P3 and Wiki/ACL remain unimplemented. No gateway SQL history/execution, data-source writes, connection tests or Token tools are exposed. Gateway identifiers cannot be used as Data Agent `database_id`, and the existing SQL path gains no gateway row/column protection. See [gateway configuration and contract](server/README.md#optional-metadata-gateway).

Full documentation: [alibabacloud-data-agent-mcp-skill/README.md](alibabacloud-data-agent-mcp-skill/README.md) (deployment guide: stdio / Streamable HTTP / Aily multi-tenant) · [alibabacloud-data-agent-mcp-skill/SKILL.md](alibabacloud-data-agent-mcp-skill/SKILL.md) (tool reference & workflows)

> **Note**: server binaries are not committed to this repository. Build the server once (`cd server && make build`) or point `DATA_AGENT_SERVER_BIN` at a deployed binary — `scripts/select-binary.sh` locates it (see the MCP skill's INSTALLATION.md).

## Authoring Skill Packages — data-agent-arcvis-skill

[data-agent-arcvis-skill](data-agent-arcvis-skill/SKILL.md) (English, with Chinese counterparts `SKILL_zh.md` / `references/*_zh.md`) is a **meta-skill** that teaches an AI agent how to author skill packages Data Agent can reliably load and follow:

- **Quest packages** — turn a recurring multi-step analysis workflow (attribution, comparison, trend, target attainment) into a skill package: table facts (`tables/`), metric definitions as the single source of truth (`metrics/`), objective playbooks with machine-checkable gates (`objectives/`), and report requirements (`report/`);
- **Standalone web report templates** — turn a finished HTML report into a self-contained template package (`manifest.toml` + `schema.json` + `template.html`), with `$read` direct injection for numbers and tables, metrics SQL passthrough as an exception channel, and merged skill+template delivery.

Use it when the user wants to "make a report template", "turn this HTML into a template", or "distill this recurring analysis into a reusable skill". Bundled validators (standalone template validation requires `pip install pyyaml`; the rest is stdlib-only):

```bash
python3 data-agent-arcvis-skill/scripts/validate_skill.py <skill-package-dir>   # quest skill package
python3 data-agent-arcvis-skill/scripts/validate_template.py <template-dir>     # web report template package
python3 data-agent-arcvis-skill/scripts/preview_template.py <template-dir>      # local preview of the template
```

## Repository Layout

```
├── alibabacloud-data-agent-skill/        # CLI skill (Python)
│   ├── SKILL.md                          # CLI skill instructions
│   ├── scripts/                          # CLI source (SDK + CLI modules + entry point)
│   ├── references/                       # CLI skill reference docs
│   ├── assets/                           # Example data (example_game_data.csv)
│   ├── sessions/                         # CLI session data (gitignored)
│   ├── tests/                            # CLI test suite
│   └── evals/                            # Eval scenarios
├── alibabacloud-data-agent-mcp-skill/    # MCP skill (launcher + docs, no server source)
│   ├── SKILL.md                          # MCP skill instructions
│   ├── README.md                         # Deployment guide (stdio / HTTP / Aily)
│   ├── scripts/select-binary.sh          # Server binary locator
│   ├── assets/                           # Static assets (+ optional assets/bin/ for binaries)
│   └── references/                       # INSTALLATION.md, ram-policies.md
├── data-agent-arcvis-skill/              # Skill-authoring meta-skill (English, with Chinese counterparts)
│   ├── SKILL.md / SKILL_zh.md            # Quest packages + standalone web report templates
│   ├── references/                       # Authoring specs (structure, contracts, checklists; *_zh.md counterparts)
│   ├── examples/                         # Example skill packages (example-zh + example-en, pass all validators)
│   └── scripts/                          # validate_skill.py / validate_template.py / preview_template.py
└── server/                               # Go MCP Server (standalone project)
    ├── main.go / go.mod / Makefile
    ├── cmd/dacli/                        # Manual verification CLI
    └── internal/                         # Server implementation
```

## License

Apache License 2.0 — see [alibabacloud-data-agent-skill/assets/LICENSE](alibabacloud-data-agent-skill/assets/LICENSE).
