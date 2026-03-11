"""Attach to existing session subcommand (attach).

Author: Tinker
Created: 2026-03-04
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

from cli.streaming import _stream_response
from cli.cmd_db import _db_attach
# from cli.notify import push_notification
from cli.worker_lock import check_worker_lock, write_worker_pid, acquire_worker_lock, release_worker_lock
from data_agent import (
    DataAgentConfig,
    DataAgentClient,
    SessionManager,
    MessageHandler,
    FileManager,
    SSEClient,
)


def cmd_attach(args: argparse.Namespace) -> None:
    """Connect to an existing session for continuing analysis or confirming plan."""
    session_id = args.session_id
    is_worker = os.environ.get("DATA_AGENT_ASYNC_WORKER") == "1"
    async_run = getattr(args, "async_run", True)

    # Initialize components
    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)
    session_manager = SessionManager(client)
    message_handler = MessageHandler(client)
    sse_client = SSEClient(config)
    file_manager = FileManager(client)

    # Async mode only applies when a query is provided
    if async_run and args.query and not is_worker:
        # PARENT PROCESS: spawn background worker
        print(f"Connecting to session: {session_id}")
        try:
            session = client.describe_session(agent_id="", session_id=session_id)
        except Exception as e:
            print(f"Error: Failed to connect to session: {e}", file=sys.stderr)
            sys.exit(1)

        session_dir = Path(f"sessions/{session.session_id}")
        session_dir.mkdir(parents=True, exist_ok=True)

        # Check for existing worker
        existing_pid = check_worker_lock(session_dir)
        if existing_pid:
            print(f"⚠️  A worker process (PID {existing_pid}) is already running for session {session.session_id}.", file=sys.stderr)
            print(f"   Check progress: cat sessions/{session.session_id}/progress.log", file=sys.stderr)
            print(f"   Current status: {(session_dir / 'status.txt').read_text().strip() if (session_dir / 'status.txt').exists() else 'unknown'}", file=sys.stderr)
            sys.exit(1)

        with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
            f.write("running")

        # Construct worker command (strip --async-run if explicit)
        cmd = [sys.executable] + [arg for arg in sys.argv if arg != "--async-run"]
        env = os.environ.copy()
        env["DATA_AGENT_ASYNC_WORKER"] = "1"
        env["DATA_AGENT_SESSION_ID"] = session.session_id
        env["DATA_AGENT_AGENT_ID"] = session.agent_id
        env["PYTHONIOENCODING"] = "utf-8"
        env["PYTHONUNBUFFERED"] = "1"

        log_file = open(session_dir / "progress.log", "w", encoding="utf-8")
        proc = subprocess.Popen(
            cmd,
            stdout=log_file,
            stderr=subprocess.STDOUT,
            start_new_session=True,
            env=env,
        )
        write_worker_pid(session_dir, proc.pid)

        print(f"\n✅ Async task started. Session ID: {session.session_id}")
        print(f"Check progress at: sessions/{session.session_id}/progress.log")

        # 让 OpenClaw 知道后台进程退出
        # if os.environ.get("OPENCLAW_SHELL") == "exec":
        #     proc.wait()

        sys.exit(0)

    elif is_worker and args.query:
        # WORKER PROCESS: send query in background
        session_id = os.environ.get("DATA_AGENT_SESSION_ID", session_id)
        agent_id = os.environ.get("DATA_AGENT_AGENT_ID", "")
        session_dir = Path(f"sessions/{session_id}")

        print(f"[Worker] Session ID: {session_id}", flush=True)
        print(f"[Worker] Agent ID: {agent_id}", flush=True)
        acquire_worker_lock(session_dir)

        try:
            print("[Worker] Connecting to session...", flush=True)
            session = session_manager.create_or_reuse(
                session_id=session_id, agent_id=agent_id, wait_for_running=False,
            )
            print(f"[Worker] Session connected (status={session.status.value})", flush=True)

            query = args.query
            output_mode = getattr(args, "output", "summary")

            print(f"\n> User Query: {query}\n", flush=True)
            got_content, need_confirm, output_text = _stream_response(
                message_handler, session, query,
                output_mode=output_mode, output_dir=session_dir,
            )

            # Do not write output.md anymore
            # if output_text:
            #     with open(session_dir / "output.md", "w", encoding="utf-8") as f:
            #         f.write(output_text + "\n")

            # Determine final status
            if need_confirm:
                final_status = "waiting_input"
            else:
                final_status = "completed"

            with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
                f.write(final_status)
            with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                json.dump({
                    "status": final_status,
                    "session_id": session_id,
                    # "output_file": "output.md" if output_text else None,
                }, f, ensure_ascii=False, indent=2)

            if need_confirm:
                # push_notification(session_id, (
                #     f"⚠️ Data Agent session {session_id} is waiting for your input (worker has exited).\n"
                #     f"Please review the pending content: cat sessions/{session_id}/output.md\n"
                #     f"Then confirm: python3 data_agent_cli.py attach --session-id {session_id} -q '确认执行'"
                # ))
                pass
            else:
                # push_notification(session_id, f"✅ Data Agent task completed for session {session_id}. Check `sessions/{session_id}/output.md` for results.")
                pass

        except Exception as e:
            with open(session_dir / "status.txt", "w", encoding="utf-8") as f:
                f.write("failed")
            with open(session_dir / "result.json", "w", encoding="utf-8") as f:
                json.dump({"status": "failed", "error": str(e)}, f, ensure_ascii=False, indent=2)
            print(f"Error: {e}", file=sys.stderr, flush=True)
            # push_notification(session_id, f"❌ Data Agent task failed for session {session_id}: {str(e)}")
        finally:
            release_worker_lock(session_dir)

        sys.exit(0)

    else:
        # SYNCHRONOUS MODE (--no-async-run, or no query, or live stream)
        print(f"Connecting to session: {session_id}")
        print(f"  Region: {config.region}")
        try:
            session = client.describe_session(agent_id="", session_id=session_id)
            rid = f", request_id: {session.request_id}" if session.request_id else ""
            print(f"Session connected: {session.session_id} (agent: {session.agent_id}, status: {session.status.value}{rid})")
        except Exception as e:
            print(f"Error: Failed to connect to session: {e}", file=sys.stderr)
            sys.exit(1)

        output_mode = getattr(args, "output", "summary")
        session_dir = Path(f"sessions/{session.session_id}")

        if args.query:
            query = args.query
            print(f"\n> User Query: {query}\n")
            try:
                got_content, need_confirm, _ = _stream_response(
                    message_handler, session, query,
                    output_mode=output_mode, output_dir=session_dir,
                )
                if not got_content:
                    print("(No response received, please retry)")
                elif need_confirm:
                    print("\n⚠️  Agent has created an execution plan. User confirmation required.")
                    print(f"   To continue: python3 skill/data_agent_cli.py attach --session-id {session.session_id} -q 'your input'")
                else:
                    try:
                        updated_session = client.describe_session(agent_id=session.agent_id, session_id=session.session_id)
                        if updated_session.status.value == "WAIT_INPUT":
                            print("\n⚠️  Agent has created an execution plan and is waiting for confirmation.")
                            print("   Use -q option to confirm the plan or provide feedback:")
                            print(f"     python3 skill/data_agent_cli.py attach --session-id {session.session_id} -q 'confirm'")
                            print(f"     python3 skill/data_agent_cli.py attach --session-id {session.session_id} -q 'modify the plan'")
                    except Exception:
                        pass
            except Exception as e:
                print(f"Request failed: {e}")
        else:
            from_start = getattr(args, "from_start", False)
            checkpoint = getattr(args, "checkpoint", None)
            _db_attach(sse_client, file_manager, session, from_start=from_start, checkpoint=checkpoint, output_mode=output_mode)
