"""Cancel session subcommand.

Author: Tinker
Created: 2026-10-13
"""

import argparse
import sys

from data_agent import (
    DataAgentConfig,
    DataAgentClient,
)


def cmd_cancel(args: argparse.Namespace) -> None:
    """Cancel a running Data Agent session by sending a cancel message."""
    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)

    print(f"Cancelling session: {args.session_id}")
    try:
        session = client.describe_session(agent_id="", session_id=args.session_id)
    except Exception as e:
        print(f"Error: Failed to describe session: {e}", file=sys.stderr)
        sys.exit(1)

    if not session.agent_id:
        print(
            f"Error: No agent ID for session {args.session_id}. "
            "The session may not exist or does not belong to the current account.",
            file=sys.stderr,
        )
        sys.exit(1)

    print(f"  Agent ID: {session.agent_id} (status: {session.status.value})")

    try:
        resp = client.send_message(
            agent_id=session.agent_id,
            session_id=session.session_id,
            message="cancel",
            message_type="cancel",
            workspace_id=getattr(args, "workspace_id", None) or "",
        )
    except Exception as e:
        print(f"Error: Failed to send cancel message: {e}", file=sys.stderr)
        sys.exit(1)

    request_id = None
    if isinstance(resp, dict):
        request_id = resp.get("RequestId") or resp.get("requestId")
        success = resp.get("Success", resp.get("success"))
        if str(success).lower() != "true":
            error_code = resp.get("ErrorCode") or resp.get("errorCode")
            error_message = resp.get("ErrorMessage") or resp.get("errorMessage")
            print(f"  Business failure: {error_code} {error_message}")
            if request_id:
                print(f"  RequestId: {request_id}")
            sys.exit(1)

    print("  Cancel message accepted.")
    if request_id:
        print(f"  RequestId: {request_id}")
    print(f"Session {args.session_id} cancellation requested. Use attach to verify the final status.")
    sys.exit(0)
