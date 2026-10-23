# Consistency Audit Checklist

After all files of a skill package are finished, a full consistency audit must be run before delivery. The audit is cross-validation — checking reference closure and format compliance file to file — not a rewrite of content.

The audit has two steps: run the validators for machine checking first, then manually review the semantic items against this checklist.

## Step 1: Run the Validators

```bash
python scripts/validate_skill.py <skill-package-directory>
```

The script covers the machine-decidable format items (frontmatter required keys and enums, the objective contract schema, file naming and order numbers, size budgets, metric reference cross-closure, report/ directory structure, report linkage contracts R1/R2/R3/R4, web_report template package machine validation). Any FAIL must be fixed before continuing.

When the skill package contains web report template packages, also run per theme under report/web_report/<theme>/:

```bash
# Item-by-item package validation (validate_skill.py invokes it automatically;
# running it standalone shows the full [pass]/[warn]/[fail] list)
python scripts/validate_template.py <template-package-directory>

# Generate preview.html and confirm in a browser that rendering matches the target
# finished page and charts draw
python scripts/preview_template.py <template-package-directory>
```

preview.html is a local preview artifact; delete it after confirmation — it is not delivered with the skill package.

## Step 2: Cross-Validation Checklist (must be walked item by item)

1. **Metric reference closure**:
   - every metric ID appearing in the objectives files (gates[].metrics and metrics_deps[].id in frontmatter, `[METRIC-NNN]` in the body) and in SKILL.md prerequisites has a same-named file under metrics/;
   - every metric in metrics/ is referenced at least once (in SKILL.md prerequisites or some objective);
   - the "metric calculation compendium" quick-reference table in approach.md has exactly as many rows as there are metric files — no omissions, no fabrications.
2. **Table name reference closure**:
   - every table name appearing in objectives' "Tables Used" and SKILL.md prerequisites has a corresponding manual under tables/;
   - every `{table, measure}` referenced by metric-manual numerators/denominators can be found as a same-named measure in the corresponding table manual's measures.
3. **SKILL.md frontmatter validity**: the frontmatter is valid YAML; name contains only letters, digits, hyphens, and underscores and matches the directory name; archetype/provenance/visibility values are legal; virtual table definitions (if any) have complete fields with exactly one of source_table / source_sql.
4. **Filename order contiguity**: files under objectives/ match `objective_{order}_{name}.md`, with order contiguous from 1, no duplicates, and consistent with the objective order of the analysis plan.
5. **Size compliance** (utf-8 bytes):
   - SKILL.md and approach.md combined under 32768;
   - each single objective file under 32768, all objectives/ files combined under 65536;
   - all metrics/ files combined under 49152.
6. **Objective contract and body, reviewed item by item**:
   - every objective file starts with a valid YAML frontmatter with contract_version/gates/metrics_deps present;
   - every gate has a unique id, a legal type value (no chart entries in hand-written files), a block-scalar check, and no vague wording like "output done / generated / reasonable / appropriate";
   - body sections complete and in fixed order (objective title, analysis approach, value list, tables used, deliverables, chart methodology), and containing **no** "## Gates" or "## Metrics" sections;
   - the value list has the three fixed header columns; deliverables state fixed filenames and column names, consistent with the frontmatter deliverables.
7. **Definition consistency spot check**: sample several core metrics and cross-check definition, sql_template, the approach compendium excerpt, and the objective value lists for consistency; for ratio metrics, the non-additivity note, the NULLIF division guard, and the LIKE parameter caveat are all in place.
8. **Report material coverage closure** (when report/ exists): manually re-verify R1/R2/R3/R4 of references/report-structure.md item by item — every metrics.jsonl name has an owner among the objectives' metric references or gates[].ext.report_metrics; every non-reserved schema.json field has a source mark in fill_guide.md; the text_report.md material list marks a source per item pointing at gates that actually exist, whose checks actually cover the material's spec; the files pointed to by direct-read marks (Direct read: {filename} / 直读：{文件名}) are in the referenced objective's deliverables, and that deliverable's four-lock format contract (filename / column names / shape / format) is fully locked in both check and columns.
9. **Web template semantic consistency review** (when web_report/ exists): check the eight items of section 9 of references/web-report-template.md one by one — SQL semantics unified, the same number physically same-source, granularity and copy consistent, schema orphan-field audit, sample_data reconciliation, render-function defensive consistency, the three-way direct-read contract comparison (fill_guide source file ↔ schema orient/column_map ↔ deliverables declaration), and the template-specific assertion script.

## Audit Results on Disk

The audit process and conclusions must be written into `AUDIT.md`: item by item, the check points, the verification method, and the result (pass / problems found and how they were fixed), plus the validator run output. Problems must be fixed and re-verified before delivery — delivering with known defects is forbidden.

## Pre-Delivery Live Test

After the audit, load-test the skill package once in your runtime environment (e.g. drop it into the runtime skill directory and load it via /load-skill), confirming the skill loads and browses normally.
