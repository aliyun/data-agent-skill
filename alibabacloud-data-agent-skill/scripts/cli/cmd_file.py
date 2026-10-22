"""File analysis subcommand (file).

A workflow layer on top of the shared send message path (same architecture as
db): resolve the file (upload a local file or take --file-id), then the first
message — carrying the file as a session-level remote_data_center DataSources
entry — is sent by ``cli.cmd_send.start_session_with_message`` and creates the
session implicitly. No CreateDataAgentSession round-trip.

Author: Tinker
Created: 2026-03-04
"""

import argparse
import sys
import json
from pathlib import Path
from typing import Optional

from cli.worker_utils import is_worker_process, setup_async_worker, build_send_options
from cli.streaming_utils import run_worker_with_handler
from cli.streaming import _stream_response, _stream_existing_response
from cli.cmd_send import start_session_with_message
from data_agent import (
    DataAgentConfig,
    DataAgentClient,
    MessageHandler,
    FileManager,
    SSEClient,
)


_FILE_PRESET_QUERIES = [
    "请分析上传文件的数据结构",
    "数据的关键统计指标和分布情况是什么？",
    "数据中是否存在异常值或离群点？",
]


def _resolve_file_queries(args: argparse.Namespace) -> list:
    """Return the queries to run: the user query, or the presets when omitted."""
    if args.query:
        return [args.query]
    return list(_FILE_PRESET_QUERIES)


def _consume_first_response(
    client: DataAgentClient,
    session,
    output_mode: str,
    session_dir: Path,
) -> tuple[bool, bool, Optional[str]]:
    """Consume the in-flight response of the first direct-sent message.

    Returns (got_content, need_confirm, task_failed); prints the standard notices.
    """
    print("\nAnalyzing...\n")
    got_content, need_confirm, task_failed, _ = _stream_existing_response(
        # SSEClient takes the DataAgentConfig, not the client
        SSEClient(client._config), session,
        output_mode=output_mode, output_dir=session_dir, checkpoint=0,
    )
    if not got_content:
        print("(No response received, please retry)")
    elif need_confirm:
        print("\n⚠️  Agent has created an execution plan. User confirmation required.")
        print(f"   To continue: python3 scripts/data_agent_cli.py attach --session-id {session.session_id} -q 'User Input'")
    return got_content, need_confirm, task_failed


def _resolve_file_id(args: argparse.Namespace, file_manager: FileManager) -> str:
    """Return the Data Center file ID, uploading the local file when needed."""
    file_id = getattr(args, "file_id", None)
    if file_id:
        print(f"Using Data Center file ID: {file_id}")
        return file_id

    file_path = args.file_path
    if not Path(file_path).exists():
        print(f"Error: File not found: {file_path}", file=sys.stderr)
        sys.exit(1)
    if not file_manager.is_supported_file(file_path):
        print("Error: Unsupported file type. Supported formats: .csv, .xlsx, .xls", file=sys.stderr)
        sys.exit(1)

    print(f"Uploading file: {file_path}")
    try:
        file_info = file_manager.upload_file(file_path)
    except Exception as e:
        print(f"Upload failed: {e}", file=sys.stderr)
        sys.exit(1)
    print("File uploaded successfully!")
    print(f"  File ID : {file_info.file_id}")
    print(f"  Filename: {file_info.filename}")
    print(f"  Size    : {file_info.size} bytes")
    return file_info.file_id


def cmd_file(args: argparse.Namespace) -> None:
    """Handle file subcommand."""
    file_id = getattr(args, 'file_id', None)
    file_path = getattr(args, 'file_path', None)

    if not file_id and not file_path:
        print("Error: Either FILE path or --file-id must be provided", file=sys.stderr)
        sys.exit(1)
    if file_id and file_path:
        print("Error: Cannot specify both FILE path and --file-id", file=sys.stderr)
        sys.exit(1)

    config = DataAgentConfig.from_env()
    if getattr(args, 'custom_agent_id', None):
        config.custom_agent_id = args.custom_agent_id
    client = DataAgentClient(config)
    file_manager = FileManager(client)

    is_worker = is_worker_process()

    # Resolve the file and put its ID on the args slot that build_send_options
    # reads (--data-source), so the file rides the unified session-level
    # DataSources list of the first message. Uploaded only in the parent / sync
    # process; the worker re-derives everything from argv + session state.
    if not is_worker:
        args.data_source = _resolve_file_id(args, file_manager)

    send_options = build_send_options(args)
    # Data sources are session-level and merge server-side, so only the first
    # message of the session carries them; follow-ups go bare.
    followup_options = {k: v for k, v in send_options.items() if k != "data_sources"}
    message_handler = MessageHandler(client, followup_options)

    session_mode = args.session_mode
    queries = _resolve_file_queries(args)

    if getattr(args, "async_run", False) and not is_worker:
        # PARENT PROCESS LOGIC — the shared send path delivers the first
        # message (creating the session implicitly, file attached); the worker
        # consumes its response instead of re-sending.
        print(f"Region: {config.region}")
        session = start_session_with_message(
            client, config, args, queries[0],
            mode=session_mode,
            plan_mode=getattr(args, 'plan_mode', 'force'),
            enable_search=getattr(args, 'enable_search', False),
        )
        setup_async_worker(args, session)

    elif is_worker:
        # WORKER PROCESS LOGIC — consume the parent-sent first response, then
        # stream the remaining queries.
        def file_query_executor(message_handler, session, args):
            message_handler.set_send_options(followup_options)
            output_mode = getattr(args, "output", "summary")
            session_dir = Path(f"sessions/{session.session_id}")

            print(f"\n{'=' * 60}")
            print("File Analysis")
            print("=" * 60)

            got_content, need_confirm, task_failed = _consume_first_response(
                client, session, output_mode, session_dir
            )
            if need_confirm or task_failed:
                return got_content, need_confirm, task_failed

            for query in queries[1:]:
                print(f"\nQuery: {query}")
                print("-" * 50)
                c, nc, tf, _ = _stream_response(
                    message_handler, session, query,
                    output_mode=output_mode, output_dir=session_dir,
                )
                got_content = got_content or c
                task_failed = task_failed or tf
                if nc or tf:
                    return got_content, nc, task_failed

            if args.list_generated_files:
                _print_generated_files(file_manager, session.session_id)

            return got_content, False, task_failed

        run_worker_with_handler(args, data_source=None, query_execution_func=file_query_executor)

    # NORMAL SYNCHRONOUS LOGIC
    mode_desc = {
        "auto": "auto mode (backend intelligent decision)",
        "lite": "lite mode (quick query)",
        "pro": "pro mode (deep analysis + report generation)",
        "ultra": "ultra mode (most thorough analysis)",
    }.get(session_mode, session_mode)

    print(f"Starting file analysis ({mode_desc})...")
    print(f"  Region: {config.region}")
    session = start_session_with_message(
        client, config, args, queries[0],
        mode=session_mode,
        plan_mode=getattr(args, 'plan_mode', 'force'),
        enable_search=getattr(args, 'enable_search', False),
    )
    print(f"Session ready: {session.session_id}")
    print(f"\n💡 Tip: To continue this session later, use: python3 scripts/data_agent_cli.py attach --session-id {session.session_id}")

    output_mode = getattr(args, "output", "summary")
    session_dir = Path(f"sessions/{session.session_id}")
    session_dir.mkdir(parents=True, exist_ok=True)

    # Initialize structured logging for sync mode
    from cli.streaming import init_structured_logging, close_structured_logging
    init_structured_logging(session_dir)

    try:
        print(f"\n{'=' * 60}")
        print("File Analysis")
        print("=" * 60)

        got_content, need_confirm, task_failed = _consume_first_response(
            client, session, output_mode, session_dir
        )
        if need_confirm:
            with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                json.dump({"status": "waiting_input"}, f)
            return
        if task_failed:
            with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                json.dump({"status": "failed", "error": task_failed}, f, ensure_ascii=False)
            sys.exit(1)

        for query in queries[1:]:
            print(f"\nQuery: {query}")
            print("-" * 50)
            try:
                got_content, need_confirm, tf, _ = _stream_response(
                    message_handler, session, query,
                    output_mode=output_mode, output_dir=session_dir,
                )
                task_failed = task_failed or tf
                if need_confirm:
                    with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                        json.dump({"status": "waiting_input"}, f)
                    return
                if task_failed:
                    with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                        json.dump({"status": "failed", "error": task_failed}, f, ensure_ascii=False)
                    sys.exit(1)
            except Exception as e:
                print(f"Request failed: {e}")

        if args.list_generated_files:
            _print_generated_files(file_manager, session.session_id)
    finally:
        close_structured_logging()

    # Write result status
    with open(session_dir / "result.json", "w", encoding="utf-8") as f:
        json.dump({"status": "completed"}, f)


def _print_generated_files(file_manager: FileManager, session_id: str) -> None:
    """Print list of files generated by the Agent."""
    print(f"\n{'=' * 60}")
    print("Generated Files")
    print("=" * 60)
    try:
        generated = file_manager.list_files(session_id)
        if generated:
            for f in generated:
                print(f"  - {f.filename} ({f.file_type}, {f.size} bytes)")
                if f.download_url:
                    print(f"    Download: {f.download_url}")
        else:
            print("  No generated files.")
    except Exception as e:
        print(f"  Failed to get file list: {e}")
