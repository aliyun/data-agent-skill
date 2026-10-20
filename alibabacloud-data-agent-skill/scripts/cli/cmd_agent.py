"""Custom Agent management subcommand (agent).

Author: Tinker
Created: 2026-04-16
"""

import argparse
import json
import sys
from pathlib import Path

from data_agent import DataAgentConfig, DataAgentClient


def _get_field(obj: dict, *names: str, default=""):
    """Get a field value trying multiple possible key names."""
    for name in names:
        if name in obj:
            return obj[name]
    return default


def _require_workspace_id(args: argparse.Namespace) -> str:
    workspace_id = getattr(args, "workspace_id", None)
    if not workspace_id:
        print("Error: --workspace-id is required for create/update/publish", file=sys.stderr)
        sys.exit(1)
    return workspace_id


def _read_instruction(value: str) -> str:
    """Resolve --instruction: inline text, or @path to read the whole file."""
    if not value.startswith("@"):
        return value
    path = Path(value[1:])
    if not path.is_file():
        print(f"Error: --instruction file not found: {path}", file=sys.stderr)
        sys.exit(1)
    text = path.read_text(encoding="utf-8")
    if not text.strip():
        print(f"Error: --instruction file is empty: {path}", file=sys.stderr)
        sys.exit(1)
    print(f"  Instruction loaded: {len(text)} chars")
    return text


def _load_dsl(path_str: str) -> str:
    """Read --dsl-file and validate it is well-formed JSON."""
    path = Path(path_str)
    if not path.is_file():
        print(f"Error: --dsl-file not found: {path}", file=sys.stderr)
        sys.exit(1)
    content = path.read_text(encoding="utf-8")
    try:
        json.loads(content)
    except json.JSONDecodeError as e:
        print(f"Error: --dsl-file is not valid JSON: {e}", file=sys.stderr)
        sys.exit(1)
    print(f"  DSL loaded: {len(content)} chars")
    return content


def _cmd_list(client: DataAgentClient, args: argparse.Namespace) -> None:
    """List custom agents."""
    workspace_id = getattr(args, "workspace_id", None)
    search = getattr(args, "search", None)
    page_number = getattr(args, "page_number", 1)
    page_size = getattr(args, "page_size", 20)

    print(f"Region: {client.config.region}")
    print(f"Fetching custom agents (status=RELEASED)...")

    try:
        resp = client.list_custom_agents(
            workspace_id=workspace_id,
            search_key=search,
            page_number=page_number,
            page_size=page_size,
        )
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)

    # Extract agent list from response
    data = resp.get("data") or resp.get("Data") or {}
    items = []
    if isinstance(data, dict):
        items = data.get("content") or data.get("Content") or []
    elif isinstance(data, list):
        items = data

    total_elements = 0
    total_pages = 0
    if isinstance(data, dict):
        total_elements = data.get("totalElements") or data.get("TotalElements") or len(items)
        total_pages = data.get("totalPages") or data.get("TotalPages") or 1

    if not items:
        print("No custom agents found.")
        return

    sep = "=" * 60
    print(f"\n{sep}")
    print(f"  Custom Agents ({len(items)})  Total: {total_elements}")
    print(f"{sep}")

    for agent in items:
        name = _get_field(agent, "agentName", "AgentName", "name", "Name")
        agent_id = _get_field(agent, "customAgentId", "CustomAgentId")
        status = _get_field(agent, "status", "Status", default="unknown")
        ws_id = _get_field(agent, "workspaceId", "WorkspaceId")
        creator = _get_field(agent, "creator", "Creator")
        desc = _get_field(agent, "description", "Description")

        print(f"\n  {name}  [{status}]")
        print(f"    CustomAgentId : {agent_id}")
        if ws_id:
            print(f"    WorkspaceId   : {ws_id}")
        if creator:
            print(f"    Creator       : {creator}")
        if desc:
            print(f"    Description   : {desc}")

    if total_pages > 1:
        print(f"\n  Page {page_number}/{total_pages} (use --page-number to navigate)")

    # Print usage hint
    print(f"\n{'-' * 60}")
    print("  Tip: 使用 db --custom-agent-id <ID> ... 来指定自定义Agent进行分析")
    print(f"{'-' * 60}")
    print()


def _cmd_describe(client: DataAgentClient, args: argparse.Namespace) -> None:
    """Describe a custom agent in detail."""
    custom_agent_id = getattr(args, "custom_agent_id", None)
    if not custom_agent_id:
        print("Error: --custom-agent-id is required for describe action", file=sys.stderr)
        sys.exit(1)

    workspace_id = getattr(args, "workspace_id", None)

    print(f"Region: {client.config.region}")
    print(f"Fetching custom agent details: {custom_agent_id}...")

    try:
        resp = client.describe_custom_agent(
            custom_agent_id=custom_agent_id,
            workspace_id=workspace_id,
        )
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)

    # Extract agent detail from response
    data = resp.get("data") or resp.get("Data") or {}

    if not data:
        print("No agent details returned.")
        return

    name = _get_field(data, "agentName", "AgentName", "name", "Name")
    agent_id = _get_field(data, "customAgentId", "CustomAgentId")
    status = _get_field(data, "status", "Status", default="unknown")
    ws_id = _get_field(data, "workspaceId", "WorkspaceId")
    creator = _get_field(data, "creator", "Creator")
    desc = _get_field(data, "description", "Description")
    instruction = _get_field(data, "instruction", "Instruction")
    knowledge = _get_field(data, "knowledge", "Knowledge")

    sep = "=" * 60
    print(f"\n{sep}")
    print(f"  Custom Agent Detail")
    print(f"{sep}")
    print(f"  Name            : {name}")
    print(f"  CustomAgentId   : {agent_id}")
    print(f"  Status          : {status}")
    if ws_id:
        print(f"  WorkspaceId     : {ws_id}")
    if creator:
        print(f"  Creator         : {creator}")
    if desc:
        print(f"  Description     : {desc}")
    if instruction:
        print(f"  Instruction     : {instruction}")
    if knowledge:
        print(f"  Knowledge       : {knowledge}")

    # Print any additional fields not covered above
    known_keys = {
        "agentName", "AgentName", "name", "Name",
        "customAgentId", "CustomAgentId",
        "status", "Status",
        "workspaceId", "WorkspaceId",
        "creator", "Creator",
        "description", "Description",
        "instruction", "Instruction",
        "knowledge", "Knowledge",
    }
    extra = {k: v for k, v in data.items() if k not in known_keys and v}
    if extra:
        print(f"\n  -- Additional Fields --")
        for k, v in extra.items():
            print(f"  {k:16s}: {v}")

    print(f"\n{'-' * 60}")
    print(f"  Tip: db --custom-agent-id {agent_id} ... 来使用此Agent")
    print(f"{'-' * 60}")
    print()


def _cmd_create(client: DataAgentClient, args: argparse.Namespace) -> None:
    """Create a custom agent and release it."""
    name = getattr(args, "agent_name", None)
    if not name:
        print("Error: --agent-name is required for create", file=sys.stderr)
        sys.exit(1)
    workspace_id = _require_workspace_id(args)

    web_report_config = _load_dsl(args.dsl_file) if getattr(args, "dsl_file", None) else None
    instruction = _read_instruction(args.instruction) if getattr(args, "instruction", None) else None
    description = getattr(args, "description", "") or ""

    print(f"Region: {client.config.region}")
    print(f"Creating custom agent: {name}")
    try:
        agent_id = client.create_custom_agent(
            workspace_id=workspace_id,
            name=name,
            description=description,
            web_report_config=web_report_config,
            instruction=instruction,
        )
    except Exception as e:
        print(f"Error: create failed: {e}", file=sys.stderr)
        sys.exit(1)
    print(f"  Created CustomAgentId: {agent_id}")

    print("Releasing (operate_type=RELEASE)...")
    try:
        client.operate_custom_agent(
            workspace_id=workspace_id,
            custom_agent_id=agent_id,
            operate_type="RELEASE",
        )
    except Exception as e:
        print(f"Error: release failed: {e}", file=sys.stderr)
        print(
            "  The agent was created but not released; retry with:\n"
            f"  agent --action publish --custom-agent-id {agent_id} --workspace-id {workspace_id}",
            file=sys.stderr,
        )
        sys.exit(1)
    print(f"  Released CustomAgentId: {agent_id}")
    print(
        f"\nNext: use it in a session with\n"
        f"  send --workspace-id {workspace_id} --custom-agent-id {agent_id} ..."
    )


def _cmd_update(client: DataAgentClient, args: argparse.Namespace) -> None:
    """Modify an existing custom agent and re-release it."""
    custom_agent_id = getattr(args, "custom_agent_id", None)
    if not custom_agent_id:
        print("Error: --custom-agent-id is required for update action", file=sys.stderr)
        sys.exit(1)
    workspace_id = _require_workspace_id(args)

    web_report_config = _load_dsl(args.dsl_file) if getattr(args, "dsl_file", None) else None
    instruction = _read_instruction(args.instruction) if getattr(args, "instruction", None) else None

    print(f"Region: {client.config.region}")
    print(f"Updating custom agent: {custom_agent_id}")
    try:
        client.modify_custom_agent(
            workspace_id=workspace_id,
            custom_agent_id=custom_agent_id,
            name=getattr(args, "agent_name", None),
            description=getattr(args, "description", None),
            web_report_config=web_report_config,
            instruction=instruction,
        )
    except Exception as e:
        print(f"Error: update failed: {e}", file=sys.stderr)
        sys.exit(1)
    print(f"  Updated CustomAgentId: {custom_agent_id}")

    print("Releasing (operate_type=RELEASE)...")
    try:
        client.operate_custom_agent(
            workspace_id=workspace_id,
            custom_agent_id=custom_agent_id,
            operate_type="RELEASE",
        )
    except Exception as e:
        print(f"Error: release failed: {e}", file=sys.stderr)
        print(
            "  The agent was updated but not released; retry with:\n"
            f"  agent --action publish --custom-agent-id {custom_agent_id} --workspace-id {workspace_id}",
            file=sys.stderr,
        )
        sys.exit(1)
    print(f"  Released CustomAgentId: {custom_agent_id}")


def _cmd_publish(client: DataAgentClient, args: argparse.Namespace) -> None:
    """Release an existing custom agent."""
    custom_agent_id = getattr(args, "custom_agent_id", None)
    if not custom_agent_id:
        print("Error: --custom-agent-id is required for publish action", file=sys.stderr)
        sys.exit(1)
    workspace_id = _require_workspace_id(args)

    print(f"Region: {client.config.region}")
    print(f"Releasing custom agent: {custom_agent_id} (operate_type=RELEASE)")
    try:
        client.operate_custom_agent(
            workspace_id=workspace_id,
            custom_agent_id=custom_agent_id,
            operate_type="RELEASE",
        )
    except Exception as e:
        print(f"Error: publish failed: {e}", file=sys.stderr)
        sys.exit(1)
    print(f"  Released CustomAgentId: {custom_agent_id}")


def cmd_agent(args: argparse.Namespace) -> None:
    """Handle agent subcommand."""
    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)

    action = getattr(args, "action", "list")

    if action == "describe":
        _cmd_describe(client, args)
    elif action == "create":
        _cmd_create(client, args)
    elif action == "update":
        _cmd_update(client, args)
    elif action == "publish":
        _cmd_publish(client, args)
    else:
        _cmd_list(client, args)
