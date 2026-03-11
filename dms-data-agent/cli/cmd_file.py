"""File analysis subcommand (file).

Author: Tinker
Created: 2026-03-04
"""

import argparse
import sys
import os
import json
import subprocess
from pathlib import Path

# from cli.notify import push_notification
from cli.worker_lock import check_worker_lock, write_worker_pid, acquire_worker_lock, release_worker_lock
from cli.streaming import _stream_response
from data_agent import (
    DataAgentConfig,
    DataAgentClient,
    SessionManager,
    MessageHandler,
    FileManager,
    DataSource,
)


def cmd_file(args: argparse.Namespace) -> None:
    """Handle file subcommand."""
    file_path = args.file_path

    # Validate file exists (before any API initialization)
    if not Path(file_path).exists():
        print(f"Error: File not found: {file_path}", file=sys.stderr)
        sys.exit(1)

    # Initialize components
    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)
    session_manager = SessionManager(client)
    message_handler = MessageHandler(client)
    file_manager = FileManager(client)

    # Validate file type
    if not file_manager.is_supported_file(file_path):
        supported = ".csv, .xlsx, .xls"
        print(f"Error: Unsupported file type. Supported formats: {supported}", file=sys.stderr)
        sys.exit(1)

    is_worker = os.environ.get("DATA_AGENT_ASYNC_WORKER") == "1"
    session_mode = args.session_mode.upper()

    if getattr(args, "async_run", False) and not is_worker:
        # PARENT PROCESS LOGIC
        enable_search = getattr(args, 'enable_search', False)
        print(f"Creating session for async file analysis...")
        session = session_manager.create_or_reuse(mode=session_mode, enable_search=enable_search)

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

        input_data = vars(args).copy()
        input_data.pop("func", None)
        with open(session_dir / "input.json", "w", encoding="utf-8") as f:
            json.dump(input_data, f, ensure_ascii=False, indent=2)

        with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
            f.write("running")

        cmd = [sys.executable] + [arg for arg in sys.argv if arg != "--async-run"]
        env = os.environ.copy()
        env["DATA_AGENT_ASYNC_WORKER"] = "1"
        env["DATA_AGENT_SESSION_ID"] = session_id
        env["DATA_AGENT_AGENT_ID"] = session.agent_id

        # Make sure PYTHONIOENCODING is set so unicode is flushed properly
        env["PYTHONIOENCODING"] = "utf-8"
        # Force unbuffered output so logs appear immediately
        env["PYTHONUNBUFFERED"] = "1"

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
            print(f"Uploading file: {file_path}")
            file_info = file_manager.upload_file(file_path)

            print(f"File uploaded successfully!")
            print(f"  File ID : {file_info.file_id}")
            print(f"  Filename: {file_info.filename}")
            print(f"  Size    : {file_info.size} bytes")

            # Parent already verified the session is ready, skip wait_for_running
            session = session_manager.create_or_reuse(session_id=session_id, agent_id=agent_id, wait_for_running=False)
            file_data_source = DataSource(
                data_source_type="FILE",
                file_id=file_info.file_id,
            )

            output_mode = getattr(args, "output", "summary")

            if args.query:
                queries = [args.query]
            else:
                queries = [
                    "请分析上传文件的数据结构",
                    "数据的关键统计指标和分布情况是什么？",
                    "数据中是否存在异常值或离群点？",
                ]

            print(f"\n{'=' * 60}")
            print("File Analysis")
            print("=" * 60)

            output_text = ""
            for query in queries:
                print(f"\nQuery: {query}")
                print("-" * 50)
                got_content, need_confirm, t = _stream_response(
                    message_handler, session, query,
                    data_source=file_data_source, output_mode=output_mode,
                    output_dir=session_dir,
                )
                if t:
                    output_text += f"\n### Query: {query}\n" + t + "\n"

                if need_confirm:
                    if output_text:
                        # with open(session_dir / "output.md", "w", encoding="utf-8") as f:
                        #     f.write(output_text)
                        with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                            json.dump({"status": "waiting_input"}, f)
                    else:
                        with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
                            f.write("waiting_input")
                    # push_notification(session_id, ...)
                    sys.exit(0)

            if args.list_generated_files:
                _print_generated_files(file_manager, session.session_id)

            # if output_text:
            #     with open(session_dir / "output.md", "w", encoding="utf-8") as f:
            #         f.write(output_text)

            # Success
            with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
                f.write("completed")
            with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                json.dump({
                    "status": "completed",
                    # "output_file": "output.md"
                }, f)

            # push_notification(session_id, f"✅ Data Agent File analysis completed for session {session_id}. Please use `attach` to view details or check `sessions/{session_id}/progress.log`.")

        except Exception as e:
            # Error
            with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
                f.write("failed")
            with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                json.dump({"status": "failed", "error": str(e)}, f)

            # push_notification(session_id, f"❌ Data Agent File analysis failed for session {session_id}: {str(e)}")
        finally:
            release_worker_lock(session_dir)

        sys.exit(0)

    # NORMAL SYNCHRONOUS LOGIC
    # Upload file
    print(f"Uploading file: {file_path}")
    try:
        file_info = file_manager.upload_file(file_path)
    except Exception as e:
        print(f"Upload failed: {e}", file=sys.stderr)
        sys.exit(1)

    print(f"File uploaded successfully!")
    print(f"  File ID : {file_info.file_id}")
    print(f"  Filename: {file_info.filename}")
    print(f"  Size    : {file_info.size} bytes")

    # Create session
    session_mode = args.session_mode.upper()
    mode_desc = {
        "ASK_DATA": "ASK_DATA mode",
        "ANALYSIS": "ANALYSIS mode (recommended for file analysis)",
        "INSIGHT": "INSIGHT mode",
    }.get(session_mode, session_mode)

    print(f"\nCreating session: {mode_desc}...")
    print(f"  Region: {config.region}")
    enable_search = getattr(args, 'enable_search', False)
    session = session_manager.create_or_reuse(mode=session_mode, enable_search=enable_search)
    print(f"Session ready: {session.session_id}")
    print(f"\n\U0001f4a1 Tip: To continue this session later, use: python3 skill/data_agent_cli.py attach --session-id {session.session_id}")

    # Create DataSource for file analysis
    file_data_source = DataSource(
        data_source_type="FILE",
        file_id=file_info.file_id,
    )

    # Get output mode
    output_mode = getattr(args, "output", "summary")
    session_dir = Path(f"sessions/{session.session_id}")
    session_dir.mkdir(parents=True, exist_ok=True)

    # Determine queries to execute
    if args.query:
        queries = [args.query]
    else:
        # Default preset analysis questions
        queries = [
            "\u8bf7\u5206\u6790\u4e0a\u4f20\u6587\u4ef6\u7684\u6570\u636e\u7ed3\u6784",
            "\u6570\u636e\u7684\u5173\u952e\u7edf\u8ba1\u6307\u6807\u548c\u5206\u5e03\u60c5\u51b5\u662f\u4ec0\u4e48\uff1f",
            "\u6570\u636e\u4e2d\u662f\u5426\u5b58\u5728\u5f02\u5e38\u503c\u6216\u79bb\u7fa4\u70b9\uff1f",
        ]

    # Execute queries
    print(f"\n{'=' * 60}")
    print("File Analysis")
    print("=" * 60)

    output_text = ""
    for query in queries:
        print(f"\nQuery: {query}")
        print("-" * 50)
        try:
            got_content, need_confirm, t = _stream_response(
                message_handler, session, query,
                data_source=file_data_source, output_mode=output_mode,
                output_dir=session_dir,
            )
            if t:
                output_text += f"\n### Query: {query}\n" + t + "\n"

            if not got_content:
                print("(No response received, please retry)")
            elif need_confirm:
                print("\n\u26a0\ufe0f  Agent has created an execution plan. User confirmation required.")
                print(f"   To continue: python3 skill/data_agent_cli.py attach --session-id {session.session_id} -q 'User Input' ")
                if output_text:
                    with open(session_dir / "output.md", "w", encoding="utf-8") as f:
                        f.write(output_text)
                    with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                        json.dump({"status": "waiting_input", "output_file": "output.md"}, f)
                return
        except Exception as e:
            print(f"Request failed: {e}")

    # List generated files if requested
    if args.list_generated_files:
        _print_generated_files(file_manager, session.session_id)

    # Write output to output.md is disabled
    # if output_text:
    #     with open(session_dir / "output.md", "w", encoding="utf-8") as f:
    #         f.write(output_text)
    #     with open(session_dir / "result.json", "w", encoding="utf-8") as f:
    #         json.dump({"status": "completed", "output_file": "output.md"}, f)
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
