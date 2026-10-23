# Objective File Contract Specification

This volume specifies the complete format of objective files under objectives/: the YAML frontmatter contract at the top of the file (gates, metric dependencies, deliverables declarations) and the body section structure.

The objectives/ directory holds the execution manual for each analysis objective: one Markdown file per objective, answering "how to run this objective next time, and what its outputs must look like to qualify". Objective files distill process, not re-analysis of data. File naming rules are in references/file-structure.md.

## Overall File Structure

Each objective file consists of two parts in fixed order:

1. **YAML frontmatter**: valid YAML wrapped in paired `---` lines at the very top of the file, declaring machine contracts — gates, metrics_deps, deliverables, and depends_on;
2. **Body**: a plain-prose Markdown execution manual; section structure below.

The frontmatter is the only place gates and metric dependencies may be declared: **the body must not contain "## Gates" or "## Metrics" sections**.

## Frontmatter Contract (schema v1)

### Complete Example

```yaml
---
contract_version: 1
depends_on: [1]
gates:
  - id: G1
    type: table
    title: Achievement rate table
    severity: blocking
    check: |-
      The output is written to disk as achievement_detail.csv with 12 month rows and
      3 fixed columns (Month / Reported Value / Definition), column names matching
      the deliverables declaration. Nulls display as "N/A"; displaying 0 is forbidden.
      The values follow the [METRIC-003] semantics.
  - id: G2
    type: conclusion
    title: Met/missed verdict
    check: |-
      Give a binary Met / Missed verdict for each region, baseline achievement rate >= 1.0
      (semantics per [METRIC-003], [METRIC-016]).
      The conclusion must cite concrete numbers; qualitative-only wording is forbidden.
  - id: G3
    type: constraint
    severity: warning
    title: Never average ratios
    check: |-
      When switching granularity, aggregate the numerator and denominator first, then divide;
      SUM/AVG over the achievement rate itself is forbidden.
      Applies only when this objective involves cross-granularity aggregation.
metrics_deps:
  - id: METRIC-003
    usage: |-
      Judging monthly achievement health; used for the achievement column and status verdicts
  - id: METRIC-016
    usage: |-
      Achievement rate computation at region granularity
deliverables:
  - file: achievement_detail.csv
    columns: [Month, Reported Value, Definition]
---
```

### Top-Level Fields

| Field | Required | Description |
|---|---|---|
| contract_version | required | Always `1` |
| gates | required | Gate list, at least one entry |
| metrics_deps | required | Metric dependency list; use an empty list `[]` when the objective depends on no metric |
| deliverables | optional | Filenames and column names of on-disk outputs |
| depends_on | optional | List of prerequisite objective order numbers (e.g. `depends_on: [1]`), consumed structurally by the platform; write `[]` or omit the key entirely when there are no dependencies (existing packages without this field are unaffected); authoring guidance below |

### depends_on Authoring Guidance

`depends_on` declares this objective's prerequisites (objectives that must complete first in execution order), with three rules:

1. **Copy declared dependencies verbatim**: when the source plan (the sample task's plan) already declares dependencies, copy that list — no additions, no removals, no renumbering;
2. **Judge for yourself when undeclared**: when the source has no dependency declaration, infer reasonable prerequisites from the analysis logic, based only on two kinds of evidence: data-artifact flow (this objective reads files written by a prerequisite objective) and concept-definition ordering (e.g. a base-month definition or shared semantics established by a prerequisite);
3. **No fabrication**: with no real prerequisite, leave it empty; fabricating chained dependencies to fill the count (e.g. mechanically making every objective depend on the previous one) is forbidden.

Constraints: reference only order numbers of objectives that exist in the package; self-reference is forbidden; the whole package must not contain cycles (the validator warns).

### Gate Entry Fields (the standard five)

The standard form uses only the five core fields: id / type / title / check / severity — **avoid writing anything else**.

#### The five core fields (first four recommended-required, severity optional)

| Field | Level | Description |
|---|---|---|
| id | recommended | `G` + digits (fully matching `^G\d+$`), recommended unique within the file; the rendered G number is assigned by the platform in order — id mainly serves report-linkage tracing and human reading |
| type | recommended | Use `table` / `conclusion` / `constraint`; `chart` is a reserved value that hand-written files must not use (see "type rules"); entries with a missing or invalid type fall into the "other gates" partition |
| title | recommended | Gate title, one sentence summarizing what is checked |
| check | recommended | Free-text check description; **the `\|-` block scalar style is strongly recommended** (see "block scalar style") |
| severity | optional | `blocking` (unmet means unqualified) or `warning` (advisory); defaults to `blocking` |

#### Do not write keys beyond the five

- All requirements about format, presentation, ordering, and applicability **go into the check block scalar text**; inventing custom keys (verify / metrics / when / ext or anything else) to carry them is forbidden;
- To be clear: extra keys are **not rejected** by the platform — the raw YAML block is passed through whole to the LLM (see "platform processing commitment") — but engineering does not parse them and there is no structured consumption: they add noise without benefit;
- validate_skill.py emits a WARN for keys beyond the five (not a FAIL);
- Metric dependencies are declared once at top level in `metrics_deps`, not in `gates[].metrics` (if written anyway, valid `METRIC-NNN` items still join the metric cross-check — but that is legacy compatibility, not the standard form);
- The single structured-consumption exception is `ext.report_metrics` for report linkage (feeds the R1 check, see references/report-structure.md); do not write ext without a report-linkage need.

### Platform Processing Commitment (lenient pass-through)

- Once the gate block is recognized (frontmatter is valid YAML containing `contract_version`, see "frontmatter hard constraints" below), each gate's **raw YAML is passed through whole to the LLM**; extra fields beyond the five remain fully visible to the LLM and are not dropped;
- An individually malformed entry (missing id/title/check, invalid type, ...) **does not lose the gate**; other entries are unaffected;
- Entries with missing or invalid type merely fall into the "other gates" partition — still passed through whole — but are excluded from type-filtered mechanisms such as chart assembly and conclusion/deliverable extraction.

#### type Rules

- `table` (table artifact delivery): hard checks on file/table row counts, column names, ordering, etc.;
- `conclusion` (conclusion delivery): must-answer questions, verdict baselines, conclusion format;
- `constraint` (process constraint): execution-style constraints; keep them few and sharp;
- `chart` (chart artifact delivery): **reserved for the auto-generation flow; hand-written objectives must not fill it** — chart specs and drawing rationale go in the body's "## Chart Methodology" section.

#### check Writing Requirements

- Wording must be precise enough for machine checking; vague terms like "output done", "generated", "reasonable", "appropriate" are forbidden;
- Header column names and filenames inside must match the deliverables declaration and the business constraints;
- When the business specifies a template for text outputs, write "per the corresponding section of report_template.md".

### metrics_deps Entry Fields

| Field | Required | Description |
|---|---|---|
| id | required | Metric ID fully matching `^METRIC-\d{3}$` (three digits), identical to a filename under metrics/ — no omission, no fabrication |
| usage | required | One sentence on what this objective uses the metric for; **`\|-` block scalar style recommended** |

Reference by ID only; redefining metric semantics in usage is forbidden — the metric manuals are the single authoritative source.

### deliverables Entry Fields

| Field | Required | Description |
|---|---|---|
| file | required | Fixed output filename |
| columns | optional | Fixed column-name list, consistent with the body's "## Deliverables" section |

**Deliverables consumed by report-template direct-read (the direct-read contract)**: when the file will be referenced by a template `$read` instruction, columns is upgraded from optional to required, and check must lock the **four-lock** format contract —

1. Fixed filename (no date, batch, or sequence components);
2. Fixed column names and order (column names are the direct-read contract — renaming a column changes the contract);
3. Explicit row granularity and table shape (one row = what; wide/long table; expected row count);
4. Explicit file format (csv: UTF-8 with a header row; xlsx: sheet names when multi-sheet), row count ≤ 1000.

The stability of direct-read equals exactly the stability of the source file format: any later iteration that changes any lock must be synchronized on the template side (column_map / schema / fill_guide); linkage marks and validation rules are in R4 of references/report-structure.md and section 5 of references/web-report-template.md.

### Block Scalar Style (strongly recommended)

- Free-text fields like `check` and `usage` should **always use the `|-` block scalar style**: `|-` after the field name, content on following indented lines;
- Avoid writing long single-line flow scalars containing colons, quotes, `#`, `%`, and similar specials — they easily produce invalid YAML, and "the whole thing must be valid YAML" is a hard red line;
- Inside a block scalar any punctuation and multi-line text is safe, no escaping needed;
- Short single-line enumerations (e.g. `depends_on: [1]`, `columns: [Month, Reported Value]`) may use flow style.

### Frontmatter Hard Constraints (only two red lines)

1. **The whole thing must be valid YAML**: starts with `---` on the first line, ends with a paired `---` line; wrapping it in Markdown code fences (```) is forbidden — breaking it anywhere makes the entire frontmatter unparseable;
2. **It must contain `contract_version`** (always `1`) — the precondition for the platform to recognize the gate contract block.

Touching a red line makes the gate block unrecognizable and the whole file falls back to legacy body parsing; every other former red line is downgraded to advisory:

- gate ids are recommended unique within a file (duplicates don't lose gates; validate_skill.py warns);
- every `METRIC-NNN` reference (gates[].metrics and metrics_deps[].id) should have a matching `.yaml` file under metrics/ — this doesn't affect gate recognition, but metric-manual closure is an engineering requirement, and validate_skill.py still FAILs on missing files;
- missing or empty gates likewise don't affect block recognition, but an empty contract is meaningless; validate_skill.py still FAILs.

## Body Section Structure (fixed order, all sections present)

The body after the frontmatter follows this order:

1. `# Objective: {objective name}`: matching the name part of the filename.
2. `## Analysis Approach`: how this objective decomposes and the analysis logic; time ranges / filters / sort rules must match the business constraints; time-window statements must be self-consistent — a "recent N months" count contradicting the actual range is forbidden; when unsure, describe the range directly (e.g. "Jan–Dec last year + Jan this year through the base month").
3. `## Value List`: the data-retrieval definition table for every displayed value in the outputs (format below).
4. `## Tables Used`: one `- {table-name}` per line; reference by table name only, never re-describe table structure.
5. `## Deliverables`: the list of outputs this objective delivers (format below).
6. `## Chart Methodology`: specs and drawing rationale for chart outputs (format below); when there is no chart, keep the section heading and note it as not applicable.

The metric manuals are the single authoritative source: the body references metrics only by `[METRIC-NNN]` ID; redefining metric semantics is forbidden.

### Value List Format

A Markdown table giving the retrieval definition per value, with three fixed header columns:

| Reported Value | Definition (filter → aggregate → formula) | Metrics |
|---|---|---|
| {value/column name} | {definition} | {METRIC-ID or metric name; "—" if none} |

It must cover every displayed value appearing in this objective's outputs, so that every number in the report can trace to a definition here. For ratio metrics, the denominator semantics must match the metric manual's definition.

### Deliverables Format

List the outputs one by one, skeleton:

- **{output filename or table name}**: {row count/granularity × column list, ordering and format conventions}
- **{chart/conclusion outputs}**: {theme, count, format essentials}

On-disk files must state fixed filenames and fixed column names, consistent with the frontmatter deliverables declaration; purely report-type objectives may describe tables/charts for report sections instead.

### Chart Methodology Section

One `### Chart gate: {chart title}` block per chart, with two parts:

- **Chart spec**: chart type, chart title, X axis (field/labels/scale rules), Y axis (field/labels/unit), data series (name/source/color/type), auxiliary elements (gridlines/legend/data labels/threshold lines/layout);
- **Drawing rationale**: what this chart is meant to show, and the key design decisions.
