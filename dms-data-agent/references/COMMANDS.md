# 子命令详细参考

本文档包含所有子命令的详细说明和完整参数列表。

---

## ls 子命令 — 发现数据资源

在发起分析之前，先用 `ls` 了解有哪些可用的数据库和表。

> **⚠️ 重要提示**：`ls` 命令只显示**已导入到 Data Agent Data Center** 的数据库。如果找不到需要的数据库，说明该数据库尚未从 DMS 导入。

### 列出所有数据库

```bash
python3 dms-data-agent/data_agent_cli.py ls
```

输出分两组：
- **Database Connections**（ImportType: RDS/DMS）— 真实关系型数据库，可用于 `db` 子命令
- **File Data Sources**（ImportType: FILE）— 已上传的文件数据集

每条数据库记录显示：
```
  chinook  [mysql]  (RDS)
    AgentDbId     : <AGENT_DB_ID>
    DmsDbId       : <DMS_DB_ID>
    DmsInstanceId : <DMS_INSTANCE_ID>
    InstanceName  : <INSTANCE_NAME>
```

### 按关键词过滤

```bash
python3 dms-data-agent/data_agent_cli.py ls --search chinook
```

### 列出指定库的表 + 生成可用命令

```bash
python3 dms-data-agent/data_agent_cli.py ls --db-id <AgentDbId>
```

输出包含可直接复制使用的 `db` 命令模板。

---

## db 子命令 — 数据库分析

> **⚠️ 重要提示**：`db` 子命令需要数据库**已存在于 Data Agent Data Center** 中。

### 数据源参数

| 参数 | 说明 |
|------|------|
| `--dms-instance-id` | DMS 实例 ID（数字）|
| `--dms-db-id` | DMS 数据库 ID（数字）|
| `--instance-name` | RDS 实例名称 |
| `--db-name` | 数据库名称 |
| `--tables` | 表名列表，逗号分隔 |
| `--engine` | 数据库引擎，默认 `mysql` |

### 会话参数

| 参数 | 说明 |
|------|------|
| `--session-mode` | `ASK_DATA`（默认）/ `ANALYSIS` / `INSIGHT` |
| `--output` | `summary`（默认）/ `detail` / `raw` |
| `--enable-search` | 启用搜索能力（默认 `false`）|

### 查询方式

| 参数 | 说明 |
|------|------|
| `-q` / `--query` | 单次查询（不指定则运行预设查询）|

---

## file 子命令 — 文件分析

文件分析默认使用 `ANALYSIS` 模式。

### 参数

| 参数 | 说明 |
|------|------|
| `--session-mode` | `ASK_DATA` / `ANALYSIS`（默认）/ `INSIGHT` |
| `--output` | `summary`（默认）/ `detail` / `raw` |
| `--enable-search` | 启用搜索能力（默认 `false`）|
| `-q` / `--query` | 自定义查询问题 |

---

## reports 子命令 — 获取生成结果

查看并下载会话生成的报表和图表文件。

### 参数

| 参数 | 说明 |
|------|------|
| `--session-id` | **必填**，会话 ID |

---

## 会话模式说明

| 模式 | 特点 | 耗时 | 报告 | 适用场景 |
|------|------|------|------|----------|
| `ASK_DATA` | 快速 SQL 查数 + 自然语言回答，可以连续追问 | 30 秒 -2 分钟 | ❌ | 简单查询、数据验证 |
| `ANALYSIS` | 深度分析，生成完整报告 | 5-15 分钟 | ✅ | 专题分析、业务报告 |
| `INSIGHT` | 多维度、深度的数据洞察 | 20-40 分钟 | ✅ | 战略分析、趋势研究 |

> **⚠️ 重要**：需要生成报告（HTML/Markdown/图表）时，**建议使用 ANALYSIS 模式**。

## dms 子命令 — DMS 工具集成

直接访问 DMS 元数据，用于发现实例、数据库和表。

### 工具列表

| 工具 | 描述 |
|------|------|
| `list-instances` | 搜索 DMS 中的数据库实例列表 |
| `search-database` | 按 schema 名称搜索数据库 |
| `list-tables` | 列出指定数据库中的表 |

---

## import 子命令 — 导入 DMS 数据库

将 DMS 中的数据库表导入到 Data Agent Data Center。

### 参数

| 参数 | 说明 |
|------|------|
| `--dms-instance-id` | **必填**，DMS 实例 ID（数字）|
| `--dms-db-id` | **必填**，DMS 数据库 ID（数字）|
| `--instance-name` | **必填**，RDS 实例名称 |
| `--db-name` | **必填**，数据库名称 |
| `--tables` | **必填**，要导入的表名列表（逗号分隔）|
| `--engine` | 数据库引擎类型（默认：mysql）|
| `--region` | 区域 ID（默认：cn-hangzhou）|

---

## attach 子命令 — 会话复用

连接已创建的会话，继续对话、确认计划或查看进度。

### 参数

| 参数 | 说明 |
|------|------|
| `--session-id` | **必填**，要连接的会话 ID |
| `-q` / `--query` | 发送单次查询 |
| `--from-start` | 从头回放会话历史（等同于 --checkpoint 0） |
| `--checkpoint` | 指定从某个具体的断点（checkpoint）恢复流（如 `--checkpoint 219`），在遇到网络中断等情况时用于精确恢复 |
| `--output` | `summary`（默认）/ `detail` / `raw` |
