# Analysis Approach

**Core principles**:

- Volume first, mix second, target diagnosis last: first establish the reporting-month volume fact with [METRIC-001], then break down by channel, and close with [METRIC-002] achievement diagnosis;
- Ratios are non-additive: shares, achievement rates, and MoM growth always aggregate the numerator and denominator first, then divide; never SUM/AVG the ratio itself;
- The diagnosis scope = the target table's full track set for the month, no sampling allowed: the report detail must be fully written to disk, guaranteed by gates;
- Report numbers are physically same-sourced with analysis outputs: channel distribution and track details are written to disk and direct-read into the web report via $read, never retold by the LLM;
- NULL in sales_qty means the channel had no invoiced activity that month (≠ 0) and is not counted in sums; 0 means confirmed zero sales and participates in sums normally.

**Execution order**: objective_1 (overall sales and channel performance) → objective_2 (key-track target achievement diagnosis, reusing the reporting month and NULL semantics established in objective 1).

**General notes**: the reporting-month parameter uses the yyyyMM string format (e.g. 202512); the prior month for MoM = the calendar month before the reporting month, derived by the executor.

## Metric Calculation Compendium

| ID | Name | Formula | Semantics |
|---|---|---|---|
| METRIC-001 | Monthly Sales Volume | SUM(sales_qty) (biz_month = reporting month; applicable at channel/track granularity) | NULL means no invoiced activity, not counted; 0 means confirmed zero sales |
| METRIC-002 | Sales Target Achievement Rate | Monthly sales ÷ monthly target (NULLIF guards division by zero; aggregate numerator and denominator first, then divide) | Cross-table aligned on month+track; >= 1.0 is met; the rate is non-additive |
| METRIC-003 | Sales MoM Growth Rate | (current-month sales − prior-month sales) ÷ prior-month sales (NULLIF guards division by zero) | Prior month = the calendar month before the reporting month; record "—" when prior month is 0 or NULL |
