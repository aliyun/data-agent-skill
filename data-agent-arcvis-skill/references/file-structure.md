# Skill Package File Structure and Naming

This volume specifies the directory structure of a quest skill package, the SKILL.md frontmatter, virtual table declaration, file naming rules, size budgets, and how to write the two top-level documents.

## Directory Structure

A complete quest skill package directory looks like:

```
{skill-name}/
  SKILL.md                          # frontmatter + top-level guide (required)
  approach.md                       # analysis approach and metric compendium (required)
  objectives/
    objective_1_{name}.md           # execution manual per analysis objective (at least one)
    objective_2_{name}.md
    ...
  metrics/
    METRIC-001.yaml                 # one file per metric
    ...
  tables/
    {table-name}.yaml               # one file per table
    vt_{name}.yaml                  # virtual table definitions (if any)
  report/                           # report deliverables (joint production recommended; presence means the skill covers the full analysis + reporting flow)
    text_report.md                  # text report requirements (if a text report is needed)
    web_report/
      {theme_id}/                   # standalone web report template packages; multiple themes allowed
  report_template.md                # optional: business-provided report template, preserved verbatim
  chart_style.md                    # optional: chart style conventions
  askdata_style.md                  # optional: answering-style conventions when inquiry mode consumes this skill
```

## Archetype Determination

- **quest (long-running task)**: the skill distills a systematic analysis workflow — multi-objective analysis plans, attribution/comparison/trend methods, chart and report outputs. Most analysis skills fall here; when in doubt, choose quest.
- **inquiry (data lookup)**: the skill is mainly about retrieving data / lookups / clarifying calculation semantics — the use case is repeatedly asking "how much is metric X" or "pull this segment of that table"; the value being distilled is table structure and metric definitions, not analysis methodology.
- This meta-skill only covers the creation of quest-type skill packages; the workflow does not apply to inquiry-type packages.

## Skill Name (name)

- Must be a slug: only letters, digits, hyphens, and underscores, i.e. must fully match `^[a-zA-Z0-9_-]+$`;
- Chinese topics must be translated or transliterated into English (e.g. "music-retail sales analysis" → `music-retail-sales-analysis`);
- Overly generic names (such as `analysis`, `skill`) are forbidden; the name must reflect the concrete business scenario;
- The skill name is also the skill package directory name; the name field must match the directory name verbatim.

## The Six SKILL.md Frontmatter Fields

The frontmatter sits at the very top of SKILL.md, wrapped in paired `---` lines, and must be valid YAML.

| Field | Required | Description |
|---|---|---|
| name | required | Skill name in slug form, fully matching `^[a-zA-Z0-9_-]+$` |
| description | required | One sentence describing what kind of analysis the skill teaches and when it applies; `>-` folded multi-line style supported |
| archetype | required | Must be `quest` or `inquiry` |
| provenance | required | Must be `distilled` (auto-extracted) or `official` (hand-written and maintained); manually created packages must use `official` |
| virtual_tables | optional | List of virtual table definition files, items shaped like `- file: tables/vt_xxx.yaml` |
| visibility | optional | Must be `augment` (default semantics: virtual tables presented alongside regular tables) or `virtual_only` (the data source only exposes virtual tables) |

## Virtual Table Declaration (if needed)

Each virtual_tables entry points to a definition file under tables/. The definition file supports exactly one of two forms:

- **source_table form** (column subset of a single table + row filter):
  - `source_table` holds the physical table name;
  - `columns` lists the exposed columns — plain string items inherit the physical table definition, object items (name/type/description) can override inherited values;
  - `row_filter` holds the row filter condition and may legally reference non-exposed columns (e.g. a country column used only for filtering, not listed in columns);
  - `replace_source` true (default) replaces the source physical table with this virtual table in presentation; false keeps both visible.
- **source_sql form** (multi-table JOIN wide table):
  - `source_sql` holds the complete SELECT; filter conditions go into the SQL WHERE;
  - filling `row_filter` as well is forbidden;
  - `columns` here is pure descriptive metadata and must correspond one-to-one with the SELECT output columns.

**Data source binding (bind_datasource)**: in single-datasource sessions it may be omitted and the session's only data source is bound automatically; in multi-datasource sessions the definition file must fill in `bind_datasource` with the db_name (or instance_id) of the binding target.

## objectives File Naming

- The filename must be `objective_{order}_{name}.md`;
- order is a positive integer, contiguous from 1, no duplicates, corresponding one-to-one with the objective order in the overall analysis plan;
- omitting the order number is forbidden; the name part must match the `# Objective: ` title inside the file.

The content structure of objective files (frontmatter contract + body sections) is specified in references/objective-contract.md.

## Size Budgets (utf-8 bytes, hard requirements)

| Scope | Limit |
|---|---|
| SKILL.md + approach.md combined | 32768 (32KB) |
| Each objectives/*.md | 32768 (32KB) |
| All objectives/ files combined | 65536 (64KB) |
| All metrics/ files combined | 49152 (48KB) |

Keep the top-level documents lean rather than exhaustive: details sink into objectives and manual files; SKILL.md/approach.md serve only as quick reference and navigation.

The report/ directory is exempt from the four budgets above (template.html is large and never injected into prompts), but text_report.md is recommended ≤ 8KB.

## Writing the Two Top-Level Documents

The two top-level documents must be written after tables/, metrics/, and objectives/ are all complete; they summarize and navigate. They must not define metric semantics or table structures of their own; the single exception is the "metric calculation compendium" quick-reference table, whose content must be excerpted verbatim from the metric manuals.

### SKILL.md Body Structure

1. `# Overview`: one paragraph on what problem this class of analysis solves and the overall decomposition framework.
2. `# When to Use`: which requests should use this skill; promising scenarios beyond actual capabilities is forbidden.
3. `# Prerequisites`:
   - Metrics: the list of metric IDs this skill uses (e.g. `[METRIC-001]` sales), consistent with the metrics/ manuals — no fabrication, no omission;
   - Tables: the list of table names used, consistent with the tables/ manuals; for virtual tables, state their key semantics and definition file location one by one.
4. A "Package Structure" section listing the full file manifest is recommended for navigation.

### approach.md Structure

1. `# Analysis Approach`: the overall methodology — core principles, how objectives decompose, execution order, general notes.
   - **Every core principle must cite its source**: each output/format-level principle must trace to the business side's own specifications verbatim; extending their scope is forbidden. For example, if the business says "ratio/share metrics annotate numerator and denominator; YoY/MoM output numbers only", it must never become "YoY/MoM must also annotate numerator and denominator";
   - The execution order must correspond to the order numbers in the objectives/ filenames and agree with the process constraints given by the business.
2. `## Metric Calculation Compendium`: a Markdown quick-reference table with four fixed header columns:

   | ID | Name | Formula | Semantics |
   |---|---|---|---|
   | METRIC-NNN | {metric name} | {formula} | {key semantics} |

   - It must cover every METRIC-ID in the metrics/ manuals — no omission, no fabrication;
   - Formulas and semantics must be excerpted verbatim from the metric manuals; rewriting is forbidden.

### Optional Files

- **report_template.md**: must be created when the business provides a report template; the template content is preserved verbatim, never rewritten; other files that touch output formatting uniformly say "per the corresponding section of report_template.md" instead of copying template content into approach or objectives. Relationship to report/: report_template.md is the authoritative input, report/text_report.md is the machine-checkable requirements distilled from it.
- **chart_style.md**: distills chart style conventions (colors, fonts, layout, annotation rules) for all objectives' charts to follow.
- **askdata_style.md**: answering-style conventions (tone, count and granularity of insights, chart preferences, terminology and units, summary format) for when inquiry mode consumes this skill; detected at runtime, it is read whole and injected into the inquiry agent's prompt. Write declarative style conventions only — SQL, calculation semantics, table structures, and metric computation logic are forbidden; recommended ≤ 2KB. The usage matches the inquiry-type convention.
- **report/ directory**: report deliverables (text report requirements and web report template packages), recommended for joint production with the skill package — its presence means the skill covers the full analysis + reporting flow, linked to the objectives' gates (R1/R2/R3/R4); pure template requests reverse-produce a "minimal skill shell" that also lands here. Directory specs and linkage contracts: references/report-structure.md.
