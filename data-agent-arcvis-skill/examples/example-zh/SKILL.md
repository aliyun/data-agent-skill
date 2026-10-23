---
name: example-zh
description: >-
  月度音乐零售销售分析（中文完整示例包）：整体销量与渠道结构表现、重点曲目目标达成诊断，
  产出渠道分布与曲目达成明细落盘、文字报告与配套网页报告模板。适用于
  数字音乐零售业务的月度经营复盘。
archetype: quest
provenance: official
---

# 概述

本包为 arcvis-skill 的完整中文示例包（英文对照版见 examples/example-en/），演示一个合格 quest 技能包的完整形态（含 report/ 与网页模板包），可整包模仿。

月度经营复盘要回答三个递进问题：这个月卖得怎么样（总量）、结构上谁强谁弱（渠道）、目标完成得怎么样（曲目达成）。本技能把这套打法沉淀为两个分析目标——整体销量与渠道表现 → 重点曲目达成诊断——并配套月度报告需求物，报告数字经直读与落盘产物物理同源。

# 适用场景

- 月度（或任意报告月）数字音乐零售经营复盘；
- 渠道结构变化跟踪与重点曲目目标达成诊断；
- 不适用：单曲目专题归因（一次性临时分析即可，不值得沉淀为技能）。

# 前置依赖

**指标**（口径唯一权威源见 metrics/，此处仅列清单）：

- [METRIC-001] 当月销量
- [METRIC-002] 销量达成率
- [METRIC-003] 销量环比增长率

**表**（字段真理与语义模型见 tables/）：

- track_sales_month：曲目月度销量事实表（月 × 曲目 × 渠道），销量与收入的唯一事实源；
- track_target_month：曲目月度目标表（月 × 曲目），目标覆盖全部上架曲目。

# 技能包结构说明

| 路径 | 内容 |
|---|---|
| SKILL.md / approach.md | 总纲导览与指标计算总纲 |
| objectives/objective_1_整体销量与渠道表现.md | 总量与渠道结构分析 |
| objectives/objective_2_重点曲目达成诊断.md | 曲目目标达成诊断 |
| metrics/METRIC-001.yaml ~ METRIC-003.yaml | 三个指标口径手册 |
| tables/track_sales_month.yaml、track_target_month.yaml | 表手册 |
| report/text_report.md | 文字报告要求（三章结构 + 素材清单） |
| report/web_report/monthly-sales-overview/ | 网页报告模板包（直读两张落盘明细） |
