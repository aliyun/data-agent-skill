# fill_guide（schema_version 1）

本模板为「数字优先直读」示范：两张明细表由程序 $read 直读 objective 落盘产物，LLM 只填报告月份、一句话总览与要点；无 metrics.jsonl 例外通道。

### report_month（报告月份）

来源：objective_1/G1

报告月份，yyyyMM 字符串，与两张落盘明细同月。示例：`202512`。禁止写"12月""2025-12"等非 yyyyMM 形态。

### headline（一句话总览）

来源：objective_1/G2

从整体销量结论提炼一句话总览：本月结构性与达成性结论的方向性概括。禁止手写具体销量 / 达成率 / 环比数字——数值一律由页面 KPI 卡呈现，文案只写方向与归因（如"渠道集中度上升、整体达成承压"）。禁止臆造结论中不存在的判断。

### highlights（本月要点）

来源：objective_1/G3、objective_2/G2

2~4 条要点，每条一句话：渠道结构维度（源自渠道表现结论）与达成诊断维度（源自整体达成结论）各至少一条。可点名渠道与曲目名称，禁止手写具体数字，禁止臆造。示例：`渠道结构上数字单曲贡献过半，达成侧未达标曲目集中在目标偏高的新发行曲目`。

### channel_rows（渠道销量分布）

来源：objective_1/G1（直读：渠道销量分布.csv）

schema 已标记可直读：优先输出 $read 指令由程序从源文件搬运，禁止手抄。
列名对照（column_map）：渠道 → channel、销量 → sales_qty、收入 → sales_amount、销量占比 → share_pct。
源文件为 3 行渠道全集（按销量降序）；resolve 后形态示例（样例为 mock 演示值）：

```json
[{"channel": "数字单曲", "sales_qty": 58000, "sales_amount": 116000.00, "share_pct": 0.58}]
```

### track_rows（曲目达成明细）

来源：objective_2/G1（直读：曲目达成明细.csv）

schema 已标记可直读：优先输出 $read 指令由程序从源文件搬运，禁止手抄。
列名对照（column_map）：曲目编号 → track_id、曲目名称 → track_name、当月销量 → sales_qty、当月目标 → target_qty、达成率 → achievement、判定 → verdict。
源文件为目标表当月曲目全集、达成率升序、目标缺失行记 "—"；resolve 后形态示例（样例为 8 行 mock 演示值，真实运行行数以源文件为准）：

```json
[{"track_id": "T0107", "track_name": "目光之外", "sales_qty": 4800, "target_qty": 12000, "achievement": 0.4000, "verdict": "未达标"}]
```
