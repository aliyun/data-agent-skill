# data-agent-mcp-server

Go MCP Server exposing **Alibaba Cloud Apsara Data Agent for Analytics** as 18
native `data_agent_*` tools. This directory is a standalone Go project; the
distributable skill wrapper lives in
[`../alibabacloud-data-agent-mcp-skill/`](../alibabacloud-data-agent-mcp-skill/)
and ships **without** this source — deployments build the binary here.

## Prerequisites

- **Go 1.23+** (https://go.dev/dl/)
- `upx` optional — only used by `make build-all` to shrink Linux binaries
  (skipped automatically when not installed)

## Build

All targets run from this directory and output to `bin/` (gitignored;
binaries are never committed).

```bash
make build       # -> bin/data-agent-mcp-server (current platform)
make build-all   # -> bin/data-agent-mcp-server-linux-{amd64,arm64} (cross-compile + upx)
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

## Configure

```bash
cp .env.example .env               # secrets: AK/SK (gitignored, never commit)
cp config.yaml.example config.yaml # non-secret settings (gitignored)
```

Priority: **env vars > `.env` > `config.yaml` > defaults**. Config file lookup:
`$DATA_AGENT_CONFIG` > `./config.yaml` > `~/.data-agent/config.yaml`.

## Run

```bash
# stdio (an MCP client launches the process)
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
| `DATA_AGENT_UPLOAD_DIRS` | Allowlisted upload directories; unset disables `data_agent_upload_file` on HTTP transports (fail-closed) |
| `DATA_AGENT_LOG_REQUESTS` | Per-call logging: `basic` / `full` / `off` |

## Verify

```bash
make dacli
./bin/dacli --url http://localhost:61026/mcp \
  --user <user-id> --token <identity-auth-token> tools   # expect 18 tools
./bin/dacli ... dbs                                      # workspace databases
./bin/dacli ... ask <db> "<tables>" "<question>"         # end-to-end analysis
```

Identity headers (`--user`/`--token`) are only required when the `identity`
section is enabled in `config.yaml`.

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
│   ├── mcp/             # MCP server + 18 tool handlers + wait cap
│   ├── session/         # session manager, SSE watcher, housekeeping
│   ├── dataagent/       # Alibaba Cloud API client (V3 signing, SSE, paging)
│   ├── tenant/          # identity → RAM role AssumeRole registry (JWT/token)
│   ├── config/          # YAML + .env loader
│   └── event/           # SSE event parser
├── config.yaml.example  # config template
└── .env.example         # secrets template
```
