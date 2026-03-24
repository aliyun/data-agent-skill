---
name: dms-data-agent
description: |
  通过命令行调用阿里云瑶池 Data Agent for Analytics，帮助用户对企业数据库进行自然语言驱动的数据分析。
  Data Agent for Analytics 是阿里云瑶池数据库团队推出的面向企业用户的数据分析智能体，可根据自然语言描述自动完成需求分析、数据理解、分析洞察及报告生成。
  本工具支持：发现已托管在 DMS 的数据资源（实例/库/表）、发起问数或深度分析会话、实时跟踪执行进度、获取分析结论及生成的报告文件。
compatibility: |
  需要有效的阿里云凭证（AK/SK 或 STS 临时凭证，或者 API_KEY）；
  需要安装 requirements.txt 中的依赖；
  数据源需已托管在阿里云瑶池数据库或 DMS。
---
metadata:
  author: DataAgent 研发
  version: "1.7.1"
---

# 更新日志
- **v1.7.1**: 修复 CLI `ls` 命令 API 响应解析问题（支持大小写字段名）、优化 SKILL 文档结构、ANALYSIS 模式规范独立文档
- **v1.7.0**: API_KEY 认证支持、原生异步执行模式、会话隔离、增强的attach模式、优化的日志输出

---

---

# 安装


## 配置凭证和地域（复制示例文件并填入真实值）
### OpenClaw 配置方法
- 检查在 `~/.openclaw/openclaw.json` 中是否存在下述变量，如果不存在则需要首先配置：
```json
{
  "env": {
    "ALIBABA_CLOUD_ACCESS_KEY_ID": "your-access-key-id",
    "ALIBABA_CLOUD_ACCESS_KEY_SECRET": "your-access-key-secret"
  }
}
```
- 或者使用 API_KEY 进行认证（仅适用于文件分析场景）：
```json
{
  "env": {
    "DATA_AGENT_API_KEY": "your-api-key"
  }
}
```

### 其他配置（QoderWork,Claude Code等）
- 在Skill目录复制创建.env 文件
- 或者使用环境变量


- **注意**：如果同时配置了 AK/SK 和 API_KEY，系统将优先使用 AK/SK 认证
- DATA_AGENT_REGION（如 cn-hangzhou, cn-beijing 等）
- **权限要求**：确保 RAM 用户具有 DMS 管理权限（AliyunDMSFullAccess 或 AliyunDMSDataAgentFullAccess）

## 调试功能
- **API请求调试**: 通过设置环境变量 `DATA_AGENT_DEBUG_API` 可以启用详细的API请求和响应日志
  - 可用值: `'true'`, `'1'`, `'yes'` (不区分大小写)
  - 启用后将输出所有API调用的详细信息，包括请求参数和响应内容
  - 示例: `DATA_AGENT_DEBUG_API=1 python3 data_agent_cli.py file ...`

## 💡 使用建议

提示用户使用内置的数据集 `assets/example_game_data.csv` 完成一次分析体验。


# Data Agent CLI — 统一命令行数据分析工具

## 概述

`data_agent_cli.py` 帮助用户完成从**发现数据 → 发起分析 → 跟踪进度 → 获取结果**的完整流程。

### 核心概念

> **⚠️ 关键前提**：Data Agent 只能分析**已导入到 Data Agent Data Center** 的数据库。
>
> - **Data Center**：Data Agent 的数据中心，只有这里的数据库才能被分析
> - **DMS**：阿里云数据管理服务，存储着所有数据库的元数据
> - **关系**：DMS 中注册的数据库 ≠ Data Center 中的数据库
>
> **使用流程**：
> 1. 先用 `ls` 查看 Data Center 中是否有目标数据库
> 2. 如果**没有**，先用 `dms` 子命令搜索数据库信息，再用 `import` 子命令导入
> 3. 导入成功后，才能使用 `db` 子命令进行分析

---

## 分析模式说明

`db` 和 `file` 命令通过 `--session-mode` 参数指定分析模式：

| 模式 | 参数值 | 特点 | 推荐执行方式 |
|------|--------|------|-------------|
| **问数模式**（默认） | `ASK_DATA` | 即问即答，秒级响应，返回 SQL 查询结果 | **同步执行**（默认） |
| **分析模式** | `ANALYSIS` | 深度分析，生成多维度洞察报告，耗时 5-40 分钟 | **建议spawn 子 Agent 异步处理** |

### ASK_DATA 模式（默认，同步）

`db` 和 `file` 命令**默认使用 ASK_DATA 模式，同步执行**，直接返回查询结果，无需等待后台任务：

```bash
# 数据库问数（同步，直接返回结果）
python3 dms-data-agent/data_agent_cli.py db \
    --dms-instance-id <ID> --dms-db-id <ID> \
    --instance-name <NAME> --db-name <DB> \
    --tables "t1,t2" -q "谁的销售额最高"

# 文件问数（同步，直接返回结果）
python3 dms-data-agent/data_agent_cli.py file example.csv -q "数据概览"
```

### ANALYSIS 模式（spawn 子 Agent）

ANALYSIS 模式耗时长（5-40 分钟），**必须 spawn 子 Agent 后台执行**，避免阻塞主对话。

```bash
python3 dms-data-agent/data_agent_cli.py db \
    --dms-instance-id <ID> --dms-db-id <ID> \
    --instance-name <NAME> --db-name <DB> \
    --tables "t1,t2" --session-mode ANALYSIS \
    -q "深度分析销售趋势"
# 输出：Session ID: abc123xyz
```

**关键监控点：**

| 状态 | 操作 |
|------|------|
| `waiting_input` | 展示执行计划，等待用户确认后 `attach -q '确认执行'` |
| `completed` | 下载报告或引导到控制台查看 |
| `failed` | 提示错误信息 |

> 详细子 Agent 规范见 [ANALYSIS_MODE.md](references/ANALYSIS_MODE.md)

---

## ⚠️ 重要：会话复用原则

### ✅ 正确做法

```bash
# 首次分析（ASK_DATA 默认同步，直接返回结果）
python3 dms-data-agent/data_agent_cli.py db ... -q "问题 1"

# 追问 - 使用 attach 复用会话（ASK_DATA 同步执行）
python3 dms-data-agent/data_agent_cli.py attach --session-id abc123xyz -q "按月分解"
```

### ❌ 错误做法

```bash
# 每次都创建新会话（浪费资源，丢失上下文）
python3 dms-data-agent/data_agent_cli.py db ... -q "问题 1"  # 创建会话 A
python3 dms-data-agent/data_agent_cli.py db ... -q "问题 2"  # 又创建会话 B（错误！）
```

### 何时使用哪个命令？

| 场景 | 命令 | 说明 |
|------|------|------|
| 第一次问数 | `db` / `file` | 创建新会话，默认 ASK_DATA 同步执行 |
| 同一会话继续追问 | `attach --session-id <ID>` | 复用会话 |
| 修改/确认执行计划 | `attach --session-id <ID>` | 复用会话，发送新指令或确认 |
| 获取最终报告 | `reports --session-id <ID>` | 下载生成的文件到本地 |

---

## Session 目录结构

每个会话在 `sessions/{session_id}/` 下维护独立状态：

| 文件 | 内容 | 用途 |
|------|------|------|
| `status.txt` | `running` / `waiting_input` / `completed` / `failed` | 快速判断任务状态 |
| `progress.log` | 执行日志，含计划内容与阶段结论 | **子 Agent 读取进度与结果** |
| `checkpoint.txt` | SSE 流位点值 | 断点续传依据 |
| `result.json` | 结构化状态 | 程序化检查 |
| `input.json` | 输入参数 | 输入参数记录 |
| `worker.pid` | Worker 进程 PID | 并发防护 |

```bash
# 子 Agent 轮询示例
cat sessions/{session_id}/status.txt      # 检查状态
cat sessions/{session_id}/progress.log    # 读取进度与结论
```

> **⚠️ 会话时效**：会话有效时间为 **6 小时**，超时后需通过 `db` 重新发起新会话。

---

## 快速开始

```bash
# 1. 查看可用数据库
python3 dms-data-agent/data_agent_cli.py ls

# 2. 问数分析（ASK_DATA，同步，直接返回结果）
python3 dms-data-agent/data_agent_cli.py db \
    --dms-instance-id <ID> --dms-db-id <ID> \
    --instance-name <NAME> --db-name <DB> \
    --tables "t1,t2" -q "谁的销售额最高"

# 3. 追问（复用会话）
python3 dms-data-agent/data_agent_cli.py attach --session-id <ID> -q "按月分解"

# 4. 深度分析（ANALYSIS，spawn 子 Agent 执行）
python3 dms-data-agent/data_agent_cli.py db \
    --dms-instance-id <ID> --dms-db-id <ID> \
    --instance-name <NAME> --db-name <DB> \
    --tables "t1,t2" --session-mode ANALYSIS \
    -q "深度分析销售趋势"
```

> 📖 完整工作流请参考 [工作流示例](references/WORKFLOWS.md)

---

## 子命令一览

| 子命令 | 用途 | 默认模式 |
|--------|------|----------|
| `ls` | 列出 Data Center 中的数据库和表 | - |
| `db` | 连接数据库，发起分析会话 | ASK_DATA，同步 |
| `file` | 上传本地文件或分析数据中心中的现有文件 | ASK_DATA，同步 |
| `attach` | 连接已有会话，继续对话 | 同步 |
| `dms` | DMS 工具集成（发现数据）| - |
| `import` | 将 DMS 数据库导入 Data Center | - |
| `reports` | 查看或下载会话生成的报告和图表文件 | - |

> 详情见：[命令参考](references/COMMANDS.md)

---

## 从数据中心文件进行分析

使用 `file` 子命令配合 `--file-id` 参数，可直接分析数据中心中的文件：

```bash
# 默认 ASK_DATA 同步问数
python3 dms-data-agent/data_agent_cli.py file --file-id f-8941bx83xy9513xvpewrha01m -q "数据概览"

# ANALYSIS 深度分析（spawn 子 Agent 执行）
python3 dms-data-agent/data_agent_cli.py file --file-id f-8941bx83xy9513xvpewrha01m --session-mode ANALYSIS -q "深度分析"
```

---

## 常见问题与最佳实践

| 问题/场景 | 解决方案 |
|------|----------|
| 会话复用 | 用 `attach --session-id <旧ID>` 继续 |
| 会话超时/网络中断 | 用 `attach --checkpoint <N>` 断点续传 |
| 想修改分析计划 | 用 `attach -q "修改为..."` 而非重新 `db` |
| 查看历史会话 | 查看 `sessions/<session_id>/` 目录 |
| ANALYSIS 任务耗时太长 | spawn 子 Agent 后台处理，主流程不阻塞 |
| 重复执行报"worker already running" | 等待当前任务完成，或查看 `progress.log` 确认进度 |

> 完整的工作流示例（如从 DMS 导入、后台执行等）见：[工作流示例](references/WORKFLOWS.md)

---

## 项目结构

```
dms-data-agent/           # Skill 目录
├── SKILL.md              # 本文档
├── data_agent/           # SDK 模块
├── cli/                  # CLI 模块
├── sessions/             # 会话数据（分析结果、报告、图片）
└── references/           # 参考文档
```
