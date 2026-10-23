# Web Report Template Package (standalone) Authoring Contract

This volume is the **single authoritative source** for web report template production, covering two output forms: templates embedded in a skill package (`report/web_report/<theme_id>/`, joint production with the skill recommended) and standalone template packages (delivered separately, uploaded via the console). A template package is a self-contained report template: `schema.json` (LLM filling contract) + `template.html` (self-contained renderer) + `manifest.toml` (kind declaration); at runtime there is exactly one LLM JSON filling + string injection, zero build steps; the SQL in `metrics.jsonl` is the exception direct-output channel.

This volume is **self-contained**: all contracts of a template package live in this document, with structural validation carried by the bundled script `scripts/validate_template.py` (an executable contract). A finished template package must pass both structural validation and the semantic consistency review; only passing both counts as done.

## 1. Two Output Forms and Directory Structure

| Form | Location | Data source guarantee | Delivery |
|---|---|---|---|
| **Embedded in skill package (recommended)** | skill package `report/web_report/<theme_id>/` | objective gates lock the existence and format of direct-read source files (R4 linkage, section 7) | delivered with the whole skill package |
| Standalone template package | `<theme_id>/` as its own package | relies on the session happening to produce matching data, or metrics direct output | console upload (section 11) |

Both forms have exactly the same file structure:

```
<theme_id>/                       # directory name is the theme ID; use a semantic English name (e.g. sales_weekly)
  manifest.toml     # required: report_schema = "standalone" (the authoritative criterion)
  schema.json       # required: JSON Schema (draft 2020-12), the LLM filling contract
  template.html     # required: self-contained HTML with the unique __REPORT_DATA__ injection point
  fill_guide.md     # optional but strongly recommended: per-field filling guide; directly determines fill quality
  metrics.jsonl     # optional: fixed-metric SQL, direct engine output (exception channel, see 6.4)
  sample_data.json  # optional (per this spec): sample data for preview/regression; not read at runtime
```

If any of the three required files is missing or fails validation, the platform explicitly errors on load — **there is no silent fallback**.

When the user only wants a report template with no analysis-method distillation need, producing it embedded as a "minimal skill shell" is still recommended (data-supply objective + template package); see the "Reverse production: minimal skill shell" section of references/report-structure.md.

## 2. AI-Guided Clarification (mandatory before starting)

Users usually have only an HTML that "looks good" and don't understand the templating data mechanism. After receiving the finished HTML, **first walk through the page content block by block, then confirm the following questions with the user** — the answers directly determine the data attribution in section 3:

1. **Which numbers / detail tables should be carried over directly from analysis outputs every time a report is produced?** (the default recommended path of joint production: $read direct-read, with the skill package's gates guaranteeing source-file existence and stable format — sections 4 and 5)
2. **Which numbers / tables should be queried live from the database every time?**
   - User-named items only; do not proactively push unnamed items to direct output (see the section 3 decision principles)
   - For named items → follow up: can you provide the query SQL (or describe the retrieval logic)? Do you know the database name?
   - Named for direct output but can't write SQL → fine, fall back to AI analysis filling (see the section 3 decision tree)
3. **Which text should the AI rewrite based on each run's analysis results?** (conclusions, recommendations, anomaly attribution, etc.)
4. **Which content is fixed?** (headers/footers, section titles, legend notes — kept verbatim in the template)
5. **Are the report title and time range fixed or changing per run?** (generation time uses the `{{current_time}}` placeholder, replaced automatically at runtime — never hard-code)
6. **Where chart data comes from — same as questions 1 / 2**: keep the chart structure (type, colors), strip the data.
7. **"List all records satisfying X"-type full-detail tables**: you must follow up on whether the selection condition can be formalized (keywords, regex, field conditions). If formalizable → write the condition into the gate producing that detail, write the full set to disk, and direct-read (or SQL-conditional direct output); having the LLM "find all" from analysis context inevitably drops items — **full-detail tables must never be hand-filled by the LLM**.
8. **Statistical cutoff semantics**: are all metrics uniformly filtered to the same cutoff point? Are stock-type metrics (sales, installed base) intentionally unfiltered in time? The answers directly determine the retrieval and SQL WHERE semantics (see 9.1).

After asking, form a "content block → data source" list and confirm it with the user before starting.

Ways to explore unknown table structure (in priority order): ask the user for DDL or field notes → reverse-engineer column names from historical session artifacts (SQL and cell outputs in notebooks) → if neither is available, that data degrades to LLM filling.

## 3. Core Workflow: "Reverse Rendering" from Finished HTML to Template

All data in the finished HTML is hard-coded. Your task: keep the **visual structure verbatim**, strip **all the data**, and inject it at runtime instead. Attribute each content block with the decision tree below (explain to the user in plain words: "where does this number come from each time a report is produced?"):

```
What is this content block?
├── A number / detail rows / large table, and the skill package has an objective producing this data
│   (the default path of joint production)
│     → $read program direct-read of analysis on-disk outputs (most reliable)
│       report numbers are physically same-source with analysis outputs;
│       large tables never pass through LLM retelling;
│       the source-file format contract is locked by objective gates (sections 4 and 5)
├── Needs translation / classification / judgment / copywriting / picking highlights per
│   this run's analysis (conclusions, recommendations, attribution leads... including cases
│   where the data could be direct-read but the content needs flexible elaboration)
│     → declare a field in schema.json, filled by the LLM from analysis context
│       fill_guide.md must state where the field comes from, how to fill it, what is forbidden
├── Deterministically derivable from the two categories above (sums, sorting, shares, YoY...)
│     → compute at runtime in template.html's JS; never have the LLM fill it
└── Fixed-semantics retrieval the user explicitly names as "query this number/table live from
    the database each time", or a full-detail selection condition already formalized as SQL
      → metrics.jsonl SQL direct output (exception channel, see 6.4; independent of the
        analysis chain — use with care)
```

Decision principles:

- **Numbers prefer direct-read in joint production**: when skill package and template are designed as one, numbers / tables in the report default to $read direct-read of objective on-disk outputs — the same data is both gate-verified and reported, physically same-source, never disagreeing; metrics.jsonl is used only for user-named live queries or formalized full-detail extracts;
- **Only what is explicitly named gets direct output**: metrics direct output is off by default, used only for fixed-semantics retrieval the user names as "query this live each time"; being writable as SQL ≠ should be direct output;
- **Flexible content is never direct-read or direct-output**: content that requires picking highlights, summarizing, narrative organization, or working with this run's analysis context goes to LLM filling even when the underlying data could be direct-read — direct read/output would freeze flexible elaboration into a fixed shape, reducing the report's value;
- **Whatever JS can compute never enters the schema** — the fewer schema fields, the more stable the filling;
- **Full-detail tables must never be LLM-hand-filled**: for "list all records satisfying X" requirements, formalize the condition into SQL / regex (e.g. DTC codes with `REGEXP '[PBCU][0-9][0-9A-Z]{3,5}'`), have gates guarantee the full set on disk for direct-read, or SQL-conditional direct output; this is a hard completeness requirement — guide the user toward formalization even if they didn't ask;
- LLM fields must be "results that actually exist in the analysis context"; fill_guide states that fabrication is forbidden;
- For core counts in LLM copy that share a metric with KPI cards, use the placeholder same-source mechanism (see 6.5);
- **Clarify statistical cutoff semantics first** (question 8 in section 2).

### Concrete Transformation Steps

1. **Deconstruct the structure**: extract the finished HTML's `<style>` and `<body>` skeleton, keeping the visuals verbatim. Handle third-party JS inclusion by size class:
   - **Bounded-size libraries (e.g. ECharts, ~1MB) are strongly recommended inline** — report templates must remain self-contained in offline / intranet environments where CDNs are unreachable; a broken external link degrades entire chart sections to "data unavailable";
   - **Oversized libraries: first confirm they are truly needed**: when genuinely required, present both risks to the user (inlining bloats the template significantly / external links carry CDN-unreachability risk) and let the user decide; if the user picks an external link, use a stable CDN (e.g. `https://cdn.jsdelivr.net/npm/echarts@5.5.0/dist/echarts.min.js`).
2. **Strip the data**: delete all hard-coded numbers/copy/static `setOption({...})` scripts, recording the semantics of every piece of data (this is where schema field names, direct-read sources, and metric names come from).
3. **Wire the injection**: place the bridge script at the end of body (the placeholder literal may appear exactly once in the whole file):
   ```html
   <script>
   window.__RT_DATA__ = __REPORT_DATA__;
   </script>
   ```
4. **Write the consuming JS**: read uniformly from an alias for static-check friendliness:
   ```javascript
   var D = window.__RT_DATA__ || {};
   var M = D.metrics || {};
   ```
   - initialize charts after `window load` (avoids the zero-width container timing race) and register resize;
   - degrade each missing data source to a placeholder card ("data unavailable") — **the page never blanks out or throws**;
   - wrap every render function in try/catch so a single point of failure doesn't take out other modules.
5. **Write schema.json**: declare only LLM-filled fields; `properties` **must not contain a `metrics` key**; prefer marking large-table / detail fields as direct-read (section 4).
6. **Write fill_guide.md**: per field, state the data source (which kind of result in the analysis context), format examples, prohibitions, and the source objective; direct-read fields add the direct-read mark and column mapping (mark format in section 5, linkage in section 7).
7. **Make sample_data.json**: copy the hard-coded values from the finished HTML back as samples (including a hand-mocked `metrics` key; direct-read fields as the post-resolve flattened shape) for preview regression comparison.
8. **Two reviews** (sections 8 and 9): structural validation + semantic review all pass, and the browser confirms visual fidelity — only then is it done.

## 4. $read Program Direct-Read (template-side contract)

For large-table / detail-matrix fields, the LLM can skip transcribing data at fill time and instead emit a **read instruction**, and the program carries the data over from the source file directly into reportData: no transcription errors, no omissions (no truncation within 1000 rows), report numbers physically same-source with the source file.

### 4.1 Instruction Shape

The LLM emits at the marked field position:

```json
{"$read": {"source": "file:/home/notebook/<sid>/achievement_detail.csv", "orient": "records"}}
```

| Key | Required | Description |
|---|---|---|
| `source` | yes | `file:<full path>` or `cell:DATA-<name>`, must appear in the session's analysis-artifact list |
| `sheet` | no | Excel sheet name; required for multi-sheet files (a missing sheet errors out directly, no fallback) |
| `orient` | no | Read shape: `records` / `columns` / `matrix`; defaults to the marked suggestion |

The program reads → reshapes per orient → (optionally) renames columns per column_map → injects into that field's position in reportData. A `$read` emitted for an unmarked field is rejected outright.

### 4.2 schema Marker Syntax

Declare `x-direct-read-allowed` on the schema node needing direct-read (top-level fields, nested objects, and array items all work):

```json
{
  "type": "array",
  "items": {
    "type": "object",
    "properties": {
      "raw_text": {"type": "string"},
      "count": {"type": "number"}
    },
    "required": ["raw_text", "count"]
  },
  "x-direct-read-allowed": {
    "orient": "records",
    "column_map": {"raw fault description": "raw_text", "claim count": "count"}
  }
}
```

- `orient` (optional): the suggested read shape for the field, default `records`;
- `column_map` (optional): a deterministic rename table of **source column name → schema field name**, applied key by key after orient reshaping; without it, source column names must match schema field names verbatim;
- the marker does not prevent the field from still being hand-filled — on direct-read failure (missing source / shape mismatch), convert to hand-filling per the rejection message; never rewrite or fabricate data to force a direct read.

### 4.3 The Three orient Shapes (output shape must align with the schema structure)

| orient | Injected shape | Use |
|---|---|---|
| `records` | array of row objects: `[{column: value, ...}, ...]` | row-wise records (default) |
| `columns` | column arrays: `{column: [values...], ...}` | grouped data consumed per column |
| `matrix` | `[{"name": first-column value, "counts": [remaining numeric columns]}, ...]` | row name + numeric vector (one object per row, fixed key names) |

On shape mismatch the final validation rejects (with a summary of the actually injected shape); fix orient / source per the summary or convert to hand-filling.

### 4.4 Source Constraints

- `file:` sources support only `.csv` / `.xls` / `.xlsx` and must be files registered in the session's **analysis-artifact list** — instructions pointing outside the list error out directly;
- `cell:` sources support only metric cells whose names start with `DATA-`;
- row limit **1000**: beyond that, the first 1000 rows are kept (the truncation is logged). For data that must be shown in full, keep the on-disk file ≤ 1000 rows (pre-aggregate, paginate, or summarize) — do not expect direct-read to haul an entire large table.

### 4.5 The Shape Contract Has Zero Elasticity (important)

`$read` does exactly three things: **read, rename columns, truncate**. It has no pivoting, aggregation, derivation, or type conversion:

- the source file's shape (wide/long table, row granularity, column order) is the contract itself. Changing the source from wide to long breaks column_map immediately, and the field can only fall back to hand-filling;
- type caveats: csv/excel type inference can swallow leading zeros or treat `N/A` as missing — for columns relying on these fragile types (codes, order numbers), write them as text on disk (quote leading zeros or use text format);
- this is why the section 5 "direct-read source-file contract" exists: the skill package must lock the direct-read source file's format; the stability of direct-read = the stability of the source file format.

### 4.6 Large Tables Prefer Direct-Read

Hundred-row tables exceed the LLM's tool-reading limit and get truncated when hand-copied — mark such fields `x-direct-read-allowed` and state the expected source file and column mapping in fill_guide, letting the program carry the full set.

## 5. Direct-Read Source-File Contract (skill-package side)

In joint production, the source files referenced by `$read` come from the skill package objective's on-disk outputs (deliverables). Whether direct-read is stable depends entirely on whether the source file format is stable. **Deliverables consumed by direct-read must satisfy the four locks** (objective-side authoring in references/objective-contract.md; linkage validation R4 in section 7):

1. **Filename lock**: `deliverables.file` declares a fixed filename (e.g. `achievement_detail.csv`); variable components like dates, batches, or sequence numbers are forbidden;
2. **Column-name lock**: `deliverables.columns` declares the complete column list (order included) — column names are the direct-read contract; renaming a column changes the contract and must be synchronized with the template's column_map and fill_guide;
3. **Shape lock**: the gate's `check` states the row granularity (one row = which month and dealer), the table shape (wide/long), and the expected row count (e.g. "rows = product lines × 2 periods; truncation or sampling forbidden");
4. **Format lock**: `csv` (UTF-8, header row) or `xlsx` (sheet names stated in check when multi-sheet); keep rows within 1000.

### fill_guide Direct-Read Marks

Each direct-read field's section in fill_guide appends the direct-read declaration on the same line as the source mark:

```markdown
### classification.mapping (fault classification map)

Source: objective_3/G1 (Direct read: fault_classification_map.csv)

The schema field is marked direct-read-allowed: prefer emitting a $read instruction so the
program carries the data over from the source file.
Column mapping (column_map): raw fault description → raw_text, claim count → count.
```

The validator extracts marks by the `Direct read: {filename}` pattern (Chinese packages may equivalently write `直读：{文件名}`) and checks the file is in that objective's `deliverables` (R4, section 7).

### Iteration discipline: changing the source file format = breaking the direct-read contract

When iterating the skill package, changing a direct-read file's filename / column names / shape / format **must be synchronized on the template side**: column_map (column-name changes) → schema.json (structure changes) → fill_guide marks and column mapping → sample_data.json → re-run the full reviews of sections 8 and 9. Change only one side, and after the other side breaks, the live symptom is that section degrading to hand-filling (quality drop) or missing data — no error, just quietly getting worse.

## 6. File Contracts (self-contained; this is the authority)

### 6.1 manifest.toml

```toml
report_schema = "standalone"   # kind declaration (authoritative criterion; must be exactly "standalone")
schema_version = "1"           # private version of schema.json; semantics frozen
description = "template description"  # recommended; delivery notes such as pending data sources go here too
```

### 6.2 The data Structure Injected at Runtime

```json
{
  // —— filled by the LLM per schema.json below (direct-read fields emit $read
  //    instructions; the program resolves and injects) ——
  "title": "...",
  "summary": "...",
  // —— injected by engineering; schema.json must not declare it ——
  "metrics": {
    "metric_name_1": {
      "sql": "...",
      "columns": ["col1", "col2"],
      "data": [["v1", "v2"]],
      "data_preview": "...",
      "description": "..."
    }
  }
}
```

- pure BI templates (no LLM filling): leave schema `properties` as `{}`; runtime skips the LLM and template.html consumes only `data.metrics`;
- optional markers: `<jarvis-metric name="xxx" layout="table" title="Title">` references metrics data rendered by engineering; `{{current_time}}` is replaced with the current time at runtime.

### 6.3 schema.json / fill_guide.md Essentials

- schema.json uses JSON Schema draft 2020-12;
- every field carries a `description` stating its semantics and format (the LLM reads the schema directly);
- lock array items fully with `required` to avoid half-empty objects;
- for judgment-type fields (e.g. "aligned or not") use a boolean plus one explanatory text field — don't make the LLM output "yes/no" strings;
- mark large-table / detail fields with `x-direct-read-allowed` (section 4); the marked node's items structure is the post-resolve shape contract;
- fill_guide has one section per field containing: data source (which kind of artifact in context), source objective mark (direct-read fields add the direct-read mark, section 5), examples, prohibitions (no fabrication, no omission; reconciliation invariants such as `before + after == count`).

### 6.4 metrics.jsonl (the exception channel)

metrics.jsonl is a **second data channel independent of the analysis chain**: its SQL runs directly against the database at report time, bypassing the skill package's analysis process and gate verification. It is therefore an exception channel, used only for: fixed-semantics retrieval the user names as "query live each time", or full details whose selection condition has been formalized as SQL; the default paths are $read direct-read of analysis outputs (same-source numbers) or LLM filling.

Accept two inherent limitations when using it; the authoring essentials revolve around them:

1. **Semantics-drift risk**: when the template SQL's WHERE semantics diverge from the analysis outputs' semantics, report numbers disagree with analysis conclusions (item-by-item cross-check in 9.1);
2. **Silent empty data from placeholders**: `{param}` placeholders are replaced by exact key match against user input shaped `{param:value}` — a mismatched key name or a different input format leaves the placeholder executing literally: the query succeeds with 0 rows, **no error**. Parameter names must match verbatim the key names you guide the user to type, and required parameters and date formats must be stated in the manifest description / fill_guide.

One JSON object per line:

```json
{"name": "total_sales", "sql": "SELECT COUNT(*) AS total_sales FROM ...", "description": "total sales", "db_id": "12345678"}
```

| Field | Required | Description |
|------|------|------|
| `name` | yes | unique identifier, matching `M.<name>` in JS and `<jarvis-metric name>` |
| `sql` | yes | the query; supports `{param}` placeholders (parsed and replaced from the user's question at execution) |
| `description` | no | description (note the semantics here when stock-type metrics are intentionally unfiltered in time) |
| `db_id` / `db_name` | no | data source; when empty, the session's first database source is used |

- column aliases are the JS consumption contract; once you pick them, don't change them;
- bake session-specific parameters (codes, dates) into the SQL where possible; when a dynamic parameter is truly needed, use a `{param}` placeholder and follow the placeholder discipline above;
- **when you cannot provide db_id/db_name**: write the SQL and name as usual, leave db fields empty (runtime defaults to the session source), and note in the manifest `description` that "metrics depend on the session's bound data source; use this template in a session on the corresponding data source";
- numbers you cannot write SQL for: degrade to a schema field for LLM filling; never force a fabricated SQL;
- every metric in a skill-package-embedded template must additionally satisfy the R1 linkage (section 7).

### 6.5 The Placeholder Same-Source Mechanism for Core Counts in LLM Copy (strongly recommended)

Writing "this number must follow metric X" in fill_guide is only a soft prompt constraint — the LLM may still mis-copy or re-count, making the copy disagree with the KPI cards. For core counts sharing a metric with a KPI card, upgrade to a template-render-time replacement **hard constraint**:

- schema / fill_guide convention: LLM copy writes `{metric_name}` placeholders for core counts and `{pct:metric_name}` for percentages; derived values (YoY/MoM, within-group shares and other numbers requiring LLM computation and judgment) are still filled by the LLM itself;
- template.html provides fillVars(), reading the metric's first row/first column at render time — physically same-source:

```javascript
function fillVars(text) {
  return (text || "").replace(/\{(pct:)?(\w+)\}/g, function(_, isPct, name) {
    var v = scalar(name);              // metric first row, first column
    if (v === null) return "—";        // fallback when the metric is missing
    return isPct ? pct(v) : num(v);
  });
}
// Apply uniformly in every function rendering LLM text — don't miss any:
// setList(id, (items || []).map(fillVars))
```

- scope: single-value metrics (COUNT/SUM/ratios); detail numbers of multi-row metrics are out of scope (they should come from tables/charts via direct output or direct read instead);
- give a complete example sentence with placeholders in fill_guide — the LLM imitates the example's format.

## 7. Linkage with Analysis-Stage Gates (R1/R2/R4)

A template package is not an isolated artifact: every metric, every LLM-filled field, and every direct-read source file must trace to some objective's gate output. Formal definitions and the backfill working method are in references/report-structure.md; only the authoring essentials are listed here.

### 7.1 metrics.jsonl name linkage (aligning R1)

Every metric's `name` in metrics.jsonl must be covered, in either of two ways:

- **the name is directly a `METRIC-NNN`**: the metric already exists in the metric manuals and some objective's `metrics_deps` or `gates[].metrics` references it — suitable for core metrics whose semantics are already registered;
- **free naming + the gate declares `ext.report_metrics`**: declare on the gate responsible for producing that data:

```yaml
gates:
  - id: G1
    type: table
    title: Achievement rate table
    check: |-
      The output contains 12 month rows and 3 fixed columns (Month / Reported Value / Definition).
    ext:
      report_metrics: [monthly_achievement_trend]
```

With neither, validation FAILs. The metric's SQL semantics must match the owning gate's outputs — the gate is the verification source for the semantics.

### 7.2 fill_guide.md source marks (aligning R2)

Every field's section in fill_guide must mark which objective's gate output the field's content comes from, with the unified mark `Source: objective_N/G<id>` (Chinese packages may write `来源：objective_N/G<id>`):

```markdown
### summary (one-sentence summary)

Source: objective_1/G2

Distill a one-sentence summary from the met/missed verdicts; it must cite the concrete
achievement-rate numbers. Fabricating judgments absent from the conclusions is forbidden.
```

A field without a source mark is a WARN; a mark pointing to a nonexistent objective/gate is a FAIL. Direct-read fields append the direct-read mark on the same line (see 7.3).

### 7.3 Direct-read source-file linkage (aligning R4)

Every `Direct read: {filename}` mark in fill_guide (Chinese packages: `直读：{文件名}`):

- must sit on the same line as the source mark, shaped `Source: objective_N/G<id> (Direct read: {filename})`;
- the filename must appear in the referenced objective's `deliverables[].file` — otherwise FAIL;
- that deliverable should declare its `columns` list (a direct-read source without a column contract is a WARN).

When schema contains `x-direct-read-allowed` markers but fill_guide has no direct-read marks anywhere, a WARN is emitted — state the expected source file and column mapping per field so the direct-read path has design-time guarantees.

## 8. Structural Validation and Preview

Validation comes in two tiers; **prefer the strong tier** — downgrade to the weak tier only when the user's environment genuinely cannot run scripts.

### 8.1 Strong validation (preferred): script machine checking

Two stdlib-only scripts are bundled (jsonschema is an optional enhancement); the template directory can be anywhere:

```bash
# Item-by-item package validation; prints [pass]/[warn]/[fail]; non-zero exit on any fail
python scripts/validate_template.py <template-package-directory>

# Injects sample_data.json into template.html, producing preview.html (replicates runtime injection semantics)
python scripts/preview_template.py <template-package-directory>
```

The validator covers: manifest required keys, schema validity and the reserved key, unique injection point, the `__RT_DATA__` bridge, static checking of JS fields against schema, legality of `x-direct-read-allowed` markers, jarvis-metric cross-checks, metrics.jsonl line legality, fill_guide field coverage, and sample_data conformance to schema (when jsonschema is installed). On Python < 3.11, manifest validation automatically degrades to regex parsing (with a warn).

Whole-skill validation (validate_skill.py) automatically invokes validate_template.py for every theme under report/web_report/ and additionally validates the section 7 linkage contracts (R1/R2/R4).

**Definition of done (DoD)**:

1. Strong validation: `validate_template.py` has no `[fail]` (every `[warn]` must be individually confirmed acceptable); weak validation: the checklist passes item by item **and the risk reminder has been output to the user**;
2. The section 9 semantic consistency review checklist passes item by item;
3. preview.html is visually consistent with the target finished page in a browser and charts render (open preview.html yourself or guide the user to open it);
4. sample_data.json contains no dirty data: numbers come from the finished page or are explicitly marked mock;
5. preview.html is a local preview artifact and is not part of the delivered template package.

### 8.2 Weak validation (fallback): AI item-by-item manual checking

When the user's environment has no python3 or cannot run scripts, the AI **reads through the template package files**, checks the following list item by item, and reports conclusions to the user line by line:

- [ ] manifest.toml contains `report_schema = "standalone"` and `schema_version`
- [ ] schema.json is valid JSON and `properties` contains no `"metrics"` key
- [ ] the injection placeholder literal appears **exactly once** in template.html (comments included), shaped like `window.__RT_DATA__ = <placeholder>;` in a JS expression position
- [ ] every `data.field` read by JS is declared in schema properties (or is `metrics`)
- [ ] `x-direct-read-allowed` markers have legal orient values (records/columns/matrix) and column_map is a string mapping
- [ ] every `<jarvis-metric name>` is defined in metrics.jsonl; every metrics.jsonl line is valid with a unique name
- [ ] fill_guide.md covers all schema fields
- [ ] sample_data.json's structure matches schema (by eye)

**When taking the weak path, you must clearly output the following risk reminder to the user (never omit or soften it):**

> ⚠️ No script machine validation was performed this time; the AI checked manually only. Manual checking can miss problems (especially character-level checks like placeholder counting and JS field consistency); after upload the template may still **fail to load, render a blank page, or not draw charts**. Strongly re-run `validate_template.py` on any machine with Python 3 before uploading; if you insist on uploading now, verify the first generated report immediately.

Preview likewise: when `preview_template.py` cannot run, the AI may hand-serialize sample_data.json into a JSON string, replace the placeholder, and produce preview.html — reminding the user that this preview does not replicate runtime escaping semantics (`</` escaping etc.) and is for visual reference only.

### Hard Constraints (machine-checked at platform load; violation makes the template unusable)

| # | Constraint |
|---|------|
| 1 | manifest.toml parses and `report_schema = "standalone"` |
| 2 | schema.json is valid JSON and `properties` contains no reserved key `"metrics"` |
| 3 | the `__REPORT_DATA__` literal appears **exactly once** in template.html (string count, comments included), in a JS expression position |

### Soft Constraints (unchecked at load; rely on the validator and discipline — violation means runtime blanks/errors)

| # | Constraint | Consequence of violation |
|---|------|----------|
| 1 | every `data.field` read by JS is declared in schema properties (or is `metrics`) | undefined at render |
| 2 | `<jarvis-metric name>` references must be defined in metrics.jsonl | placeholder card shown |
| 3 | every metrics.jsonl line is valid JSON with a unique name | invalid lines skipped; duplicates keep the first |
| 4 | fill_guide.md covers all schema fields | fill quality uncontrolled |
| 5 | `$read` appears only in fields marked `x-direct-read-allowed`, with source pointing into the artifact list | the field is rejected; falls back to hand-filling |

### Common Pitfalls

- **Writing `__REPORT_DATA__` inside a comment**: load is rejected by string count. Always refer to it by an alias ("the injection placeholder") in documentation.
- **Charts initialized too early**: the container hasn't laid out yet, width is 0, charts can't draw. Initialize uniformly after `window load`.
- **Metric column names as contract**: JS reads by column alias (e.g. `o["claims"]`); changing the SQL alias silently breaks the data. When changing SQL, sweep the JS too. **Direct-read likewise**: changing source-file column names requires synchronizing column_map and the fill_guide mapping (see the section 5 iteration discipline).
- **Direct-read source shape drift**: if the skill package changes a direct-read source from wide to long (or renames columns, or changes row granularity), column_map breaks and the field silently degrades to hand-filling with no template-side error. Source-format changes must follow the section 5 iteration discipline.
- **An LLM field swallowing a whole HTML block**: schema field values should be text/structured data; lightweight emphasis like `<strong>` is allowed; having the LLM emit whole HTML structures is forbidden.
- **`{{current_time}}`**: replaced at runtime with the current time; never hard-code a generation time in the template.
- **Injection escaping**: the system automatically escapes `</` → `<\/` and U+2028/U+2029; template authors need not handle it — the safety of data containing `</script>` depends on the bridge sitting in a JS expression position.

## 9. Semantic Consistency and Validity Review (mandatory post-processing)

The structural validator can only check "will it load"; it cannot catch semantic problems like "numbers disagree" or "semantics conflict". Once the template package is structurally ready (and after every revision), you must read through all files and cross-review against this checklist item by item. Every item below comes from a real failure.

### 9.1 SQL semantics unification

- All metrics on the same fact table must share identical WHERE filter semantics (especially time cutoffs). Classic failure: a distribution metric carries `date<='{cutoff}'` while the total metric doesn't — the moment the database has data past the cutoff, the KPI card and the distribution chart total disagree. **List every metric's WHERE clause side by side and compare**;
- metrics direct output is a second channel independent of the analysis chain and must also be cross-checked against the skill package's analysis outputs — when the same metric appears both as a metric and in analysis conclusions, both WHERE clauses must match;
- stock-type metrics (sales, installed base) missing a time filter may be intentional; confirm with the user and note the semantics in the metric description;
- check CASE WHEN branch by branch for dead branches (earlier conditions already intercept; later branches never fire).

### 9.2 The same number appearing in multiple places must be physically same-source

- when the same metric appears in KPI cards, tables, and LLM copy, independent retrieval per location is forbidden;
- core counts in LLM copy prefer the 6.5 placeholder mechanism, upgrading the soft prompt constraint to a render-time physical guarantee;
- when a chart and a table show the same dimension they must be **same-source, same-order** (consume the same array, same sort); dual tracks like the chart reading a metric while the table reads an LLM field are forbidden;
- direct-read fields are inherently physically same-source (data injected once by the program); the same number going through direct-read in one place and a metric in another is the dual track — forbidden.

### 9.3 Granularity and copy consistency

- Chart axis granularity = table row granularity = header/title copy. After changing data granularity (e.g. "failure category" → "failure mode"), **grep the whole document for the old granularity term**: section titles, table headers, empty-state copy, and the manifest description all get updated — every leftover is a misleading spot.

### 9.4 Schema orphan-field audit

- Confirm field by field that template.html consumes it. Fields orphaned by structural iteration (the template no longer renders them but schema still requires them): either downgrade to optional with a description noting "not rendered; for LLM reasoning only", or delete and synchronize sample/fill_guide.

### 9.5 sample_data reconciliation

The sample is what the LLM imitates; errors in the sample get amplified through every generation:

- **internal reconciliation**: parts sum to totals; metrics cross-check each other (distribution segments sum to the corresponding aggregate metric);
- **copy matches data**: numbers cited in sample copy must match the sample metrics' values — copy saying 18651 while the metric says 9860 is forbidden;
- **cover the schema's full contract**: if a schema description requires "must cover all five categories", the sample must show all five — if the sample misses one, the LLM will miss one;
- **direct-read fields sampled in post-resolve shape**: give the flattened row-object array (matching the orient output) in sample_data, and provide the same sample in the fill guide, so the LLM doesn't think it must hand-copy the whole table.

### 9.6 Render-function defensive consistency

- Fallback logic of same-kind render functions must be consistent: all have empty-data fallbacks; all truncate overlong text (e.g. uniformly 100 chars + "..."). If one table truncates and another doesn't, long text breaks the layout.

### 9.7 Direct-read contract cross-check (when direct-read markers exist)

- three-way check per direct-read field: the source file declared in fill_guide ↔ the orient / column_map marked in schema ↔ the objective gate's deliverables declaration — the source column-name set against the column_map key set (and identically-named direct fields) verbatim; row granularity and shape (wide/long) consistent with the orient output structure;
- row-count reconciliation: the gate's expected rows vs the template's display need (e.g. the report needs 12 months, so the source file must have exactly 12 rows), and ≤ 1000 rows;
- fragile types: leading zeros, `N/A`, thousands-separator numbers — confirm the on-disk source format won't trigger type misjudgment at read time.

### 9.8 A template-specific assertion script (recommended)

- the generic validator covers structure; template-specific semantic assertions (filter-semantics assertions, reconciliation equations, dead-branch assertions, zero-residue assertions for old terms, direct-read column-contract assertions) go into an ad-hoc python script outside the template directory, reused at every revision to prevent regressions.

## 10. Iterative Revision Workflow (when the template already exists)

For revision requests on a delivered template, follow these steps — **never change only one spot**: a single request often spans template/schema/fill_guide/metrics/sample.

1. **Read the current state**: read all files completely and understand the blast radius before touching anything;
2. **Implement the change**: route new data through the section 3 decision tree first; on granularity changes or field deletions, sweep for follow-on changes per 9.3 / 9.4; on new metrics or schema fields, add the section 7 linkage declarations and source marks; on direct-read source-format changes, synchronize the template side per the section 5 iteration discipline;
3. **The version-sync trio** (the easiest to miss — check every time):
   - manifest.toml: bump `schema_version` when schema changes; append a `# vN (schema_version X): change summary` comment at the top; keep description current (modules that were removed must not linger in the description);
   - fill_guide.md: the version number on the first line matches manifest;
   - sample_data.json: add samples for new metrics, reconciled with existing samples (9.5);
4. **Re-run the full review**: structural validation + preview + the entire section 9 review redone — not just the changed spots; last time's consistency may have been broken by this change;
5. **Surface data-side problems**: problems the template layer cannot fix (e.g. a missing dimension-table mapping leaving codes displayed) go into the manifest notes and are told to the user explicitly — never swallow them silently.

## 11. Delivery

**Standalone template packages**: after validation and preview confirmation, guide the user to upload:

1. Open the **DataAgent console** and click any historical session
2. Click **"Apps"** in the right panel
3. Click **"Custom Web Page"**, then the **"Upload Theme"** icon
4. Follow the page's guidance to upload the finished template package

Notes:

- templates only work after being fixed on the platform side; **in-session direct upload is not supported**
- **for packages that received only weak validation (no script run), the section 8.2 risk reminder must be repeated to the user before upload**; proceed only after the user confirms
- `preview.html` is a local preview artifact and is **not recommended** as part of the upload; even if uploaded, the platform ignores it at load and it will not cause load failures

**Skill-package-embedded templates** ship with the whole skill package (loaded via /load-skill); their gate linkage contracts (R1/R2/R4) are validated together with the skill package; pre-delivery audit requirements for the report parts are in references/quality-checklist.md.
