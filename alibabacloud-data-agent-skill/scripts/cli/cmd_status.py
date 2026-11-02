"""Session status subcommand (status).

A thin read-only primitive on top of DescribeDataAgentSession: one call, no
SSE, no streaming, no file downloads. Prints the current session state and,
for WAIT_INPUT sessions, the same confirmation guidance the attach flow
prints. Complements the ``--poll`` flag of ``send`` (which waits for the
message it just sent) rather than replacing it.

Author: Tinker
Created: 2026-10-21
"""

import argparse
import sys
from pathlib import Path

from data_agent import (
    DataAgentConfig,
    DataAgentClient,
)


def cmd_status(args: argparse.Namespace) -> None:
    """Print the current status of a Data Agent session."""
    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)

    session_id = args.session_id
    print(f"Session: {session_id}")
    try:
        session = client.describe_session(
            session_id=session_id,
            agent_id=getattr(args, "agent_id", None) or "",
            workspace_id=getattr(args, "workspace_id", None) or "",
        )
    except Exception as e:
        print(f"Error: Failed to describe session: {e}", file=sys.stderr)
        sys.exit(1)

    status = session.status.value if hasattr(session.status, "value") else session.status
    raw_status = getattr(session, "raw_status", None)
    if raw_status and raw_status.strip().upper() != status:
        print(f"  Status:  {status} (server: {raw_status})")
    else:
        print(f"  Status:  {status}")
    if session.title:
        print(f"  Title:   {session.title}")
    if session.agent_id:
        print(f"  Agent:   {session.agent_id}")
    if session.mode:
        print(f"  Mode:    {session.mode}")
    if session.workspace_id:
        print(f"  Workspace: {session.workspace_id}")
    if session.request_id:
        print(f"  RequestId: {session.request_id}")

    # Local async-worker hint: db/file --async-run sessions record their
    # worker state in sessions/<id>/status.txt relative to the CLI working
    # directory. Read-only convenience; the describe status above is
    # authoritative.
    worker_status_file = Path(f"sessions/{session_id}/status.txt")
    if worker_status_file.exists():
        worker_status = worker_status_file.read_text(encoding="utf-8").strip()
        print(f"  Local async worker: {worker_status}")

    if status == "WAIT_INPUT":
        print("\n⚠️  Session is in WAIT_INPUT state.")
        print("   The agent has generated SQL/a plan and is waiting for your confirmation.")
        print()
        print("   To view the pending plan or SQL:")
        print(f"     python3 scripts/data_agent_cli.py attach --session-id {session_id} --from-start")
        print()
        print("   To confirm and execute ONLY this SQL (DO NOT create a new session):")
        print(f"     python3 scripts/data_agent_cli.py attach --session-id {session_id} -q '确认执行当前SQL'")
        print()
        print("   To agree to execute all subsequent SQL automatically:")
        print(f"     python3 scripts/data_agent_cli.py attach --session-id {session_id} -q '同意后续所有SQL执行'")
