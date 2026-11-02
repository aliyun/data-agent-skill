# 报告需求物结构与联动契约

本分册规定技能包 report/ 目录的结构规范、text_report.md 的写法、报告与分析目标之间的联动契约（R1/R2/R3/R4）、纯模板需求下的「极简技能壳」反向产出，以及「先定报告、反推门禁」的工作方法。

report/ 目录在技能包与报告模板**推荐合并产出**的基调下属于推荐产出物：不存在时技能包只覆盖分析阶段；存在即表示技能覆盖「分析 + 报告」完整流程，联动契约随之生效（校验脚本自动启用相应校验项）。

## report/ 目录结构

```
report/
  text_report.md                     # 文字报告要求（如需文字报告）
  web_report/
    <theme_id>/                      # standalone 网页报告模板包，可多个主题
      manifest.toml                  # 必需：模板种类声明
      schema.json                    # 必需：LLM 填充契约
      template.html                  # 必需：自包含渲染器
      fill_guide.md                  # 强烈建议：逐字段填充指引 + 来源标注 + 直读记号
      metrics.jsonl                  # 可选：固定指标 SQL，工程直出（例外通道）
      sample_data.json               # 可选：预览/回归用样例数据
```

- text_report.md 与 web_report/ **至少存在其一**（存在 report/ 目录却两者皆无判 FAIL）；
- web_report/ 下每个主题子目录必须齐备 manifest.toml / schema.json / template.html 三件（缺任一判 FAIL）；
- 网页模板包六文件的编写契约详见 references/web-report-template_zh.md，本分册只管目录规范与联动契约。

## text_report.md 写法规范

text_report.md 是**提炼后的机器可校验文字报告要求**，纯 Markdown 编写，内容为报告输出格式约束：

1. **章节结构**：报告分几章、每章标题与顺序、章节间的逻辑关系；
2. **每章必含素材清单**：逐章列出该章必须出现的图 / 表 / 结论，**每项必须标注来源记号**（见下文「来源记号」）；
3. **语气风格**：措辞基调（如管理层汇报体 / 技术分析体）、数值精度、单位与标点约定；
4. **格式模板**：段落骨架、表格表头、图文混排规则；业务方给定模板时写「以 report_template.md 对应章节为准」，禁止把模板内容抄进来重写。

运行期，text_report.md 的内容会作为自定义 agent 的**文字报告约束**注入——即生成文字报告时逐条遵循这里声明的章节结构与素材要求。因此措辞必须是可直接执行的输出约束，禁止写成给人看的背景介绍。

### 来源记号（统一格式）

素材清单每项标注来源，统一记号：

```
来源：objective_N/G<id>
```

- `N` 为 objective 文件名中的 order 序号，`G<id>` 为该 objective frontmatter 中的门禁 id；
- 一项素材可标注多个来源（如「来源：objective_2/G1、objective_3/G2」），每个记号都会被校验；
- 校验脚本用正则 `objective_(\d+)\s*/\s*(G\d+)` 提取记号并核验指向的 objective 文件与 gate id 真实存在。

示例片段：

```markdown
## 第 2 章 销售达成分析

本章必含素材：
- 达成率趋势折线图（近 12 个月，含警戒线）——来源：objective_1/G3
- 战区达成率明细表（战区 / 达成率 / 判定）——来源：objective_1/G1
- 达标判定结论（逐战区二值判定，引用具体数值）——来源：objective_1/G2
```

## 联动契约（R1/R2/R3/R4，机器可校验）

联动契约保证：**报告需要的所有图 / 表 / 指标 / 素材，必须由分析阶段各 objective 的门禁产出覆盖**。联动信息就地声明在 objective frontmatter 内，不改 contract_version。`ext.report_metrics` 是门禁标准五字段（id / type / title / check / severity）之外**唯一带结构化消费的例外键**（validate_skill.py 的 R1 校验消费；平台工程不解析，仅随门禁整块透传）：仅在下述报告联动场景需要时写，不要借 ext 承载其他内容（validate_skill.py 会对 ext 输出一条透传提醒 WARN，属预期，不影响校验通过）。

**数据源基调**：合并产出时模板数字优先经 $read 直读 objective 落盘产物（R4 保障），metrics.jsonl SQL 直出仅作为用户点名实时查库 / 全量明细形式化的例外通道（R1 保障）——它独立于分析链路，存在口径漂移与占位符静默空数据两条固有限制（见 references/web-report-template_zh.md 6.4）。模板包内出现 metrics.jsonl 时校验脚本输出一条例外通道提醒 WARN，属预期。

### ext.report_metrics 键约定

某条门禁的产出被网页报告 metrics.jsonl 中的 metric 消费、且该 metric 的 name 不是 `METRIC-NNN` 形式时，在该门禁的 ext 中声明：

```yaml
gates:
  - id: G1
    type: table
    title: 达成率数值表
    check: |-
      输出包含 12 个月份行、3 个固定列（月份 / 报告数值 / 取数口径）。
    metrics: [METRIC-003]
    ext:
      report_metrics: [monthly_achievement_trend, region_achievement_detail]
```

含义：metrics.jsonl 中 name 为 `monthly_achievement_trend`、`region_achievement_detail` 的两个 metric，其 SQL 口径由本门禁的产出物负责验证与覆盖。

注意：`ext.report_metrics` 声明的是**本条门禁产物所消费的报告基础数据源**，metrics.jsonl 中对应 metric 的 SQL 只需产出基础列；门禁产物中的衍生列（环比、占比、客单价等均值类派生值）由执行 Agent 在 SQL 结果上按正文数值清单口径补算得到。因此两者并非 1:1 列对应关系，不应期望 SQL 直出门禁要求的全部列。

### R1 网页指标联动

`report/web_report/<theme>/metrics.jsonl` 每个 metric 的 `name` 必须被覆盖，匹配优先级：

- (a) name 即 `METRIC-NNN` 形式，且出现在某 objective 的 `metrics_deps` 或 `gates[].metrics` 中；
- (b) 某 objective 的门禁声明 `ext.report_metrics: [<name>, ...]` 包含该 name。

两者皆无 → **FAIL**。metrics.jsonl 为例外通道（见上文数据源基调），使用时逐 metric 满足本联动。

### R2 网页字段联动

`schema.json` 每个非保留顶层字段（排除 `metrics`）必须在 `fill_guide.md` 中标注来源 objective（统一记号 `来源：objective_N/G<id>`）。校验脚本正则提取记号并校验被引用的 objective 文件与 gate id 真实存在：

- 字段在 fill_guide.md 中无来源标注 → **WARN**；
- 标注指向不存在的 objective/gate → **FAIL**。

### R3 文字报告联动

`report/text_report.md` 中的素材清单（章节所需图 / 表 / 结论）必须逐项标注来源 `objective_N/G<id>`。校验脚本提取并校验存在性：

- 引用断链（指向不存在的 objective/gate）→ **FAIL**；
- 通篇无任何标注 → **WARN**。

### R4 直读源文件联动

`fill_guide.md` 中的直读记号（模板 `$read` 引用的源文件声明）必须闭合到 objective 的落盘产物。记号与来源记号同一行：

```
来源：objective_3/G1（直读：故障归类映射.csv）
```

校验规则：

- 记号中的文件名不在所标 objective 的 `deliverables[].file` 中 → **FAIL**；
- 直读记号所在行无来源记号（无法归属）→ **FAIL**；
- 该 deliverable 未声明 `columns` 列名清单 → **WARN**（直读源文件无列名契约，格式锁不完整）；
- schema 含 `x-direct-read-allowed` 标记而 fill_guide 通篇无直读记号 → **WARN**。

被直读消费的 deliverable 还必须满足**四锁**格式契约（文件名 / 列名 / 形状 / 格式锁死），写法见 references/web-report-template_zh.md 第五节与 references/objective-contract_zh.md「deliverables 条目字段」；这是 R4 存在的原因——直读的稳定性完全等于源文件格式的稳定性。

## 反推流程：先定报告，回补门禁

报告需求物与门禁的正确编写顺序是**反推**：先定稿报告需求物（text_report.md / 网页模板包），再回头核对、补充各 objective 的 gates（含 ext.report_metrics）与 deliverables，直到 R1-R4 闭合。

逐步方法：

1. **列全报告素材**：通读 text_report.md 素材清单、metrics.jsonl 全部 name、schema.json 全部非保留字段与直读标记，汇成一张「报告素材 → 期望来源」清单；
2. **逐项找归属**：每项素材找到应产出它的 objective——按分析计划语义判断哪个目标天然产出该素材；找不到归属时说明分析计划有缺口，需新增 objective 或扩展某个 objective 的范围；
3. **核对门禁覆盖**：检查归属 objective 的现有 gates 是否已覆盖该素材的规格（行列、口径、判定标准）；未覆盖则补门禁或改写既有门禁的 check；**数字 / 大表素材按数据源基调优先走直读**——为该素材补齐 deliverable 与四锁格式契约（文件名 / 列名 / 形状 / 格式），而非引入 metric；
4. **补联动声明**：metric 类素材按 R1 补 `gates[].metrics`（METRIC-NNN 形式）或 `ext.report_metrics`（自由命名）；直读类素材按 R4 在 fill_guide 补直读记号，并核对 deliverables 四锁在位；落盘文件类素材同步核对 deliverables 声明；
5. **回填来源记号**：把每项素材的最终归属写回 text_report.md 素材清单与 fill_guide.md 字段说明（直读字段同行追加直读记号）；
6. **跑脚本闭合**：运行 validate_skill.py，R1-R4 相关 FAIL/WARN 逐条清零。

### 反推示例

报告需求：text_report.md 第 2 章要求「达成率趋势折线图（近 12 个月）」。

- 找归属：达成率分析属于 objective_1（月度达成健康度诊断）；
- 核对门禁：objective_1 现有 G1（数值表）、G2（判定结论），**没有覆盖趋势图素材的门禁**；
- 补门禁：在 objective_1 正文「## 图表方法论」补一段该图的规格与画图思路，并在 frontmatter 补充对应素材的产出保障——若趋势数据同时被网页报告的 metric `monthly_achievement_trend` 消费，则在负责产出该数据的门禁（如 G1）上补 `ext.report_metrics: [monthly_achievement_trend]`，并核对 deliverables 含趋势数据落盘文件；
- 回填记号：第 2 章素材清单该项标注 `来源：objective_1/G1`；
- 跑脚本确认 R1/R3 闭合。

## 反向产出：极简技能壳（纯模板需求）

用户拿着一份满意的成品 HTML 只想要报告模板、没有分析方法沉淀诉求时，**推荐以「极简技能壳」形态交付**而非独立模板包：模板数据源直读壳内 objective 锁定格式的落盘产物，报告稳定性由门禁保证，不再依赖会话恰好产出对的数据。独立模板包仅在无技能配套的会话内场景使用（交付差异见 references/web-report-template_zh.md 第一节）。

### 壳的形态

完整 quest 技能包结构，但分析层约束最小化——**几乎无分析约束，只锁产物契约**：

- **objectives/**：一个或少数几个「数据供给」目标。门禁只锁产物文件的结构契约（文件名 / 列名 / 行粒度 / 形状 / 格式四锁），**禁止**规定分析方法、拆解思路、指标计算过程——"怎么取数由执行者自行决定，落盘长这样"；
- **metrics/**：按需——仅当模板存在需要口径沉淀的指标时创建；纯结构供给可无指标（objectives 的 metrics_deps 填 `[]`）；
- **tables/**：仍按工作流第 1 步沉淀表手册（数据事实底座不可省，直读文件毕竟由分析产生）；
- **report/web_report/<theme_id>/**：模板包本体，按 references/web-report-template_zh.md 制作；
- **SKILL.md / approach.md**：总纲说明本技能为报告数据供给用途，正文从简。

### objective 写法要点（与常规 quest 的差异）

```yaml
---
contract_version: 1
gates:
  - id: G1
    type: table
    title: 达成率明细落盘
    check: |-
      输出落盘为 达成率明细.csv（UTF-8，首行表头），列名固定 3 列且顺序一致：
      月份 / 报告数值 / 取数口径。行粒度：一行 = 一个自然月，共 12 行，
      禁止截断抽样。取数口径由执行者按业务合理口径确定，本门禁仅锁结构。
metrics_deps: []
deliverables:
  - file: 达成率明细.csv
    columns: [月份, 报告数值, 取数口径]
---
```

- check 措辞仍是精确到可机器校验的结构约束；「分析方法自由」的意图写进正文「## 分析思路」节；
- 正文「## 数值清单」退化为列语义说明（每列是什么意思、供模板哪个字段消费），不规定过滤 / 聚合过程；模板确需口径边界（时间范围、判定基准）时作为要求写明；
- 模板侧每个直读字段按 R4 记号回指壳内 objective 的 deliverable，形成闭环。

## 与既有规范的衔接

- **report_template.md 是权威输入**：业务方给定的报告模板逐字保留、禁止改写（见 references/file-structure_zh.md「可选文件」节）；
- **text_report.md 是提炼后的机器可校验要求**：从 report_template.md 提炼出章节结构与素材清单，并补上来源记号；两者并存时，格式细节以 report_template.md 为准，text_report.md 负责素材溯源与联动闭合；
- 网页模板包编写契约、脚本校验与语义审查清单见 references/web-report-template_zh.md；
- 交付前审计对报告部分的要求见 references/quality-checklist_zh.md。
