"""Common utilities for async worker management in Data Agent CLI."""

import os
import sys
import json
import subprocess
import tempfile
from pathlib import Path
from typing import Dict, Any, Optional
from cli.worker_lock import write_worker_pid, check_worker_lock
from data_agent import (
    DataAgentConfig,
    DataAgentClient,
    SessionManager,
    MessageHandler,
)


def is_worker_process() -> bool:
    """Check if this is a worker process."""
    return os.environ.get("DATA_AGENT_ASYNC_WORKER") == "1"


def get_worker_session_details() -> tuple[str, str]:
    """Get session and agent IDs from environment variables in worker process."""
    session_id = os.environ["DATA_AGENT_SESSION_ID"]
    agent_id = os.environ.get("DATA_AGENT_AGENT_ID", "")
    return session_id, agent_id


_PERMISSION_KEY_MAP = {
    "default_action": "DefaultAction",
    "tables": "Tables",
    "table_name": "TableName",
    "allowed_columns": "AllowedColumns",
    "disallowed_columns": "DisallowedColumns",
    "required_row_filter": "RequiredRowFilter",
    "required_row_filter_list": "RequiredRowFilterList",
}


def _to_pascal_permission(value):
    """Recursively convert known snake_case permission keys to the PascalCase wire format.

    Unknown keys (including already-PascalCase ones) pass through unchanged.
    """
    if isinstance(value, list):
        return [_to_pascal_permission(v) for v in value]
    if isinstance(value, dict):
        return {_PERMISSION_KEY_MAP.get(k, k): _to_pascal_permission(v) for k, v in value.items()}
    return value


def _load_permission_file(path: str) -> dict:
    """Read a --permission-file JSON document and convert it to the wire format."""
    file_path = Path(path)
    if not file_path.is_file():
        print(f"Error: --permission-file not found: {file_path}", file=sys.stderr)
        sys.exit(1)
    try:
        raw = json.loads(file_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as e:
        print(f"Error: --permission-file is not valid JSON: {file_path} ({e})", file=sys.stderr)
        sys.exit(1)
    if not isinstance(raw, dict):
        print(f"Error: --permission-file must contain a JSON object: {file_path}", file=sys.stderr)
        sys.exit(1)
    return _to_pascal_permission(raw)


def build_send_options(args: object) -> dict:
    """Build message send options from report-related CLI arguments.

    Reads ``message_type``, ``report_theme``, ``report_prompt``, ``data_source``
    and the database data-source parameters (``dms_instance_id`` / ``db_name`` /
    ``tables``) from ``args`` (all optional) and returns a dict of keyword
    arguments for ``client.send_message`` / ``MessageHandler`` send options:
    ``message_type``, ``report_config``, ``data_sources``,
    ``permission_config``.

    ``data_source`` may be a single file ID or a list (``--data-source`` is
    repeatable on the send subcommand); each becomes a remote_data_center
    data source. ``report_prompt`` accepts ``@path`` to read the whole file
    as the prompt (same convention as ``@file`` arguments in common CLIs).

    ``permission_file`` (``--permission-file``) is a JSON document with
    table-level column / row restrictions, attached to the database data
    source as ``Permission``; its top-level ``default_action`` and the
    explicit ``permission_default_action`` both promote to the session-level
    ``permission_config`` (``{"DefaultAction": ...}``).
    """
    options: Dict[str, Any] = {}

    message_type = getattr(args, "message_type", None) or "primary"
    if message_type != "primary":
        options["message_type"] = message_type

    theme = getattr(args, "report_theme", None)
    prompt = getattr(args, "report_prompt", None)
    if prompt and prompt.startswith("@"):
        prompt_path = Path(prompt[1:])
        if not prompt_path.is_file():
            print(f"Error: --report-prompt file not found: {prompt_path}", file=sys.stderr)
            sys.exit(1)
        prompt = prompt_path.read_text(encoding="utf-8")
        if not prompt.strip():
            print(f"Error: --report-prompt file is empty: {prompt_path}", file=sys.stderr)
            sys.exit(1)

    if message_type == "report" or theme or prompt:
        report_config: Dict[str, Any] = {"ReportType": "WebReport"}
        report_config["ReportTheme"] = theme or "default"
        if prompt:
            report_config["ReportPrompt"] = prompt
        options["report_config"] = report_config

    permission_file = getattr(args, "permission_file", None)
    permission_default = getattr(args, "permission_default_action", None)
    ds_permission = None
    file_default_action = None
    if permission_file:
        permission = _load_permission_file(permission_file)
        file_default_action = permission.pop("DefaultAction", None)
        if file_default_action and permission_default and file_default_action != permission_default:
            print(
                f"Error: --permission-default-action ({permission_default}) conflicts with the "
                f"default_action in {permission_file} ({file_default_action})",
                file=sys.stderr,
            )
            sys.exit(1)
        ds_permission = permission

    data_sources = _build_data_sources(args, ds_permission)
    if data_sources:
        options["data_sources"] = data_sources

    default_action = permission_default or file_default_action
    if default_action:
        options["permission_config"] = {"DefaultAction": default_action}

    return options


def _build_data_sources(args: object, permission: Optional[dict] = None) -> list:
    """Build the message-level data_sources list.

    File IDs come from ``--data-source`` (single or repeatable); the database
    data source comes from ``--dms-instance-id`` / ``--db-name`` / ``--tables``
    (comma-separated table names), matching the POP DataSources structure.
    ``permission`` (from ``--permission-file``) is attached to the database
    data source as ``Permission``.
    """
    data_sources: list = []

    data_source_id = getattr(args, "data_source", None)
    if data_source_id:
        file_ids = data_source_id if isinstance(data_source_id, list) else [data_source_id]
        data_sources.extend(
            {"DataSourceType": "remote_data_center", "FileId": file_id}
            for file_id in file_ids
        )

    dms_instance_id = getattr(args, "dms_instance_id", None)
    db_name = getattr(args, "db_name", None)
    tables_str = getattr(args, "tables", None)
    if dms_instance_id or db_name or tables_str:
        db_source: Dict[str, Any] = {"DataSourceType": "database"}
        if dms_instance_id:
            db_source["DmsInstanceId"] = str(dms_instance_id)
        if db_name:
            db_source["DbName"] = db_name
        if tables_str:
            db_source["Tables"] = [t.strip() for t in tables_str.split(",") if t.strip()]
        if permission:
            db_source["Permission"] = permission
        data_sources.append(db_source)
    elif permission:
        print(
            "Error: --permission-file requires database data source parameters "
            "(--dms-instance-id / --db-name / --tables)",
            file=sys.stderr,
        )
        sys.exit(1)

    return data_sources


def initialize_components() -> tuple:
    """Initialize common Data Agent components."""
    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)
    session_manager = SessionManager(client)
    message_handler = MessageHandler(client)
    return config, client, session_manager, message_handler


def setup_async_worker(
    args: object,
    session,
    additional_env_vars: Optional[Dict[str, str]] = None
) -> None:
    """Setup and run async worker process.

    Args:
        args: Command line arguments
        session: Session object containing session_id and agent_id
        additional_env_vars: Additional environment variables to set
    """
    session_id = session.session_id
    session_dir = Path(f"sessions/{session_id}")
    session_dir.mkdir(parents=True, exist_ok=True)

    # Check for existing worker
    existing_pid = check_worker_lock(session_dir)
    if existing_pid:
        print(f"⚠️  A worker process (PID {existing_pid}) is already running for session {session_id}.", file=sys.stderr)
        print(f"   Check progress: cat sessions/{session_id}/progress.log", file=sys.stderr)
        print(f"   Current status: {(session_dir / 'status.txt').read_text().strip() if (session_dir / 'status.txt').exists() else 'unknown'}", file=sys.stderr)
        sys.exit(1)

    # Save input.json
    input_data = vars(args).copy()
    input_data.pop("func", None)
    with open(session_dir / "input.json", "w", encoding="utf-8") as f:
        json.dump(input_data, f, ensure_ascii=False, indent=2)

    # Write status.txt
    with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
        f.write("running")

    # Construct worker command
    cmd = [sys.executable] + [arg for arg in sys.argv if arg != "--async-run"]
    env = os.environ.copy()
    env["DATA_AGENT_ASYNC_WORKER"] = "1"
    env["DATA_AGENT_SESSION_ID"] = session_id
    env["DATA_AGENT_AGENT_ID"] = session.agent_id

    # Set additional environment variables if provided
    if additional_env_vars:
        env.update(additional_env_vars)

    # Make sure PYTHONIOENCODING is set so unicode is flushed properly
    env["PYTHONIOENCODING"] = "utf-8"
    # Force unbuffered output so logs appear immediately
    env["PYTHONUNBUFFERED"] = "1"

    # Start the subprocess in background and immediately write the PID
    # Worker will redirect its own stdout to progress.log
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.DEVNULL,  # Worker handles its own logging
        stderr=subprocess.DEVNULL,
        start_new_session=True,
        env=env
    )

    # Write PID immediately after starting the process
    write_worker_pid(session_dir, proc.pid)

    # For true async behavior, we return immediately without waiting
    # The actual logging will be handled by the worker process itself
    print(f"\n✅ Async task started. Session ID: {session_id}")
    print(f"Check progress at: sessions/{session_id}/progress.log")

    sys.exit(0)


def handle_worker_completion(session_dir: Path, need_confirm: bool = False, error: Optional[Exception] = None) -> None:
    """Handle worker process completion by updating status files.

    Args:
        session_dir: Session directory path
        need_confirm: Whether user confirmation is needed
        error: Exception object if there was an error, None otherwise
    """
    if error:
        # Error occurred
        with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
            f.write("failed")
        with open(session_dir / "result.json", "w", encoding="utf-8") as f:
            json.dump({"status": "failed", "error": str(error)}, f)
    elif need_confirm:
        # Need user confirmation
        with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
            f.write("waiting_input")
        with open(session_dir / "result.json", "w", encoding="utf-8") as f:
            json.dump({"status": "waiting_input"}, f)
    else:
        # Success
        with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
            f.write("completed")
        with open(session_dir / "result.json", "w", encoding="utf-8") as f:
            json.dump({"status": "completed"}, f)