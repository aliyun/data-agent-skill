---
contract_version: 1
depends_on: [1]
gates:
  - id: G1
    type: table
    title: Track achievement detail written to disk
    check: |-
      The reporting-month track achievement detail is written to disk as track_achievement_detail.csv
      (UTF-8, header in the first row),
      with exactly 6 fixed columns in order: Track ID / Track Name / Monthly Sales / Monthly Target /
      Achievement Rate / Verdict.
      Row granularity: one row = one track in the target table's full track set for the month
      (targets in this domain cover the full catalog, 120 rows in total),
      listing only head or tail samples is forbidden. Monthly sales follows [METRIC-001]
      (track granularity, NULL for no invoiced activity not counted),
      achievement rate follows [METRIC-002] (aggregate numerator and denominator within the track first,
      then divide, keep 4 decimal places),
      verdict = achievement rate >= 1.0 is Met, < 1.0 is Missed; tracks with a missing target
      (no row in the target table) get "—" for both achievement rate and verdict,
      excluded from verdicts. Sorting: achievement rate ascending (worst first), "—" rows last.
      Row count <= 1000.
  - id: G2
    type: conclusion
    title: Overall achievement conclusion
    check: |-
      Report the reporting-month overall sales achievement rate ([METRIC-002], all-tracks basis) and the
      count and share of unmet tracks (unmet track count ÷ tracks participating in verdicts),
      citing concrete numbers; purely qualitative wording is forbidden.
metrics_deps:
  - id: METRIC-001
    usage: |-
      Data-retrieval definition for monthly sales at track granularity
  - id: METRIC-002
    usage: |-
      Track achievement rate computation and the met/missed verdict baseline
deliverables:
  - file: track_achievement_detail.csv
    columns: [Track ID, Track Name, Monthly Sales, Monthly Target, Achievement Rate, Verdict]
---

# Objective: Key-Track Target Diagnosis

## Analysis Approach

Continuing from the reporting month and NULL semantics established in objective 1, focus on "how targets were met": take the target table's full track set for the month as the diagnosis scope (covering the full catalog), retrieve each track's monthly sales ([METRIC-001] at track granularity), divide by its target to get the achievement rate ([METRIC-002]); write track_achievement_detail.csv sorted by achievement rate ascending, worst-unmet tracks first, for direct consumption by the report. The overall achievement rate aggregates the numerator and denominator over the full track set before dividing; averaging per-track achievement rates is forbidden; tracks with a missing target are listed on "—" rows, counted neither as met nor unmet.

## Value List

| Reported Value | Data-retrieval Definition (filter → aggregate → formula) | Metrics |
|---|---|---|
| Track monthly sales | biz_month = reporting month AND track_id = the track → SUM(sales_qty) | METRIC-001 |
| Track achievement rate | track monthly sales ÷ track monthly target (NULLIF guards division by zero; aggregate first, then divide) | METRIC-002 |
| Verdict | achievement rate >= 1.0 Met / < 1.0 Missed / missing target "—" | METRIC-002 |
| Overall sales achievement rate | sum of monthly sales over all tracks ÷ sum of targets (aggregate numerator and denominator first, then divide) | METRIC-002 |
| Unmet track share | unmet track count ÷ tracks participating in verdicts ("—" rows excluded) | METRIC-002 |

## Tables Used

- track_sales_month
- track_target_month

## Deliverables

- **track_achievement_detail.csv**: 120 track rows × 6 columns (Track ID / Track Name / Monthly Sales / Monthly Target / Achievement Rate / Verdict), achievement rate ascending, "—" rows last
- **Overall achievement conclusion**: overall achievement rate + unmet count and share (citing concrete numbers)

## Chart Methodology

This objective has no standalone chart; the achievement detail is presented by the report template as a table + achievement bars, with data direct-read from this objective's on-disk file (see report/web_report/monthly-sales-overview/ for the spec).
