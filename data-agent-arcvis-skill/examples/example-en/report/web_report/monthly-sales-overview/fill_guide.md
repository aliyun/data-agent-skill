# fill_guide (schema_version 1)

This template demonstrates the "numbers-first direct-read" pattern: the two detail tables are carried over by the program via $read direct-read of objective on-disk outputs, while the LLM fills only the report month, the one-sentence overview, and the highlight list; there is no metrics.jsonl exception channel.

### report_month (reporting month)

Source: objective_1/G1

The reporting month, a yyyyMM string, same month as the two on-disk details. Example: `202512`. Writing non-yyyyMM forms such as "December" or "2025-12" is forbidden.

### headline (one-sentence overview)

Source: objective_1/G2

Distill a one-sentence overview from the overall sales conclusion: a directional summary of this month's structural and achievement conclusions. Writing concrete sales / achievement / MoM numbers by hand is forbidden — all numbers are shown by the page's KPI cards; the copy states only direction and attribution (e.g. "channel concentration rose while overall achievement came under pressure"). Inventing judgments absent from the conclusions is forbidden.

### highlights (monthly highlights)

Source: objective_1/G3, objective_2/G2

2~4 highlights, one sentence each: at least one on the channel-mix dimension (from the channel performance conclusions) and at least one on the achievement-diagnosis dimension (from the overall achievement conclusion). Channel and track names may be cited; writing concrete numbers by hand is forbidden, as is inventing. Example: `On the channel mix, Digital Singles contributed more than half of sales; on the achievement side, unmet tracks concentrated among newly released tracks with aggressive targets`.

### channel_rows (channel sales distribution)

Source: objective_1/G1 (direct read: channel_sales_distribution.csv)

The schema field is marked direct-read-allowed: prefer emitting a $read instruction so the program carries the data over from the source file; manual transcription is forbidden.
Column mapping (column_map): Channel → channel, Sales Qty → sales_qty, Revenue → sales_amount, Sales Share → share_pct.
The source file is the full 3-row channel set (descending by sales); example shape after resolve (values are mock demos):

```json
[{"channel": "Digital Singles", "sales_qty": 58000, "sales_amount": 116000.00, "share_pct": 0.58}]
```

### track_rows (track achievement detail)

Source: objective_2/G1 (direct read: track_achievement_detail.csv)

The schema field is marked direct-read-allowed: prefer emitting a $read instruction so the program carries the data over from the source file; manual transcription is forbidden.
Column mapping (column_map): Track ID → track_id, Track Name → track_name, Monthly Sales → sales_qty, Monthly Target → target_qty, Achievement Rate → achievement, Verdict → verdict.
The source file is the target table's full track set for the month, achievement rate ascending, rows with missing targets recorded as "—"; example shape after resolve (8 mock demo rows; the real row count follows the source file):

```json
[{"track_id": "T0107", "track_name": "Beyond the Gaze", "sales_qty": 4800, "target_qty": 12000, "achievement": 0.4000, "verdict": "Missed"}]
```
