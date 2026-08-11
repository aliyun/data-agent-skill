# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

# Data Agent Skill

This repository contains the Data Agent Skill, an AI Agent Skill that enables AI assistants (like Codex) to call Alibaba Cloud's瑶池 Data Agent for Analytics to help enterprise users perform natural language-driven data analysis.

## Architecture Overview

The codebase consists of:

1. **SDK Module** (`alibabacloud-data-agent-skill/scripts/data_agent/`):
   - `client.py`: Synchronous and asynchronous clients for Data Agent API
   - `session.py`: Session management for maintaining conversation state
   - `config.py`: Configuration management loading from environment variables
   - `models.py`, `message.py`, `sse_client.py`, `file_manager.py`, `mcp_tools.py`, `exceptions.py`: Supporting modules

2. **CLI Module** (`alibabacloud-data-agent-skill/scripts/cli/`):
   - `parser.py`: Main argument parser defining all subcommands
   - Command modules: `cmd_db.py`, `cmd_file.py`, `cmd_attach.py`, `cmd_ls.py`, `cmd_dms.py`, `cmd_import.py`, `cmd_reports.py`
   - Helper modules: `formatters.py`, `streaming.py`, `notify.py`, `worker_lock.py`

3. **Main Entry Point**: `alibabacloud-data-agent-skill/scripts/data_agent_cli.py`

> **Note**: A Go-based MCP Server integration exists as a **separate, standalone skill** in `alibabacloud-data-agent-mcp-skill/` (own SKILL.md, scripts, and assets). It is independent of this CLI skill — do not mix the two; see `alibabacloud-data-agent-mcp-skill/SKILL.md`.

## Core Concepts

- **Data Agent**: Alibaba Cloud's analytics service that translates natural language queries into SQL and generates insights
- **DMS**: Database Management Service that stores metadata about databases
- **Data Center**: Where databases must be imported to be analyzed by Data Agent
- **Sessions**: Conversational state maintained for multi-turn analysis

## Key Features

- **Data Discovery**: Query DMS for instances, databases, and tables
- **Data Import**: Move databases from DMS to Data Agent Data Center
- **Query Analysis**: Natural language processing for database queries
- **File Analysis**: Upload and analyze CSV/Excel/JSON files
- **Session Reuse**: Continue conversations with existing sessions
- **Async Execution**: Background processing with progress tracking

## Working with Sessions

- **Session Modes**: auto (backend decides), lite (quick queries), pro (deep analysis with reports), ultra (most thorough analysis)
- **Async Operations**: Default mode for long-running tasks, returns immediately with Session ID
- **Session Directory**: Progress stored in `sessions/<session_id>/` with status.txt, progress.log, etc.
- **Session State**: Running, waiting_input, completed, failed

## Commands

- `ls`: List Data Center databases and tables
- `db`: Connect to database for analysis
- `file`: Upload and analyze local files
- `attach`: Connect to existing session for follow-up
- `dms`: DMS integration (list-instances, search-database, list-tables)
- `import`: Import DMS databases to Data Center
- `reports`: Download generated reports

## Development Guidelines

### Environment Setup

```bash
# 1. Clone repository
git clone <repo-url>
cd data-agent-skill

# 2. Create virtual environment and install dependencies
python3 -m venv venv
source venv/bin/activate  # Windows: venv\Scripts\activate
pip install -r alibabacloud-data-agent-skill/scripts/requirements.txt

# 3. Configure credentials
cp alibabacloud-data-agent-skill/.env.example alibabacloud-data-agent-skill/.env
# Edit .env with ALIBABA_CLOUD_ACCESS_KEY_ID, ALIBABA_CLOUD_ACCESS_KEY_SECRET, DATA_AGENT_REGION
```

### Testing

To run tests:
```bash
# Install test dependencies if any
pip install pytest

# Run tests
python -m pytest tests/ -v
```

### Key Dependencies

- Alibaba Cloud SDKs (tea-openapi, dms20250414, dms-enterprise20181101, openapi-util)
- HTTP libraries (requests, aiohttp)
- python-dotenv for environment variable management

### Common Patterns

- Use async execution by default for long-running operations
- Implement proper session management with error handling
- Follow the pattern of immediate feedback with periodic status checking
- Respect the `waiting_input` state which requires user intervention
- Maintain session directory structure for progress tracking

### Error Handling

- Handle API errors with retry logic
- Manage authentication errors properly
- Process session timeout scenarios
- Implement graceful degradation when services are unavailable