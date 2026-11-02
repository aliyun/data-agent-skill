# Report Deliverables Structure and Linkage Contracts

This volume specifies the structure of the skill package's report/ directory, how to write text_report.md, the linkage contracts between the report and the analysis objectives (R1/R2/R3/R4), reverse production as a "minimal skill shell" for pure template requests, and the "report first, backfill gates" working method.

Under the recommended **joint production** of skill package and report template, report/ is a recommended deliverable: when absent, the skill package covers only the analysis stage; when present, the skill covers the full "analysis + reporting" flow and the linkage contracts take effect (the validator enables the corresponding checks automatically).

## report/ Directory Structure

```
report/
  text_report.md                     # text report requirements (if a text report is needed)
  web_report/
    <theme_id>/                      # standalone web report template packages; multiple themes allowed
      manifest.toml                  # required: kind declaration
      schema.json                    # required: LLM filling contract
      template.html                  # required: self-contained renderer
      fill_guide.md                  # strongly recommended: per-field filling guide + source marks + direct-read marks
      metrics.jsonl                  # optional: fixed-metric SQL, direct engine output (exception channel)
      sample_data.json               # optional: sample data for preview/regression
```

- text_report.md and web_report/ — **at least one must exist** (a report/ directory with neither FAILs);
- each theme directory under web_report/ must contain the three files manifest.toml / schema.json / template.html (missing any FAILs);
- the authoring contract of the six template-package files is detailed in references/web-report-template.md; this volume covers only directory rules and linkage contracts.

## text_report.md Authoring Spec

text_report.md is a **distilled, machine-checkable set of text report requirements**, written in plain Markdown as output-format constraints:

1. **Chapter structure**: how many chapters, each chapter's title and order, and the logical relations between chapters;
2. **Per-chapter required material list**: the charts / tables / conclusions that must appear in each chapter, **each item carrying a source mark** (see "source marks" below);
3. **Tone and style**: wording register (e.g. executive briefing vs. technical analysis), number precision, unit and punctuation conventions;
4. **Format template**: paragraph skeleton, table headers, mixed text-and-figure rules; when the business provides a template, write "per the corresponding section of report_template.md" — copying the template content in and rewriting it is forbidden.

At runtime, text_report.md's content is injected as the custom agent's **text report constraints** — the chapter structure and material requirements declared here are followed item by item when generating the text report. The wording must therefore be directly executable output constraints, not background prose for human readers.

### Source Marks (unified format)

Each item in a material list carries a source mark:

```
Source: objective_N/G<id>
```

- `N` is the order number from the objective filename, `G<id>` the gate id from that objective's frontmatter;
- one material item may carry multiple sources (e.g. "Source: objective_2/G1, objective_3/G2"); every mark is validated;
- the validator extracts marks with the regex `objective_(\d+)\s*/\s*(G\d+)` and checks that the referenced objective file and gate id actually exist.

Example fragment:

```markdown
## Chapter 2 Sales Achievement Analysis

Required materials in this chapter:
- Achievement rate trend line chart (last 12 months, with threshold line) — Source: objective_1/G3
- Region achievement detail table (Region / Achievement Rate / Verdict) — Source: objective_1/G1
- Met/missed verdicts (binary verdict per region, citing concrete numbers) — Source: objective_1/G2
```

Chinese skill packages may equivalently write `来源：objective_N/G<id>`; the validator accepts both notations.

## Linkage Contracts (R1/R2/R3/R4, machine-checkable)

The linkage contracts guarantee: **every chart / table / metric / material the report needs must be covered by the output of some objective's gates.** Linkage information is declared in place inside the objective frontmatter, without changing contract_version. `ext.report_metrics` is the **only structured-consumption exception key** beyond a gate's standard five fields (id / type / title / check / severity) (consumed by validate_skill.py's R1 check; platform engineering does not parse it, it is merely passed through with the gate block): write it only for the report-linkage scenarios below, and do not use ext to carry anything else (validate_skill.py emits one pass-through reminder WARN for ext, which is expected and does not affect validation).

**Data-source doctrine**: in joint production, template numbers prefer $read direct-read of objective on-disk outputs (guaranteed by R4); metrics.jsonl SQL direct output is only an exception channel for formalizing user-named real-time queries / full-detail extracts (guaranteed by R1) — it is independent of the analysis chain and carries two inherent limitations, semantics drift and silent empty data from placeholders (see references/web-report-template.md 6.4). When metrics.jsonl appears in a template package, the validator emits one exception-channel reminder WARN — expected.

### The ext.report_metrics Key Convention

When a gate's output is consumed by a metric in the web report's metrics.jsonl and that metric's name is not in `METRIC-NNN` form, declare it in the gate's ext:

```yaml
gates:
  - id: G1
    type: table
    title: Achievement rate table
    check: |-
      The output contains 12 month rows and 3 fixed columns (Month / Reported Value / Definition).
    metrics: [METRIC-003]
    ext:
      report_metrics: [monthly_achievement_trend, region_achievement_detail]
```

Meaning: the two metrics named `monthly_achievement_trend` and `region_achievement_detail` in metrics.jsonl have their SQL semantics verified and covered by this gate's outputs.

Note: `ext.report_metrics` declares **the report data sources consumed by this gate's outputs**; the corresponding metric's SQL only needs to produce the base columns; derived columns in the gate's outputs (MoM, shares, per-customer averages and similar derived values) are computed by the executing agent on top of the SQL results per the body's value list. The two are therefore not a 1:1 column mapping — do not expect the SQL to emit every column the gate requires.

### R1 Web metric linkage

Every metric `name` in `report/web_report/<theme>/metrics.jsonl` must be covered, with matching priority:

- (a) the name is itself in `METRIC-NNN` form and appears in some objective's `metrics_deps` or `gates[].metrics`;
- (b) some objective's gate declares `ext.report_metrics: [<name>, ...]` containing the name.

Neither → **FAIL**. metrics.jsonl is an exception channel (see the data-source doctrine above); when used, every metric must satisfy this linkage.

### R2 Web field linkage

Every non-reserved top-level field of `schema.json` (excluding `metrics`) must carry a source objective mark in `fill_guide.md` (unified mark `Source: objective_N/G<id>`; Chinese packages may write `来源：objective_N/G<id>`). The validator extracts the marks and checks the referenced objective file and gate id exist:

- a field without a source mark in fill_guide.md → **WARN**;
- a mark pointing to a nonexistent objective/gate → **FAIL**.

### R3 Text report linkage

The material list in `report/text_report.md` (charts / tables / conclusions per chapter) must carry a source mark `objective_N/G<id>` per item. The validator extracts and checks existence:

- a broken reference (nonexistent objective/gate) → **FAIL**;
- no marks anywhere in the file → **WARN**.

### R4 Direct-read source-file linkage

The direct-read marks in `fill_guide.md` (declarations of source files referenced by template `$read`) must close to an objective's on-disk outputs. The mark sits on the same line as the source mark:

```
Source: objective_3/G1 (Direct read: fault_classification_map.csv)
```

Validation rules:

- the filename in the mark is not in the referenced objective's `deliverables[].file` → **FAIL**;
- a direct-read mark on a line without a source mark (unattributable) → **FAIL**;
- that deliverable declares no `columns` list → **WARN** (the direct-read source file has no column-name contract; the format lock is incomplete);
- schema contains `x-direct-read-allowed` markers while fill_guide has no direct-read marks anywhere → **WARN**.

Chinese packages may write the equivalent Chinese mark `来源：objective_3/G1（直读：故障归类映射.csv）`; the validator accepts both.

Deliverables consumed by direct-read must additionally satisfy the **four-lock** format contract (filename / column names / shape / format locked); authoring is covered in section 5 of references/web-report-template.md and the "deliverables entry fields" of references/objective-contract.md. This is why R4 exists — the stability of direct-read equals exactly the stability of the source file format.

## The Backfill Flow: report first, backfill gates

The correct authoring order for report deliverables and gates is **backwards**: finalize the report deliverables first (text_report.md / the web template package), then go back and cross-check, supplementing each objective's gates (including ext.report_metrics) and deliverables until R1–R4 close.

Step-by-step method:

1. **List all report materials**: read through the text_report.md material list, all names in metrics.jsonl, all non-reserved fields and direct-read markers in schema.json, and consolidate into a "report material → expected source" list;
2. **Assign ownership per item**: find the objective that should produce each material — judge by the analysis plan's semantics which objective naturally produces it; when no owner can be found, the analysis plan has a gap: add an objective or extend an existing one's scope;
3. **Check gate coverage**: verify the owning objective's existing gates already cover the material's spec (rows/columns, semantics, verdict criteria); if not, add a gate or extend an existing gate's check; **for number / large-table materials, prefer direct-read per the data-source doctrine** — complete the deliverable and its four-lock format contract (filename / column names / shape / format) for that material rather than introducing a metric;
4. **Add linkage declarations**: for metric-type materials add `gates[].metrics` (METRIC-NNN form) or `ext.report_metrics` (free naming) per R1; for direct-read materials add the direct-read mark in fill_guide per R4 and verify the deliverables' four locks are in place; for on-disk-file materials verify the deliverables declaration;
5. **Backfill source marks**: write each material's final owner back into the text_report.md material list and the fill_guide.md field descriptions (direct-read fields get the direct-read mark on the same line);
6. **Run the validator to close**: run validate_skill.py and drive every R1–R4-related FAIL/WARN to zero.

### Backfill Example

Report requirement: chapter 2 of text_report.md requires an "achievement rate trend line chart (last 12 months)".

- Find the owner: achievement analysis belongs to objective_1 (monthly achievement health diagnosis);
- Check gate coverage: objective_1 currently has G1 (value table) and G2 (verdict conclusions) — **no gate covers the trend-chart material**;
- Add a gate: add a chart spec block in objective_1's body "## Chart Methodology" and add the output guarantee for that material in the frontmatter — if the trend data is also consumed by the web report's metric `monthly_achievement_trend`, add `ext.report_metrics: [monthly_achievement_trend]` on the gate responsible for producing that data (e.g. G1), and verify deliverables includes the trend-data on-disk file;
- Backfill marks: that item in chapter 2's material list gets `Source: objective_1/G1`;
- Run the validator to confirm R1/R3 close.

## Reverse Production: the Minimal Skill Shell (pure template requests)

When the user has a finished HTML they like and only wants the report template — with no analysis-method distillation need — **deliver as a "minimal skill shell"** rather than a standalone template package: the template's data sources direct-read on-disk outputs whose format is locked by the shell's objectives; report stability is guaranteed by gates instead of depending on the session happening to produce the right data. Standalone template packages are only for in-session use without an accompanying skill (delivery differences in section 1 of references/web-report-template.md).

### Shape of the Shell

The complete quest skill package structure, with analysis-layer constraints minimized — **almost no analysis constraints, only output contracts locked**:

- **objectives/**: one or a few "data supply" objectives. Gates lock only the structural contract of the output files (the four locks: filename / column names / row granularity / shape / format), and **must not** prescribe analysis methods, decomposition, or metric computation — "how the data is retrieved is up to the executor; the on-disk file looks like this";
- **metrics/**: as needed — create only when the template has metrics that need semantics distilled; a pure structural supply can have no metrics (objectives' metrics_deps is `[]`);
- **tables/**: still distilled per workflow step 1 (the data-fact foundation is not optional — after all, direct-read files are produced by analysis);
- **report/web_report/<theme_id>/**: the template package itself, built per references/web-report-template.md;
- **SKILL.md / approach.md**: the top-level documents state that this skill serves report data supply; keep the body lean.

### Objective Authoring Essentials (differences from a regular quest)

```yaml
---
contract_version: 1
gates:
  - id: G1
    type: table
    title: Achievement detail on disk
    check: |-
      The output is written to disk as achievement_detail.csv (UTF-8, header row),
      with exactly 3 fixed columns in order: Month / Reported Value / Definition.
      Row granularity: one row = one calendar month, 12 rows in total, no truncation or sampling.
      The retrieval semantics are decided by the executor per reasonable business judgment;
      this gate locks structure only.
metrics_deps: []
deliverables:
  - file: achievement_detail.csv
    columns: [Month, Reported Value, Definition]
---
```

- check wording remains a machine-checkable structural constraint; the "analysis method is free" intent goes into the body's "## Analysis Approach" section;
- the body's "## Value List" degenerates into column semantics (what each column means, which template field consumes it) without prescribing filtering / aggregation; when the template truly needs semantics boundaries (time range, verdict baseline), state them as requirements;
- every direct-read field on the template side points back per R4 marks to the shell objective's deliverable, closing the loop.

## Relation to Existing Specs

- **report_template.md is the authoritative input**: the business-provided report template is preserved verbatim, never rewritten (see the "optional files" section of references/file-structure.md);
- **text_report.md is the distilled machine-checkable requirement**: distill the chapter structure and material list from report_template.md and add source marks; when both exist, format details defer to report_template.md, while text_report.md owns material tracing and linkage closure;
- the web template package authoring contract, script validation, and semantic review checklist are in references/web-report-template.md;
- pre-delivery audit requirements for the report parts are in references/quality-checklist.md.
