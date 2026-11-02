---
name: data-agent-arcvis-skill
description: >-
  Meta-skill that guides the creation of quest-type skill packages: format
  specs and quality gates for every file in SKILL.md, approach.md,
  objectives/, metrics/, tables/, and report/; recommends producing the skill
  package and web report template together (numbers direct-read from analysis
  outputs via $read, with metrics.jsonl SQL direct output only as an
  exception channel), including a minimal skill-shell form for pure template
  requests. Use when the user wants to distill a recurring long-running
  analysis task into a reusable skill package, or mentions making a report
  template, converting HTML to a template, standalone templates, custom web
  pages, uploading themes, locking down report styling, or updating/revising
  report templates (Chinese trigger phrases are listed in SKILL_zh.md).
archetype: quest
provenance: official
---

# Overview

A quest (long-running task) skill package distills the complete playbook for a recurring class of multi-step data analysis tasks: decomposing them into analysis objectives, executing them in order, and producing tables, charts, and conclusion-style reports. A qualified quest skill package consists of four layers of knowledge:

1. **Table manuals (tables/*.yaml)**: one file per table, capturing field truth and the semantic model (columns/dimensions/measures) — the factual foundation on the data side;
2. **Metric manuals (metrics/METRIC-NNN.yaml)**: one file per metric, capturing the three-level calculation semantics of field → measure → metric; this is the single authoritative source of calculation definitions for the whole skill package;
3. **Analysis knowledge (objectives/ + SKILL.md + approach.md)**: an execution manual per analysis objective plus the top-level guide; each objective file declares its gates and metric dependency contract in YAML frontmatter;
4. **Report requirements (report/, recommended joint production)**: text report requirements (text_report.md) and web report template packages (web_report/), linked to the gates of each objective — every piece of material the report needs must be covered by the output of some gate.

This skill is a **meta-skill**: it does not analyze any business data. Instead, it guides you in creating skill packages with the structure above — what goes in each file, what the formats look like, and what gates must pass for the package to qualify.

**Recommended joint production (both directions)**: when the skill package has reporting requirements, produce the report/ deliverables together with it (template and gates designed as one, data from a single source); when the user only wants a report template, produce in the reverse direction as a "minimal skill shell" (a data-supply objective + template package, with almost no analysis constraints) — see the "Reverse production" section of references/report-structure.md.

# When to Use

- **Creating from scratch**: you have a recurring class of long-running analysis tasks (attribution, comparison, trend, target-achievement diagnosis, ...) and want to distill the playbook into a reusable skill package;
- **Reverse production from a template**: the user has a finished HTML report they like and only wants a template (matching phrases such as "make a report template", "convert HTML to a template", "custom web page", "upload a theme", "lock down report styling", "revise a report template") — produce it as a minimal skill shell (data-supply objective + template package) without distilling analysis methods;
- **Iterative revision**: an already delivered template package needs changes (after revision, the full review must be re-run — see section 10 of references/web-report-template.md);
- **Audit and repair**: an existing skill package fails to qualify (format violations, broken references, missing calculation definitions) and needs item-by-item review and repair against the specs;
- **Manual calibration**: the business side has provided authoritative inputs such as formula lists, calibrated SQL, or report templates that must be accurately placed into the corresponding files of the skill package.

Not applicable: one-off data retrieval / lookup requests (inquiry type) do not need this skill's multi-objective structure; the decision criteria are in references/file-structure.md.

# Prerequisites

- The user must specify the database and table structures.

# Workflow

Proceed strictly in this order; later steps depend on earlier outputs:

1. **Clarify the table structures**: determine the archetype (quest / inquiry), pick the skill name, write the SKILL.md frontmatter and virtual table plan, then write the table manuals tables/*.yaml — the field truth and measure definitions must exist first, since metrics reference them. Sources include existing material (searching session history and documents) or direct input from the user. Specs: references/file-structure.md and references/metrics-and-tables.md.
2. **Clarify the analysis requirements**: write the metric manuals metrics/METRIC-NNN.yaml (numerators and denominators reference measures from the table manuals; they are the source of calculation semantics for everything that follows), then write the first draft of the objectives/ files — the YAML frontmatter declares the initial version of the gates and the metrics_deps contract. Specs: references/metrics-and-tables.md and references/objective-contract.md.
3. **Confirm the presentation form and joint-production strategy**: confirm with the user whether the final deliverable is a text report, a web report, both, or another form. **Joint production is recommended**: when the skill package has reporting requirements, produce the report/ deliverables together (template and gates designed as one); when the user only wants a report template, produce in the reverse direction as a minimal skill shell (how-to in the "Reverse production" section of references/report-structure.md). Based on this, read only the relevant sections in step 4 (progressive disclosure: for text reports read references/report-structure.md; for web reports additionally read references/web-report-template.md). Skip step 4 if no report deliverable is needed.
4. **Produce the report requirements**: write the report/ directory per the confirmed form — text_report.md (text report requirements with a material list whose items each carry a source mark) and/or web_report/<theme_id>/ template packages. Decide the data source of every template field with the decision tree: **numbers and large tables prefer $read direct-read of objective on-disk outputs** (physically same-source), flexible content goes to LLM filling, values computable in JS never enter the schema, and metrics.jsonl SQL direct output is only an exception channel for user-named real-time queries / full-detail extracts. After finishing a web template package, run validate_template.py and generate a preview with preview_template.py to confirm rendering. Specs: references/report-structure.md and references/web-report-template.md.
5. **Backfill gates and close out**: per the linkage contracts R1–R4, backfill and cross-check each objective's gates (including ext.report_metrics), deliverables, and the direct-read source-file format contract (files consumed by direct-read must satisfy the four locks: filename / column names / shape / format), until every piece of report material traces to a closed source; then write the two top-level documents (SKILL.md prerequisites and the approach.md metric calculation compendium — both require metrics/ and tables/ to be finalized); finally run the consistency audit — validators first, then manual spot checks — and produce AUDIT.md. Specs: references/report-structure.md, references/file-structure.md, and references/quality-checklist.md.

## Core Principles (apply throughout)

- **Authority grading**: authority is either "truth" (business-confirmed or actually verified) or "reference" (inferred, unconfirmed); on conflict, truth wins;
- **Evidence constraint**: every calculation definition must rest on facts (business-provided formulas, verified SQL, confirmed-by-execution); inventing definitions from table structure alone is forbidden;
- **Verbatim preservation**: SQL and report templates calibrated by the business side must be preserved character-for-character — no rewriting, optimizing, or simplifying; every core principle must cite its source, and extending the business side's rules beyond their stated scope is forbidden;
- **Single source of definitions**: the metric manuals are the only authoritative source of calculation semantics; other files reference metrics only via `[METRIC-NNN]`, and duplicating definitions is forbidden; similar-but-different definitions must be split into separate metrics (e.g. "current-month sales" vs "trailing 3-month average sales") — mixing them is forbidden;
- **Report material traceability**: every chart/table/metric/material the report needs must trace to the output of some objective's gate (closed by linkage contracts R1–R4); the report must never demand material that the analysis phase does not guarantee;
- **Data same-sourcing**: in joint production, numbers and tables in the report template prefer $read direct-read of objective on-disk outputs (physically same-source); metrics.jsonl SQL direct output is only an exception channel for user-named real-time queries / full-detail extracts; files consumed by direct-read must satisfy the four-lock format contract (filename / column names / shape / format — see section 5 of references/web-report-template.md); changing a source file's format must be synchronized with the template-side contract;
- **Precise wording**: gate wording must be precise enough for machine checking — vague terms like "output done", "generated", "reasonable", "appropriate" are forbidden; time-window statements must be self-consistent; when unsure, write the range explicitly (e.g. "Jan–Dec last year + Jan this year through the base month") instead of a "recent N months" count that contradicts the actual range;
- **Zero fabrication**: enumeration values must be either business-defined or actually observed in the data; inventing values is forbidden;
- **Language consistency**: write the package in a single language following the user's working language — Chinese packages model on examples/example-zh/, English packages on examples/example-en/ (identifiers such as name, filenames, and fixed YAML keys remain in their specified forms).

# Reference Documents

Consult the references/ volumes on demand. English files are the main versions; `*_zh.md` alongside each are the Chinese counterparts.

| File | Content |
|---|---|
| references/file-structure.md | Skill package file structure, the six frontmatter fields, virtual table declaration, file naming, size budgets, how to write the two top-level documents |
| references/objective-contract.md | The objective file's YAML frontmatter contract (gates / metrics_deps / deliverables) and body section structure; the four locks of direct-read deliverables |
| references/metrics-and-tables.md | YAML specs of table manuals and metric manuals, additivity classification, key rules, evidence constraints |
| references/report-structure.md | report/ directory structure, how to write text_report.md, linkage contracts R1/R2/R3/R4 and the backfill-gates working method, minimal skill-shell reverse production |
| references/web-report-template.md | The six-file authoring contract of standalone web report template packages, $read program direct-read and the direct-read source-file contract, data-attribution decision tree, hard constraints and common pitfalls, semantic consistency review checklist, iterative revision workflow |
| references/quality-checklist.md | Pre-delivery consistency audit checklist and AUDIT.md delivery requirements |

# Validation Scripts

After the skill package is finished, run the bundled validators for machine checking:

```bash
python scripts/validate_skill.py <skill-package-directory>
```

The script prints PASS / FAIL per item and exits non-zero on any FAIL. Checks cover: SKILL.md frontmatter required fields, objective frontmatter contract format, objective file naming with contiguous order numbers, size budgets, metric reference cross-closure, report/ directory structure, report linkage contracts R1/R2/R3/R4, and machine validation of each web_report template package (report-related checks are skipped automatically when there is no report/ directory). All PASS is a necessary condition for delivery (not sufficient — the semantic layer still requires manual review per references/quality-checklist.md).

Web report template packages have two dedicated scripts (validate_skill.py automatically invokes the first for every theme):

```bash
# Item-by-item validation of one template package; prints [pass]/[warn]/[fail]
python scripts/validate_template.py <template-package-directory>

# Injects sample_data.json into template.html to produce preview.html for browser confirmation
python scripts/preview_template.py <template-package-directory>
```

# Package Structure

File manifest of this skill package:

| File | Content |
|---|---|
| SKILL.md | This file: meta-skill guide, workflow, and navigation |
| SKILL_zh.md | Chinese counterpart of this guide |
| references/*.md | The six authoring specs (English main versions) |
| references/*_zh.md | Chinese counterparts of the six specs |
| scripts/validate_skill.py | Skill package format validator |
| scripts/validate_template.py | Web report template package validator |
| scripts/preview_template.py | Web report template preview generator |
| examples/example-zh/ | Complete example skill package in Chinese (fictional music-retail domain) |
| examples/example-en/ | Complete example skill package in English (same domain) |

This package contains no objectives/, metrics/, or tables/ directories of its own: as a meta-skill it has no analysis objectives, metrics, or tables — the corresponding template skeletons are embedded in the references/ volumes. The objectives/, metrics/, and tables/ directories under examples/ belong to the example packages, not to this meta-skill.
