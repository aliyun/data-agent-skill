## Data Agent 自动监控

当 HEARTBEAT 触发时，按照以下步骤 chain-of-thought 执行：

1. 扫描目录：`~/.openclaw/dms-data-agent/sessions/*`（或你的实际 sessions 路径）
   - 使用 file_list 或 ls 列出所有子目录（每个对应一个 session_id）

2. 对每个 session 检查状态：
   - 读 `status.txt`
     - 如果 "running"：
       - 读 `progress.jsonl` 的最后一行（JSON 格式：{"percent": xx, "stage": "...", "message": "..."}）
       - 示例通知："📊 Data Agent Session {id} 已完成 {percent}% - {stage}：{message}"
     - 如果 "completed"：
       - 读 reports/ 目录或 result 总结文件
       - 生成 3-5 条核心洞察 bullet points
       - 示例通知："🎉 Session {id} 分析完成！核心洞察：\n- ...\n- ..."
     - 如果 "failed"：
       - 读 error.log，总结原因
       - 建议："请用 attach {id} 重试或检查日志"

3. 通知规则（防刷屏）：
   - 只在里程碑首次到达、完成或失败时发消息
   - 使用 send_message tool 或当前 channel（如 Telegram/WhatsApp）推送
   - 避免重复：记录上次报告的 percent（可写到 SESSION-STATE.md 或专用 data-agent-state.md）

4. 如果无新通知内容：
   - 安静结束 turn（回复 HEARTBEAT_OK，gateway 会自动丢弃）

优先使用 isolated agentTurn 执行检查（不干扰主对话）。