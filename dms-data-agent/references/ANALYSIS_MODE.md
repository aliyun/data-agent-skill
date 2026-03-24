# ANALYSIS 模式子 Agent 规范

ANALYSIS 模式任务耗时长（5-40 分钟），**必须 spawn 一个子 Agent 在后台执行**，避免阻塞主对话流程。

---

## 主 Agent 行为

1. 告知用户已启动深度分析，给出 Session ID
2. 立即返回，继续响应用户其他问题（不阻塞）

---

## 子 Agent 行为

1. 后台执行命令，定期轮询 `status.txt`（每 30-60 秒）
2. 遇到关键节点时向主 Agent / 用户推送进度
3. 任务完成后返回报告或引导用户到控制台查看

---

## 子 Agent 需要上报的关键节点

| 节点 | 触发条件 | 上报内容 | 所需操作 |
|------|----------|----------|----------|
| 🚀 任务启动 | 命令执行后 | Session ID、预计耗时 | 无需操作 |
| 📋 执行计划就绪 | `status.txt` = `waiting_input` | 展示分析计划/SQL | **必须等待用户确认，然后 `attach -q '确认执行'`** |
| 📊 阶段结论 | `progress.log` 有新内容 | 阶段性分析结论摘要 | 无需操作 |
| ✅ 任务完成 | `status.txt` = `completed` | 报告链接或文件路径 | 引导用户查看报告 |
| ❌ 任务失败 | `status.txt` = `failed` | 错误信息 | 提示用户重试 |

> ⚠️ **`waiting_input` 是强制等待点**：Worker 已退出，子 Agent 必须将执行计划展示给用户，收到确认后执行 `attach -q '确认执行'`，否则任务将永久暂停。

---

## 任务完成后引导用户查看报告

```bash
# 下载报告到本地
python3 dms-data-agent/data_agent_cli.py reports --session-id <SESSION_ID>

# 或引导用户到 Data Agent 控制台查看（推荐）
# https://agent.dms.aliyun.com/<地域>/session/<SESSION_ID>
```
