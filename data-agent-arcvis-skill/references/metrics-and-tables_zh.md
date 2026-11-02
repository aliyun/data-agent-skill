# 表手册与指标手册规范

本分册规定 tables/*.yaml 表手册与 metrics/METRIC-NNN.yaml 指标手册的编写格式。两者是技能包三层知识模型的底座：表手册沉淀字段真理与语义模型，指标手册沉淀计算口径。

## 三级模型：字段 → 度量 → 指标

- **字段 field**：表里的物理列（如 `actual_month`）；
- **度量 measure**：对字段的原子聚合（如 `SUM(actual_month)` = 度量「实销」），定义在表手册；
- **指标 metric**：有业务含义的 KPI，由度量组合而成（如 实销 ÷ 目标 = 比值指标「实销达成率」），定义在指标手册。

**指标的分子分母必须引用表手册里的度量，禁止直接引用裸字段。**

## 表手册 tables/*.yaml

每张表一个 YAML 文件，文件名为 `tables/{表名}.yaml`。表手册只沉淀「关于表本身的事实」，禁止输出分析洞察（比率、达成率、排名、异常结论等一律不写）。

每个文件包含两部分：

1. **字段真理**：数据剖析出来的事实——类型、时间格式、空值表示、数据分布、枚举分布；
2. **语义模型**：维度（可分组/筛选的列）+ 度量（可聚合的列，带可加性）。

### 完整字段骨架

```yaml
table_name: ads_auto_sales_month
business_description: 汽车经销实销库存月报（虚构示例域），按月×经销商统计实销/库存/目标
granularity: 一行 = 某月(biz_month) 的 某经销商(dealer_code)
columns:
  - column_name: biz_month
    type: string
    meaning: 业务月份
    time_format: "yyyyMM，如 202606（字符串存储，非 date 类型）"
    null_representation: 无空值
    data_distribution: "202401 ~ 202606，共 18 个月"
    authority: 真理
  - column_name: region_name
    type: string
    meaning: 战区名称
    null_representation: 无空值
    enum_distribution:
      - {value: 京沪战区, ratio: 22%}
      - {value: 华南战区, ratio: 31%}
    authority: 真理
  - column_name: actual_month
    type: decimal(18,2)
    meaning: 当月实销量
    null_representation: "NULL 表示未上报（≠ 0；0 表示确认零销量）"
    data_distribution: "min=0, p50=42, p95=310, max=1580；NULL 占 3%"
    authority: 真理
dimensions:
  - {name: 月份, column: biz_month, type: 时间, granularity: 月, nullable: false, authority: 真理}
  - {name: 战区, column: region_name, type: 分类, nullable: false, authority: 真理}
measures:
  - {name: 实销, aggregation: SUM, column: actual_month, additivity: 完全可加, authority: 真理}
  - {name: 目标, aggregation: SUM, column: actual_target, additivity: 完全可加, authority: 真理}
  - {name: 库存, aggregation: SUM, column: terminal_stock_month, additivity: 半可加, non_additive_across: [月份], authority: 真理}
```

### 逐字段编写要求

**顶层三字段**：

- `table_name`：物理表名，必须与文件名一致；
- `business_description`：一句话业务描述；
- `granularity`：一行数据代表什么，用「一行 = …」句式。

**columns（字段真理）**——必须覆盖表结构中的每一列，不多不少；禁止漏列，禁止编造表结构中不存在的列：

- `column_name` / `type` / `meaning`：列名、类型、业务含义；
- `time_format`：时间类字段必填，如 `yyyyMM` / `yyyy-MM-dd`，避免使用者猜格式导致过滤条件写错；
- `null_representation`：明确空值表示是 `NULL`、`""` 还是 `0`/`-`，或混用；**必须区分 NULL ≠ 0 ≠ ""**——三者在聚合、筛选、分组时处理方式不同；
- `data_distribution`：min/分位/max/空值率；
- `enum_distribution`：分类字段的取值 + 占比；
- `authority`：真理 / 参考（见下方权威级别）。

**dimensions / measures（语义模型）**：

- 维度：可分组/筛选的列，逐项含 name/column/type（时间/分类等）/granularity（时间维度必填）/nullable/authority；
- 度量：可聚合的列，是从字段聚合而来的原子单位；`aggregation` 写明聚合方式（SUM/COUNT/AVG 等）；
- 度量的 `additivity` 三分类：
  - **完全可加**：可跨所有维度求和（如销量）；
  - **半可加**：可跨部分维度但不能跨时间（如库存月末快照），必须用 `non_additive_across` 列出不可跨的维度；
  - **非可加**：不能加也不能平均——率/比类天然非可加，不在表手册声明，统一在指标层处理；
- 可加性判定必须在表手册的度量层完成，指标手册直接引用，禁止把可加性判断推迟到指标层或使用现场。

### 表手册硬性规则

- **枚举取值铁律**：`enum_distribution` 只允许写两类值——①业务方明确定义的取值；②数据中实际观察到的取值。两者都没有的值绝对禁止列出（标「参考」也不行），禁止用行业常识补全枚举。
- 业务方定义了取值集合时（如「仅自主、合资两种取值」），枚举必须与定义完全一致，禁止增删。
- **权威级别**：真理（业务方输入/表结构/实际查询确认）> 参考（历史/推断）。多来源冲突时按真理优先裁决；仅从少量样本推断的分布必须标「参考」，禁止把参考级推断当真理呈现。
- 禁止与业务方给出的字段/枚举定义相矛盾；冲突时以业务方定义为准。

## 指标手册 metrics/METRIC-NNN.yaml

每个指标一个 YAML 文件，把指标的计算方法沉淀为既能被人理解、又能直接转成 SQL 的定义。

指标类型三分：**直接量**（单度量，如实销量）/ **比值**（两度量相除）/ **衍生**（基于其他指标的公式）。

### 编号规则

- 指标 ID 格式固定为 `METRIC-NNN`，NNN 为三位数字，从 001 起连续编号；
- 文件名必须为 `metrics/METRIC-NNN.yaml`；
- 正文 `metric_id` 必须与文件名一致；objectives、approach 等其他文件对该指标的引用必须与文件名逐字一致。

### 完整 YAML 骨架（以比值指标为例）

```yaml
metric_id: METRIC-016
metric_name: 实销达成率
type: 比值
format: 百分比
definition: 当月实销量 ÷ 当月目标量，衡量销售目标的完成程度；= 1.0 即刚好达标
when_to_use: |
  - 进销存/目标达成诊断：判断某战区/经销商是否完成当月目标
  - 责任穿透：按 战区→区域经理→经销商 逐层定位未达标主体
  - 不适用：跨月累计达成请用累计口径指标；本指标是率，不能跨月/跨组求平均
numerator: {table: ads_auto_sales_month, measure: 实销}
denominator: {table: ads_auto_sales_month, measure: 目标}
thresholds:
  - {verdict: 达标, condition: ">= 1.0"}
  - {verdict: 未达标, condition: "< 1.0"}
exceptions:
  - 分母为0或NULL → 结果记 "—"，不参与排名
  - 区域经理为空 → 单列一行 "未分配"，不并入他人
  - 当月未走完 → 分子分母都截取相同日历天数再算
notes: 率类非可加——换粒度时先聚合分子、分母再相除，绝不能对达成率本身 SUM/AVG
authority: 真理
sql_template: |
  SELECT SUM(actual_month) / NULLIF(SUM(actual_target), 0) AS 实销达成率
  FROM ads_auto_sales_month
  WHERE biz_month = '{月份}' AND region_name LIKE '%{战区}%'
parameters:
  - {name: 月份, example: "202606"}
  - {name: 战区, example: "京沪", description: "模糊匹配，模板已带%，值不要再写%"}
```

### 逐字段说明

| 字段 | 必填 | 说明 |
|---|---|---|
| metric_id | 必填 | `METRIC-NNN`，必须与文件名一致 |
| metric_name | 必填 | 指标业务名称（如「实销达成率」） |
| type | 必填 | 只能取：直接量 / 比值 / 衍生 |
| format | 必填 | 展示格式，如 百分比 / 金额 / 数量 |
| definition | 必填 | 一句话口径定义，含判定基准（如「= 1.0 即刚好达标」） |
| when_to_use | 必填 | 适用场景 + 不适用场景，逐条列出 |
| numerator | 必填 | `{table, measure}` 形式引用表手册度量 |
| denominator | 必填 | 比值填 `{table, measure}`；直接量固定写 `"none"` |
| thresholds | 可选 | 判定阈值，`{verdict, condition}` 列表 |
| exceptions | 必填 | 异常口径处理清单，必须成体系（见关键规则 7） |
| notes | 必填 | 使用注意；率类必须写非可加提示 |
| authority | 必填 | 真理 / 参考 |
| sql_template | 必填 | 参数化 SQL 模板，参数用 `{参数名}` 占位 |
| parameters | 视需要 | 模板参数清单，`{name, example, description}` |
| derived_formula | 衍生必填 | 衍生指标的计算公式 |
| cross_table | 跨表必填 | `{no_join: true, align_dimensions: [对齐维度]}` |

### 指标手册关键规则（必须全量遵守）

1. **分子分母只引用度量**：numerator/denominator 用 `{table, measure}` 形式引用表手册度量，禁止直接写字段名；引用的度量必须能在表手册 measures 中找到，找不到必须标「参考/未确认」。
2. **直接量指标**：只有 numerator，denominator 固定写 `"none"`。
3. **衍生指标**：type 填「衍生」，numerator/denominator 可引用其他指标的 `[METRIC-NNN]`，此时必须加一行 `derived_formula:` 写明公式；公式关键口径（如「去年同期累计 = 去年1月至去年同月」「Q1 = 1月+2月+3月」）必须写进 definition / derived_formula。
4. **跨表指标**：分子分母度量来自不同表时，必须加 `cross_table: {no_join: true, align_dimensions: [对齐维度]}`。
5. **SQL 防除零**：sql_template 中的除法恒用 `NULLIF(分母, 0)`，无一例外。
6. **LIKE 参数**：模糊匹配参数在模板里带 `%`（如 `LIKE '%{战区}%'`），传入的值里禁止再加 `%`，并在 parameters 的 description 中注明。
7. **exceptions 成体系**：不止「分母为 0」一条，必须覆盖本指标实际会遇到的异常情形——分母 0/NULL、维度空值（如负责人为空单列「未分配」）、期间未走完（分子分母截取相同天数）、口径切换等。
8. **率类非可加**：所有率/比指标必须在 notes 写明「换粒度时先聚合分子、分母再相除，禁止对率本身 SUM/AVG」。
9. **definition/when_to_use/notes 必须写进 YAML 正文**，禁止只放在文件之外的说明文字里。
10. **禁止混淆口径**：相近字段必须拆成不同指标（如「当月实销」与「近三月均销」），禁止混用。

### 完备性与证据约束（必须逐一对账）

- 每个指标口径必须有事实依据——业务方明确定义的公式、实际验证过的 SQL、或实际执行确认过的算法之一；**禁止仅凭表结构推断编造指标口径**。
- 业务方给出的**每一个**计算公式都必须落地为一个独立指标文件——含简单指标、率指标，也含同比、环比、累计同比、季度销量、季度同比等衍生公式，禁止遗漏或合并省略。
- 条件性指标（如季度数据仅 3/6/9/12 月展示）不因条件性而省略，展示条件写入 when_to_use / notes。
- 业务方校准过的 SQL 必须逐字符保留到 sql_template（含 `{参数}` 占位符），禁止改写、重排、优化或简化；与其他来源冲突时以业务方校准版为准。
- 阈值必须与业务方的判定标准一致，禁止自行调整。
- **完稿前自检两遍**：先逐条对照业务方公式清单，确认每条都有对应指标文件；再逐个对照指标文件，确认每个口径都能指认到事实依据。
