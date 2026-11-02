---
contract_version: 1
gates:
  - id: G1
    type: table
    title: Channel sales distribution written to disk
    check: |-
      The reporting-month ({month}, yyyyMM) channel sales distribution is written to disk as
      channel_sales_distribution.csv (UTF-8, header in the first row),
      with exactly 4 fixed columns in order: Channel / Sales Qty / Revenue / Sales Share.
      Row granularity: one row = one sales channel, exactly 3 rows (Digital Singles / Digital Albums / Membership Single Bundle),
      no truncation or sampling. Sales and revenue follow the [METRIC-001] monthly semantics
      (SUM(sales_qty) / SUM(sales_amount), NULL for no invoiced activity is not counted);
      sales share = channel sales ÷ total monthly sales, aggregate the numerator and denominator first then divide,
      keep 4 decimal places. Sorting: descending by sales. Row count <= 1000.
  - id: G2
    type: conclusion
    title: Overall sales conclusion
    check: |-
      Report the reporting-month total sales ([METRIC-001]) and the sales MoM growth rate ([METRIC-003])
      as concrete numbers with an up/down verdict;
      the conclusion must cite concrete numbers; purely qualitative wording (e.g. "sales grew somewhat") is forbidden.
  - id: G3
    type: conclusion
    title: Channel performance conclusion
    check: |-
      For each channel, report the magnitude (monthly sales number) and the MoM direction (up / down / flat),
      citing concrete numbers; the channel enumeration must match channel_sales_distribution.csv
      (the full set of three channels), omitting channels is forbidden.
  - id: G4
    type: constraint
    severity: warning
    title: Never average ratios
    check: |-
      When aggregating across channel granularity, aggregate the numerator and denominator first then divide;
      SUM/AVG over sales shares or achievement rates themselves is forbidden.
      Applies only when this objective involves cross-granularity aggregation.
metrics_deps:
  - id: METRIC-001
    usage: |-
      Data-retrieval definition for monthly total sales and per-channel sales
  - id: METRIC-003
    usage: |-
      Judging the MoM direction and magnitude, overall and per channel
deliverables:
  - file: channel_sales_distribution.csv
    columns: [Channel, Sales Qty, Revenue, Sales Share]
---

# Objective: Overall Sales and Channel Performance

## Analysis Approach

Volume first, then mix: retrieve the [METRIC-001] total sales and [METRIC-003] MoM for the reporting month to establish the "how did we sell" fact; then apply the same semantics channel by channel along the channel enumeration of track_sales_month (the full set of three channels) and write channel_sales_distribution.csv to disk. The sales share is computed before writing (the column enters the direct-read contract; the template does not recompute shares). The reporting month uses the yyyyMM string format; NULL in sales_qty means no invoiced activity and is not counted (consistent with the [METRIC-001] semantics).

## Value List

| Reported Value | Data-retrieval Definition (filter → aggregate → formula) | Metrics |
|---|---|---|
| Monthly total sales | biz_month = reporting month → SUM(sales_qty), NULL for no invoiced activity not counted | METRIC-001 |
| Per-channel sales | biz_month = reporting month AND channel_name = the channel → SUM(sales_qty) | METRIC-001 |
| Per-channel revenue | same filter as above → SUM(sales_amount) | METRIC-001 |
| Channel sales share | channel sales ÷ total monthly sales (aggregate numerator and denominator first, then divide) | METRIC-001 |
| Overall MoM | (reporting-month [METRIC-001] − prior-month [METRIC-001]) ÷ prior-month [METRIC-001] | METRIC-003 |
| Channel MoM | same formula as overall MoM, computed within the channel | METRIC-003 |

## Tables Used

- track_sales_month

## Deliverables

- **channel_sales_distribution.csv**: 3 channel rows × 4 columns (Channel / Sales Qty / Revenue / Sales Share), descending by sales; share kept to 4 decimal places
- **Overall sales conclusion**: total sales + MoM number and up/down verdict (citing concrete numbers)
- **Channel performance conclusion**: per-channel magnitude and MoM direction (citing concrete numbers)

## Chart Methodology

### Chart gate: reporting-month channel sales distribution horizontal bar chart

- **Chart spec**: horizontal bar chart; title "Reporting-Month Channel Sales Distribution"; Y axis = channels (three channels, descending by sales); X axis = sales (copies), linear scale starting at 0; a single data series, bar length = channel sales, sales number and share annotated at the right end inside the bar; no legend.
- **Drawing rationale**: only three bars for channels, horizontal layout leaves enough room for channel names; the data is same-sourced with channel_sales_distribution.csv (the report template direct-reads the same on-disk file), so chart and table can never disagree.
