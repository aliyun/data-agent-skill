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

### 新版特性 (v1.6.6)

- **原生异步执行模式**：`db` 和 `file` 命令默认异步运行，发起任务后立刻返回 `Session ID` 并后台执行。
- **会话隔离**：每个 Session 独享工作目录（`sessions/<id>/`），包含状态记录和进度流。
- **JSON格式进度日志**：progress.log 输出 JSON 格式日志，便于机器解析。
- **增强的 attach 模式**：修复了 attach 模式下计划确认后进度不更新的问题，确保进度日志持续刷新。
- **优化的日志输出**：移除了 `progress.jsonl` 文件，仅保留 `progress.log` 作为主要日志输出。

### Skill 信息

| 属性 | 值 |
|------|-----|
| **Name** | `dms-data-agent` |
| **Version** | `1.6.6` |
| **Author** | DataAgent研发 |
| **标准** | [Agent Skills](https://agentskills.io/) |

---

## 快速上手

### 安装

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

### 配置OpenClaw的Proactive Agent 能力

可以把 assets/HEARTBEAT.md 复制或者更新到OpenClaw工作目录下的HEARTBEAT.md文件中，OpenClaw会自动检测HEARTBEAT.md文件，并自动将技能信息同步到OpenClaw中。


### 基本使用

```bash
# 1. 查看 Data Center 中的数据库
python3 dms-data-agent/data_agent_cli.py ls

# 2. 发起问数分析（默认异步执行）
python3 dms-data-agent/data_agent_cli.py db \
    --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> \
    --instance-name <INSTANCE_NAME> --db-name chinook \
    --tables "album,artist,invoice" \
    -q "谁的销售额最高"

# 3. 查看状态和进度
cat sessions/<session_id>/status.txt
cat sessions/<session_id>/progress.log

# 4. 连接已有会话继续追问
python3 dms-data-agent/data_agent_cli.py attach --session-id <SESSION_ID> -q "按月分解"
```

## 会话目录结构

每个 Session 在 `sessions/<session_id>/` 目录下维护独立的状态和日志：

| 文件 | 内容 | 用途 |
|------|------|------|
| `status.txt` | `running` / `waiting_input` / `completed` / `failed` | 快速判断任务状态 |
| `progress.log` | JSON格式执行日志（每行一个 JSON 对象） | **Agent 实时监控进展（推荐）** |
| `checkpoint.txt` | SSE 流位点值 | 断点续传依据 |
| `result.json` | 结构化状态 | 程序化检查 |
| `input.json` | 输入参数 | 追溯任务配置 |
| `worker.pid` | Worker 进程 PID | 并发防护锁 |

## 更多信息

完整使用指南请参见：[dms-data-agent/SKILL.md](dms-data-agent/SKILL.md)

## 依赖要求

- Python 3.9+
- 详见 `requirements.txt`

## License

Apache-2.0 license