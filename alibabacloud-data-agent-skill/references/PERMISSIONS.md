# Data Permissions (SQL Access Control)

Data Agent supports attaching a **permission configuration** to a database data source, restricting which tables, columns and rows the agent may access when it generates and executes SQL. Two levels are available on `send`:

| Level | CLI flag | Wire location | Controls |
|-------|----------|---------------|----------|
| Data-source level | `--permission-file <PATH>` | `DataSources[].Permission` | Per-table column / row rules |
| Session level | `--permission-default-action allow\|deny` | `SessionConfig.PermissionConfig.DefaultAction` | Default action for tables not declared in any permission rule |

## Permission JSON file format

`--permission-file` accepts a JSON document (snake_case keys; the CLI converts them to the PascalCase wire format automatically — already-PascalCase keys also work):

```json
{
  "default_action": "deny",
  "tables": [
    {
      "table_name": "orders",
      "allowed_columns": ["order_id", "region", "amount"],
      "disallowed_columns": ["cost"],
      "required_row_filter": "region = 'east'",
      "required_row_filter_list": ["region = 'east'", "region = 'south'"]
    },
    {
      "table_name": "customers",
      "disallowed_columns": ["id_card", "mobile"]
    }
  ]
}
```

All fields are optional except `table_name`. The top-level `default_action` promotes to the session-level permission config (same as `--permission-default-action`); passing both with different values is rejected.

### Field semantics

| Field | Semantics |
|-------|-----------|
| `table_name` | Target table; matching is case-insensitive. When the same table appears multiple times, the **last entry wins**. |
| `allowed_columns` | Column whitelist. `null` (omitted) = no column control. An **empty array `[]` denies all columns** (only `COUNT(*)` still passes). |
| `disallowed_columns` | Column blacklist. `null` / `[]` = no blacklist control. |
| `required_row_filter` | A single row-filter condition; queries against the table must include it (as a top-level AND condition), otherwise they are rejected. |
| `required_row_filter_list` | A list of row-filter conditions with **OR semantics**; can be combined with `required_row_filter`. |
| `default_action` | `allow` (default): tables not declared in `tables` are accessible. `deny`: tables not declared are rejected — an explicit whitelist. |

Additional rules:

- When both whitelist and blacklist are set, the effective allowed set is **whitelist minus blacklist**.
- Row filters must be valid WHERE conditions; queries missing the required filter are rejected (fail-closed).
- `SELECT *` on a table with any column control is rejected (fail-closed); `COUNT(*)` is the exception.

## CLI usage

```bash
# 1) Write permission.json (example above)
# 2) Attach it to a database analysis message
python3 scripts/data_agent_cli.py send \
    --dms-instance-id 1234567 --db-name mydb --tables "orders,customers" \
    --permission-file permission.json \
    -m "Analyze last month's order volume by region" --poll

# Session-level default only (no per-table rules): undeclared tables are denied
python3 scripts/data_agent_cli.py send \
    --dms-instance-id 1234567 --db-name mydb --tables "orders" \
    --permission-default-action deny -m "Analyze orders"
```

`--permission-file` requires the database data-source parameters (`--dms-instance-id` / `--db-name` / `--tables`).

## Python SDK usage

The same structure can be passed programmatically via `client.send_message`:

```python
client.send_message(
    session_id="...",
    message="Analyze last month's orders",
    data_sources=[{
        "DataSourceType": "database",
        "DmsInstanceId": "1234567",
        "DbName": "mydb",
        "Tables": ["orders", "customers"],
        "Permission": {
            "Tables": [{
                "TableName": "orders",
                "AllowedColumns": ["order_id", "region", "amount"],
                "RequiredRowFilter": "region = 'east'",
            }],
        },
    }],
    permission_config={"DefaultAction": "deny"},
)
```

If you call the POP API directly with the official `alibabacloud-dms20250414` SDK instead, use `SendChatMessageRequestDataSourcesPermission` / `SendChatMessageRequestSessionConfigPermissionConfig` models (SDK >= 2.0.0).
