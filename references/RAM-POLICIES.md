# RAM 权限策略

本 Skill 需要以下阿里云 RAM 权限：

## 必需权限

| 权限策略 | 说明 |
|----------|------|
| `AliyunDMSFullAccess` | DMS 数据管理服务全权限 |
| `AliyunDMSDataAgentFullAccess` | Data Agent 全权限 |

## 最小权限策略（推荐）

如果只需要使用 Data Agent 功能，可以配置以下最小权限：

```json
{
  "Version": "1",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "dms:ListInstances",
        "dms:ListDatabases",
        "dms:ListTables",
        "dms:ListColumns",
        "dms:GetInstance",
        "dms:GetDatabase",
        "dms:GetTableTopology",
        "dms:GetMetaTableDetailInfo",
        "dms:DescribeInstance",
        "dms:CreateDataAgentSession",
        "dms:DescribeDataAgentSession",
        "dms:ListDataAgentSession",
        "dms:SendChatMessage",
        "dms:GetChatContent",
        "dms:DescribeDataAgentUsage",
        "dms:UpdateDataAgentSession",
        "dms:CreateDataAgentFeedback",
        "dms:DescribeFileUploadSignature",
        "dms:FileUploadCallback",
        "dms:ListFileUpload",
        "dms:DeleteFileUpload",
        "dms:ListDataCenterDatabase",
        "dms:ListDataCenterTable",
        "dms:AddDataCenterTable"
      ],
      "Resource": "*"
    }
  ]
}
```

## 配置说明

1. 登录 [阿里云 RAM 控制台](https://ram.console.aliyun.com/)
2. 创建或选择用户
3. 为用户添加以上权限策略
4. 创建 AccessKey 用于 Skill 认证
