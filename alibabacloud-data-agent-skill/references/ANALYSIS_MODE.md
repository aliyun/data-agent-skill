# Session Modes & Pro/Ultra Sub-Agent Specification

## Session Modes

| Mode | Features | Duration | Report |
|------|----------|----------|--------|
| `auto` (default) | Backend intelligent decision — automatically selects the best mode based on query complexity | varies | auto |
| `lite` | Quick query mode, sub-second response, suitable for quick Q&A and simple SQL queries | 30 sec - 2 min | ❌ |
| `pro` | Deep analysis with planning, multi-step reasoning, and report generation | 5-15 min | ✅ |
| `ultra` | Most thorough analysis with multi-dimensional insights; follows the same plan-confirmation flow as pro | 20-40 min | ✅ |

## Plan Mode

`--plan-mode` controls `SessionConfig.PlanMode`:

- `force` (default): always generate an execution plan — pro/ultra sessions enter `waiting_input` until the plan is confirmed via `attach -q "confirm"`.
- `disable`: skip planning and go straight to execution.

Set at session creation (`db` / `file` / `send`) or per message (`send -s <SESSION_ID>`); when omitted on a message, the session-level setting applies.

## Pro/Ultra Sub-Agent Behavior

pro and ultra mode tasks are long-running (5-40 minutes), **you must spawn a sub-agent to execute in background** to avoid blocking the main conversation flow.

---

## Main Agent Behavior

1. Inform user that deep analysis has started, provide Session ID
2. Return immediately, continue responding to other user questions (non-blocking)

---

## Sub-Agent Behavior

1. Execute command in background, periodically poll `status.txt` (every 30-60 seconds)
2. Push progress updates to main agent/user at key checkpoints
3. Return report or guide user to console when task completes

---

## Key Checkpoints for Sub-Agent to Report

| Checkpoint | Trigger Condition | Report Content | Required Action |
|------|----------|----------|----------|
| 🚀 Task Started | After command execution | Session ID, estimated duration | No action needed |
| 📋 Execution Plan Ready | `status.txt` = `waiting_input` | Show analysis plan/SQL | **Must wait for user confirmation, then `attach -q 'confirm execution'`** (only with `--plan-mode force`) |
| 📊 Phase Conclusion | New content in `progress.log` | Phase analysis summary | No action needed |
| ✅ Task Completed | `status.txt` = `completed` | Report link or file path | Guide user to view report |
| ❌ Task Failed | `status.txt` = `failed` | Error message | Prompt user to retry |

> ⚠️ **`waiting_input` is a mandatory wait point**: Worker has exited, sub-agent must present execution plan to user, execute `attach -q 'confirm execution'` after receiving confirmation, otherwise task will be permanently paused.

---

## Guide User to View Report After Task Completion

```bash
# Download report to local
python3 scripts/data_agent_cli.py reports --session-id <SESSION_ID>

# Or guide user to Data Agent console (recommended)
# https://agent.dms.aliyun.com/<region>/session/<SESSION_ID>
```
