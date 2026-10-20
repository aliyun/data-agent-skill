"""Standalone file upload subcommand.

Author: Tinker
Created: 2026-10-13
"""

import argparse
import sys
from pathlib import Path

from data_agent import (
    DataAgentConfig,
    DataAgentClient,
    FileManager,
)


def cmd_upload(args: argparse.Namespace) -> None:
    """Upload a local file to Data Agent and print the resulting file_id."""
    file_path = args.file_path

    if not Path(file_path).exists():
        print(f"Error: File not found: {file_path}", file=sys.stderr)
        sys.exit(1)

    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)
    file_manager = FileManager(client)

    if not file_manager.is_supported_file(file_path):
        supported = ".csv, .xlsx, .xls"
        print(f"Error: Unsupported file type. Supported formats: {supported}", file=sys.stderr)
        sys.exit(1)

    print(f"Uploading file: {file_path}")
    workspace_id = getattr(args, "workspace_id", None)
    if workspace_id:
        print(f"  Target workspace: {workspace_id}")
    try:
        file_info = file_manager.upload_file(file_path, workspace_id=workspace_id)
    except Exception as e:
        print(f"Upload failed: {e}", file=sys.stderr)
        sys.exit(1)

    print("File uploaded successfully!")
    print(f"  File ID : {file_info.file_id}")
    print(f"  Filename: {file_info.filename}")
    print(f"  Size    : {file_info.size} bytes")
    print(
        "\nUse this file ID to analyze the file in a session, e.g.:\n"
        f"  python3 scripts/data_agent_cli.py file --file-id {file_info.file_id} -q \"analyze\""
    )
