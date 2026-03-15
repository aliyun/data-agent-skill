# Data Agent Skill

一个 [Agent Skill](https://agentskills.io/)，用于通过自然语言驱动阿里云瑶池 **Data Agent for Analytics** 进行企业数据分析。

## 概述

本项目是一个 AI Agent Skill，让 AI 助手（如 Claude、Qoder 等）能够调用阿里云瑶池 Data Agent，帮助企业用户通过自然语言完成数据分析任务。

### 核心能力

- **数据发现** - 查询 DMS 中的实例、库、表
- **数据导入** - 将 DMS 数据库导入 Data Agent Data Center
- **问数分析** - 发起 ASK_DATA / ANALYSIS / INSIGHT 会话
- **文件分析** - 上传 CSV/Excel/JSON 文件并分析
- **会话复用** - 连接已有会话、确认计划、追问
- **流式输出** - 实时跟踪分析进度，自动保存报告

### 新版特性 (v1.4.1)

- **原生异步执行模式**：重构了底层执行架构，`db` 和 `file` 命令现已默认采用异步模式运行。发起任务后立刻返回 `Session ID` 并将进程置于后台执行。
- **状态机与会话隔离**：每个 Session 独享隔离的工作目录（`sessions/<id>/`），内置独立的状态记录（`status.txt`）、进度流（`progress.log`）以及专供 Agent 读取的结构化数据流（`progress.jsonl`）。
- **完善的进度保护与并发锁**：支持 `worker.pid` 进程锁机制，避免同一会话产生数据冲突；支持精确的 SSE `checkpoint.txt` 位点记录以实现断点续传。
- **原生后台通知集成**：支持 OpenClaw / Clawdbot 的原生挂起通知事件（`tools.exec.notifyOnExit`），分析结束后立刻唤醒前端大模型。
- **Agent 友好的输出格式**：重构了终端输出格式，采用标准 Markdown，消除了复杂 ASCII 边框对 AI 解析带来的干扰，方便大模型精准提取关键结论。
- **独立报告下载**：新增 `reports` 子命令，支持一键查看与自动下载生成的报告和图表文件。
- **实时进度监控**：worker 进程强制 flush 输出，确保 `progress.log` 和 `progress.jsonl` 实时同步更新，Agent 可通过 `tail -f sessions/{session_id}/progress.jsonl` 实时监控分析进展。


### Skill 信息

| 属性 | 值 |
|------|-----|
| **Name** | `dms-data-agent` |
| **Version** | `1.4.1` |
| **Author** | DataAgent研发 |
| **标准** | [Agent Skills](https://agentskills.io/) |

---

## CLI 功能特性

- **数据发现**：通过 `dms` 子命令查询 DMS 中的实例、库、表
- **数据导入**：通过 `import` 子命令将 DMS 数据库导入 Data Agent Data Center
- **问数分析**：通过 `db` 子命令发起 ASK_DATA / ANALYSIS / INSIGHT 会话
- **文件分析**：通过 `file` 子命令上传 CSV/Excel/JSON 文件并分析
- **会话复用**：通过 `attach` 子命令连接已有会话、确认计划、追问（会话有效期为 6 小时）
- **后台执行与状态追踪**：自动使用异步非阻塞模式执行分析任务，随时读取工作目录下的进度日志与最新状态。
- **产物管理**：通过 `reports` 子命令一键列出和下载会话中生成的所有结果文件和图表。

## 安装

```bash
# 1. 克隆仓库
git clone <repo-url>
cd data-agent-skill

# 2. 创建虚拟环境并安装依赖
python3 -m venv venv
source venv/bin/activate  # Windows 用户请使用: venv\Scripts\activate
pip install -r requirements.txt

# 3. 配置凭证
cp dms-data-agent/.env.example dms-data-agent/.env
# 编辑 dms-data-agent/.env，填入以下必填项：
#   ALIBABA_CLOUD_ACCESS_KEY_ID
#   ALIBABA_CLOUD_ACCESS_KEY_SECRET
#   DATA_AGENT_REGION（如 cn-hangzhou）
```

### 环境变量说明

| 变量 | 必填 | 说明 |
|------|------|------|
| `ALIBABA_CLOUD_ACCESS_KEY_ID` | 是 | 阿里云 Access Key ID |
| `ALIBABA_CLOUD_ACCESS_KEY_SECRET` | 是 | 阿里云 Access Key Secret |
| `ALIBABA_CLOUD_SECURITY_TOKEN` | 否 | STS 临时凭证 Token |
| `DATA_AGENT_REGION` | 是 | 地域 ID，如 `cn-hangzhou`、`cn-beijing` |
| `DATA_AGENT_ENDPOINT` | 否 | 自定义接入点，不填则自动生成 |
| `DATA_AGENT_TIMEOUT` | 否 | 请求超时秒数，默认 `300` |

## 核心概念

> **⚠️ 关键前提**：`db` 子命令只能分析**已导入到 Data Agent Data Center** 的数据库。
>
> - **Data Center**：Data Agent 的数据中心，只有这里的数据库才能被分析
> - **DMS**：阿里云数据管理服务，存储着所有数据库的元数据
> - **两者不等**：DMS 中注册的数据库 ≠ Data Center 中的数据库

## 快速上手

### 方式一：Data Center 已有数据库（推荐）

```bash
# 1. 查看 Data Center 中的数据库
python3 dms-data-agent/data_agent_cli.py ls

# 2. 发起问数分析（默认异步执行，瞬间返回 Session ID）
python3 dms-data-agent/data_agent_cli.py db \
    --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> \
    --instance-name <INSTANCE_NAME> --db-name chinook \
    --tables "album,artist,invoice" \
    --session-mode ASK_DATA \
    -q "谁的销售额最高"

# 3. 查看状态和最新执行进度
cat sessions/<session_id>/status.txt
cat sessions/<session_id>/progress.log

# 4. 连接已有会话继续追问、或确认执行计划
python3 dms-data-agent/data_agent_cli.py attach --session-id <SESSION_ID> -q "按月分解"

# 5. 分析结束后下载报告和图表
python3 dms-data-agent/data_agent_cli.py reports --session-id <SESSION_ID>
```

### 方式二：从 DMS 发现并导入（Data Center 没有目标库时）

```bash
# 1. 查询 DMS 实例列表
python3 dms-data-agent/data_agent_cli.py dms list-instances

# 2. 搜索目标数据库（获取 Database ID）
python3 dms-data-agent/data_agent_cli.py dms search-database --search-key employees

# 3. 查看库中的表
python3 dms-data-agent/data_agent_cli.py dms list-tables --database-id <DATABASE_ID>

# 4. 检查 Data Center 是否已有该库
python3 dms-data-agent/data_agent_cli.py ls --search employees

# 5. 导入到 Data Center（必须步骤）
python3 dms-data-agent/data_agent_cli.py import \
    --dms-instance-id <DMS_INSTANCE_ID> \
    --dms-db-id <DMS_DB_ID> \
    --instance-name <INSTANCE_NAME> \
    --db-name employees \
    --tables "departments,employees,salaries"

# 6. 发起分析
python3 dms-data-agent/data_agent_cli.py db \
    --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> \
    --instance-name <INSTANCE_NAME> --db-name employees \
    --tables "departments,employees,salaries" \
    -q "查询平均工资最高的部门"
```

## 子命令一览

| 子命令 | 用途 |
|--------|------|
| `ls` | 列出 Data Center 中的数据库和表 |
| `db` | 连接数据库，异步发起 ASK_DATA / ANALYSIS / INSIGHT 分析会话 |
| `file` | 上传本地 CSV/Excel/JSON 文件并异步分析 |
| `attach` | 连接已有会话，继续对话、确认计划或查看进度 |
| `dms` | 查询 DMS 元数据（list-instances / search-database / list-tables）|
| `import` | 将 DMS 数据库表导入到 Data Agent Data Center |
| `reports`| 查看或下载会话中生成的报告和图表产物 |

## 会话模式说明

| 模式 | 耗时 | 特点 | 适用场景 |
|------|------|------|----------|
| `ASK_DATA` | 秒级 | 快速 SQL 查数 + 自然语言回答 | 简单查询、报表取数 |
| `ANALYSIS` | **15~20 分钟** | 深度分析、多步推理、生成完整报告（HTML/Markdown/图表） | 复杂分析、趋势研究 |
| `INSIGHT` | 半小时以上 | 数据洞察 | 数据探索 |

> **⚠️ 重要行为指令**：
> 1. 在 `ANALYSIS` 模式下，如果从输出中读取到了 Data Agent 返回的分析**计划（Plan）**，默认情况下 **必须先与用户确认**。根据用户的确认反馈，再决定是否将反馈发送给 Data Agent 继续执行。
> 2. 当遇到 `ask_report_render`（例如：将要为您绘制网页报告...请确认是否需要执行绘制）时，**必须先与用户确认**，获取用户确认后再将结果发给 Data Agent。

> **⏱️ 耗时提示**：ANALYSIS 模式耗时较长（15~20 分钟），请耐心等待。系统默认在后台运行分析任务。你可以通过查看本地的进度日志来跟踪它的状态：

```bash
# 查看会话的当前状态（running, waiting_input, completed, failed）
cat sessions/<SESSION_ID>/status.txt

# 查看最新的输出日志（纯文本格式）
cat sessions/<SESSION_ID>/progress.log

# 实时监控结构化进度（推荐，JSONL 格式）
tail -f sessions/<SESSION_ID>/progress.jsonl | jq '.data.content'
```

## 会话目录结构

每个 Session 在 `sessions/<session_id>/` 目录下维护独立的状态和日志：

| 文件 | 内容 | 用途 |
|------|------|------|
| `status.txt` | `running` / `waiting_input` / `completed` / `failed` | 快速判断任务状态 |
| `progress.log` | 完整执行日志（纯文本） | 人工阅读进度与结果 |
| `progress.jsonl` | 结构化执行日志（每行一个 JSON） | **Agent 实时监控进展（推荐）** |
| `checkpoint.txt` | SSE 流位点值 | 断点续传依据 |
| `result.json` | 结构化状态 | 程序化检查 |
| `input.json` | 输入参数 | 追溯任务配置 |
| `worker.pid` | Worker 进程 PID | 并发防护锁 |

## 项目结构

```
data-agent-skill/
├── requirements.txt
├── dms-data-agent/           # Skill 目录（符合 Agent Skills 规范）
│   ├── SKILL.md              # Skill 主文档
│   ├── data_agent/           # SDK 模块
│   │   ├── client.py         # API 客户端
│   │   ├── config.py         # 配置管理
│   │   ├── session.py        # 会话管理
│   │   ├── message.py        # 消息处理
│   │   ├── sse_client.py     # SSE 流式接收
│   │   ├── file_manager.py   # 文件上传管理
│   │   ├── mcp_tools.py      # DMS 工具集成
│   │   ├── models.py         # 数据模型
│   │   └── exceptions.py     # 异常定义
│   ├── cli/                  # CLI 模块
│   │   ├── parser.py         # 参数解析与入口
│   │   ├── cmd_ls.py         # ls 子命令
│   │   ├── cmd_db.py         # db 子命令
│   │   ├── cmd_file.py       # file 子命令
│   │   ├── cmd_attach.py     # attach 子命令
│   │   ├── cmd_dms.py        # dms 子命令
│   │   ├── cmd_import.py     # import 子命令
│   │   ├── formatters.py     # SSE 事件格式化
│   │   └── streaming.py      # 流式输出处理
│   ├── scripts/              # 可执行脚本
│   ├── references/           # 参考文档
│   └── assets/               # 静态资源
└── tests/                    # 单元测试
```

## 在 AI Agent 中使用

### 在 OpenClaw 中使用

* 直接告诉OpenClaw本项目地址即可

<img width="2528" height="1694" alt="image" src="https://github.com/user-attachments/assets/2f3795c4-4ab5-4805-9d49-7088cbf4a0f9" />

* 查看有哪些数据库

<img width="2364" height="1478" alt="image" src="https://github.com/user-attachments/assets/ae1295ae-ddc9-4ac9-8fbf-d0d13bee1c2e" />

* 问数
<img width="2358" height="1456" alt="image" src="https://github.com/user-attachments/assets/99a4c166-61e8-404c-9042-7ffded595124" />

* 分析出报告

<img width="2360" height="2034" alt="image" src="https://github.com/user-attachments/assets/335476d1-a766-4457-8b43-a904decbcdc2" />



### 在其他 AI Agent 中使用

确保 Agent 能访问 `dms-data-agent/SKILL.md` 文件，该文件包含：
- **YAML Frontmatter** - Skill 元数据（名称、描述、兼容性）
- **详细使用指南** - 完整的工作流、参数说明、示例命令

## 详细文档

- **[Skill 主文档](dms-data-agent/SKILL.md)** — Skill 概述与快速开始
- **[命令参考](dms-data-agent/references/COMMANDS.md)** — 完整参数说明
- **[工作流示例](dms-data-agent/references/WORKFLOWS.md)** — 详细操作步骤

## 依赖要求

- Python 3.9+
- 详见 `requirements.txt`

## License

Apache-2.0 license
