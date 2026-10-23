# 一致性审计清单

技能包全部文件完稿后，必须做一轮整体一致性审计再交付。审计的定位是交叉校验——逐条核对文件之间的引用闭合与格式合规，而不是重写内容。

审计分两步：先运行校验脚本做机器校验，再按本清单人工复核语义层项目。

## 第一步：运行校验脚本

```bash
python scripts/validate_skill.py <技能包目录路径>
```

脚本覆盖可机器判定的格式项（frontmatter 必填与枚举、objective 契约 schema、文件命名序号、尺寸预算、指标引用交叉闭合、report/ 目录结构、报告联动契约 R1/R2/R3/R4、web_report 模板包机器校验），任一 FAIL 必须先修复再继续。

技能包含网页报告模板包时，还需对每个 report/web_report/<theme>/ 单独执行：

```bash
# 逐项校验模板包（validate_skill.py 会自动调用，也可单独跑看完整 [pass]/[warn]/[fail] 清单）
python scripts/validate_template.py <模板包目录>

# 生成 preview.html，在浏览器中人工确认渲染效果与目标成品视觉一致、图表可渲染
python scripts/preview_template.py <模板包目录>
```

preview.html 是本地预览产物，确认后删除，不随技能包交付。

## 第二步：交叉校验清单（必须逐项过一遍）

1. **指标引用闭合**：
   - 全部 objectives 文件（frontmatter 的 gates[].metrics 与 metrics_deps[].id、正文的 `[METRIC-NNN]`）与 SKILL.md 前置依赖中出现的每个指标 ID，在 metrics/ 目录下都有同名文件；
   - metrics/ 中的每个指标，都至少被一处引用（SKILL.md 前置依赖或某个 objective）；
   - approach.md「指标计算总纲」速查表行数等于 metrics/ 指标文件数，无遗漏、无编造。
2. **表名引用闭合**：
   - objectives「用到的表」与 SKILL.md 前置依赖中出现的每个表名，在 tables/ 手册中都有对应文件；
   - 指标手册 numerator/denominator 引用的每个 `{table, measure}`，都能在对应表手册的 measures 中找到同名度量。
3. **SKILL.md frontmatter 合法**：frontmatter 为合法 YAML；name 只含字母、数字、连字符、下划线且与目录名一致；archetype/provenance/visibility 取值在合法值内；虚拟表定义（如有）字段齐全、source_table 与 source_sql 恰选其一。
4. **文件名序号连续**：objectives/ 下文件名匹配 `objective_{order}_{名称}.md`，order 从 1 起连续、无重复、与分析计划目标顺序一致。
5. **尺寸合规**（utf-8 字节数）：
   - SKILL.md 与 approach.md 合计小于 32768；
   - 单个 objective 文件小于 32768，objectives/ 全部文件合计小于 65536；
   - metrics/ 全部文件合计小于 49152。
6. **objective 契约与正文逐条复查**：
   - 每个 objective 文件以合法 YAML frontmatter 开头，contract_version/gates/metrics_deps 齐全；
   - gates 每条 id 唯一、type 取值合法（手写文件无 chart 条目）、check 为块标量、无「已输出/已生成/合理/适当」等模糊措辞；
   - 正文段落齐全、顺序固定（目标标题、分析思路、数值清单、用到的表、产出物、图表方法论），且**不含**「## 门禁」「## 依赖指标」节；
   - 数值清单表头三列固定；产出物写明固定文件名与列名，并与 frontmatter 的 deliverables 一致。
7. **口径一致性抽查**：抽取若干核心指标，核对 definition、sql_template、approach 总纲摘录、objective 数值清单四处口径互相一致；率类指标的非可加提示、NULLIF 防除零、LIKE 参数注意事项均在位。
8. **报告素材覆盖闭合**（有 report/ 目录时）：按 references/report-structure_zh.md 的 R1/R2/R3/R4 逐项人工复核——metrics.jsonl 每个 name 都能在某 objective 的指标引用或 gates[].ext.report_metrics 中找到归属；schema.json 每个非保留字段在 fill_guide.md 有来源标注；text_report.md 素材清单逐项标注来源且指向的门禁真实存在、门禁 check 确实覆盖该素材的规格；直读记号（直读：{文件名}）指向的文件在所标 objective 的 deliverables 中，且该 deliverable 四锁格式契约（文件名 / 列名 / 形状 / 格式）在 check 与 columns 声明中锁全。
9. **网页模板语义一致性审查**（有 web_report/ 时）：按 references/web-report-template_zh.md 第九节八项清单逐项核对——SQL 口径统一、同一数字物理同源、粒度与文案一致、schema 孤儿字段审计、sample_data 勾稽、渲染函数防御一致性、直读契约三方对照（fill_guide 源文件 ↔ schema orient/column_map ↔ deliverables 声明）、沉淀模板专属断言脚本。

## 审计结果落盘

审计过程与结论必须写入 `AUDIT.md`：逐项列出校验点、核对方法、结果（通过 / 发现问题及修复情况），并附校验脚本的运行结果。发现问题必须先修复、复核通过后再交付，禁止带病交付。

## 交付前实测

完成审计后，按你的运行环境把技能包加载实测一次（如放入运行时技能目录后用 /load-skill 加载），确认技能可被正常加载与浏览。
