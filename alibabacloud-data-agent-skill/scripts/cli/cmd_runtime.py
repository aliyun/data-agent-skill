"""Manage the caller's Data Agent runtime, not custom-agent definitions."""

import argparse
import sys

from data_agent import DataAgentConfig, DataAgentClient


def cmd_runtime_release(args: argparse.Namespace) -> None:
    """Explicitly release the caller's runtime without provisioning a replacement."""
    print(
        "WARNING: This releases only the runtime instance belonging to the "
        "currently authenticated sub-account. Running sessions will become "
        "unavailable. Use only when a runtime refresh is necessary.",
        file=sys.stderr,
    )
    # Check consent before loading credentials or making any API calls.
    if not args.yes:
        print(
            "No request sent. Confirm the impact and current credentials, "
            "then rerun with --yes.",
            file=sys.stderr,
        )
        sys.exit(2)

    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)

    print(f"Region: {config.region}")
    print(f"Endpoint: {config.endpoint}")
    if config.using_credentials_file:
        print(f"Credentials profile: {config.credentials_env}/{config.credentials_profile}")
    print("Requesting runtime release...")
    response = client.delete_data_agent()
    print("Runtime release request accepted; replacement readiness has not been checked.")
    if response.get("requestId"):
        print(f"  RequestId: {response['requestId']}")
    print(
        "Start a new analysis with db, file or send to provision the latest runtime "
        "resources. This command does not restart the runtime or resume interrupted sessions."
    )
