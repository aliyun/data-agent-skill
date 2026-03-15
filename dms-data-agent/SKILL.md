---
name: dms-data-agent
description: |
  通过命令行调用阿里云瑶池 Data Agent for Analytics，帮助用户对企业数据库进行自然语言驱动的数据分析。
  Data Agent for Analytics 是阿里云瑶池数据库团队推出的面向企业用户的数据分析智能体，可根据自然语言描述自动完成需求分析、数据理解、分析洞察及报告生成。
  本工具支持：发现已托管在 DMS 的数据资源（实例/库/表）、发起问数或深度分析会话、实时跟踪执行进度、获取分析结论及生成的报告文件。
compatibility: |
  需要有效的阿里云 AccessKey（AK/SK）或 STS 临时凭证（AK/SK/Token）；
  需要安装 requirements.txt 中的依赖；
  数据源需已托管在阿里云瑶池数据库或 DMS。
metadata:
  author: DataAgent 研发
  version: "1.4.1"
---

---

# 安装


## 1. 创建虚拟环境并安装依赖
```bash
python3 -m venv venv
source venv/bin/activate  # Windows 用户请使用: venv\Scripts\activate
pip install -r requirements.txt
```

## 2. 配置凭证和地域（复制示例文件并填入真实值）
```bash
cp dms-data-agent/.env.example dms-data-agent/.env
# 然后编辑 dms-data-agent/.env，填入以下必填项：
#   - ALIBABA_CLOUD_ACCESS_KEY_ID
#   - ALIBABA_CLOUD_ACCESS_KEY_SECRET
#   - DATA_AGENT_REGION（如 cn-hangzhou, cn-beijing 等）
```

## 3. 配置 HEARTBEAT 自动监控
* 打开所在的workspace的HEARTBEAT文件 (默认路径为：~/.openclaw/workspace/HEARTBEAT.md），追加 dms-data-agent/assets/HEARTBEAT.md 的内容
* HEARTBEAT 会自动扫描 sessions 目录，读取 progress.jsonl 监控进展，并在里程碑到达时推送通知。



# Data Agent CLI — 统一命令行数据分析工具

> **IMPORTANT — 用户体验要求**
>
> Data Agent 的分析任务（尤其是 ANALYSIS/INSIGHT 模式）可能需要 **5-40 分钟**才能完成。为避免用户长时间等待而无感知，调用方（Agent）**必须**遵守以下规则：
>
> 1. **即时反馈**：发起异步任务后，立即告知用户已启动，并给出 Session ID 和查看进度的方式
> 2. **主动轮询**：定期检查 `status.txt`（建议每 30-60 秒），并将状态变化（running → waiting_input → completed）及时告知用户
> 3. **阶段性结论输出**：每当 `progress.log` 有新内容，可以读取并向用户展示当前阶段的结论，而非等到全部完成才一次性输出
> 4. **等待确认时必须中断**：当 `status.txt` 变为 `waiting_input` 时，**必须立即暂停**，向用户展示 `progress.log` 中最新的执行计划或 SQL，等待用户确认后再继续
> 5. **失败快速通知**：当 `status.txt` 变为 `failed` 时，立即读取 `result.json` 中的 error 信息并告知用户
>
> **⚠️ 特别注意：`waiting_input` 状态意味着 Worker 进程已退出**
>
> `waiting_input` 不是"系统正在处理中请等待"，而是 **"Worker 已完成当前阶段并退出，需要调用方采取行动"**。
> 此时如果只是继续轮询 `status.txt`，状态将**永远不会自动变化**，会导致调用方无限等待。
>
> 正确处理流程：
> 1. 检测到 `status.txt` = `waiting_input` 后，**立即读取 `progress.log`** 获取当前阶段的分析结论和待确认内容
> 2. 将执行计划、SQL 或报告绘制请求**展示给用户**，请求用户确认
> 3. 收到用户确认后，使用 `attach --session-id <ID> -q '确认执行'` 发送确认指令（这会启动新的 Worker 继续执行）
> 4. 确认指令发出后，继续轮询 `status.txt` 等待下一个状态变化

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

## ⚠️ 重要：会话复用原则

> **完整示例见：[工作流示例 - 会话复用](references/WORKFLOWS.md#会话复用工作流)**

### ✅ 正确做法

```bash
# 首次分析（默认异步执行，立即返回 Session ID）
python3 data_agent_cli.py db ... -q "问题 1"
# 输出：✅ Async task started. Session ID: abc123xyz

# 追问/修改/确认 - 使用 attach（复用会话，同样异步执行）
python3 data_agent_cli.py attach --session-id abc123xyz -q "..."
```

### ❌ 错误做法

```bash
# 每次都创建新会话（浪费资源，丢失上下文）
python3 data_agent_cli.py db ... -q "问题 1"  # 创建会话 A
python3 data_agent_cli.py db ... -q "问题 2"  # 又创建会话 B（错误！）
```

### 何时使用哪个命令？

| 场景 | 命令 | 说明 |
|------|------|------|
| 第一次分析某数据库 | `db` | 创建新会话 |
| 同一会话继续追问 | `attach --session-id <ID>` | 复用会话 |
| 修改执行计划 | `attach --session-id <ID>` | 复用会话，发送新指令 |
| 确认执行计划 | `attach --session-id <ID>` | 复用会话，发送确认 |
| 获取最终报告 | `reports --session-id <ID>` | 自动下载生成的文件，报告到本地 |

---

## 🤖 异步执行模式（默认）

所有 `db`、`file`、`attach` 命令默认以**异步模式**运行。命令立即返回 Session ID，实际分析在后台执行。

### 执行流程

```
用户执行命令 → 创建/连接会话 → 后台 Worker 执行分析 → 结果写入 session 目录
                                    ↓
                        sessions/{session_id}/
                        ├── status.txt        # 当前状态
                        ├── progress.log      # 完整执行日志与输出
                        ├── progress.jsonl    # Agent 专用的结构化执行日志
                        ├── checkpoint.txt    # 当前记录的 SSE 流位点
                        ├── result.json       # 结构化状态
                        ├── input.json        # 输入参数
                        └── worker.pid        # Worker 进程锁
```

### Session 目录文件说明

| 文件 | 内容 | 用途 |
|------|------|------|
| `status.txt` | `running` / `waiting_input` / `completed` / `failed` | 快速判断任务状态 |
| `progress.log` | 完整执行日志 | **Agent 读取进度与结果** |
| `progress.jsonl`| 结构化执行日志（每行一个 JSON） | **实时监控进展（推荐）** |
| `checkpoint.txt`| SSE 流位点值 | 断点续传依据 |
| `result.json` | 结构化状态 | 程序化检查 |
| `worker.pid` | Worker 进程 PID | 并发防护 |

### Agent 集成推荐读取方式

```bash
# 1. 检查状态
cat sessions/{session_id}/status.txt

# 2. 读取 progress.log 获取进展和结果
cat sessions/{session_id}/progress.log

# 3. 实时监控 JSONL 结构化日志（推荐）
tail -f sessions/{session_id}/progress.jsonl | jq '.data.content'

# 4. 检查 checkpoint 位点（判断是否有新进展）
tail -1 sessions/{session_id}/progress.jsonl | jq '.data.checkpoint'
```

**状态说明：**
- `running` → 任务执行中，可读取 `progress.jsonl` 获取阶段性结论
- `waiting_input` → ⚠️ Worker 已退出！需读取 `progress.log` 展示给用户，等用户确认后发送 `attach -q` 指令
- `completed` → 任务完成
- `failed` → 任务失败，读取 `result.json` 获取错误信息

### 同步模式

如果需要阻塞等待结果（不推荐），使用 `--no-async-run`：

```bash
python3 data_agent_cli.py db ... -q "问题" --no-async-run
```

### 并发防护

同一 Session 同时只允许一个 Worker 进程运行。如果对同一 Session 重复发起命令，会收到错误提示：

```
⚠️  A worker process (PID 12345) is already running for session abc123xyz.
   Check progress: cat sessions/abc123xyz/progress.log
   Current status: running
```

此时应等待当前任务完成，或通过 `cat sessions/<session_id>/progress.log` 查看进度。Worker 进程正常结束（完成/失败）后锁会自动释放；如果进程异常退出，下次执行时会自动检测并清理过期的锁文件。

### 通知机制

Worker 在状态变化时会主动推送通知（通过 `ASYNC_TASK_PUSH_URL` 或 CLI）。通知触发时机：

| 状态变化 | 通知内容 | 调用方应如何响应 |
|----------|----------|------------------|
| → `waiting_input` | 包含 `output.md` 路径和 `attach -q` 确认命令 | **立即读取 `output.md`，展示给用户，等用户确认后执行 `attach -q`** |
| → `completed` | 任务完成，包含 `output.md` 路径 | 读取 `output.md` 展示最终结果 |
| → `failed` | 任务失败，包含错误信息 | 展示错误信息给用户 |

> **关键**：收到 `waiting_input` 通知时，Worker 已退出，不会自动恢复。调用方必须主动执行 `attach -q` 才能继续推进任务。

配置通知推送：
```bash
# 方式一：HTTP 推送（推荐）
export ASYNC_TASK_PUSH_URL="https://your-webhook-endpoint/notify"
export ASYNC_TASK_AUTH_TOKEN="your-token"  # 可选

# 方式二：CLI 推送（openclaw / clawdbot）
export OPENCLAW_SESSION="your-session-id"
```

---

### 关键规则

| 场景 | Agent 行为 |
|------|-----------|
| `status.txt` = `waiting_input` | **Worker 已退出！** 读取 `progress.log` 展示给用户，等用户确认后通过 `attach -q` 发送指令 |
| 读取到执行计划 (Plan) | **必须**暂停，展示给用户，等待用户确认 |
| 读取到 `ask_report_render` | **必须**暂停，展示给用户，等待用户确认 |
| 用户说"减少步骤"/"修改计划" | 使用 `attach` 发送新指令，**不要**创建新会话 |
| 用户说"确认" | 使用 `attach` 发送确认到对应会话 |
| 实时监控进展 | `tail -f sessions/{session_id}/progress.jsonl \| jq '.data.content'` |

> **⚠️ 会话时效**：一个会话（Session）的有效时间为 **6 小时**，超过此时间后继续追问会失败，需要通过 `db` 命令重新发起新会话。


---

## 快速开始

```bash
# 1. 查看可用数据库
python3 data_agent_cli.py ls

# 2. 发起分析（默认异步，立即返回 Session ID）
python3 data_agent_cli.py db --dms-instance-id <ID> --dms-db-id <ID> \
    --instance-name <NAME> --db-name <DB> --tables "t1,t2" -q "问题"

# 3. 查看状态和结果
cat sessions/<session_id>/status.txt
cat sessions/<session_id>/progress.log

# 4. 复用会话追问/确认
python3 data_agent_cli.py attach --session-id <ID> -q "确认执行"
```

> 📖 完整工作流请参考 [工作流示例](references/WORKFLOWS.md)

---

## 子命令一览

| 子命令 | 用途 | 默认异步 |
|--------|------|:--------:|
| `ls` | 列出 Data Center 中的数据库和表 | - |
| `db` | 连接数据库，发起分析会话 | ✅ |
| `file` | 上传本地文件或分析数据中心中的现有文件 | ✅ |
| `attach` | 连接已有会话，继续对话（有 -q 时异步） | ✅ |
| `dms` | DMS 工具集成（发现数据）| - |
| `import` | 将 DMS 数据库导入 Data Center | - |
| `reports`| 查看或下载会话生成的报告和图表文件 | - |

> 详情见：[命令参考](references/COMMANDS.md)

---

## 从数据中心文件进行分析

使用 `file` 子命令配合 `--file-id` 参数，可直接分析数据中心中的文件：

```bash
python3 data_agent_cli.py file --file-id f-8941bx83xy9513xvpewrha01m --session-mode ANALYSIS
```

支持直接使用已存在的文件ID（如 `f-8941bx83xy9513xvpewrha01m`），无需上传本地文件。

---

## 常见问题与最佳实践

| 问题/场景 | 解决方案 |
|------|----------|
| 会话复用 | 用 `attach --session-id <旧ID>` 继续 |
| 会话超时/网络中断 | 用 `attach --checkpoint <N>` 断点续传 |
| 想修改分析计划 | 用 `attach -q "修改为..."` 而非重新 `db` |
| 查看历史会话 | 查看 `sessions/<session_id>/` 目录 |
| 需要同步执行 | 使用 `--no-async-run` 参数 |
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
