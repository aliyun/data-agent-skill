---
name: data-agent-arcvis-skill
description: >-
  指导创建长程任务（quest）类技能包的元技能：定义 SKILL.md、approach.md、
  objectives/、metrics/、tables/ 每个文件的格式规范与质量门禁；推荐技能包与
  网页报告模板合并产出（含纯模板需求反向产出的极简技能壳），模板数据优先
  $read 直读分析中间产物，metrics 直出仅作例外通道。当用户要把一类长程分析
  任务沉淀成可复用技能包，或提到「制作报告模板」「HTML 转模板」「standalone
  模板」「自定义网页」「上传主题」「固化报告样式」「更新/改版报告模板」时使用。
archetype: quest
provenance: official
---

# 概述

quest（长程任务）类技能包沉淀的是一类多步骤数据分析任务的完整打法：拆成多个分析目标、按计划推进、产出表格/图表/结论式报告。一个合格的 quest 技能包由四层知识构成：

1. **表手册（tables/*.yaml）**：每张表一个文件，沉淀字段真理与语义模型（columns/dimensions/measures）——这是数据侧的事实底座；
2. **指标手册（metrics/METRIC-NNN.yaml）**：每个指标一个文件，沉淀「字段 → 度量 → 指标」三级计算口径，是全技能包唯一的口径权威源；
3. **分析技能（objectives/ + SKILL.md + approach.md）**：逐分析目标的执行手册与总纲导览；每个 objective 文件头部以 YAML frontmatter 声明门禁与依赖指标契约；
4. **报告需求（report/，推荐合并产出）**：文字报告要求（text_report.md）与网页报告模板包（web_report/），与各 objective 的门禁联动——报告需要的每项素材都必须由某个门禁的产出覆盖。

本技能是**元技能**：不分析任何业务数据，而是指导你创建上述结构的技能包——每个文件写什么、格式长什么样、达到什么门禁才算合格。

**推荐合并产出（双向）**：技能包有报告要求时，一并产出 report/ 报告需求物（模板与门禁一体设计，数据同源）；用户只要报告模板时，反向以「极简技能壳」形态产出（数据供给目标 + 模板包，几乎无分析约束），见 references/report-structure_zh.md「反向产出」节。

**完整示例包**：examples/example-zh/ 是一个端到端的最小范例（虚构音乐零售域）——两张表手册、三个指标手册（覆盖直接量 / 跨表比值 / 衍生三类）、两个 objective（门禁四锁 + 正文六段）、文字报告要求与网页模板包（两张明细经 $read 直读、无 metrics.jsonl 例外通道），已通过全部校验脚本；examples/example-en/ 为同一范例的英文对照版（含英文表手册 / 指标 / objective / 报告模板，供英文场景参照）。分册规范看不出格式长什么样时，直接整包对照模仿。

# 适用场景

- **从零创建**：手上有一类反复出现的长程分析任务（归因、对比、趋势、达成诊断等），要把打法沉淀成可复用的技能包；
- **模板反向产出**：用户拿着满意的成品 HTML 报告只要模板（对应「制作报告模板」「HTML 转模板」「自定义网页」「上传主题」「固化报告样式」「改版报告模板」等提法）——以极简技能壳形态产出（数据供给目标 + 模板包），不做分析方法沉淀；
- **迭代改版**：已交付模板包要改版（改版后同样必须重跑完整审查，见 references/web-report-template_zh.md 第十节）；
- **审查修复**：已有技能包不合格（格式违规、引用断链、口径缺失），需要按规范逐项审查修复；
- **人工校准**：业务方给出了公式清单、SQL 校准、报告模板等权威输入，需要把它们准确落进技能包对应文件。

不适用：单次取数/查数类需求（inquiry 型）不需要走本技能的多目标结构，判定标准见 references/file-structure_zh.md。

# 前置依赖

- 用户需指定库表结构

# 工作流程

严格按以下顺序推进，后序步骤依赖前序产物：

1. **弄清库表结构**：判定 archetype（quest / inquiry）、确定技能名、编写 SKILL.md frontmatter 与虚拟表方案，随后编写表手册 tables/*.yaml——先有字段真理与度量定义，指标才有引用对象。规范见 references/file-structure_zh.md 与 references/metrics-and-tables_zh.md。包括从已有的资料（如会话历史，文档中搜索），或者直接由用户提供
2. **弄清分析需求**：编写指标手册 metrics/METRIC-NNN.yaml（分子分母引用表手册度量，是后续口径源），再编写 objectives/ 目标文件初稿——文件头部 YAML frontmatter 声明门禁（gates）与依赖指标（metrics_deps）契约的初版。规范见 references/metrics-and-tables_zh.md 与 references/objective-contract_zh.md。
3. **确认展示形式与合并产出策略**：与用户确认最终交付要文字报告、网页报告、两者都要还是其他形式。**推荐合并产出**：技能包有报告要求时一并产出 report/ 报告需求物（模板与门禁一体设计）；用户只要报告模板时反向以「极简技能壳」形态产出（做法见 references/report-structure_zh.md「反向产出」节）。据此决定第 4 步只读对应分册章节（渐进披露：文字报告读 references/report-structure_zh.md，网页报告加读 references/web-report-template_zh.md）。不需要报告交付时跳过第 4 步。
4. **生成报告需求物**：按确认的展示形式编写 report/ 目录——text_report.md（文字报告要求，素材清单逐项标注来源）与/或 web_report/<theme_id>/ 模板包。模板数据源按决策树判定：**数字 / 大表优先 $read 直读 objective 落盘产物**（物理同源），灵活内容走 LLM 填充，能 JS 现算的不进 schema，metrics.jsonl SQL 直出仅作点名实时查库 / 全量明细的例外通道；网页模板包完稿后运行 validate_template.py 校验并用 preview_template.py 生成预览确认渲染。规范见 references/report-structure_zh.md 与 references/web-report-template_zh.md。
5. **反推补充门禁并收尾**：按联动契约 R1-R4 反推核对各 objective 的 gates（含 ext.report_metrics）、deliverables 与直读源文件格式契约（被直读消费的落盘文件满足四锁：文件名 / 列名 / 形状 / 格式），直到报告每项素材都溯源闭合；然后编写总纲两件套（SKILL.md 前置依赖与 approach.md 指标计算总纲，要求 metrics/、tables/ 已定稿）；最后做一致性审计，先运行校验脚本再人工抽查，产出 AUDIT.md。规范见 references/report-structure_zh.md、references/file-structure_zh.md 与 references/quality-checklist_zh.md。

## 核心原则（全流程适用）

- **权威分级**：authority 分「真理」（业务方确认或实际验证）与「参考」（推断未确认），冲突时以真理为准；
- **证据约束**：每个口径必须有事实依据（业务方公式、验证过的 SQL、实际执行确认），禁止仅凭表结构推断编造；
- **逐字保留**：业务方校准过的 SQL、报告模板必须逐字符保留，禁止改写、优化或简化；核心原则逐条要有出处，禁止扩大业务方规范的适用范围；
- **口径唯一**：指标手册是全技能包唯一口径权威源，其他文件只用 `[METRIC-NNN]` 引用，禁止重复定义口径；相近口径必须拆成不同指标（如「当月实销」与「近三月均销」），禁止混用；
- **报告素材溯源**：报告需要的每个图/表/指标/素材必须能溯源到某个 objective 的门禁产出（联动契约 R1-R4 闭合），禁止报告凭空要求分析阶段没有保障的素材；
- **数据同源**：合并产出时报告模板中的数字/表格优先经 $read 直读 objective 落盘产物（物理同源），metrics.jsonl SQL 直出仅作点名实时查库/全量明细的例外通道；被直读消费的落盘文件必须满足四锁格式契约（文件名/列名/形状/格式，见 references/web-report-template_zh.md 第五节），改源文件格式必须同步模板侧契约；
- **精确措辞**：门禁措辞必须精确到可机器校验，禁止「已输出」「已生成」「合理」「适当」等模糊措辞；时间窗口表述必须自洽，拿不准就直接写范围（如「去年 1-12 月 + 今年 1 月至基准月」），禁止写与实际范围矛盾的「近 N 个月」计数；
- **零编造**：枚举取值只写业务方定义或数据中实际观察到的值，禁止杜撰；
- **语言一致**：技能包以单一语言编写，跟随用户工作语言（中文参照 examples/example-zh/，英文参照 examples/example-en/；name、文件名、固定 YAML 键名等标识符仍按规范形态书写）。

> 本文件是 SKILL.md（英文主文件）的中文对照副本；references/ 下各分册的中文版为 `*_zh.md`，与英文主文件内容对应。

# 参考文档

按需查阅 references/ 下的分册规范（下表为中文版文件名，英文主文件为同名去 `_zh` 后缀）：

| 文件 | 内容 |
|---|---|
| references/file-structure_zh.md | 技能包文件结构、frontmatter 六字段、虚拟表声明、文件命名、尺寸预算、总纲两件套写法 |
| references/objective-contract_zh.md | objective 文件的 YAML frontmatter 契约规范（gates / metrics_deps / deliverables）与正文段落结构、直读 deliverable 四锁 |
| references/metrics-and-tables_zh.md | 表手册与指标手册的 YAML 规范、可加性分类、关键规则与证据约束 |
| references/report-structure_zh.md | report/ 目录结构、text_report.md 写法、联动契约 R1/R2/R3/R4 与反推补充门禁的工作方法、极简技能壳反向产出 |
| references/web-report-template_zh.md | standalone 网页报告模板包六文件编写契约、$read 程序直读与直读源文件契约、数据归属决策树、硬约束与常见坑、语义一致性审查清单、迭代改版流程 |
| references/quality-checklist_zh.md | 交付前一致性审计清单与 AUDIT.md 交付要求 |
| examples/example-zh/、examples/example-en/ | 完整示例技能包（中文版 + 英文对照版，端到端最小范例，已过全部校验脚本，可整包模仿） |

# 校验脚本

技能包完稿后必须运行随附校验脚本做机器校验：

```bash
python scripts/validate_skill.py <技能包目录路径>
```

脚本逐项输出 PASS / FAIL，任一 FAIL 时退出码非 0。校验项覆盖：SKILL.md frontmatter 必填字段、objective frontmatter 契约格式、objectives 文件命名序号连续、尺寸预算、指标引用交叉闭合、report/ 目录结构、报告联动契约 R1/R2/R3/R4、web_report 模板包机器校验（无 report/ 目录时报告类校验项自动跳过）。全部 PASS 是交付的必要条件（非充分条件——语义层校验仍需按 references/quality-checklist_zh.md 人工复核）。

网页报告模板包另有两个专用脚本（validate_skill.py 会自动对每个主题调用前者）：

```bash
# 逐项校验单个模板包，输出 [pass]/[warn]/[fail] 清单
python scripts/validate_template.py <模板包目录>

# 将 sample_data.json 注入 template.html 生成 preview.html，浏览器人工确认渲染
python scripts/preview_template.py <模板包目录>
```

# 技能包结构说明

本技能包文件清单：

| 文件 | 内容 |
|---|---|
| SKILL.md / SKILL_zh.md | 元技能总纲、工作流程与导览（英文主文件 / 中文对照副本） |
| references/*.md、references/*_zh.md | 六个分册规范（英文主文件 / 中文对照副本） |
| scripts/validate_skill.py | 技能包格式校验脚本 |
| scripts/validate_template.py | 网页报告模板包校验脚本 |
| scripts/preview_template.py | 网页报告模板包预览生成脚本 |
| examples/example-zh/、examples/example-en/ | 完整示例技能包（中文版 + 英文对照版，见「概述」的完整示例包说明） |

本技能包自身不含 objectives/、metrics/、tables/ 目录：作为元技能它没有自己的分析目标、指标与表，相应的模板骨架已内嵌在 references/ 各分册中；examples/ 下的示例包含这些目录，属示例包内容而非本元技能自身结构。
