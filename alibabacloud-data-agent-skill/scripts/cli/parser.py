"""Argument parsing and CLI entry point.

Author: Tinker
Created: 2026-03-04
"""

import argparse
import os
import sys

from cli.cmd_db import cmd_db
from cli.cmd_ls import cmd_ls
from cli.cmd_file import cmd_file
from cli.cmd_attach import cmd_attach
from cli.cmd_cancel import cmd_cancel
from cli.cmd_status import cmd_status
from cli.cmd_upload import cmd_upload
from cli.cmd_send import cmd_send, cmd_send_legacy
from cli.cmd_dms import cmd_dms
from cli.cmd_import import cmd_import
from cli.cmd_reports import cmd_reports
from cli.cmd_workspace import cmd_workspace
from cli.cmd_agent import cmd_agent


def _add_common_endpoint(parent: argparse.ArgumentParser) -> None:
    """Register the shared --endpoint overrides on a subparser parent."""
    parent.add_argument(
        "--endpoint",
        default=None,
        metavar="HOST",
        help="Explicit API gateway endpoint override (defaults derived from region; also settable via DATA_AGENT_ENDPOINT)",
    )
    parent.add_argument(
        "--stream-endpoint",
        default=None,
        metavar="HOST",
        help="API_KEY data-plane (stream) endpoint override, e.g. pre gateways; ignored under AK/SK (also settable via DATA_AGENT_STREAM_ENDPOINT)",
    )


def _add_report_options(parser: argparse.ArgumentParser, repeatable_data_source: bool = False) -> None:
    """Add report-message options shared by db / file / attach / send subcommands.

    ``repeatable_data_source`` makes ``--data-source`` appendable so a single
    message can carry multiple Data Center file data sources (send).
    """
    group = parser.add_argument_group("Message Parameters")
    group.add_argument(
        "--message-type",
        choices=["primary", "report", "diag"],
        default="primary",
        metavar="TYPE",
        help="Message type: primary (default, plain chat) | report (web report generation request) | diag (report diagnosis request, message text is the complaint)",
    )
    group.add_argument(
        "--report-theme",
        default=None,
        metavar="THEME",
        help="Report visual theme (with --message-type report; default 'default')",
    )
    group.add_argument(
        "--report-prompt",
        default=None,
        metavar="TEXT|@PATH",
        help="Extra prompt injected into the report guide (with --message-type report); @path reads the whole file",
    )
    if repeatable_data_source:
        group.add_argument(
            "--data-source",
            dest="data_source",
            action="append",
            default=None,
            metavar="FILE_ID",
            help="Data Center file ID attached to the message as a remote_data_center data source (repeatable for multiple files)",
        )
    else:
        group.add_argument(
            "--data-source",
            dest="data_source",
            default=None,
            metavar="FILE_ID",
            help="Data Center file ID attached to the message as a remote_data_center data source",
        )


def _add_send_message_params(parser: argparse.ArgumentParser) -> None:
    """Add the message parameters shared by the send / legacy-send subcommands."""
    _add_common_endpoint(parser)
    _add_report_options(parser, repeatable_data_source=True)
    parser.add_argument(
        "--message", "-m",
        required=True,
        metavar="TEXT",
        help="Message content to send",
    )
    parser.add_argument(
        "--session-id", "-s",
        metavar="SESSION_ID",
        help="Reuse an existing session instead of creating one",
    )
    parser.add_argument(
        "--agent-id", "-a",
        metavar="AGENT_ID",
        help="Agent ID (with --session-id; auto-resolved via DescribeDataAgentSession when omitted)",
    )
    parser.add_argument(
        "--workspace-id",
        metavar="WORKSPACE_ID",
        help="Workspace ID for newly created sessions (or workspace-bound reuse)",
    )
    parser.add_argument(
        "--session-mode",
        default=None,
        choices=["auto", "lite", "pro", "ultra"],
        metavar="MODE",
        help="Session mode for the created session: auto | lite | pro | ultra (default: backend intelligent decision)",
    )
    parser.add_argument(
        "--custom-agent-id",
        metavar="AGENT_ID",
        help="Custom Agent ID bound to the session created by this message",
    )
    parser.add_argument(
        "--custom-agent-stage",
        choices=["debug", "prod"],
        default="prod",
        metavar="STAGE",
        help="Custom agent stage (default: prod)",
    )
    db_source_group = parser.add_argument_group("Database Data Source Parameters")
    db_source_group.add_argument(
        "--dms-instance-id",
        type=int,
        metavar="INT",
        help="DMS Instance ID for a database data source attached to the message",
    )
    db_source_group.add_argument(
        "--dms-db-id",
        type=int,
        metavar="INT",
        help="DMS Database ID (required when any database data source parameter is passed; the backend resolves table schemas and executes SQL by database ID)",
    )
    db_source_group.add_argument(
        "--db-name",
        metavar="STR",
        help="Database name for the database data source",
    )
    db_source_group.add_argument(
        "--tables",
        metavar="T1,T2,...",
        help="Table names for the database data source (comma-separated)",
    )
    perm_group = parser.add_argument_group("Permission Parameters")
    perm_group.add_argument(
        "--permission-file",
        default=None,
        metavar="PATH",
        help="Permission JSON file (table-level column/row restrictions) attached to the database data source; its top-level default_action promotes to the session-level permission config",
    )
    perm_group.add_argument(
        "--permission-default-action",
        choices=["allow", "deny"],
        default=None,
        metavar="ACTION",
        help="Session-level default action for tables not declared in the permission config (allow | deny)",
    )
    parser.add_argument(
        "--poll",
        action="store_true",
        help="After sending, poll DescribeDataAgentSession until the session completes (2 consecutive IDLE) or reaches a terminal status",
    )
    parser.add_argument(
        "--timeout",
        type=int,
        default=300,
        metavar="SECONDS",
        help="Poll timeout in seconds (with --poll; default: 300)",
    )


def build_parser() -> argparse.ArgumentParser:
    """Build command-line argument parser."""
    parser = argparse.ArgumentParser(
        prog="data_agent_cli",
        description="Data Agent Unified CLI - Database Query/Analysis & File Analysis",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Session Mode Description
  auto  : Backend intelligent decision (default)
  lite  : Quick query mode with SQL execution + natural language response
  pro   : Deep analysis mode with planning, multi-step reasoning, and report generation
  ultra : Most thorough analysis with multi-dimensional insights

Examples:
  # -- Database query (auto mode, default) --
  python data_agent_cli.py db \\
      --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> \\
      --instance-name <INSTANCE_NAME> --db-name chinook \\
      --tables "album,artist,customer" \\
      -q "Who has the highest sales?"

  # -- Database deep analysis (pro mode) --
  python data_agent_cli.py db --session-mode pro \\
      --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> \\
      --instance-name <INSTANCE_NAME> --db-name chinook \\
      --tables "album,artist,customer" \\
      -q "Analyze sales trends and generate report"

  # -- File Analysis --
  python data_agent_cli.py file /path/to/data.csv
  python data_agent_cli.py file /path/to/data.csv -q "Analyze sales trends"
""",
    )

    subparsers = parser.add_subparsers(dest="command", metavar="COMMAND")
    subparsers.required = True

    # -- db subcommand --
    db_parser = subparsers.add_parser(
        "db",
        help="Database query/analysis (supports auto / lite / pro / ultra modes)",
        description="""Connect to DMS database for natural language queries or data analysis.

Mode Description:
  auto  (default): Backend intelligent decision, selects the best mode per query
  lite            : Quick SQL query + natural language response
  pro             : Deep analysis, multi-step reasoning, report generation
  ultra           : Most thorough analysis with multi-dimensional insights""",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_common_endpoint(db_parser)
    _add_report_options(db_parser)

    # Data source parameters
    ds_group = db_parser.add_argument_group("Data Source Parameters (required unless --custom-agent-id is set)")
    ds_group.add_argument(
        "--dms-instance-id",
        type=int,
        metavar="INT",
        help="DMS Instance ID (e.g., 1234567). Not required when --custom-agent-id is specified.",
    )
    ds_group.add_argument(
        "--dms-db-id",
        type=int,
        metavar="INT",
        help="DMS Database ID (e.g., 12345678)",
    )
    ds_group.add_argument(
        "--instance-name",
        metavar="STR",
        help="RDS instance name (e.g., rm-xxxxxx.mysql.rds.aliyuncs.com)",
    )
    ds_group.add_argument(
        "--db-name",
        metavar="STR",
        help="Database name (e.g., chinook)",
    )
    ds_group.add_argument(
        "--tables",
        metavar="T1,T2,...",
        help="Table names to include (comma-separated, e.g., album,artist,track)",
    )
    ds_group.add_argument(
        "--table-ids",
        metavar="ID1,ID2,...",
        help="Data Center table IDs (comma-separated)",
    )
    ds_group.add_argument(
        "--engine",
        default="mysql",
        metavar="ENGINE",
        help="Database engine type (default: mysql)",
    )
    # Default region from env, fallback to cn-hangzhou
    _default_region = os.environ.get("DATA_AGENT_REGION", "cn-hangzhou")
    ds_group.add_argument(
        "--region",
        default=_default_region,
        metavar="REGION",
        help=f"Region ID (default: {_default_region})",
    )

    # Query
    db_parser.add_argument(
        "--query", "-q",
        metavar="QUERY",
        help="Execute a single natural language query (runs preset queries if omitted)",
    )

    db_parser.add_argument(
        "--async-run",
        action=argparse.BooleanOptionalAction,
        default=True,
        help="Run asynchronously in background (default: True). Use --no-async-run for synchronous mode.",
    )

    # Session options
    db_parser.add_argument(
        "--session-mode",
        default="auto",
        choices=["auto", "lite", "pro", "ultra"],
        metavar="MODE",
        help="Session mode: auto (default) | lite | pro | ultra",
    )
    db_parser.add_argument(
        "--plan-mode",
        default="force",
        choices=["force", "disable"],
        metavar="PLAN",
        help="Plan mode: force (default, always generate execution plan) | disable (skip planning)",
    )
    db_parser.add_argument(
        "--output",
        choices=["summary", "detail", "raw"],
        default="summary",
        metavar="MODE",
        help="Output verbosity: summary (default, conclusion only) | detail (full process) | raw (every SSE event)",
    )
    db_parser.add_argument(
        "--enable-search",
        action="store_true",
        help="Enable search capability in the session (default: False)",
    )
    db_parser.add_argument(
        "--workspace-id",
        metavar="WORKSPACE_ID",
        help="Workspace ID to bind the session to a specific workspace",
    )
    db_parser.add_argument(
        "--custom-agent-id",
        type=str,
        default=None,
        help="Custom Agent ID to use for the analysis session. When specified, data source parameters (--dms-instance-id, --dms-db-id, --db-name, --tables) are not required.",
    )

    db_parser.set_defaults(func=cmd_db)

    # -- file subcommand --
    file_parser = subparsers.add_parser(
        "file",
        help="File upload and analysis (CSV / Excel / JSON)",
        description="Upload local data files for intelligent analysis via Data Agent.",
    )
    _add_common_endpoint(file_parser)
    _add_report_options(file_parser)

    # Create a mutually exclusive group for file source
    file_source_group = file_parser.add_mutually_exclusive_group(required=True)

    file_source_group.add_argument(
        "file_path",
        nargs='?',  # Make it optional since we have --file-id
        metavar="FILE",
        help="Local file path to upload for analysis (supports: .csv / .xlsx / .xls / .json / .txt)",
    )

    file_source_group.add_argument(
        "--file-id",
        metavar="FILE_ID",
        help="Data Center file ID to analyze directly (e.g., f-8941bx83xy9513xvpewrha01m)",
    )

    # Query
    file_parser.add_argument(
        "--query", "-q",
        metavar="QUERY",
        help="Execute a single custom query (uses preset questions if not specified)",
    )

    file_parser.add_argument(
        "--async-run",
        action=argparse.BooleanOptionalAction,
        default=True,
        help="Run asynchronously in background (default: True). Use --no-async-run for synchronous mode.",
    )

    file_parser.add_argument(
        "--list-generated-files",
        action="store_true",
        help="List agent-generated reports/charts after analysis",
    )
    file_parser.add_argument(
        "--session-mode",
        default="auto",
        choices=["auto", "lite", "pro", "ultra"],
        metavar="MODE",
        help="Session mode: auto (default) | lite | pro | ultra",
    )
    file_parser.add_argument(
        "--plan-mode",
        default="force",
        choices=["force", "disable"],
        metavar="PLAN",
        help="Plan mode: force (default, always generate execution plan) | disable (skip planning)",
    )
    file_parser.add_argument(
        "--output",
        default="summary",
        choices=["summary", "detail", "raw"],
        metavar="MODE",
        help="Output mode: summary (default, conclusion only) | detail (full process) | raw (all SSE events)",
    )
    file_parser.add_argument(
        "--enable-search",
        action="store_true",
        help="Enable search capability in the session (default: False)",
    )
    file_parser.add_argument(
        "--workspace-id",
        metavar="WORKSPACE_ID",
        help="Workspace ID to bind the session to a specific workspace",
    )
    file_parser.add_argument(
        "--custom-agent-id",
        type=str,
        default=None,
        help="Custom Agent ID to use for the analysis session",
    )

    file_parser.set_defaults(func=cmd_file)

    # -- upload subcommand --
    upload_parser = subparsers.add_parser(
        "upload",
        help="Upload a local file and print its file ID (no session created)",
        description=(
            "Upload a local data file (CSV / Excel) to Data Agent and print\n"
            "the resulting file ID, without starting an analysis session.\n\n"
            "Examples:\n"
            "  python3 scripts/data_agent_cli.py upload data.csv\n"
            "  python3 scripts/data_agent_cli.py upload data.csv --endpoint custom-gw.example.com"
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_common_endpoint(upload_parser)
    upload_parser.add_argument(
        "file_path",
        metavar="FILE",
        help="Local file path to upload (supports: .csv / .xlsx / .xls)",
    )
    upload_parser.add_argument(
        "--workspace-id",
        metavar="WORKSPACE_ID",
        help="Workspace ID to upload into (defaults to the caller's personal workspace)",
    )
    upload_parser.set_defaults(func=cmd_upload)

    # -- send subcommand --
    send_parser = subparsers.add_parser(
        "send",
        help="Send a single message (direct send, or reuse one)",
        description=(
            "Lightweight message trigger: send a single message, printing the response.\n"
            "Without --session-id the message is sent directly and the backend\n"
            "creates the session (the new SessionId is parsed from the response).\n"
            "With AK/SK auth, --custom-agent-id is carried in the SendChatMessage\n"
            "SessionConfig directly — no CreateDataAgentSession round-trip.\n"
            "Use this for connectivity checks and report-generation requests\n"
            "without the full streaming workflow of db / file / attach.\n\n"
            "Examples:\n"
            "  python3 scripts/data_agent_cli.py send -m \"Analyze sales trends\"\n"
            "  python3 scripts/data_agent_cli.py send --data-source <FILE_ID> -m \"Analyze store sales\"\n"
            "  python3 scripts/data_agent_cli.py send --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> --db-name <DB> --tables \"t1,t2\" -m \"Analyze the database\"\n"
            "  python3 scripts/data_agent_cli.py send --workspace-id <WORKSPACE_ID> --custom-agent-id <AGENT_ID> -m \"Generate a report\" --poll --timeout 600\n"
            "  python3 scripts/data_agent_cli.py send -s <SID> --message-type report --report-theme default -m \"Generate a web report\"\n"
            "  python3 scripts/data_agent_cli.py send -s <SID> --message-type report --report-prompt @report_prompt.md -m \"Generate report\"\n"
            "  python3 scripts/data_agent_cli.py send -s <SID> --message-type diag -m \"Why does the web report show no data for metric X\"\n"
            "  python3 scripts/data_agent_cli.py send --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> --db-name <DB> --tables \"t1,t2\" \\\n"
            "      --permission-file permission.json --permission-default-action deny -m \"Analyze orders\" --poll"
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_send_message_params(send_parser)
    send_parser.set_defaults(func=cmd_send)

    # -- legacy-send subcommand --
    legacy_send_parser = subparsers.add_parser(
        "legacy-send",
        help="Legacy send: pre-creates the session via CreateDataAgentSession (supports --title)",
        description=(
            "Legacy variant of send that pre-creates the session via\n"
            "CreateDataAgentSession. Kept only for what SendChatMessage cannot\n"
            "express today: session titles (--title) and custom-agent binding\n"
            "under API_KEY auth. Prefer plain `send` unless a title is really\n"
            "required; sessions created without a title get a server-side\n"
            "default title.\n\n"
            "Examples:\n"
            "  python3 scripts/data_agent_cli.py legacy-send --title \"poc session\" --data-source <FILE_ID> -m \"Generate a report\"\n"
            "  python3 scripts/data_agent_cli.py legacy-send --custom-agent-id <AGENT_ID> -m \"Analyze\"   # API_KEY auth path"
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_send_message_params(legacy_send_parser)
    legacy_send_parser.add_argument(
        "--title",
        metavar="TEXT",
        help="Title for a newly created session (default: data-agent-session)",
    )
    legacy_send_parser.set_defaults(func=cmd_send_legacy)

    # -- attach subcommand --
    attach_parser = subparsers.add_parser(
        "attach",
        help="Connect to an existing session to continue analysis or confirm plan",
        description=(
            "Attach to an existing Data Agent session. Use this to:\n"
            "  - Continue an interrupted pro/ultra session\n"
            "  - Confirm an execution plan after WAIT_INPUT\n"
            "  - Ask follow-up questions in an existing session\n\n"
            "Examples:\n"
            "  python3 scripts/data_agent_cli.py attach --session-id <ID> -q '\u786e\u8ba4\u6267\u884c\u8ba1\u5212'\n"
            "  python3 scripts/data_agent_cli.py attach --session-id <ID>"
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_common_endpoint(attach_parser)
    _add_report_options(attach_parser)
    attach_parser.add_argument(
        "--session-id",
        required=True,
        metavar="SESSION_ID",
        help="Session ID to connect to",
    )
    attach_parser.add_argument(
        "--output",
        choices=["summary", "detail", "raw"],
        default="summary",
        metavar="MODE",
        help="Output verbosity: summary (default, conclusion only) | detail (full process) | raw (every SSE event)",
    )
    attach_parser.add_argument(
        "--checkpoint",
        type=int,
        metavar="CHECKPOINT",
        help="Specific checkpoint to resume from (e.g. 52). Resumes from breakpoint.",
    )
    attach_parser.add_argument(
        "--from-start",
        action="store_true",
        help="Replay from checkpoint=0 (full history) instead of watching live stream",
    )
    attach_parser.add_argument(
        "--query", "-q",
        metavar="QUERY",
        help="Execute a single query (e.g., '\u786e\u8ba4\u6267\u884c\u8ba1\u5212' or '\u7ee7\u7eed\u5206\u6790')",
    )
    attach_parser.add_argument(
        "--async-run",
        action=argparse.BooleanOptionalAction,
        default=True,
        help="Run asynchronously in background (default: True). Use --no-async-run for synchronous mode.",
    )
    attach_parser.add_argument(
        "--workspace-id",
        metavar="WORKSPACE_ID",
        help="Workspace ID override",
    )
    attach_parser.set_defaults(func=cmd_attach)

    # -- cancel subcommand --
    cancel_parser = subparsers.add_parser(
        "cancel",
        help="Cancel a running session",
        description=(
            "Cancel a running Data Agent session by sending a cancel message.\n\n"
            "Examples:\n"
            "  python3 scripts/data_agent_cli.py cancel --session-id <ID>"
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_common_endpoint(cancel_parser)
    cancel_parser.add_argument(
        "--session-id",
        required=True,
        metavar="SESSION_ID",
        help="Session ID to cancel",
    )
    cancel_parser.add_argument(
        "--workspace-id",
        metavar="WORKSPACE_ID",
        help="Workspace ID if the session is workspace-bound",
    )
    cancel_parser.set_defaults(func=cmd_cancel)

    # -- status subcommand --
    status_parser = subparsers.add_parser(
        "status",
        help="Show the current status of a session",
        description=(
            "Show the current status of a Data Agent session via a single\n"
            "DescribeDataAgentSession call. Read-only: no streaming, no worker.\n"
            "For WAIT_INPUT sessions, prints how to confirm via attach.\n\n"
            "Examples:\n"
            "  python3 scripts/data_agent_cli.py status --session-id <ID>"
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_common_endpoint(status_parser)
    status_parser.add_argument(
        "--session-id",
        required=True,
        metavar="SESSION_ID",
        help="Session ID to query",
    )
    status_parser.add_argument(
        "--agent-id", "-a",
        metavar="AGENT_ID",
        help="Agent ID (optional; the backend resolves it from the session)",
    )
    status_parser.add_argument(
        "--workspace-id",
        metavar="WORKSPACE_ID",
        help="Workspace ID if the session is workspace-bound",
    )
    status_parser.set_defaults(func=cmd_status)

    # -- ls subcommand --
    ls_parser = subparsers.add_parser(
        "ls",
        help="List DMS databases and tables with analysis-ready parameters",
        description=(
            "List databases registered in DMS Data Center and print the\n"
            "key IDs and parameters needed for the db subcommand.\n\n"
            "Examples:\n"
            "  python3 scripts/data_agent_cli.py ls                  # all databases\n"
            "  python3 scripts/data_agent_cli.py ls --search chinook # filter by name\n"
            "  python3 scripts/data_agent_cli.py ls --db-id <DB_ID> # tables in DB"
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_common_endpoint(ls_parser)
    ls_parser.add_argument(
        "--workspace-id",
        type=str,
        default=None,
        help="Workspace ID. Leave empty for automatic resolution (CLI --workspace-id > env DATA_AGENT_WORKSPACE_ID > personal workspace).",
    )
    ls_parser.add_argument(
        "--search", "-s",
        metavar="KEYWORD",
        help="Filter databases by keyword (database or instance name)",
    )
    ls_parser.add_argument(
        "--db-id",
        metavar="DB_ID",
        help="AgentDbId (MetaEntityAttrs.dbId) — list tables for this database",
    )
    ls_parser.set_defaults(func=cmd_ls)

    # -- dms subcommand --
    dms_parser = subparsers.add_parser(
        "dms",
        help="DMS tools integration (list-instances, search-database, list-tables)",
        description="""Access DMS Server tools for instance, database and table management.

Tools:
  list-instances    Search and list database instances in DMS
  search-database   Search databases by schema name
  list-tables       List tables in a database

Examples:
  python3 scripts/data_agent_cli.py dms list-instances
  python3 scripts/data_agent_cli.py dms list-instances --search mysql --db-type mysql
  python3 scripts/data_agent_cli.py dms search-database --search-key mydb
  python3 scripts/data_agent_cli.py dms list-tables --database-id <DATABASE_ID>
""",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_common_endpoint(dms_parser)
    dms_parser.add_argument(
        "tool",
        choices=["list-instances", "search-database", "list-tables"],
        help="DMS tool to execute",
    )
    # list-instances options
    dms_parser.add_argument(
        "--search",
        metavar="KEYWORD",
        help="Search key for instances (host, alias, etc.)",
    )
    dms_parser.add_argument(
        "--db-type",
        metavar="TYPE",
        help="Database type filter (e.g., mysql, polardb, oracle)",
    )
    dms_parser.add_argument(
        "--env-type",
        metavar="ENV",
        help="Environment type filter (e.g., product, dev, test)",
    )
    # search-database options
    dms_parser.add_argument(
        "--search-key",
        metavar="KEYWORD",
        help="Schema name search keyword for search-database",
    )
    dms_parser.add_argument(
        "--page-number",
        type=int,
        default=1,
        metavar="PAGE",
        help="Page number for pagination (default: 1)",
    )
    dms_parser.add_argument(
        "--page-size",
        type=int,
        default=200,
        metavar="SIZE",
        help="Page size for pagination (default: 200, max: 1000)",
    )
    # list-tables options
    dms_parser.add_argument(
        "--database-id",
        metavar="DB_ID",
        help="Database ID for list-tables",
    )
    dms_parser.add_argument(
        "--search-name",
        metavar="NAME",
        help="Table name search keyword for list-tables",
    )
    dms_parser.set_defaults(func=cmd_dms)

    # -- import subcommand --
    import_parser = subparsers.add_parser(
        "import",
        help="Import DMS database tables to Data Agent Data Center",
        description="""Add DMS database tables to Data Agent Data Center.

This command imports tables from DMS into Data Agent's Data Center,
making them available for analysis via the 'db' subcommand.

Examples:
  python3 scripts/data_agent_cli.py import \\
    --dms-instance-id <DMS_INSTANCE_ID> \\
    --dms-db-id <DMS_DB_ID> \\
    --instance-name <INSTANCE_NAME> \\
    --db-name employees \\
    --tables "departments,employees,salaries" \\
    --yes
""",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_common_endpoint(import_parser)

    # Data source parameters (required)
    import_group = import_parser.add_argument_group("Data Source Parameters (required)")
    import_group.add_argument(
        "--dms-instance-id",
        type=int,
        metavar="INT",
        help="DMS Instance ID (e.g., 1234567)",
    )
    import_group.add_argument(
        "--dms-db-id",
        type=int,
        metavar="INT",
        help="DMS Database ID (e.g., 12345678)",
    )
    import_group.add_argument(
        "--instance-name",
        metavar="STR",
        help="RDS instance name (e.g., rm-xxxxxx.mysql.rds.aliyuncs.com)",
    )
    import_group.add_argument(
        "--db-name",
        metavar="STR",
        help="Database name (e.g., employees)",
    )
    import_group.add_argument(
        "--tables",
        metavar="T1,T2,...",
        help="Table names to import (comma-separated, e.g., departments,employees,salaries)",
    )
    import_group.add_argument(
        "--engine",
        default="mysql",
        metavar="ENGINE",
        help="Database engine type (default: mysql)",
    )
    _default_region = os.environ.get("DATA_AGENT_REGION", "cn-hangzhou")
    import_group.add_argument(
        "--region",
        default=_default_region,
        metavar="REGION",
        help=f"Region ID (default: {_default_region})",
    )
    import_parser.add_argument(
        "--yes", "-y",
        action="store_true",
        help="Confirm the import operation without an interactive prompt",
    )

    import_parser.set_defaults(func=cmd_import)

    # -- reports subcommand --
    reports_parser = subparsers.add_parser(
        "reports",
        help="List and download agent-generated files (reports, charts, data files)",
        description="""List and download files generated during a pro or ultra session.

Examples:
  python3 scripts/data_agent_cli.py reports --session-id <ID>
""",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_common_endpoint(reports_parser)
    reports_parser.add_argument(
        "--session-id",
        required=True,
        metavar="SESSION_ID",
        help="Session ID to fetch reports for",
    )
    # The download parameter is no longer an optional flag;
    # reports command now downloads files automatically.
    # We keep the argument parser clean by removing it.
    reports_parser.set_defaults(func=cmd_reports)

    # -- workspace subcommand --
    workspace_parser = subparsers.add_parser(
        "workspace",
        help="List Data Agent workspaces under the current account",
        description="""List Data Agent collaboration workspaces.

Examples:
  python3 scripts/data_agent_cli.py workspace
  python3 scripts/data_agent_cli.py workspace --workspace-type ALL
  python3 scripts/data_agent_cli.py workspace --search myworkspace
""",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_common_endpoint(workspace_parser)
    workspace_parser.add_argument(
        "--workspace-type",
        default="MY",
        choices=["MY", "ALL"],
        metavar="TYPE",
        help="Workspace type: MY (default, my workspaces) | ALL (all accessible)",
    )
    workspace_parser.add_argument(
        "--search", "-s",
        metavar="KEYWORD",
        help="Filter workspaces by name",
    )
    workspace_parser.add_argument(
        "--page-number",
        type=int,
        default=1,
        metavar="PAGE",
        help="Page number (default: 1)",
    )
    workspace_parser.add_argument(
        "--page-size",
        type=int,
        default=50,
        metavar="SIZE",
        help="Page size (default: 50)",
    )
    workspace_parser.set_defaults(func=cmd_workspace)

    # -- agent subcommand --
    agent_parser = subparsers.add_parser(
        "agent",
        help="List, describe, create, update and publish custom agents",
        description="""Manage custom agents.

Actions:
  list     List custom agents (default)
  describe Show one agent's details
  create   Create an agent, then release it automatically
  update   Modify an existing agent, then release it automatically
  publish  Release an existing agent (operate_type=RELEASE)

Examples:
  python3 scripts/data_agent_cli.py agent
  python3 scripts/data_agent_cli.py agent list --search myagent
  python3 scripts/data_agent_cli.py agent describe --custom-agent-id <ID>
  python3 scripts/data_agent_cli.py agent create --workspace-id <WORKSPACE_ID> \\
      --agent-name "report-agent" --dsl-file report_dsl.json \\
      --instruction @instruction.md
  python3 scripts/data_agent_cli.py agent update --custom-agent-id <ID> \\
      --workspace-id <WORKSPACE_ID> --dsl-file report_dsl_v2.json
  python3 scripts/data_agent_cli.py agent publish --custom-agent-id <ID> \\
      --workspace-id <WORKSPACE_ID>
""",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    _add_common_endpoint(agent_parser)
    agent_parser.add_argument(
        "action",
        nargs="?",
        default="list",
        choices=["list", "describe", "create", "update", "publish"],
        help="Action to perform (default: list)",
    )
    agent_parser.add_argument(
        "--workspace-id",
        type=str,
        default=None,
        help="Workspace ID (filter for list/describe; target for create/update/publish)",
    )
    agent_parser.add_argument(
        "--custom-agent-id",
        type=str,
        default=None,
        help="Custom Agent ID (required for describe/update/publish)",
    )
    agent_parser.add_argument(
        "--search",
        type=str,
        default=None,
        help="Search keyword to filter agents",
    )
    agent_parser.add_argument(
        "--agent-name",
        type=str,
        default=None,
        metavar="NAME",
        help="Custom agent name (required for create; optional for update)",
    )
    agent_parser.add_argument(
        "--description",
        type=str,
        default=None,
        metavar="TEXT",
        help="Custom agent description (create/update)",
    )
    agent_parser.add_argument(
        "--dsl-file",
        type=str,
        default=None,
        metavar="PATH",
        help="WebReport DSL JSON file (create/update; validated as JSON)",
    )
    agent_parser.add_argument(
        "--instruction",
        type=str,
        default=None,
        metavar="TEXT|@PATH",
        help="Agent instruction text; @path reads the whole file (create/update)",
    )
    agent_parser.add_argument("--page-number", type=int, default=1)
    agent_parser.add_argument("--page-size", type=int, default=20)
    agent_parser.set_defaults(func=cmd_agent)

    return parser


def main() -> None:
    parser = build_parser()
    args = parser.parse_args()

    # Propagate explicit overrides before any component reads the
    # environment; worker subprocesses inherit them automatically.
    if getattr(args, "endpoint", None):
        os.environ["DATA_AGENT_ENDPOINT"] = args.endpoint
    if getattr(args, "stream_endpoint", None):
        os.environ["DATA_AGENT_STREAM_ENDPOINT"] = args.stream_endpoint

    try:
        args.func(args)
    except KeyboardInterrupt:
        print("\n\nInterrupted.", file=sys.stderr)
        sys.exit(130)
    except Exception as e:
        print(f"\nError: {e}", file=sys.stderr)
        sys.exit(1)
