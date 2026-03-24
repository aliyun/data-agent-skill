# 典型工作流

本文档包含完整的操作工作流示例。

---

## 方式一：从 Data Center 已有数据库开始（推荐）

> **前提条件**：数据库必须已存在于 Data Agent Data Center 中。

```
第 1 步  ls          ── 列出可用数据库
第 2 步  ls --db-id  ── 列出指定库的表清单，并打印可直接复制的 db 命令
第 3 步  db -q       ── 发起问数/分析会话，实时输出进度和结论
第 4 步  attach      ── 连接已有会话（确认计划 / 追问 / 查看最新结果）
```

### 完整示例

```bash
# Step 1: 发现数据库
python3 dms-data-agent/data_agent_cli.py ls

# Step 2: 查看 chinook 库的表并获取命令模板
python3 dms-data-agent/data_agent_cli.py ls --db-id <AgentDbId>

# Step 3: 问数（复制上一步生成的命令，替换问题）
python3 dms-data-agent/data_agent_cli.py db \
    --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> \
    --instance-name <INSTANCE_NAME> --db-name chinook \
    --tables "album,artist,invoice" \
    --session-mode ASK_DATA \
    -q "谁的销售额最高"

# Step 4: 连接已有会话继续追问
python3 dms-data-agent/data_agent_cli.py attach --session-id <SESSION_ID> -q "按月份分解一下"
```

---

## 方式二：从 DMS 实例发现并导入到 Data Center

> **适用场景**：当 Data Agent Data Center 中没有需要的数据库时。

```
第 1 步  dms list-instances    ── 查询 DMS 中的数据库实例
第 2 步  dms search-database   ── 搜索实例中的数据库
第 3 步  dms list-tables       ── 列出数据库中的表
第 4 步  ls                    ── 检查 Data Center 是否已有该数据库
第 5 步  import                ── 将 DMS 数据库表导入到 Data Center
第 6 步  db                    ── 发起问数/分析会话
第 7 步  attach                ── 连接已有会话
```

### 完整示例

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

---

## 后台运行最佳实践

对于耗时较长的 `ls` 和 `db` 命令，建议在后台运行：

```bash
# 后台启动 ANALYSIS 任务
nohup python3 dms-data-agent/data_agent_cli.py db \
    --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> \
    --instance-name <INSTANCE_NAME> --db-name chinook \
    --tables "invoice,invoiceline,customer" \
    --session-mode ANALYSIS \
    -q "分析销售趋势并生成报告" > analysis.log 2>&1 &

# 从日志中获取会话 ID
grep "Session ready" analysis.log

# 随时 attach 查看进度
python3 dms-data-agent/data_agent_cli.py attach --session-id <SESSION_ID>

# 如果网络中断或者想要恢复到某个特定的状态，可以指定 checkpoint
python3 dms-data-agent/data_agent_cli.py attach --session-id <SESSION_ID> --checkpoint <CHECKPOINT_NUM>
```

**优势**：
- 避免网络中断导致任务失败（配合 `--checkpoint` 参数可无缝续传）
- 可随时通过 `attach` 查看进度
- 输出日志便于后续查看

---

## 会话复用工作流

对于同一数据库的多次分析，建议复用会话以提高效率：

```bash
# 第 1 次分析：创建新会话
python3 dms-data-agent/data_agent_cli.py db \
    --dms-instance-id <DMS_INSTANCE_ID> --dms-db-id <DMS_DB_ID> \
    --instance-name <INSTANCE_NAME> --db-name chinook \
    --tables "invoice,customer" \
    --session-mode ANALYSIS \
    -q "分析 2024 年销售趋势"
# 输出: ✅ Async task started. Session ID: abc123xyz

# 第 2 次分析：复用同一会话，追问细节
python3 dms-data-agent/data_agent_cli.py attach --session-id abc123xyz -q "按月份分解销售额"

# 第 3 次分析：修改计划
python3 dms-data-agent/data_agent_cli.py attach --session-id abc123xyz -q "简化为 3 个步骤"

# 第 4 次分析：确认执行
python3 dms-data-agent/data_agent_cli.py attach --session-id abc123xyz -q "确认执行"

# 第 5 步：读取最终结果
cat sessions/abc123xyz/progress.log

# 第 6 步：下载生成的报告
python3 data_agent_cli.py reports --session-id abc123xyz
```

**复用优势**：
- 避免重复的数据理解阶段
- 保留上下文历史
- 减少 API 调用次数

> 详细子 Agent 实现规范见 [ANALYSIS_MODE.md](ANALYSIS_MODE.md)
