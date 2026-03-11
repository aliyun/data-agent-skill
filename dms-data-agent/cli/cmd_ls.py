"""List databases and tables (ls subcommand).

Author: Tinker
Created: 2026-03-04
"""

import argparse
import sys

from data_agent import DataAgentConfig, DataAgentClient


def _extract_list(resp: dict) -> list:
    """Extract a list from an API response regardless of nesting style.

    Handles ``{Data: [...]}`` and ``{Data: {List/DataList/Content: [...]}}``.
    """
    data = resp.get("Data", [])
    if isinstance(data, list):
        return data
    if isinstance(data, dict):
        return data.get("List") or data.get("DataList") or data.get("Content") or []
    return []


def cmd_ls(args: argparse.Namespace) -> None:
    """List DMS databases and (optionally) their tables."""
    config = DataAgentConfig.from_env()
    client = DataAgentClient(config)

    search = getattr(args, "search", None)
    db_id  = getattr(args, "db_id", None)
    sep    = "-" * 60

    print(f"Region: {config.region}")

    # -- list databases --
    if db_id is None:
        print("Fetching databases...")
        try:
            resp = client.list_databases(search_key=search)
        except Exception as e:
            print(f"Error: {e}", file=sys.stderr)
            sys.exit(1)

        items = _extract_list(resp)
        if not items:
            print("No databases found.")
            return

        # Separate real DB connections from file-based data sources
        real_dbs  = [d for d in items if d.get("ImportType", "") in ("RDS", "DMS")]
        file_dbs  = [d for d in items if d.get("ImportType", "") == "FILE"]

        # -- Real databases (RDS / DMS) --
        print(f"\n{'=' * 60}")
        print(f"  Database Connections ({len(real_dbs)})  [ImportType: RDS/DMS]")
        print(f"{'=' * 60}")
        if real_dbs:
            for db in real_dbs:
                db_name         = db.get("DatabaseName", "")
                db_type         = db.get("DbType", "").lower()
                import_type     = db.get("ImportType", "")
                dms_db_id       = db.get("DmsDbId", "")
                dms_instance_id = db.get("DmsInstanceId", "")
                instance_name   = db.get("InstanceName", "")
                db_desc         = db.get("DatabaseDesc", "")
                agent_db_id     = db.get("DbId", "")

                print(f"\n  {db_name}  [{db_type}]  ({import_type})")
                if db_desc:
                    print(f"    Desc          : {db_desc}")
                print(f"    AgentDbId     : {agent_db_id}")
                print(f"    DmsDbId       : {dms_db_id}")
                print(f"    DmsInstanceId : {dms_instance_id}")
                print(f"    InstanceName  : {instance_name}")
        else:
            print("  (none)")

        # -- File-based data sources --
        print(f"\n{'=' * 60}")
        print(f"  File Data Sources ({len(file_dbs)})  [ImportType: FILE]")
        print(f"{'=' * 60}")
        if file_dbs:
            for db in file_dbs:
                db_name     = db.get("DatabaseName", "")
                db_type     = db.get("DbType", "").lower()
                agent_db_id = db.get("DbId", "")
                db_desc     = db.get("DatabaseDesc", "")
                internal    = db.get("IsInternal", "N")
                label       = "[sample]" if internal == "Y" else ""
                print(f"  {db_name:<45}  [{db_type}]  {label}  DbId={agent_db_id}")
        else:
            print("  (none)")
        print()
        return

    # -- list tables for a specific db_id --
    print(f"Fetching tables for DbId={db_id}...")

    # Fetch all databases first to get InstanceName + DatabaseName (required by API)
    db_meta: dict = {}
    try:
        db_resp = client.list_databases()
        all_dbs = _extract_list(db_resp)
        for db in all_dbs:
            if str(db.get("DbId", "")) == str(db_id):
                db_meta = db
                break
    except Exception:
        pass

    if not db_meta:
        print(f"Error: DbId '{db_id}' not found in database list.", file=sys.stderr)
        sys.exit(1)

    inst_name = db_meta.get("InstanceName", "")
    db_name_q = db_meta.get("DatabaseName", "")

    try:
        resp = client.list_tables(inst_name, db_name_q)
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)

    items = _extract_list(resp)
    if not items:
        print("No tables found.")
        return

    table_names = [t.get("TableName", t.get("Name", "")) for t in items]
    table_ids   = [t.get("TableId", t.get("Id", "")) for t in items]

    db_name         = db_meta.get("DatabaseName", "")
    db_type         = db_meta.get("DbType", "").lower()
    dms_instance_id = db_meta.get("DmsInstanceId", "")
    dms_db_id       = db_meta.get("DmsDbId", "")
    instance_name   = db_meta.get("InstanceName", "")

    print(f"\n{'=' * 60}")
    print(f"  Database  : {db_name}  [{db_type}]")
    print(f"  AgentDbId : {db_id}")
    print(f"  DmsDbId   : {dms_db_id}")
    print(f"  Instance  : {instance_name}  (DmsInstanceId={dms_instance_id})")
    print(f"  Tables    : {len(items)}")
    print(f"{'=' * 60}")
    for name, tid in zip(table_names, table_ids):
        print(f"  {name:<30}  {tid}")
    print()

    # Print ready-to-use CLI command
    tables_arg    = ",".join(table_names)
    table_ids_arg = ",".join(table_ids)
    print(sep)
    print("  Ready-to-use db command:")
    print(sep)
    print(f"  python3 skill/data_agent_cli.py db \\")
    print(f"    --dms-instance-id {dms_instance_id} \\")
    print(f"    --dms-db-id {dms_db_id} \\")
    print(f"    --instance-name {instance_name} \\")
    print(f"    --db-name {db_name} \\")
    print(f"    --engine {db_type} \\")
    print(f"    --tables {tables_arg} \\")
    print(f"    --session-mode ASK_DATA \\")
    print(f"    -q \"your question here\"")
    print(sep)
