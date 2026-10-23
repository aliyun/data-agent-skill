# Table Manual and Metric Manual Specification

This volume specifies the authoring format of the tables/*.yaml table manuals and the metrics/METRIC-NNN.yaml metric manuals. Together they are the foundation of the skill package's three-layer knowledge model: table manuals capture field truth and the semantic model; metric manuals capture calculation semantics.

## The Three-Level Model: field → measure → metric

- **field**: a physical column in a table (e.g. `actual_month`);
- **measure**: an atomic aggregation over a field (e.g. `SUM(actual_month)` = the measure "actual sales"), defined in the table manual;
- **metric**: a business-meaningful KPI composed of measures (e.g. actual sales ÷ target = the ratio metric "achievement rate"), defined in the metric manual.

**A metric's numerator and denominator must reference measures from the table manuals; referencing bare fields directly is forbidden.**

## Table Manual tables/*.yaml

One YAML file per table, named `tables/{table-name}.yaml`. A table manual captures only "facts about the table itself" — writing analysis insights (ratios, achievement rates, rankings, anomaly conclusions) is forbidden.

Each file contains two parts:

1. **Field truth**: facts profiled from the data — types, time formats, null representation, data distribution, enumeration distribution;
2. **Semantic model**: dimensions (groupable/filterable columns) + measures (aggregatable columns, with additivity).

### Complete Field Skeleton

```yaml
table_name: ads_auto_sales_month
business_description: Auto dealer sales & inventory monthly report (fictional example domain), actual sales/inventory/targets by month × dealer
granularity: one row = one dealer (dealer_code) for one month (biz_month)
columns:
  - column_name: biz_month
    type: string
    meaning: business month
    time_format: "yyyyMM, e.g. 202606 (stored as a string, not a date type)"
    null_representation: no NULLs
    data_distribution: "202401 ~ 202606, 18 months in total"
    authority: truth
  - column_name: region_name
    type: string
    meaning: region name
    null_representation: no NULLs
    enum_distribution:
      - {value: North Region, ratio: 22%}
      - {value: South Region, ratio: 31%}
    authority: truth
  - column_name: actual_month
    type: decimal(18,2)
    meaning: actual sales volume this month
    null_representation: "NULL means not reported (≠ 0; 0 means confirmed zero sales)"
    data_distribution: "min=0, p50=42, p95=310, max=1580; NULL 3%"
    authority: truth
dimensions:
  - {name: Month, column: biz_month, type: time, granularity: month, nullable: false, authority: truth}
  - {name: Region, column: region_name, type: categorical, nullable: false, authority: truth}
measures:
  - {name: Actual Sales, aggregation: SUM, column: actual_month, additivity: fully additive, authority: truth}
  - {name: Target, aggregation: SUM, column: actual_target, additivity: fully additive, authority: truth}
  - {name: Inventory, aggregation: SUM, column: terminal_stock_month, additivity: semi-additive, non_additive_across: [Month], authority: truth}
```

### Per-Field Authoring Requirements

**Three top-level fields**:

- `table_name`: physical table name, must match the filename;
- `business_description`: one-sentence business description;
- `granularity`: what one row represents, in the "one row = ..." pattern.

**columns (field truth)** — must cover every column of the table structure, no more, no less; omitting columns is forbidden, as is inventing columns that don't exist:

- `column_name` / `type` / `meaning`: column name, type, business meaning;
- `time_format`: required for time-typed fields, e.g. `yyyyMM` / `yyyy-MM-dd` — prevents users guessing the format and writing wrong filters;
- `null_representation`: state clearly whether nulls are `NULL`, `""`, or `0`/`-`, or mixed; **must distinguish NULL ≠ 0 ≠ ""** — the three are treated differently in aggregation, filtering, and grouping;
- `data_distribution`: min/percentiles/max/null ratio;
- `enum_distribution`: values + ratios for categorical fields;
- `authority`: truth / reference (see authority grading below).

**dimensions / measures (semantic model)**:

- dimensions: groupable/filterable columns, each with name/column/type (time/categorical/etc.)/granularity (required for time dimensions)/nullable/authority;
- measures: aggregatable columns, atomic units aggregated from fields; `aggregation` states the aggregation (SUM/COUNT/AVG etc.);
- a measure's `additivity` falls into three classes:
  - **fully additive**: summable across all dimensions (e.g. sales volume);
  - **semi-additive**: summable across some dimensions but not time (e.g. month-end inventory snapshots); `non_additive_across` must list the dimensions it cannot cross;
  - **non-additive**: cannot be summed or averaged — ratios are inherently non-additive, not declared in the table manual, and handled uniformly at the metric layer;
- additivity classification must be settled at the measure layer of the table manual and referenced directly by metric manuals; deferring the classification to the metric layer or to point of use is forbidden.

### Table Manual Hard Rules

- **Enumeration iron law**: `enum_distribution` may list only two kinds of values — (1) values explicitly defined by the business, (2) values actually observed in the data. Values that are neither are absolutely forbidden (even marked "reference"); filling enumerations from industry common sense is forbidden.
- When the business defines the value set (e.g. "only Domestic and Joint-venture"), the enumeration must match the definition exactly — no additions or removals.
- **Authority grading**: truth (business input / table structure / confirmed by actual query) > reference (historical / inferred). On conflict between sources, truth wins; distributions inferred from small samples must be marked "reference" — presenting reference-grade inference as truth is forbidden.
- Contradicting the business's field/enumeration definitions is forbidden; on conflict, the business definition wins.

## Metric Manual metrics/METRIC-NNN.yaml

One YAML file per metric, distilling the calculation into a definition that humans can understand and that converts directly to SQL.

Three metric types: **direct** (a single measure, e.g. actual sales) / **ratio** (two measures divided) / **derived** (a formula over other metrics).

### Numbering Rules

- Metric IDs have the fixed format `METRIC-NNN` with three digits, numbered contiguously from 001;
- The filename must be `metrics/METRIC-NNN.yaml`;
- The `metric_id` in the body must match the filename; references from objectives, approach, and other files must match the filename verbatim.

### Complete YAML Skeleton (ratio metric example)

```yaml
metric_id: METRIC-016
metric_name: Sales Achievement Rate
type: ratio
format: percentage
definition: actual monthly sales ÷ monthly target, measuring target completion; = 1.0 means exactly met
when_to_use: |
  - Sales/inventory/target-achievement diagnosis: whether a region/dealer met the monthly target
  - Accountability drill-down: locate unmet entities layer by layer, region → area manager → dealer
  - Not applicable: for cumulative multi-month achievement use a cumulative-semantics metric; this metric is a rate and cannot be averaged across months/groups
numerator: {table: ads_auto_sales_month, measure: Actual Sales}
denominator: {table: ads_auto_sales_month, measure: Target}
thresholds:
  - {verdict: Met, condition: ">= 1.0"}
  - {verdict: Missed, condition: "< 1.0"}
exceptions:
  - denominator is 0 or NULL → record the result as "—", excluded from ranking
  - area manager is empty → list a separate "Unassigned" row; do not merge into others
  - month not fully elapsed → truncate both numerator and denominator to the same calendar days before computing
notes: ratios are non-additive — when switching granularity, aggregate numerator and denominator first, then divide; never SUM/AVG the achievement rate itself
authority: truth
sql_template: |
  SELECT SUM(actual_month) / NULLIF(SUM(actual_target), 0) AS achievement_rate
  FROM ads_auto_sales_month
  WHERE biz_month = '{month}' AND region_name LIKE '%{region}%'
parameters:
  - {name: month, example: "202606"}
  - {name: region, example: "North", description: "fuzzy match; the template already carries %, do not add % in the value"}
```

### Field-by-Field Description

| Field | Required | Description |
|---|---|---|
| metric_id | required | `METRIC-NNN`, must match the filename |
| metric_name | required | Business name of the metric (e.g. "Sales Achievement Rate") |
| type | required | One of: direct / ratio / derived |
| format | required | Display format, e.g. percentage / amount / quantity |
| definition | required | One-sentence definition including the verdict baseline (e.g. "= 1.0 means exactly met") |
| when_to_use | required | Applicable and non-applicable scenarios, listed item by item |
| numerator | required | `{table, measure}` referencing a table-manual measure |
| denominator | required | `{table, measure}` for ratios; the literal `"none"` for direct metrics |
| thresholds | optional | Verdict thresholds, a list of `{verdict, condition}` |
| exceptions | required | A systematic list of exception handling (see key rule 7) |
| notes | required | Usage notes; ratio metrics must state non-additivity |
| authority | required | truth / reference |
| sql_template | required | Parameterized SQL template with `{param}` placeholders |
| parameters | as needed | Template parameter list, `{name, example, description}` |
| derived_formula | required for derived | The derived metric's formula |
| cross_table | required for cross-table | `{no_join: true, align_dimensions: [alignment dimensions]}` |

### Metric Manual Key Rules (all mandatory)

1. **Numerator and denominator reference measures only**: numerator/denominator use the `{table, measure}` form referencing table-manual measures; bare field names are forbidden; the referenced measure must exist in the table manual's measures — if it can't be found, it must be marked "reference/unconfirmed".
2. **Direct metrics**: have only a numerator; denominator is the literal `"none"`.
3. **Derived metrics**: type is "derived"; numerator/denominator may reference other metrics' `[METRIC-NNN]`, in which case a `derived_formula:` line stating the formula is required; key semantics of the formula (e.g. "same-period-last-year cumulative = Jan last year through the same month last year", "Q1 = Jan+Feb+Mar") must go into definition / derived_formula.
4. **Cross-table metrics**: when numerator and denominator measures come from different tables, add `cross_table: {no_join: true, align_dimensions: [alignment dimensions]}`.
5. **SQL division-by-zero guard**: every division in sql_template uses `NULLIF(denominator, 0)` — no exceptions.
6. **LIKE parameters**: fuzzy-match parameters carry `%` in the template (e.g. `LIKE '%{region}%'`); adding `%` in the passed value is forbidden, and this must be noted in the parameter's description.
7. **Systematic exceptions**: more than just "denominator is 0" — cover the exceptions this metric will actually encounter: zero/NULL denominators, null dimension values (e.g. an empty owner gets a separate "Unassigned" row), incomplete periods (truncate numerator and denominator to the same days), semantics switches, and so on.
8. **Ratios are non-additive**: every ratio/rate metric must state in notes "when switching granularity, aggregate numerator and denominator first, then divide; SUM/AVG over the ratio itself is forbidden".
9. **definition/when_to_use/notes must live in the YAML body** — leaving them only in surrounding prose is forbidden.
10. **Never conflate similar semantics**: similar fields must be split into different metrics (e.g. "current-month actual sales" vs "trailing 3-month average sales"); mixing them is forbidden.

### Completeness and Evidence Constraints (reconcile item by item)

- Every metric definition must rest on facts — one of: an explicitly defined business formula, actually verified SQL, or an algorithm confirmed by actual execution; **fabricating metric definitions from table structure alone is forbidden**.
- **Every** calculation formula given by the business must land as its own metric file — including simple metrics, ratios, and derived formulas such as YoY, MoM, cumulative YoY, quarterly sales, quarterly YoY; omitting or merging them away is forbidden.
- Conditional metrics (e.g. quarterly figures shown only in Mar/Jun/Sep/Dec) are not omitted for being conditional; display conditions go into when_to_use / notes.
- Business-calibrated SQL must be preserved character-for-character in sql_template (including `{param}` placeholders); rewriting, reordering, optimizing, or simplifying is forbidden; on conflict with other sources, the business-calibrated version wins.
- Thresholds must match the business's verdict criteria exactly; adjusting them on your own is forbidden.
- **Self-check twice before finalizing**: first go through the business's formula list item by item and confirm each has a metric file; then go through the metric files one by one and confirm every definition can point to a factual basis.
