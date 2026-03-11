"""Database analysis subcommand (db).

Author: Tinker
Created: 2026-03-04
"""

import argparse
import sys
import os
import json
import subprocess
from pathlib import Path
from typing import Optional

# from cli.notify import push_notification
from cli.worker_lock import check_worker_lock, write_worker_pid, acquire_worker_lock, release_worker_lock
from cli.streaming import StreamState, _print_event, _stream_response, _finalize_stream
from data_agent import (
    DataAgentConfig,
    DataAgentClient,
    SessionManager,
    MessageHandler,
    FileManager,
    DataSource,
    SSEClient,
)


def _build_data_source(args: argparse.Namespace) -> DataSource:
    """Build DataSource from command-line arguments."""
    tables = [t.strip() for t in args.tables.split(",")] if args.tables else []
    table_ids = [t.strip() for t in args.table_ids.split(",")] if args.table_ids else []

    return DataSource(
        dms_instance_id=args.dms_instance_id,
        dms_database_id=args.dms_db_id,
        instance_name=args.instance_name,
        db_name=args.db_name,
        tables=tables,
        table_ids=table_ids,
        engine=args.engine,
        region_id=args.region,
    )


def _db_attach(
    sse_client: SSEClient,
    file_manager: FileManager,
    session,
    from_start: bool = False,
    checkpoint: Optional[int] = None,
    output_mode: str = "summary",
) -> None:
    """Attach to an existing session's SSE stream without sending a message.

    Streams all incoming events to stdout in real-time using the same
    formatting as _stream_response.  Useful for:
      - Watching an ongoing analysis
      - Replaying the last round to review a plan before confirming

    After the stream ends, calls ListFileUpload to download any
    agent-generated report files to sessions/<session_id>/reports/.
    """
    session_id = session.session_id
    session_dir = Path(f"sessions/{session_id}")

    if checkpoint is None:
        checkpoint = 0 if from_start else None

    label = "watching live stream"
    if from_start:
        label = "replaying from start"
    elif checkpoint is not None:
        label = f"resuming from checkpoint {checkpoint}"

    print(f"\nAttaching to session {session_id} ({label})...")
    print(f"Session status: {session.status.value}")
    
    # Special reminder for WAIT_INPUT status
    if session.status.value == "WAIT_INPUT":
        print("\n⚠️  Session is in WAIT_INPUT state.")
        print("   The agent has generated SQL and is waiting for your confirmation.")
        print()
        print("   To view the SQL and confirm:")
        print(f"     python3 skill/data_agent_cli.py attach --session-id {session_id} --from-start")
        print()
        print("   To confirm and execute ONLY this SQL (DO NOT create a new session):")
        print(f"     python3 skill/data_agent_cli.py attach --session-id {session_id} -q '确认执行当前SQL'")
        print()
        print("   To agree to execute all subsequent SQL automatically:")
        print(f"     python3 skill/data_agent_cli.py attach --session-id {session_id} -q '同意后续所有SQL执行'")
        print()
        print("   To modify the query:")
        print(f"     python3 skill/data_agent_cli.py attach --session-id {session_id} -q 'your new question'")
    
    print(f"Output directory: {session_dir.resolve()}")
    print("Press Ctrl+C to detach.")
    print("\n---\n")

    got_content = False
    last_checkpoint = checkpoint if checkpoint else 0
    last_progress_time = 0
    import time

    state = StreamState(output_mode=output_mode)
    state.output_dir = session_dir
    state.session_id = session_id
    state.is_attach = True
    state.session_status = getattr(session, 'status', None)
    if hasattr(state.session_status, 'value'):
        state.session_status = state.session_status.value

    try:
        for event in sse_client.stream_chat_content(
            agent_id=session.agent_id,
            session_id=session_id,
            checkpoint=checkpoint,
        ):
            if event.event_type == "SSE_FINISH":
                break

            # Track checkpoint progress (only in detail/raw mode)
            if output_mode != "summary" and event.checkpoint is not None and event.checkpoint > last_checkpoint:
                current_time = time.time()
                # Show checkpoint progress every 30 seconds or every 50 checkpoints
                if current_time - last_progress_time >= 30 or event.checkpoint - last_checkpoint >= 50:
                    print(f"  [checkpoint: {event.checkpoint}]", flush=True)
                    last_progress_time = current_time
                last_checkpoint = event.checkpoint

            c, _ = _print_event(event, output_mode, state=state)
            if c:
                got_content = True

    except KeyboardInterrupt:
        print("\n\nDetached.")
        return
    except Exception as e:
        # Extract request_id from HTTP error response if available
        request_id = ""
        resp = getattr(e, "response", None)
        if resp is not None:
            request_id = resp.headers.get("x-acs-request-id", "")
        rid_str = f" (Request-Id: {request_id})" if request_id else ""
        print(f"\nError: {e}{rid_str}", file=sys.stderr)
        return

    _finalize_stream(state)
    if state.got_content:
        got_content = True

    if got_content:
        print()  # trailing newline
    else:
        print("(No new events -- session may be waiting for your input)")

    # -- Download agent-generated files via ListFileUpload --
    # Server needs a few seconds to finalize report files after session completes
    print("\nFetching agent-generated files (waiting for server to finalize)...")
    time.sleep(5)
    report_dir = session_dir / "reports"
    total_reports = 0
    for category in ("WebReport", "TextReport", "DefaultArtifact"):
        try:
            files = file_manager.list_files(session_id, file_category=category)
        except Exception as e:
            print(f"  Warning: could not list {category}: {e}", file=sys.stderr)
            continue
        if not files:
            continue
        report_dir.mkdir(parents=True, exist_ok=True)
        for rf in files:
            if not rf.download_url:
                print(f"  Skipping {rf.filename} ({category}): no download URL")
                continue
            save_path = report_dir / (rf.filename or f"{rf.file_id}.bin")
            try:
                file_manager.download_from_url(rf.download_url, str(save_path))
                print(f"  [{category}] saved \u2192 {save_path.resolve()}")
                total_reports += 1
            except Exception as e:
                print(f"  Failed to download {rf.filename} ({category}): {e}", file=sys.stderr)

    if total_reports == 0:
        print("  No report files found for this session.")

    print("\n---\n")
    print(f'> \U0001f4a1 To continue conversation:')
    print(f'>    python3 skill/data_agent_cli.py attach --session-id {session_id} -q "your message"')


def _db_batch(
    message_handler: MessageHandler,
    session,
    data_source: DataSource,
    queries: list,
    output_mode: str = "summary",
    output_dir: Optional[Path] = None,
) -> tuple[bool, bool, str]:
    """Execute batch preset queries."""
    got_content, need_confirm = False, False
    full_text = ""
    for query in queries:
        print(f"\n{'=' * 60}")
        print(f"Query: {query}")
        print("=" * 60)
        c, nc, t = _stream_response(message_handler, session, query, data_source=data_source, output_mode=output_mode, output_dir=output_dir)
        if c: got_content = True
        if t: full_text += f"\n### Query: {query}\n" + t + "\n"
        if nc:
            need_confirm = True
            break
    return got_content, need_confirm, full_text


def _db_single(
    message_handler: MessageHandler,
    session,
    data_source: DataSource,
    query: str,
    output_mode: str = "summary",
    output_dir: Optional[Path] = None,
) -> tuple[bool, bool, str]:
    """Execute a single query with streaming output."""
    print(f"\nAnalyzing...\n")
    got_content, need_confirm, full_text = _stream_response(
        message_handler, session, query, data_source=data_source, output_mode=output_mode, output_dir=output_dir
    )
    if not got_content:
        print("(No response received, please retry)")
    elif need_confirm:
        print("\n\u26a0\ufe0f  \u9700\u8981\u7528\u6237\u786e\u8ba4\uff0c\u7a0b\u5e8f\u5c06\u9000\u51fa\u3002\u8bf7\u5b8c\u6210\u786e\u8ba4\u540e\u4f7f\u7528\u4f1a\u8bddID\u7ee7\u7eed\u5bf9\u8bdd\u3002")
    return got_content, need_confirm, full_text


def cmd_db(args: argparse.Namespace) -> None:
    """Handle db subcommand."""
    # Validate required database parameters
    missing = []
    for attr, name in [
        ("dms_instance_id", "--dms-instance-id"),
        ("dms_db_id", "--dms-db-id"),
        ("instance_name", "--instance-name"),
        ("db_name", "--db-name"),
        ("tables", "--tables"),
    ]:
        if not getattr(args, attr, None):
            missing.append(name)
    if missing:
        print(f"Error: Missing required parameters: {', '.join(missing)}", file=sys.stderr)
        sys.exit(1)

    # Initialize components
    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)
    session_manager = SessionManager(client)
    message_handler = MessageHandler(client)

    # Create new session
    session_mode = args.session_mode.upper()
    data_source = _build_data_source(args)
    is_worker = os.environ.get("DATA_AGENT_ASYNC_WORKER") == "1"

    if getattr(args, "async_run", False) and not is_worker:
        # PARENT PROCESS LOGIC
        enable_search = getattr(args, 'enable_search', False)
        print(f"Creating session for async execution...")
        session = session_manager.create_or_reuse(mode=session_mode, database_id=str(args.dms_db_id), enable_search=enable_search)

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

        # Make sure PYTHONIOENCODING is set so unicode is flushed properly
        env["PYTHONIOENCODING"] = "utf-8"
        # Force unbuffered output so logs appear immediately
        env["PYTHONUNBUFFERED"] = "1"

        # Spawn worker
        log_file = open(session_dir / "progress.log", "w", encoding="utf-8")
        proc = subprocess.Popen(
            cmd,
            stdout=log_file,
            stderr=subprocess.STDOUT,
            start_new_session=True,
            env=env
        )
        write_worker_pid(session_dir, proc.pid)

        print(f"\n✅ Async task started. Session ID: {session_id}")
        print(f"Check progress at: sessions/{session_id}/progress.log")

        # 让 OpenClaw 知道后台进程退出
        # if os.environ.get("OPENCLAW_SHELL") == "exec":
        #     proc.wait()

        sys.exit(0)

    elif is_worker:
        # WORKER PROCESS LOGIC
        session_id = os.environ["DATA_AGENT_SESSION_ID"]
        agent_id = os.environ.get("DATA_AGENT_AGENT_ID", "")
        session_dir = Path(f"sessions/{session_id}")

        print(f"[Worker] Session ID: {session_id}", flush=True)
        print(f"[Worker] Agent ID: {agent_id}", flush=True)
        acquire_worker_lock(session_dir)

        try:
            print("[Worker] Connecting to session...", flush=True)
            # Parent already verified the session is ready (AgentStatus=RUNNING),
            # so skip wait_for_running — DescribeDataAgentSession may report
            # SessionStatus=CREATING for a long time even when the session is
            # fully usable.
            session = session_manager.create_or_reuse(session_id=session_id, agent_id=agent_id, wait_for_running=False)
            print(f"[Worker] Session connected (status={session.status.value})", flush=True)
            output_mode = getattr(args, "output", "summary")
            output_text = ""

            if args.query:
                _, need_confirm, output_text = _db_single(message_handler, session, data_source, args.query, output_mode=output_mode, output_dir=session_dir)
            else:
                if session_mode == "ANALYSIS":
                    default_queries = [
                        f"Analyze the overall data structure and table relationships of {data_source.db_name} database",
                        "Identify key metrics and distribution characteristics in the data",
                    ]
                else:
                    default_queries = [
                        f"What tables exist in {data_source.db_name} database?",
                        "Who has the highest sales?",
                    ]
                _, need_confirm, output_text = _db_batch(message_handler, session, data_source, default_queries, output_mode=output_mode, output_dir=session_dir)

            # Do not write output.md anymore
            # if output_text:
            #     with open(session_dir / "output.md", "w", encoding="utf-8") as f:
            #         f.write(output_text)

            if need_confirm:
                with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
                    f.write("waiting_input")
                with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                    json.dump({
                        "status": "waiting_input",
                        # "output_file": "output.md"
                    }, f)
                # push_notification(session_id, ...)
                sys.exit(0)

            # Success
            with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
                f.write("completed")
            with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                json.dump({
                    "status": "completed",
                    # "output_file": "output.md"
                }, f)

            # push_notification(session_id, f"✅ Data Agent task completed for session {session_id}. Please use `attach` to view details or check `sessions/{session_id}/progress.log`.")

        except Exception as e:
            # Error
            with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
                f.write("failed")
            with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                json.dump({"status": "failed", "error": str(e)}, f)

            # push_notification(session_id, f"❌ Data Agent task failed for session {session_id}: {str(e)}")
        finally:
            release_worker_lock(session_dir)

        sys.exit(0)

    # NORMAL SYNCHRONOUS LOGIC
    mode_desc = {
        "ASK_DATA": "ASK_DATA mode (SQL query + natural language response)",
        "ANALYSIS": "ANALYSIS mode (deep analysis + report generation)",
        "INSIGHT": "INSIGHT mode",
    }.get(session_mode, session_mode)

    print(f"Creating session: {mode_desc}...")
    print(f"  Region: {config.region}")
    enable_search = getattr(args, 'enable_search', False)
    session = session_manager.create_or_reuse(mode=session_mode, database_id=str(args.dms_db_id), enable_search=enable_search)
    print(f"Session ready: {session.session_id}")
    print(f"\n\U0001f4a1 Tip: To continue this session later, use: python3 skill/data_agent_cli.py attach --session-id {session.session_id}")

    # Execute query
    output_mode = getattr(args, "output", "summary")
    session_dir = Path(f"sessions/{session.session_id}")
    session_dir.mkdir(parents=True, exist_ok=True)

    if args.query:
        _, _, output_text = _db_single(message_handler, session, data_source, args.query, output_mode=output_mode, output_dir=session_dir)
    else:
        # Default batch preset queries
        if session_mode == "ANALYSIS":
            default_queries = [
                f"Analyze the overall data structure and table relationships of {data_source.db_name} database",
                "Identify key metrics and distribution characteristics in the data",
            ]
        else:
            default_queries = [
                f"What tables exist in {data_source.db_name} database?",
                "Who has the highest sales?",
            ]
        print(f"\nNo query specified, running preset queries ({len(default_queries)} total)...")
        _, _, output_text = _db_batch(message_handler, session, data_source, default_queries, output_mode=output_mode, output_dir=session_dir)

    # Write output to output.md is disabled
    # if output_text:
    #     with open(session_dir / "output.md", "w", encoding="utf-8") as f:
    #         f.write(output_text)
    #     with open(session_dir / "result.json", "w", encoding="utf-8") as f:
    #         json.dump({"status": "completed", "output_file": "output.md"}, f)
    with open(session_dir / "result.json", "w", encoding="utf-8") as f:
        json.dump({"status": "completed"}, f)
