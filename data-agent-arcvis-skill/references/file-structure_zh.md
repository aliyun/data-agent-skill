# 技能包文件结构与命名

本分册规定 quest 技能包的目录结构、SKILL.md frontmatter、虚拟表声明、文件命名规则、尺寸预算与总纲两件套写法。

## 目录结构

一个完整的 quest 技能包目录形如：

```
{技能名}/
  SKILL.md                          # frontmatter + 总纲导览（必须）
  approach.md                       # 分析思路与指标计算总纲（必须）
  objectives/
    objective_1_{名称}.md           # 逐分析目标的执行手册（至少一个）
    objective_2_{名称}.md
    ...
  metrics/
    METRIC-001.yaml                 # 每个指标一个文件
    ...
  tables/
    {表名}.yaml                       # 每张表一个文件
    vt_{名称}.yaml                    # 虚拟表定义（如有）
  report/                             # 报告需求物（推荐合并产出；存在即覆盖分析+报告完整流程）
    text_report.md                    # 文字报告要求（如需文字报告）
    web_report/
      {theme_id}/                     # standalone 网页报告模板包，可多个主题
  report_template.md                # 可选：业务方报告模板逐字保留
  chart_style.md                    # 可选：图表风格约定
  askdata_style.md                  # 可选：问数模式消费本技能时的回答风格约定
```

## 技能类型判定（archetype）

- **quest（长程任务型）**：技能沉淀的是系统性分析流程——多目标分析计划、归因/对比/趋势等分析方法、图表与报告产出。绝大多数分析类技能属于此类，拿不准时必须选 quest。
- **inquiry（问数型）**：技能以取数/查数/口径澄清为主——使用场景是反复查询「某指标是多少」「取某表某段数据」，沉淀价值在表结构与指标口径，而非分析方法论。
- 本元技能只覆盖 quest 型技能包的创建；判定为 inquiry 时不适用本流程。

## 技能名（name）

- 必须为 slug 格式：只含字母、数字、连字符、下划线，即必须完整匹配正则 `^[a-zA-Z0-9_-]+$`；
- 中文主题必须翻译或音译为英文（如「音乐零售销售分析」→ `music-retail-sales-analysis`）；
- 禁止过于通用的名称（如 `analysis`、`skill`），必须体现具体业务场景；
- 技能名同时是技能包目录名，name 字段值必须与目录名逐字一致。

## SKILL.md frontmatter 六字段

frontmatter 位于 SKILL.md 文件最顶部，由成对的 `---` 行包裹，必须是合法 YAML。

| 字段 | 必填 | 说明 |
|---|---|---|
| name | 必填 | 技能名，slug 格式，必须完整匹配 `^[a-zA-Z0-9_-]+$` |
| description | 必填 | 一句话说明技能教你做哪一类分析、适用什么场景；支持 `>-` 折叠多行写法 |
| archetype | 必填 | 只能取 `quest` 或 `inquiry` |
| provenance | 必填 | 只能取 `distilled`（自动抽取沉淀）或 `official`（人工编写维护）；人工创建的技能包必须填 `official` |
| virtual_tables | 可选 | 虚拟表定义文件清单，列表项形如 `- file: tables/vt_xxx.yaml` |
| visibility | 可选 | 只能取 `augment`（缺省语义：虚拟表与常规表并列呈现）或 `virtual_only`（数据源只暴露虚拟表） |

## 虚拟表声明（如需要）

virtual_tables 每项指向一个 tables/ 目录下的虚拟表定义文件。定义文件支持两种形态，必须恰选其一：

- **source_table 形态**（单表列子集 + 行过滤）：
  - `source_table` 填物理表名；
  - `columns` 列出对外暴露的列——纯字符串项直接继承物理表定义，对象项（name/type/description）可覆盖继承值；
  - `row_filter` 填行过滤条件，可合法引用未暴露的列（如仅作过滤、不进 columns 的国家列）；
  - `replace_source` 为 true（默认）时源物理表被本虚拟表替换呈现，为 false 时两者并存可查。
- **source_sql 形态**（多表 JOIN 宽表）：
  - `source_sql` 直接写完整 SELECT，过滤条件写进 SQL 的 WHERE；
  - 禁止再填 `row_filter`；
  - `columns` 此时是纯字段说明元数据，必须与 SELECT 输出列一一对应。

**数据源绑定（bind_datasource）**：单数据源会话可缺省，自动绑定会话内唯一数据源；多数据源会话必须在定义文件中填写 `bind_datasource` 的 db_name（或 instance_id）指定绑定目标。

## objectives 文件命名

- 文件名必须为 `objective_{order}_{名称}.md`；
- order 为正整数序号，从 1 起连续、无重复，与技能整体分析计划中的目标顺序一一对应；
- 禁止缺省序号；名称部分与文件内 `# 目标：` 标题保持一致。

objective 文件的内容结构（frontmatter 契约 + 正文段落）见 references/objective-contract_zh.md。

## 尺寸预算（utf-8 字节数，硬性要求）

| 范围 | 上限 |
|---|---|
| SKILL.md + approach.md 合计 | 32768（32KB） |
| 单个 objectives/*.md | 32768（32KB） |
| objectives/ 全部文件合计 | 65536（64KB） |
| metrics/ 全部文件合计 | 49152（48KB） |

总纲宜精不宜全：细节下沉到 objectives 与手册文件，SKILL.md/approach.md 只做速查与导览。

report/ 目录不计入上述四项预算（template.html 体量大且不注入 prompt），但 text_report.md 建议 ≤ 8KB。

## 总纲两件套写法

总纲两件套必须在 tables/、metrics/、objectives/ 全部完成后编写，负责汇总与导览。总纲禁止另行定义指标口径或表结构；唯一例外是「指标计算总纲」速查表，且其内容必须从指标手册逐条摘录。

### SKILL.md 正文结构

1. `# 概述`：一段话说明这类分析要解决什么问题、整体拆解框架是什么。
2. `# 适用场景`：什么样的需求应该套用这个技能；禁止写实际能力之外的场景承诺。
3. `# 前置依赖`：
   - 指标：本技能用到的指标 ID 清单（如 `[METRIC-001]` 销量），必须与 metrics/ 指标手册一致，无编造、无遗漏；
   - 表：用到的表名清单，与 tables/ 表手册一致；有虚拟表时逐个说明其口径要点与定义文件位置。
4. 建议附「技能包结构说明」节，列出技能包全部文件清单，便于查阅导航。

### approach.md 结构

1. `# 分析思路`：整体方法论——核心原则、目标怎么拆、执行顺序、通用注意事项。
   - **核心原则逐条有出处**：每条输出/格式类原则必须能在业务方规范中找到原文依据，禁止扩大适用范围。例如业务方说「率/占比指标标注分子分母、同比/环比只输出数值」，绝不能写成「同比环比也要标注分子分母」；
   - 执行顺序必须与 objectives/ 文件名中的 order 顺序对应，并与业务方给定的流程约束一致。
2. `## 指标计算总纲`：Markdown 速查表，表头四列固定：

   | ID | 名称 | 公式 | 口径 |
   |---|---|---|---|
   | METRIC-NNN | {指标名} | {计算公式} | {口径要点} |

   - 必须覆盖 metrics/ 指标手册中的全部 METRIC-ID，无遗漏、无编造；
   - 公式与口径必须从指标手册逐条摘录，禁止改写。

### 可选文件

- **report_template.md**：业务方给定报告模板时必须创建，模板内容逐字保留、禁止改写；其他文件涉及输出格式时统一写「以 report_template.md 对应章节为准」，禁止把模板内容抄进 approach 或 objectives。与 report/ 的关系：report_template.md 是权威输入，report/text_report.md 是从它提炼出的机器可校验要求。
- **chart_style.md**：沉淀图表风格约定（配色、字体、布局、标注规则等），供各目标绘图统一遵循。
- **askdata_style.md**：问数模式消费本技能时的回答风格约定（语气、洞察条数与粒度、图表偏好、术语与单位、摘要格式）；运行时检测到即整读注入问数 Agent 的 prompt。只写声明式风格约定，禁止写 SQL、口径、表结构与指标计算逻辑，建议 ≤ 2KB；用法与 inquiry 型一致。
- **report/ 目录**：报告需求物（文字报告要求与网页报告模板包），推荐与技能包合并产出——存在即表示技能覆盖分析+报告完整流程，与各 objective 门禁联动（R1/R2/R3/R4）；纯模板需求反向产出「极简技能壳」时同样落在此目录。目录规范与联动契约见 references/report-structure_zh.md。
