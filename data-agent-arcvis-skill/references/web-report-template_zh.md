# 网页报告模板包（standalone）编写契约

本分册是网页报告模板产出的**唯一权威源**，覆盖两种产出形态：技能包内置模板（`report/web_report/<theme_id>/`，推荐与技能合并产出）与独立模板包（单独交付、控制台上传）。模板包是自包含的报告模板：`schema.json`（LLM 填充契约）+ `template.html`（自包含渲染器）+ `manifest.toml`（种类声明），运行期只做一次 LLM JSON 填充 + 字符串注入，零构建；`metrics.jsonl` 中的 SQL 为例外直出通道。

本分册**自包含**：模板包的全部契约都写在本文档中，结构校验逻辑由随附脚本 `scripts/validate_template.py` 承载（可执行契约）。模板包完稿后必须跑结构校验与语义一致性审查两道工序，两者都过才算完成。

## 一、两种产出形态与目录结构

| 形态 | 产出位置 | 数据源保障 | 交付方式 |
|---|---|---|---|
| **技能包内置（推荐）** | 技能包 `report/web_report/<theme_id>/` | objective 门禁锁定直读源文件的存在与格式（R4 联动，见第七节） | 随技能包整包交付 |
| 独立模板包 | `<theme_id>/` 单独成包 | 依赖会话恰好产出匹配的数据，或 metrics 直出 | 控制台上传（见第十一节） |

两种形态的模板包文件结构完全相同：

```
<theme_id>/                       # 目录名即主题 ID，取语义化英文名（如 sales_weekly）
  manifest.toml     # 必需：report_schema = "standalone"（权威判据）
  schema.json       # 必需：JSON Schema（draft 2020-12），LLM 填充契约
  template.html     # 必需：自包含 HTML，含唯一 __REPORT_DATA__ 注入点
  fill_guide.md     # 可选但强烈建议：逐字段填充指引，直接决定填充质量
  metrics.jsonl     # 可选：固定指标 SQL，工程直出（例外通道，见 6.4）
  sample_data.json  # 可选（本规范约定）：预览/回归用样例数据，运行期不读取
```

三个必需文件任一缺失或校验不过，平台加载时显式报错，**不会静默回退**。

用户只要报告模板、没有分析方法沉淀诉求时，仍推荐以「极简技能壳」形态内置产出（数据供给目标 + 模板包），做法见 references/report-structure_zh.md「反向产出：极简技能壳」节。

## 二、AI 引导式澄清（开工前必做）

用户通常只有一份"看起来满意"的 HTML，并不清楚模板化的数据机制。拿到成品 HTML 后，**先逐块梳理页面内容，再向用户确认以下问题**，答案直接决定第三节的 数据归属：

1. **哪些数字 / 明细表希望每次出报告时直接从分析产物搬运？**（合并产出的默认推荐路径：$read 直读，由技能包门禁保证源文件存在且格式稳定，见第四、五节）
2. **哪些数字 / 表格希望每次实时从数据库查出来？**
   - 以用户点名为准，没点名的不要主动拉去直出（见第三节判定原则）
   - 点名要直出的 → 追问：能否提供查询 SQL（或描述取数逻辑）？知道数据库名称吗？
   - 点名要直出但写不出 SQL 也没关系 → 改走 AI 分析填充（见第三节决策树）
3. **哪些文字希望 AI 根据每次的分析结果重新撰写？**（结论、建议、异常归因等）
4. **哪些内容是固定不变的？**（页头页脚、栏目标题、图例说明——原样保留在模板里）
5. **报告标题、时间范围是固定的还是每次变化的？**（生成时间用 `{{current_time}}` 占位符，运行期自动替换，不要硬编码）
6. **图表的数据来源同问题 1 / 2**：图表结构（类型、配色）保留，数据剥离。
7. **"列出所有满足 X 的记录"类全量明细表**：必须追问判定条件能否形式化（关键词、正则、字段条件）。能形式化 → 判定条件写入产出该明细的门禁、全量落盘后直读（或 SQL 条件直出）；让 LLM 从分析上下文"找出所有"必然遗漏，**全量明细禁止走 LLM 手填**。
8. **统计口径**：所有指标是否统一按同一时间截止点过滤？存量类指标（销量、保有量）是否有意不设时间过滤？答案直接决定取数与 SQL 的 WHERE 口径（见 9.1）。

问完形成一张"内容块 → 数据来源"清单，与用户确认后再开工。

表结构未知时的探查途径（按优先级）：请用户提供 DDL 或字段说明 → 从历史会话产物（如 notebook 中的 SQL 与 cell 输出）反查列名 → 都拿不到时该数据降级为 LLM 填充。

## 三、核心工作流：成品 HTML → 模板的「反渲染」

成品 HTML 里所有数据都是硬编码的。你的任务是把**视觉结构原样保留**，把**数据全部剥离**，改由运行期注入。逐块内容按下面的决策树判定数据归属（对用户解释时用大白话："这个数字每次出报告时从哪来？"）：

```
这块内容是什么？
├── 数字 / 明细行 / 大表，且技能包有 objective 产出该数据（合并产出的默认路径）
│     → $read 程序直读分析落盘产物（最可靠）
│       报告数字与分析产出物理同源，大表不经 LLM 复述；
│       源文件格式契约由 objective 门禁锁定（第四、五节）
├── 需要翻译 / 归类 / 判断 / 写文案 / 结合当次分析选重点（结论、建议、
│   归因线索…，含数据能直读但内容需灵活加工的）
│     → schema.json 声明字段，LLM 从分析上下文填充
│       fill_guide.md 必须写清楚该字段从哪来、怎么填、禁止什么
├── 可由上面两类数据确定性推导（求和、排序、占比、同比…）
│     → template.html 的 JS 里现算，不要让 LLM 填
└── 用户明确点名"这个数字/表每次要实时从库查"的固定口径取数，或
    全量明细的判定条件已形式化为 SQL 条件
      → metrics.jsonl SQL 直出（例外通道，见 6.4；独立于分析链路，慎用）
```

判定原则：

- **合并产出时数字优先直读**：技能包与模板一体设计时，报告中的数字 / 表格默认走 $read 直读 objective 落盘产物——同一份数据既被门禁验证又进报告，物理同源，永不对不上；只有用户点名实时查库或全量明细形式化直出时才用 metrics.jsonl；
- **只有明确要直出的才直出**：metrics 直出默认不启用，仅用于用户点名"这个数字/表每次要实时从库查"的固定口径取数；能写成 SQL ≠ 应该直出；
- **灵活内容不做直读直出**：需要选重点、归纳、组织叙述、结合当次分析上下文的内容，即使数据能直读也走 LLM 填充——直读直出会把灵活加工冻结成固定形状，反而降低报告价值；
- **能 JS 现算的绝不进 schema**——schema 字段越少，填充越稳；
- **全量明细表禁止走 LLM 手填**："列出所有满足 X 的记录"这类需求，判定条件形式化为 SQL 条件 / 正则（如 DTC 码用 `REGEXP '[PBCU][0-9][0-9A-Z]{3,5}'`），由门禁保全量落盘走直读，或 SQL 条件直出；这是完整性硬要求，即使用户没提也要引导形式化；
- LLM 字段必须是"分析上下文中真实存在的结果"，fill_guide 里写明禁止臆造；
- LLM 文案中与 KPI 卡/metric 同指标的核心计数，用占位符同源机制（见 6.5）；
- **统计口径先问清**（见第二节问题 8）。

### 具体改造步骤

1. **拆结构**：提取成品 HTML 的 `<style>` 与 `<body>` 骨架，原样保留视觉。第三方 JS 的引入方式按体积分档处理：
   - **体积有限的库（如 ECharts，约 1MB）强烈建议内联**——报告模板须在离线 / 内网等 CDN 不可达环境下自包含可用，外链失效时图表会整片降级为"数据暂不可用"；
   - **体积过大的库先确认是否必须使用**：确需使用时向用户同时提示两重风险（内联会显著膨胀模板体积 / 外链存在 CDN 不可达风险），由用户决策；用户选外链时采用稳定 CDN（如 `https://cdn.jsdelivr.net/npm/echarts@5.5.0/dist/echarts.min.js`）。
2. **剥数据**：删除所有硬编码数字/文案/静态 `setOption({...})` 脚本，记录每处数据的语义（这是 schema 字段名、直读源与 metric name 的来源）。
3. **接注入**：body 末尾放桥接脚本（占位符字面量全文只能出现这一次）：
   ```html
   <script>
   window.__RT_DATA__ = __REPORT_DATA__;
   </script>
   ```
4. **写消费 JS**：统一从别名读数，便于静态校验：
   ```javascript
   var D = window.__RT_DATA__ || {};
   var M = D.metrics || {};
   ```
   - 图表在 `window load` 后初始化（规避容器宽度 0 的时序竞争），并注册 resize；
   - 每个数据源缺失时降级为占位卡片（"数据暂不可用"），**页面不空白、不抛错**；
   - 每个渲染函数用 try/catch 包裹，单点失败不影响其他模块。
5. **写 schema.json**：只声明 LLM 填充字段；`properties` **不得出现 `metrics` 键**；大表 / 明细字段优先标记直读（见第四节）。
6. **写 fill_guide.md**：逐字段说明数据来源（分析上下文中的哪类结果）、格式示例、禁止事项，并标注来源 objective；直读字段追加直读记号与列对照（记号见第五节，联动见第七节）。
7. **做 sample_data.json**：把成品 HTML 里的硬编码值抄回来当样例（含手工 mock 的 `metrics` 键；直读字段按 resolve 后的平铺形态给样例），用于 preview 回归对比。
8. **两道审查**（见第八、九节）：结构校验 + 语义 review 全部 pass、浏览器确认视觉一致后才算完成。

## 四、$read 程序直读（模板侧契约）

大表 / 明细矩阵类字段，填充时 LLM 可以不抄数据，改输出一条**读取指令**，由程序从源文件直接搬运进 reportData：无抄错、无遗漏（1000 行内不截断），报告数字与源文件物理同源。

### 4.1 指令形状

LLM 在被标记的字段位置输出：

```json
{"$read": {"source": "file:/home/notebook/<sid>/达成率明细.csv", "orient": "records"}}
```

| 键 | 必需 | 说明 |
|---|---|---|
| `source` | 是 | `file:<完整路径>` 或 `cell:DATA-<name>`，必须出现在会话分析产物清单中 |
| `sheet` | 否 | Excel sheet 名，多 sheet 文件必填（指定 sheet 不存在时直接报错，不会兜底） |
| `orient` | 否 | 读取形态：`records` / `columns` / `matrix`，缺省用标记里的建议值 |

程序读取 → 按 orient 变换形状 → （可选）按 column_map 重命名列 → 注入该字段的 reportData 位置。未标记的字段输出 `$read` 会被直接拒绝。

### 4.2 schema 标记写法

在需要直读的 schema 节点（顶层字段、嵌套对象、数组 items 均可）声明 `x-direct-read-allowed`：

```json
{
  "type": "array",
  "items": {
    "type": "object",
    "properties": {
      "raw_text": {"type": "string"},
      "count": {"type": "number"}
    },
    "required": ["raw_text", "count"]
  },
  "x-direct-read-allowed": {
    "orient": "records",
    "column_map": {"原始故障描述": "raw_text", "索赔单数": "count"}
  }
}
```

- `orient`（可选）：该字段的建议读取形态，缺省 `records`；
- `column_map`（可选）：**源列名 → schema 字段名**的确定性重命名表，orient 变换后逐键应用；不配置时源列名必须与 schema 字段名逐字一致；
- 标记不影响该字段仍可手填——直读失败（源缺失 / 形状不符）时，按打回信息转手填，不得为凑直读改写或编造数据。

### 4.3 orient 三形态（输出形状必须与 schema 结构对齐）

| orient | 注入形状 | 适用 |
|---|---|---|
| `records` | 行对象数组：`[{列名: 值, ...}, ...]` | 逐行记录（默认形态） |
| `columns` | 列数组：`{列名: [值, ...], ...}` | 按列消费的成组数据 |
| `matrix` | `[{"name": 首列值, "counts": [其余列数值]}, ...]` | 行名 + 数值向量（每行一个对象，键名固定） |

形状不符时终验打回（附实际注入的数据形状摘要），按摘要修正 orient / source 或转手填。

### 4.4 源约束

- `file:` 源仅支持 `.csv` / `.xls` / `.xlsx`，且必须是会话**分析产物清单**中登记的文件——指令指向清单外文件直接报错；
- `cell:` 源仅支持 `DATA-` 开头的指标 cell；
- 行数上限 **1000**：超出截断取前 1000 行（截断事实记入日志）。需要全量展示的数据，落盘时保证 ≤ 1000 行（预聚合、分页或汇总），不要指望直读搬全量大表。

### 4.5 形状契约零弹性（重要）

`$read` 只做三件事：**读取、列名重命名、截断**。它没有透视、聚合、派生、类型转换能力：

- 源文件的形状（宽表 / 长表、行粒度、列序）就是契约本身。源文件从宽表改成长表，column_map 立刻断链，该字段只能退回手填；
- 类型注意：csv / excel 读取的类型推断可能吞掉前导零、把 `N/A` 当缺失——依赖这些脆弱类型的列（编码、单号），落盘时按文本写（前导零加引号或存文本格式）；
- 这就是第五节「直读源文件契约」存在的原因：技能包必须把直读源文件的格式锁死，直读的稳定性 = 源文件格式的稳定性。

### 4.6 大表优先直读

百行级大表超出 LLM 工具读取上限，手抄必截断——这类字段标记 `x-direct-read-allowed`，并在 fill_guide 写明预期源文件与列对照，让程序搬全量。

## 五、直读源文件契约（技能包侧）

合并产出场景下，`$read` 引用的源文件来自技能包 objective 的落盘产物（deliverables）。直读是否稳定，完全取决于源文件格式是否稳定。**被直读消费的 deliverable 必须满足四锁**（objective 侧写法见 references/objective-contract_zh.md，联动校验 R4 见第七节）：

1. **文件名锁**：`deliverables.file` 声明固定文件名（如 `达成率明细.csv`），禁止含日期、批次、序号等可变成分；
2. **列名锁**：`deliverables.columns` 声明完整列名清单（顺序一致）——列名即直读契约，改列名等于改契约，必须同步模板 column_map 与 fill_guide；
3. **形状锁**：门禁 `check` 写明行粒度（一行 = 某月某经销商）、表形态（宽表 / 长表）与行数预期（如"行数 = 产品线数 × 2 期，禁止截断抽样"）；
4. **格式锁**：`csv`（UTF-8、首行表头）或 `xlsx`（多 sheet 时在 check 写明 sheet 名）；行数控制在 1000 行内。

### fill_guide 直读记号

fill_guide 中每个直读字段的分节，在来源记号同一行追加直读声明：

```markdown
### classification.mapping（故障归类映射）

来源：objective_3/G1（直读：故障归类映射.csv）

schema 已标记可直读：优先输出 $read 指令由程序从源文件搬运。
列名对照（column_map）：原始故障描述 → raw_text、索赔单数 → count。
```

校验脚本按「直读：{文件名}」提取记号，核对文件在该 objective 的 `deliverables` 中（R4，见第七节）。英文技能包可写等价的英文记号：`Source: objective_N/G<id> (Direct read: {filename})`，校验脚本同样识别。

### 迭代纪律：改源文件格式 = 破坏直读契约

技能包迭代时改动被直读文件的文件名 / 列名 / 形态 / 格式，**必须同步模板侧**：column_map（列名变化）→ schema.json（结构变化）→ fill_guide 记号与列对照 → sample_data.json → 重跑第八、九节全量审查。只改一侧，另一侧断链后线上表现为该板块退化为手填（质量下降）或数据缺失——不报错，只会悄悄变差。

## 六、文件契约（自包含，以此为准）

### 6.1 manifest.toml

```toml
report_schema = "standalone"   # 种类声明（权威判据，必须且只能为 "standalone"）
schema_version = "1"           # schema.json 的私有版本，冻结语义
description = "模板描述"        # 建议填写；数据源待补齐等交付备注也写在这里
```

### 6.2 运行期注入的 data 结构

```json
{
  // —— 以下由 LLM 按 schema.json 填充（直读字段输出 $read 指令，程序 resolve 后注入）——
  "title": "...",
  "summary": "...",
  // —— 以下由工程注入，schema.json 不得声明 ——
  "metrics": {
    "metric_name_1": {
      "sql": "...",
      "columns": ["列1", "列2"],
      "data": [["值1", "值2"]],
      "data_preview": "...",
      "description": "..."
    }
  }
}
```

- 纯 BI 模板（无 LLM 填充）：schema `properties` 留空 `{}`，运行期跳过 LLM，template.html 只消费 `data.metrics`；
- 可选标记：`<jarvis-metric name="xxx" layout="table" title="标题">` 引用 metrics 数据由工程渲染；`{{current_time}}` 运行期替换为当前时间。

### 6.3 schema.json / fill_guide.md 编写要点

- schema.json 使用 JSON Schema draft 2020-12；
- 每个字段带 `description`，写清语义与格式（LLM 直接读 schema）；
- 数组元素用 `required` 锁全字段，避免半空对象；
- 判断类字段（如"是否对齐"）用 boolean + 一个说明文本字段，别让 LLM 输出"是/否"字符串；
- 大表 / 明细字段标记 `x-direct-read-allowed`（见第四节），标记节点的 items 结构即 resolve 后的形状契约；
- fill_guide 按字段分节，包含：数据来源（上下文哪类产物）、来源 objective 标注（直读字段另加直读记号，见第五节）、示例、禁止事项（禁止臆造、禁止遗漏、计数勾稽关系如 `before + after == count`）。

### 6.4 metrics.jsonl（例外通道）

metrics.jsonl 是**独立于分析链路的第二数据通道**：SQL 在报告期直接对库执行，不经过技能包的分析过程与门禁验证。因此它是例外通道，仅用于：用户点名"每次实时从库查"的固定口径取数，或全量明细判定条件已形式化为 SQL 的场景；默认路径是 $read 直读分析产物（数字同源）或 LLM 填充。

使用时接受两条固有限制，编写要点围绕它们展开：

1. **口径漂移风险**：模板 SQL 的 WHERE 口径与分析产出口径不一致时，报告数字与分析结论对不上（逐条对照要求见 9.1）；
2. **占位符静默空数据**：`{参数名}` 占位符按用户输入 `{参数名:值}` 的键名精确替换——键名不一致、或用户未按该格式输入时，占位符按字面量执行，查询 success 但 0 行，**不报错**。参数名要与引导用户输入的键名逐字一致，并在 manifest description / fill_guide 写明必传参数与日期格式。

每行一个 JSON 对象：

```json
{"name": "total_sales", "sql": "SELECT COUNT(*) AS 总销量 FROM ...", "description": "总销量", "db_id": "12345678"}
```

| 字段 | 必需 | 说明 |
|------|------|------|
| `name` | 是 | 唯一标识，对应 JS 里 `M.<name>` 和 `<jarvis-metric name>` |
| `sql` | 是 | 查询语句，支持 `{参数名}` 占位符（执行期从用户提问解析替换） |
| `description` | 否 | 描述（存量类指标有意不设时间过滤时在此注明口径） |
| `db_id` / `db_name` | 否 | 指定数据源；为空取会话的第一个库数据源 |

- 列别名即 JS 消费契约，起中文别名后不要再改；
- SQL 中会话专属参数（编码、日期等）尽量**烘焙**进去；确需动态参数时用 `{参数名}` 占位符，并遵守上面的占位符纪律；
- **提供不了 db_id/db_name 时**：SQL 和 name 照常写，db 字段留空（运行期默认取会话数据源），并在 manifest `description` 标注"metrics 依赖会话绑定的数据源，请在对应数据源的会话中使用"；
- 完全写不出 SQL 的数字：降级为 schema 字段走 LLM 填充，不要硬造 SQL；
- 技能包内置模板中每个 metric 还须满足 R1 联动（见第七节）。

### 6.5 LLM 文案核心计数的占位符同源机制（强烈建议）

fill_guide 里写"该数字必须以某 metric 为准"只是 prompt 软约束，LLM 仍可能抄错或另行统计，导致文案与 KPI 卡对不上。对与 metric 同指标的核心计数，升级为模板渲染期替换的**硬约束**：

- schema / fill_guide 约定：LLM 文案中核心计数写 `{metric_name}` 占位符，百分比写 `{pct:metric_name}`；derived 值（同比环比、组内占比等需要 LLM 计算判断的数字）仍由 LLM 自行填写；
- template.html 提供 fillVars()，渲染期读 metric 首行首列替换，物理同源：

```javascript
function fillVars(text) {
  return (text || "").replace(/\{(pct:)?(\w+)\}/g, function(_, isPct, name) {
    var v = scalar(name);              // metric 首行首列
    if (v === null) return "—";        // metric 缺失时兜底
    return isPct ? pct(v) : num(v);
  });
}
// 所有渲染 LLM 文本的函数统一套用，勿遗漏：
// setList(id, (items || []).map(fillVars))
```

- 适用范围：单值型 metric（COUNT/SUM/比率）；多行 metric 的明细数字不适用（应改由表格/图表直出或直读）；
- fill_guide 中给出带占位符的完整示例句，LLM 会模仿示例格式。

## 七、与分析阶段门禁的联动（R1/R2/R4）

模板包不是孤立产物：其中每个 metric、每个 LLM 填充字段、每个直读源文件，都必须能溯源到某个 objective 的门禁产出。形式化定义与反推工作方法见 references/report-structure_zh.md，此处只列写法要点。

### 7.1 metrics.jsonl 的 name 联动（对齐 R1）

metrics.jsonl 每个 metric 的 `name` 必须被覆盖，两种写法任选其一：

- **name 直接取 `METRIC-NNN`**：该指标本就在指标手册中，且某 objective 的 `metrics_deps` 或 `gates[].metrics` 已引用它——适合口径已入册的核心指标；
- **name 自由命名 + 门禁声明 `ext.report_metrics`**：在负责产出该数据的门禁上声明：

```yaml
gates:
  - id: G1
    type: table
    title: 达成率数值表
    check: |-
      输出包含 12 个月份行、3 个固定列（月份 / 报告数值 / 取数口径）。
    ext:
      report_metrics: [monthly_achievement_trend]
```

两者皆无时校验判 FAIL。metric 的 SQL 口径必须与归属门禁产出物的口径一致——门禁是口径的验证来源。

### 7.2 fill_guide.md 的来源标注（对齐 R2）

fill_guide 每个字段的分节内，必须标注该字段内容来自哪个 objective 的门禁产出，统一记号 `来源：objective_N/G<id>`：

```markdown
### summary（一句话总结）

来源：objective_1/G2

从达标判定结论中提炼一句话总结，必须引用具体达成率数值。
禁止臆造结论中不存在的判断。
```

字段无来源标注判 WARN；标注指向不存在的 objective/gate 判 FAIL。直读字段在同一行追加直读记号（见 7.3）。

### 7.3 直读源文件联动（对齐 R4）

fill_guide 中每条「直读：{文件名}」记号：

- 必须与来源记号同一行，形如 `来源：objective_N/G<id>（直读：{文件名}）`；
- 文件名必须出现在所标 objective 的 `deliverables[].file` 中——否则 FAIL；
- 该 deliverable 应声明 `columns` 列名清单（直读源文件无列名契约时 WARN）。

schema 含 `x-direct-read-allowed` 标记而 fill_guide 通篇无任何直读记号时 WARN——建议逐字段写明预期源文件与列对照，直读路径才有设计期保障。

## 八、结构校验与预览

校验分两档，**优先走强校验**；仅当用户环境确实跑不了脚本时才降级弱校验。

### 8.1 强校验（首选）：脚本机器校验

随附两个 stdlib-only 脚本（jsonschema 可选增强），模板目录可以是任意路径：

```bash
# 逐项校验模板包，输出 [pass]/[warn]/[fail] 清单；存在 fail 时退出码非 0
python scripts/validate_template.py <模板包目录>

# 将 sample_data.json 注入 template.html，生成 preview.html（复刻运行期注入语义）
python scripts/preview_template.py <模板包目录>
```

校验器覆盖：manifest 必填、schema 合法性与保留键、注入点唯一、`__RT_DATA__` 桥接、JS 字段与 schema 一致性静态检查、`x-direct-read-allowed` 标记合法性、jarvis-metric 交叉核对、metrics.jsonl 行合法性、fill_guide 字段覆盖度、sample_data 与 schema 的符合性（若本机装有 jsonschema）。Python < 3.11 时 manifest 校验自动降级为正则解析（会有 warn 提示）。

技能包整体校验（validate_skill.py）会对 report/web_report/ 下每个主题自动调用 validate_template.py，并额外校验第七节的联动契约（R1/R2/R4）。

**完成定义（DoD）**：

1. 强校验：`validate_template.py` 无 `[fail]`（`[warn]` 需逐条人工确认可接受）；弱校验：清单逐项核对通过 **且已向用户输出风险提醒**；
2. 第九节语义一致性 review 清单逐项核对通过；
3. preview.html 在浏览器中与目标成品视觉一致、图表可渲染（主动打开或引导用户打开 preview.html 确认）；
4. sample_data.json 不含脏数据：数值来自成品或明确标注 mock；
5. preview.html 是本地预览产物，不放进模板包交付内容。

### 8.2 弱校验（兜底）：AI 逐项人工核对

用户环境无 python3 或无法执行脚本时，由 AI **通读模板包文件**，逐项核对以下清单并逐条向用户汇报结论：

- [ ] manifest.toml 含 `report_schema = "standalone"` 与 `schema_version`
- [ ] schema.json 是合法 JSON，`properties` 不含 `"metrics"` 键
- [ ] template.html 中注入占位符字面量**恰好出现 1 次**（含注释），且形如 `window.__RT_DATA__ = <占位符>;` 处于 JS 表达式位置
- [ ] JS 读取的每个 `data.字段` 都在 schema properties 声明（或为 `metrics`）
- [ ] `x-direct-read-allowed` 标记的 orient 取值合法（records/columns/matrix）、column_map 为字符串映射
- [ ] `<jarvis-metric name>` 均在 metrics.jsonl 有定义；metrics.jsonl 每行合法且 name 唯一
- [ ] fill_guide.md 覆盖全部 schema 字段
- [ ] sample_data.json 结构与 schema 一致（肉眼比对）

**走弱校验时，必须向用户明确输出如下风险提醒（不得省略、不得弱化措辞）：**

> ⚠️ 本次未执行脚本机器校验，仅由 AI 人工核对。人工核对可能遗漏问题（尤其是占位符计数、JS 字段一致性这类逐字符检查），模板上传后仍可能出现**加载被拒绝、页面空白或图表不渲染**的情况。强烈建议在任一装有 Python 3 的机器上补跑 `validate_template.py` 后再上传；若坚持直接上传，请在生成首份报告后立即人工核验效果。

预览同理：无法运行 `preview_template.py` 时，可由 AI 手工把 sample_data.json 序列化为 JSON 字符串替换占位符生成 preview.html，并提醒用户该预览未复刻运行期转义语义（`</` 转义等），结果仅供视觉参考。

### 硬约束（平台加载时机器校验，违反即模板不可用）

| # | 约束 |
|---|------|
| 1 | manifest.toml 可解析且 `report_schema = "standalone"` |
| 2 | schema.json 是合法 JSON，`properties` 不含保留键 `"metrics"` |
| 3 | template.html 中 `__REPORT_DATA__` 字面量**恰好 1 次**（按字符串计数，注释里出现也算），且处于 JS 表达式位置 |

### 软约束（加载不查，靠校验器和自觉，违反则运行期空白/报错）

| # | 约束 | 违反后果 |
|---|------|----------|
| 1 | JS 读取的 `data.字段` 必须在 schema properties 声明（或为 `metrics`） | 渲染时 undefined |
| 2 | `<jarvis-metric name>` 引用需在 metrics.jsonl 定义 | 显示占位卡片 |
| 3 | metrics.jsonl 每行合法 JSON 且 name 唯一 | 非法行跳过、重名保留首个 |
| 4 | fill_guide.md 覆盖全部 schema 字段 | 填充质量不可控 |
| 5 | `$read` 只出现在标记了 `x-direct-read-allowed` 的字段，source 指向产物清单内对象 | 该字段被拒、转手填 |

### 常见坑

- **注释里写了 `__REPORT_DATA__`**：加载按字符串计数直接拒绝。文档说明一律用别称（如「注入占位符」）。
- **图表初始化过早**：容器还没布局完成宽度为 0，图表画不出来。统一 `window load` 后初始化。
- **metric 列名当契约**：JS 里按中文列名取数（如 `o["索赔数"]`），改 SQL 别名会静默断数。改 SQL 时同步排查 JS。**直读同理**：改源文件列名必须同步 column_map 与 fill_guide 列对照（见第五节迭代纪律）。
- **直读源文件形状漂移**：技能包侧把直读源从宽表改长表（或列改名、行粒度变化），column_map 断链后该字段静默退化为手填，模板侧不报错。改源文件格式必须走第五节迭代纪律。
- **LLM 字段吃掉整段 HTML**：schema 字段值只应是文本/结构化数据，允许 `<strong>` 级轻量强调，禁止让 LLM 输出整段 HTML 结构。
- **`{{current_time}}`**：运行期替换为当前时间，模板里不要硬编码生成时间。
- **注入转义**：系统自动做 `</` → `<\/`、U+2028/U+2029 转义，模板作者无需处理；数据里含 `</script>` 字样是安全的这一点依赖桥接写法处于 JS 表达式位置。

## 九、语义一致性与有效性审查（后处理必做）

结构校验器只能查「能不能加载」，查不出「数字对不上、口径打架」这类语义问题。模板包结构就绪后（含每次迭代改版后），必须通读全部文件做一轮交叉 review，逐项核对以下清单。以下每条都来自真实翻车案例。

### 9.1 SQL 口径统一

- 同一事实表的所有 metric，WHERE 过滤口径必须一致（尤其时间截止过滤）。典型翻车：分布类 metric 带 `date<='{截止日}'` 而总数 metric 不带，库里一旦有晚于截止日的数据，KPI 卡与分布图合计立刻对不上。**逐条列出各 metric 的 WHERE 子句对照检查**；
- metrics 直出是独立于分析链路的第二数据通道，还需与技能包分析产出的口径对照——同一指标既走 metric 又出现在分析结论中时，两边 WHERE 必须一致；
- 存量类指标（销量、保有量）不带时间过滤可能是有意为之，与用户确认后在 metric description 注明口径即可；
- CASE WHEN 逐分支检查死分支（前面的条件已拦截、后面分支永不命中）。

### 9.2 同一数字多处出现必须物理同源

- 同一指标出现在 KPI 卡、表格、LLM 文案多处时，禁止各处独立取数；
- LLM 文案中的核心计数优先用 6.5 的占位符机制，把 prompt 软约束升级为渲染期替换的物理保证；
- 图表与表格展示同一维度时必须**同源同序**（消费同一数组、同一排序），禁止图表读 metric、表格读 LLM 字段这类双轨；
- 直读字段本身即物理同源（数据由程序一次注入）；同一数字一个走直读、一个走 metric 才是双轨，禁止。

### 9.3 粒度与文案一致

- 图表横轴粒度 = 表格行粒度 = 表头/标题文案。改数据粒度（如"失效大类"改"失效模式"）后，**全文 grep 旧粒度名词**：小节标题、表头、空态文案、manifest description 一并改，一处残留就是一处误导。

### 9.4 schema 孤儿字段审计

- 逐字段确认在 template.html 有消费者。结构迭代后失去消费者的字段（模板不再渲染但 schema 仍 required），要么降级为可选并在 description 注明"模板不渲染，仅供 LLM 推理"，要么删除并同步 sample/fill_guide。

### 9.5 sample_data 勾稽

sample 是 LLM 的模仿对象，sample 里的错误会被逐次生成放大：

- **内部勾稽**：分项加总 = 总数；跨 metric 相互印证（分布明细各段合计 = 汇总 metric 的对应值）；
- **文案与数据一致**：sample 文案里引用的数字必须与 sample metrics 的数值一致，禁止文案写 18651 而 metric 是 9860；
- **覆盖 schema 全部契约**：schema description 要求"必须覆盖五类内容"，sample 就必须给全五类——sample 缺一类，LLM 就会缺一类；
- **直读字段按 resolve 后形态给样例**：直读字段在 sample_data 中给平铺后的行对象数组（与 orient 产出一致），并填 guide 中给出同一份样例，避免 LLM 以为要手抄全表。

### 9.6 渲染函数防御一致性

- 同类渲染函数的兜底逻辑必须一致：都有空数据 fallback、超长文本都截断（如统一 100 字符 + "..."）。一个表截断另一个不截，长文本场景就破版。

### 9.7 直读契约对照（有直读标记时）

- 逐个直读字段三方对照：fill_guide 声明的源文件 ↔ schema 标记的 orient / column_map ↔ objective 门禁的 deliverables 声明——源列名集合与 column_map 键集（及同名直连字段）逐字对照，行粒度与形态（宽表 / 长表）和 orient 产出的结构一致；
- 行数勾稽：门禁的行数预期 vs 模板展示需求（如报告要 12 个月，源文件必须正好 12 行），且 ≤ 1000 行；
- 类型脆弱点：前导零、`N/A`、千分位逗号数字——确认源文件落盘格式不会触发读取时的类型误判。

### 9.8 沉淀模板专属断言脚本（建议）

- 通用校验器管结构；模板专属的语义断言（口径过滤断言、勾稽等式、死分支断言、旧名词零残留断言、直读列名契约断言）写成模板目录外的 ad-hoc python 脚本，每次改版复用防回归。

## 十、迭代改版流程（模板已存在时）

对已交付模板提改版需求时按以下步骤，**禁止只改一处**——一个需求常横跨 template/schema/fill_guide/metrics/sample 五处：

1. **通读现状**：完整读全部文件，弄清改动波及面后再动手；
2. **实施改动**：新增数据先过第三节决策树判定归属；改粒度/删字段时按 9.3 / 9.4 排查连带改动；新增 metric 或 schema 字段时同步补第七节的联动声明与来源标注；改直读源文件格式时按第五节迭代纪律同步模板侧；
3. **版本同步三件套**（最容易落下，每次必查）：
   - manifest.toml：schema 有变则 `schema_version` 递增；文件头追加 `# vN（schema_version X）：变更摘要` 注释；description 与现状一致（删掉的模块别再出现在描述里）；
   - fill_guide.md：首行版本号与 manifest 同步；
   - sample_data.json：新增 metric 补样例，且与既有样例勾稽（9.5）；
4. **重跑完整审查**：结构校验 + 预览 + 第九节 review 全量重做，不能只验证本次改动点——上次的一致性可能被这次改动打破；
5. **数据侧问题透明化**：模板层无法修复的问题（如维表缺映射导致展示代码值），记入 manifest 备注并明确告知用户，不要静默吞掉。

## 十一、交付

**独立模板包**通过校验和预览确认后，引导用户自行上传：

1. 打开 **DataAgent 控制台**，点击任意历史会话
2. 点击右侧栏目的 **"应用"**
3. 点击 **"自定义网页"**，再点击 **"上传主题"** 图标
4. 按照页面引导操作，上传制作好的模板包

注意事项：

- 模板仅支持平台侧固化后使用，**不支持会话内直传模板**
- **仅做过弱校验（未跑脚本）的模板包，上传前必须再次向用户重申第 8.2 节的风险提醒**，由用户确认后再引导上传
- `preview.html` 是本地预览产物，**不推荐**包含在上传内容里；即使一并上传，平台加载时也会忽略它，不会导致加载失败

**技能包内置模板**随技能包整包交付（/load-skill 加载使用），模板与门禁的联动契约（R1/R2/R4）在技能包校验时一并核验；交付前审计对报告部分的要求见 references/quality-checklist_zh.md。
