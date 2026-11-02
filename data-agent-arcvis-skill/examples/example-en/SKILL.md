---
name: example-en
description: >-
  Monthly music-retail sales analysis (complete English example package):
  overall sales and channel structure performance, key-track target
  achievement diagnosis, with on-disk channel distribution and track
  achievement details, a text report, and a companion web report template.
  Suited for monthly business reviews of digital music retail.
archetype: quest
provenance: official
---

# Overview

This package is the complete English example package for arcvis-skill (Chinese counterpart: examples/example-zh/). It demonstrates the full shape of a qualified quest skill package (including report/ and a web report template package) and can be imitated as a whole.

A monthly business review answers three progressive questions: how did we sell this month (volume), who is strong or weak in the mix (channels), and how did we do against targets (track achievement). This skill distills that playbook into two analysis objectives — overall sales and channel performance → key-track target achievement diagnosis — plus monthly report deliverables whose numbers are physically same-sourced with the on-disk outputs via direct read.

# When to Use

- Monthly (or any reporting-month) business review of digital music retail;
- Tracking channel-mix changes and diagnosing key-track target achievement;
- Not applicable: single-track deep-dive attribution (a one-off ad-hoc analysis suffices; not worth distilling into a skill).

# Prerequisites

**Metrics** (metrics/ is the single authority for calculation definitions; only listed here):

- [METRIC-001] Monthly Sales Volume
- [METRIC-002] Sales Target Achievement Rate
- [METRIC-003] Sales MoM Growth Rate

**Tables** (field truth and semantic model in tables/):

- track_sales_month: track monthly sales fact table (month × track × channel), the single source of truth for sales and revenue;
- track_target_month: track monthly target table (month × track), targets cover the full catalog of listed tracks.

# Package Structure

| Path | Content |
|---|---|
| SKILL.md / approach.md | Top-level guide and metric calculation compendium |
| objectives/objective_1_overall_sales_and_channel_performance.md | Volume and channel-mix analysis |
| objectives/objective_2_key_track_target_diagnosis.md | Track target achievement diagnosis |
| metrics/METRIC-001.yaml ~ METRIC-003.yaml | Three metric definition manuals |
| tables/track_sales_month.yaml, track_target_month.yaml | Table manuals |
| report/text_report.md | Text report requirements (three chapters + material list) |
| report/web_report/monthly-sales-overview/ | Web report template package (direct-reads the two on-disk details) |
