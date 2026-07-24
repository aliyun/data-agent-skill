# RAM Policy Requirements

This skill calls Alibaba Cloud Data Agent APIs and DMS Enterprise APIs. Use one of these options:

- Recommended system policy: `AliyunDMSFullAccess`.
- Data Agent scoped policy, when available in the target account: `AliyunDMSDataAgentFullAccess`.
- API Key mode: set `DATA_AGENT_API_KEY`; database and table discovery/import tools are unavailable because they require DMS Enterprise AK/SK authorization.

Minimum custom policy actions used by the MCP server:

```json
{
  "Version": "1",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "dms:CreateDataAgentSession",
        "dms:DescribeDataAgentSession",
        "dms:SendChatMessage",
        "dms:GetChatContent",
        "dms:InitDataAgentPersonalWorkspace",
        "dms:ListDataAgentWorkspace",
        "dms:ListCustomAgent",
        "dms:ListFileUpload",
        "dms:DescribeFileUploadSignature",
        "dms:FileUploadCallback",
        "dms:GetActiveRouteUnit",
        "dms:GetUserActiveTenant",
        "dms:ListTagMetaAsset",
        "dms:TagMetaAsset",
        "dms:ListTables",
        "dms:ListInstances",
        "dms:SearchDatabase"
      ],
      "Resource": "*"
    }
  ]
}
```

The RAM principal must also have DMS-level access to the target instances, databases, and tables. If database discovery works but analysis fails with `CheckDatabasePermissionFailed`, grant the user the required DMS data permissions or use a workspace/database that the user can access.
