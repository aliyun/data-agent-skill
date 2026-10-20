"""Lightweight message send subcommand (send).

Author: Tinker
Created: 2026-10-13
"""

import argparse
import sys
import time
from typing import Optional

from cli.worker_utils import build_send_options
from data_agent import (
    DataAgentConfig,
    DataAgentClient,
)


_TERMINAL_SESSION_STATUS = {"FINISHED", "CANCELED", "CANCELLED", "ERROR", "FAILED"}
_POLL_INTERVAL_SECONDS = 5
_IDLE_CONFIRMATIONS = 2


def _print_send_result(resp: dict) -> bool:
    """Print the send response. Returns True on business success."""
    request_id = resp.get("requestId") or resp.get("RequestId")
    success = str(resp.get("success", resp.get("Success", ""))).lower()
    if success != "true":
        error_code = resp.get("errorCode") or resp.get("ErrorCode")
        error_message = resp.get("errorMessage") or resp.get("ErrorMessage")
        print(f"  Business failure: {error_code} {error_message}")
        if request_id:
            print(f"  RequestId: {request_id}")
            print("  The request reached the gateway; the failure is business/permission level.")
        return False
    print("  Message accepted.")
    if request_id:
        print(f"  RequestId: {request_id}")
    return True


def _poll_session_completion(
    client: DataAgentClient,
    session_id: str,
    agent_id: str,
    workspace_id: str,
    timeout: int,
) -> str:
    """Poll DescribeDataAgentSession until the session completes.

    Completion is two consecutive IDLE polls (aligned with the POC skill
    semantics) or any non-IDLE terminal status. Returns the final
    session_status, or "TIMEOUT:<last>" when the timeout elapses.
    """
    deadline = time.time() + timeout
    idle_count = 0
    last_status = ""
    while time.time() < deadline:
        try:
            session = client.describe_session(
                agent_id=agent_id, session_id=session_id, workspace_id=workspace_id
            )
            status = str(getattr(session.status, "value", session.status))
        except Exception as e:
            print(f"  [poll] describe failed: {e}")
            status = last_status
        if status and status != last_status:
            print(f"  [poll] {time.strftime('%H:%M:%S')} session_status={status}")
            last_status = status
        upper = status.upper()
        if upper in _TERMINAL_SESSION_STATUS:
            return upper
        if upper == "IDLE":
            idle_count += 1
            if idle_count >= _IDLE_CONFIRMATIONS:
                return upper
        else:
            idle_count = 0
        time.sleep(_POLL_INTERVAL_SECONDS)
    return f"TIMEOUT:{last_status}"


def _resolve_reuse_agent(
    client: DataAgentClient,
    config: DataAgentConfig,
    session_id: str,
    agent_id: str,
) -> str:
    """Resolve the agent ID for a reused session, exiting on failure."""
    if agent_id:
        return agent_id
    try:
        session = client.describe_session(agent_id="", session_id=session_id)
    except Exception as e:
        print(f"Error: Failed to describe session: {e}", file=sys.stderr)
        sys.exit(1)
    agent_id = session.agent_id
    if not agent_id:
        print(
            f"Error: No agent ID for session {session_id}; "
            "it may not exist or not belong to the current account.",
            file=sys.stderr,
        )
        if config.using_credentials_file:
            print(
                f"Current credentials: {config.credentials_env}/{config.credentials_profile}. "
                "If the session belongs to another account, switch via "
                "DATA_AGENT_CREDENTIALS_ENV / DATA_AGENT_CREDENTIALS_PROFILE.",
                file=sys.stderr,
            )
        sys.exit(1)
    return agent_id


def _precreate_session(
    client: DataAgentClient,
    workspace_id: str,
    custom_agent_id: Optional[str] = None,
    custom_agent_stage: Optional[str] = None,
    title: Optional[str] = None,
):
    """Pre-create the session via CreateDataAgentSession; returns (session_id, agent_id)."""
    print("Creating session...")
    try:
        session = client.create_session(
            workspace_id=workspace_id or None,
            title=title or "data-agent-session",
            custom_agent_id=custom_agent_id,
            custom_agent_stage=custom_agent_stage or "prod",
        )
    except Exception as e:
        print(f"Error: Failed to create session: {e}", file=sys.stderr)
        sys.exit(1)
    print(f"  Session ID: {session.session_id}")
    print(f"  Agent ID:   {session.agent_id}")
    return session.session_id, session.agent_id


def cmd_send(args: argparse.Namespace) -> None:
    """Send a single message: direct send, custom-agent direct send, or session reuse.

    Without --session-id the message is sent directly and the backend creates
    the session; the new SessionId is parsed from the SendChatMessage response.
    With AK/SK auth a --custom-agent-id is carried in SessionConfig directly —
    no CreateDataAgentSession round-trip. The API_KEY path still pre-creates
    the session because the backend does not fully support implicit creation
    with a custom agent binding there yet. Session titles are not supported;
    use legacy-send when a title is genuinely required.
    """
    if not args.message:
        print("Error: --message/-m is required", file=sys.stderr)
        sys.exit(1)

    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)

    session_id = args.session_id
    agent_id = args.agent_id
    workspace_id = getattr(args, "workspace_id", None) or ""
    custom_agent_id = getattr(args, "custom_agent_id", None)

    custom_agent = None
    if session_id:
        agent_id = _resolve_reuse_agent(client, config, session_id, agent_id)
        print(f"Reusing session: {session_id} (agent: {agent_id})")
    elif custom_agent_id and config.api_key:
        session_id, agent_id = _precreate_session(
            client, workspace_id, custom_agent_id,
            getattr(args, "custom_agent_stage", None),
        )
    else:
        print("Direct send (backend will create the session)...")
        if custom_agent_id:
            custom_agent = {
                "custom_agent_id": custom_agent_id,
                "custom_agent_stage": getattr(args, "custom_agent_stage", None) or "prod",
            }
            print(f"  custom agent: {custom_agent_id} (stage={custom_agent['custom_agent_stage']})")

    _dispatch_message(client, args, agent_id, session_id, workspace_id, custom_agent)


def cmd_send_legacy(args: argparse.Namespace) -> None:
    """Legacy send: pre-creates the session via CreateDataAgentSession.

    Kept only for capabilities that SendChatMessage cannot express today —
    chiefly --title (and custom-agent binding under API_KEY auth). Prefer
    plain `send` without a title; sessions get a default server-side title.
    """
    if not args.message:
        print("Error: --message/-m is required", file=sys.stderr)
        sys.exit(1)

    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)

    session_id = args.session_id
    agent_id = args.agent_id
    workspace_id = getattr(args, "workspace_id", None) or ""
    custom_agent_id = getattr(args, "custom_agent_id", None)
    title = getattr(args, "title", None)

    if session_id:
        agent_id = _resolve_reuse_agent(client, config, session_id, agent_id)
        print(f"Reusing session: {session_id} (agent: {agent_id})")
    elif custom_agent_id or title:
        # Custom-agent binding under API_KEY auth and session titles are only
        # expressible through CreateDataAgentSession, so pre-create explicitly.
        session_id, agent_id = _precreate_session(
            client, workspace_id, custom_agent_id,
            getattr(args, "custom_agent_stage", None), title,
        )
    else:
        print("Direct send (backend will create the session)...")

    _dispatch_message(client, args, agent_id, session_id, workspace_id)


def _dispatch_message(
    client: DataAgentClient,
    args: argparse.Namespace,
    agent_id: str,
    session_id: str,
    workspace_id: str,
    custom_agent: Optional[dict] = None,
) -> None:
    """Send the message, print the result, optionally poll; exits the process."""
    send_options = build_send_options(args)
    if send_options.get("message_type") == "report":
        report_config = send_options.get("report_config", {})
        print(
            f"  report_config: report_type={report_config.get('ReportType')}, "
            f"report_theme={report_config.get('ReportTheme')}, "
            f"report_prompt={len(report_config.get('ReportPrompt', ''))} chars"
        )
    if send_options.get("permission_config"):
        default_action = send_options["permission_config"].get("DefaultAction")
        print(f"  permission_config: default_action={default_action}")
    for ds in send_options.get("data_sources") or []:
        permission = ds.get("Permission")
        if permission:
            table_rules = permission.get("Tables") or []
            print(f"  data source permission: {len(table_rules)} table rule(s)")

    try:
        resp = client.send_message(
            agent_id=agent_id,
            session_id=session_id,
            message=args.message,
            workspace_id=workspace_id,
            **(custom_agent or {}),
            **send_options,
        )
    except Exception as e:
        request_id = getattr(e, "request_id", None)
        if request_id:
            print(f"  Server-side error (request_id={request_id}): {e}")
            print("  The request reached the gateway; the failure is business/permission level.")
        else:
            print(f"  Network-level failure, the request did not reach the gateway: {e}")
            print("  Check: DNS resolution, network/VPN access to the gateway.")
        sys.exit(1)

    ok = _print_send_result(resp)

    resp_data = resp.get("data") or {}
    new_session_id = resp.get("sessionId") or resp_data.get("sessionId")
    resp_agent_id = resp.get("agentId") or resp_data.get("agentId")
    if new_session_id and new_session_id != session_id:
        session_id = new_session_id
        if resp_agent_id:
            agent_id = resp_agent_id
        print(f"  Session ID: {session_id}")
        print(f"  Agent ID:   {agent_id}")

    exit_code = 0 if ok else 1
    if ok and getattr(args, "poll", False):
        timeout = getattr(args, "timeout", 300)
        print(f"Polling session {session_id} (timeout {timeout}s)...")
        final = _poll_session_completion(client, session_id, agent_id, workspace_id, timeout)
        if final == "IDLE":
            print(f"Session completed: {session_id} (IDLE)")
        elif final.startswith("TIMEOUT"):
            print(f"Poll timed out after {timeout}s (last status: {final.split(':', 1)[1] or 'unknown'})")
            exit_code = 1
        else:
            print(f"Session ended with status: {final}")
            exit_code = 1

    sys.exit(exit_code)
