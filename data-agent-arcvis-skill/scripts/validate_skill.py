#!/usr/bin/env python3
"""quest 技能包格式校验脚本（随技能包交付，零平台依赖）。

只依赖 Python 标准库与 pyyaml。用法：

    python validate_skill.py <技能包目录路径>

校验项：
1. SKILL.md frontmatter 必填字段与枚举取值；
2. objectives/ 文件命名（objective_{order}_{名称}.md，order 从 1 起连续无重复）；
3. 每个 objective 的 frontmatter 契约（宽容模式：合法 YAML / contract_version / gates 非空等为硬校验，
   逐条 gate 的字段问题降为 WARN——平台按宽容透传处理，个别条目不规范不丢门禁；
   核心五字段 id/type/title/check/severity 之外的键同样 WARN 提醒——工程不解析，仅原样透传给大模型）；
4. 文件尺寸预算（utf-8 字节数）；
5. 指标引用交叉校验（gates[].metrics 与 metrics_deps[].id 在 metrics/ 目录存在）；
6. report/ 目录结构（存在时 text_report.md / web_report/ 至少其一，主题包三件齐全）；
7. 报告联动契约 R1/R2/R3/R4（metrics.jsonl 覆盖、fill_guide.md 与 text_report.md 来源记号闭合、
   直读源文件闭合到 deliverables；metrics.jsonl 存在时输出例外通道提醒 WARN）；
8. web_report/ 主题模板包逐个调用 validate_template.py 机器校验；
9. objective 依赖 DAG（frontmatter 顶层 depends_on：整数列表/order 存在/无自引用/无环，
   违规一律 WARN 不 FAIL——字段可选，存量包无此字段不受影响）。

逐项输出 PASS/FAIL，任一 FAIL 时退出码非 0。
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

try:
    import yaml
except ImportError:  # pragma: no cover
    print("缺少 pyyaml 依赖，请先安装：pip install pyyaml", file=sys.stderr)
    sys.exit(2)

NAME_RE = re.compile(r"^[a-zA-Z0-9_-]+$")
GATE_ID_RE = re.compile(r"^G\d+$")
METRIC_ID_RE = re.compile(r"^METRIC-\d{3}$")
OBJECTIVE_FILE_RE = re.compile(r"^objective_(\d+)_.+\.md$")
# 报告素材来源记号：来源：objective_N/G<id>
SOURCE_MARK_RE = re.compile(r"objective_(\d+)\s*/\s*(G\d+)")
# 直读源文件记号：直读：{文件名}（与来源记号同一行，形如 来源：objective_N/G<id>（直读：xx.csv））；
# 英文包记号：Direct read: {filename}。文件名要求带扩展名（file: 源仅支持 csv/xls/xlsx），
# 避免把行文「已标记可直读：优先输出…」误判为记号
DIRECT_READ_FILE_RE = re.compile(r"(?:直读|direct[ _-]read)[：:]\s*([^\s）)，,]+\.[A-Za-z0-9]+)", re.IGNORECASE)
# schema 直读标记键（x-direct-read-allowed），用于标记/记号匹配性 WARN
DIRECT_READ_MARKER = "x-direct-read-allowed"

ARCHETYPES = {"quest", "inquiry"}
PROVENANCES = {"distilled", "official"}
VISIBILITIES = {"augment", "virtual_only"}
GATE_TYPES = {"table", "chart", "conclusion", "constraint"}

SIZE_CORE_DOCS = 32768  # SKILL.md + approach.md 合计
SIZE_SINGLE_OBJECTIVE = 32768
SIZE_OBJECTIVES_TOTAL = 65536
SIZE_METRICS_TOTAL = 49152

# gate 条目标准写法只用核心五字段；其他键平台不拒收、整块透传给大模型，但工程不解析，出现时 WARN 提醒
GATE_CORE_KEYS = {"id", "type", "title", "check", "severity"}
CONTRACT_KNOWN_KEYS = {"contract_version", "gates", "metrics_deps", "deliverables", "depends_on"}

WEB_REPORT_REQUIRED_FILES = ("manifest.toml", "schema.json", "template.html")
SCHEMA_RESERVED_KEYS = {"metrics"}


@dataclass
class Report:
    results: list[tuple[bool, str, str]] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)

    def check(self, name: str, ok: bool, detail: str = "") -> None:
        self.results.append((ok, name, detail))

    def warn(self, message: str) -> None:
        self.warnings.append(message)

    @property
    def failed(self) -> int:
        return sum(1 for ok, _, _ in self.results if not ok)


@dataclass
class ObjectiveData:
    """单个 objective 的联动校验素材（按文件名 order 索引）。"""

    gate_ids: set[str] = field(default_factory=set)
    report_metrics: set[str] = field(default_factory=set)  # 来自 gates[].ext.report_metrics
    metric_refs: set[str] = field(default_factory=set)
    depends_on: list[int] = field(default_factory=list)  # frontmatter 顶层依赖声明（仅收合法整数项）
    deliverables: dict[str, bool] = field(default_factory=dict)  # 落盘文件名 → 是否声明 columns


def parse_frontmatter(text: str) -> tuple[dict | None, str | None]:
    """解析文件头部 frontmatter，返回 (数据, 错误信息)。无 frontmatter 返回 (None, None)。"""
    if not text.startswith("---\n") and not text.startswith("---\r\n"):
        return None, None
    lines = text.splitlines()
    for idx in range(1, len(lines)):
        if lines[idx].strip() == "---":
            block = "\n".join(lines[1:idx])
            try:
                data = yaml.safe_load(block)
            except yaml.YAMLError as exc:
                return None, f"YAML 解析失败：{exc}"
            if not isinstance(data, dict):
                return None, "frontmatter 内容不是键值映射"
            return data, None
    return None, "frontmatter 起始 --- 未找到成对的闭合 --- 行"


def check_skill_md(skill_dir: Path, report: Report) -> None:
    skill_md = skill_dir / "SKILL.md"
    if not skill_md.is_file():
        report.check("SKILL.md 存在", False, "文件不存在")
        return
    report.check("SKILL.md 存在", True)

    data, err = parse_frontmatter(skill_md.read_text(encoding="utf-8"))
    if data is None:
        report.check("SKILL.md frontmatter 合法", False, err or "文件未以 --- 开头")
        return
    report.check("SKILL.md frontmatter 合法", True)

    problems: list[str] = []
    for key in ("name", "description", "archetype", "provenance"):
        value = data.get(key)
        if not isinstance(value, str) or not value.strip():
            problems.append(f"必填字段 {key} 缺失或为空")
    name = data.get("name")
    if isinstance(name, str) and name.strip():
        if not NAME_RE.match(name):
            problems.append(f"name 不符合 ^[a-zA-Z0-9_-]+$：{name!r}")
        elif name != skill_dir.resolve().name:
            problems.append(f"name（{name}）与技能包目录名（{skill_dir.resolve().name}）不一致")
    if data.get("archetype") not in ARCHETYPES and "archetype" in data:
        problems.append(f"archetype 取值非法：{data.get('archetype')!r}（合法：{sorted(ARCHETYPES)}）")
    if data.get("provenance") not in PROVENANCES and "provenance" in data:
        problems.append(f"provenance 取值非法：{data.get('provenance')!r}（合法：{sorted(PROVENANCES)}）")
    if "visibility" in data and data.get("visibility") not in VISIBILITIES:
        problems.append(f"visibility 取值非法：{data.get('visibility')!r}（合法：{sorted(VISIBILITIES)}）")
    report.check("SKILL.md frontmatter 必填字段与枚举", not problems, "；".join(problems))


def list_objective_files(skill_dir: Path) -> list[Path]:
    objectives_dir = skill_dir / "objectives"
    if not objectives_dir.is_dir():
        return []
    return sorted(p for p in objectives_dir.iterdir() if p.suffix == ".md" and p.is_file())


def check_objective_naming(skill_dir: Path, report: Report) -> None:
    files = list_objective_files(skill_dir)
    if not (skill_dir / "objectives").is_dir():
        report.check("objectives 文件命名与序号连续", True, "无 objectives/ 目录，跳过")
        return
    problems: list[str] = []
    orders: list[int] = []
    for path in files:
        match = OBJECTIVE_FILE_RE.match(path.name)
        if not match:
            problems.append(f"{path.name} 不符合 objective_{{order}}_{{名称}}.md")
            continue
        orders.append(int(match.group(1)))
    if len(set(orders)) != len(orders):
        problems.append(f"order 序号重复：{sorted(orders)}")
    elif orders and sorted(orders) != list(range(1, len(orders) + 1)):
        problems.append(f"order 序号不从 1 起连续：{sorted(orders)}")
    if not files:
        problems.append("objectives/ 目录存在但无 .md 文件")
    report.check("objectives 文件命名与序号连续", not problems, "；".join(problems))


def validate_gate(gate: object, index: int, seen_ids: set[str]) -> list[str]:
    """逐条 gate 的建议级检查：返回的问题一律降为 WARN（平台宽容透传，个别条目不规范不丢门禁）。"""
    prefix = f"gates[{index}]"
    if not isinstance(gate, dict):
        return [f"{prefix} 不是键值映射"]
    problems: list[str] = []
    gate_id = gate.get("id")
    if not isinstance(gate_id, str) or not GATE_ID_RE.match(gate_id):
        problems.append(f"{prefix}.id 缺失或不符合 ^G\\d+$：{gate_id!r}")
    elif gate_id in seen_ids:
        problems.append(f"{prefix}.id 重复：{gate_id}")
    else:
        seen_ids.add(gate_id)
    if gate.get("type") not in GATE_TYPES:
        problems.append(
            f"{prefix}.type 缺失或取值非法：{gate.get('type')!r}（建议：{sorted(GATE_TYPES)}；"
            f"该条将落入「其他门禁」分区，不参与图表装配等按 type 过滤的机制）"
        )
    if not isinstance(gate.get("title"), str) or not gate["title"].strip():
        problems.append(f"{prefix}.title 缺失或为空")
    if not isinstance(gate.get("check"), str) or not gate["check"].strip():
        problems.append(f"{prefix}.check 缺失或为空")
    if "verify" in gate and not isinstance(gate.get("verify"), dict):
        problems.append(f"{prefix}.verify 不是键值映射")
    if "metrics" in gate:
        metrics = gate.get("metrics")
        if not isinstance(metrics, list):
            problems.append(f"{prefix}.metrics 不是列表")
        else:
            for m in metrics:
                if not isinstance(m, str) or not METRIC_ID_RE.match(m):
                    problems.append(f"{prefix}.metrics 含非法指标 ID：{m!r}")
    if "when" in gate and not isinstance(gate.get("when"), str):
        problems.append(f"{prefix}.when 不是字符串")
    if "ext" in gate and not isinstance(gate.get("ext"), dict):
        problems.append(f"{prefix}.ext 不是键值映射")
    for key in sorted(set(gate) - GATE_CORE_KEYS):
        problems.append(
            f"{prefix} 字段 {key} 不被工程解析，仅原样透传给大模型；格式类要求建议并入 check"
        )
    return problems


def validate_contract(data: dict, report: Report, label: str) -> list[str]:
    problems: list[str] = []
    if "contract_version" not in data:
        problems.append("缺少 contract_version（平台识别门禁契约块的硬前提）")
    elif data.get("contract_version") != 1:
        report.warn(f"{label}：contract_version 建议填 1，实际：{data.get('contract_version')!r}")

    gates = data.get("gates")
    if not isinstance(gates, list) or not gates:
        problems.append("gates 缺失或为空列表")
    else:
        # 逐条 gate 的字段问题降为 WARN：平台宽容透传，不因个别条目不规范丢门禁
        seen_ids: set[str] = set()
        for idx, gate in enumerate(gates):
            for message in validate_gate(gate, idx, seen_ids):
                report.warn(f"{label}：{message}")

    deps = data.get("metrics_deps")
    if not isinstance(deps, list):
        problems.append("metrics_deps 缺失或不是列表（无依赖时填空列表 []）")
    else:
        for idx, dep in enumerate(deps):
            if not isinstance(dep, dict):
                problems.append(f"metrics_deps[{idx}] 不是键值映射")
                continue
            dep_id = dep.get("id")
            if not isinstance(dep_id, str) or not METRIC_ID_RE.match(dep_id):
                problems.append(f"metrics_deps[{idx}].id 缺失或不符合 ^METRIC-\\d{{3}}$：{dep_id!r}")
            if not isinstance(dep.get("usage"), str) or not dep["usage"].strip():
                problems.append(f"metrics_deps[{idx}].usage 缺失或为空")

    if "deliverables" in data:
        deliverables = data.get("deliverables")
        if not isinstance(deliverables, list):
            problems.append("deliverables 不是列表")
        else:
            for idx, item in enumerate(deliverables):
                if not isinstance(item, dict) or not isinstance(item.get("file"), str) or not item["file"].strip():
                    problems.append(f"deliverables[{idx}].file 缺失或为空")

    # depends_on 可选；形态问题降为 WARN（平台宽容归一，非法项丢弃不拒收）
    if "depends_on" in data and data.get("depends_on") is not None:
        depends_on = data.get("depends_on")
        if not isinstance(depends_on, list) or any(
            isinstance(item, bool) or not isinstance(item, int) for item in depends_on
        ):
            report.warn(
                f"{label}：depends_on 不是整数 order 列表：{depends_on!r}"
                f"（如 depends_on: [1]；平台宽容归一，非法项丢弃）"
            )

    unknown = set(data) - CONTRACT_KNOWN_KEYS
    if unknown:
        report.warn(f"{label}：frontmatter 含未知顶层字段 {sorted(unknown)}（不判 FAIL，建议清理）")
    return problems


def collect_metric_refs(data: dict) -> set[str]:
    refs: set[str] = set()
    gates = data.get("gates")
    if isinstance(gates, list):
        for gate in gates:
            if isinstance(gate, dict) and isinstance(gate.get("metrics"), list):
                # 只收集合法格式的引用：非 METRIC-NNN 格式已由 WARN 提示，不进交叉校验判 FAIL
                refs.update(m for m in gate["metrics"] if isinstance(m, str) and METRIC_ID_RE.match(m))
    deps = data.get("metrics_deps")
    if isinstance(deps, list):
        for dep in deps:
            if isinstance(dep, dict) and isinstance(dep.get("id"), str):
                refs.add(dep["id"])
    return refs


def collect_objective_data(data: dict) -> ObjectiveData:
    obj = ObjectiveData(metric_refs=collect_metric_refs(data))
    gates = data.get("gates")
    if isinstance(gates, list):
        for gate in gates:
            if not isinstance(gate, dict):
                continue
            if isinstance(gate.get("id"), str):
                obj.gate_ids.add(gate["id"])
            ext = gate.get("ext")
            if isinstance(ext, dict) and isinstance(ext.get("report_metrics"), list):
                obj.report_metrics.update(m for m in ext["report_metrics"] if isinstance(m, str))
    depends_on = data.get("depends_on")
    if isinstance(depends_on, list):
        obj.depends_on = [d for d in depends_on if isinstance(d, int) and not isinstance(d, bool)]
    deliverables = data.get("deliverables")
    if isinstance(deliverables, list):
        for item in deliverables:
            if isinstance(item, dict) and isinstance(item.get("file"), str) and item["file"].strip():
                columns = item.get("columns")
                obj.deliverables[item["file"].strip()] = isinstance(columns, list) and bool(columns)
    return obj


def check_objective_contracts(skill_dir: Path, report: Report) -> tuple[set[str], dict[int, ObjectiveData]]:
    """逐 objective 校验 frontmatter 契约，返回（全部指标引用集合, order → 联动素材）。"""
    files = list_objective_files(skill_dir)
    all_refs: set[str] = set()
    objectives_data: dict[int, ObjectiveData] = {}
    for path in files:
        label = f"objectives/{path.name}"
        data, err = parse_frontmatter(path.read_text(encoding="utf-8"))
        if data is None:
            detail = err or "缺少 frontmatter（文件必须以 --- 开头声明契约）"
            report.check(f"{label} frontmatter 契约", False, detail)
            continue
        problems = validate_contract(data, report, label)
        report.check(f"{label} frontmatter 契约", not problems, "；".join(problems))
        all_refs.update(collect_metric_refs(data))
        match = OBJECTIVE_FILE_RE.match(path.name)
        if match:
            objectives_data[int(match.group(1))] = collect_objective_data(data)
    return all_refs, objectives_data


def check_depends_on_dag(objectives_data: dict[int, ObjectiveData], report: Report) -> None:
    """objective 依赖图检查（frontmatter depends_on）：自引用/引用缺失/成环一律 WARN 不 FAIL。

    字段可选：存量包无 depends_on 声明时直接跳过；运行期依赖调度有自己的兜底，
    这里只做交付前的可见性提醒（宽容基调）。
    """
    declared = {order: obj.depends_on for order, obj in objectives_data.items() if obj.depends_on}
    if not declared:
        report.check("objective 依赖 DAG（depends_on）", True, "无 depends_on 声明，跳过")
        return
    for order, deps in sorted(declared.items()):
        if order in deps:
            report.warn(f"objective_{order}：depends_on 自引用（声明依赖自身）")
        missing = sorted(d for d in deps if d != order and d not in objectives_data)
        if missing:
            report.warn(f"objective_{order}：depends_on 引用不存在的 objective order：{missing}")

    # DFS 三色标记找环（自引用/缺失边已单独告警，不重复计入）
    white, gray, black = 0, 1, 2
    color = dict.fromkeys(objectives_data, white)
    path: list[int] = []
    cycles: list[str] = []

    def dfs(node: int) -> None:
        color[node] = gray
        path.append(node)
        for dep in objectives_data[node].depends_on:
            if dep == node or dep not in objectives_data:
                continue
            if color[dep] == gray:
                idx = path.index(dep)
                cycles.append(" → ".join(f"objective_{n}" for n in [*path[idx:], dep]))
            elif color[dep] == white:
                dfs(dep)
        path.pop()
        color[node] = black

    for order in sorted(objectives_data):
        if color[order] == white:
            dfs(order)
    for cycle in cycles:
        report.warn(f"depends_on 成环（循环依赖）：{cycle}")
    edge_count = sum(len(deps) for deps in declared.values())
    report.check("objective 依赖 DAG（depends_on）", True, f"{edge_count} 条依赖声明（违规见 WARN）")


def check_size_budget(skill_dir: Path, report: Report) -> None:
    def size(path: Path) -> int:
        return path.stat().st_size if path.is_file() else 0

    core = size(skill_dir / "SKILL.md") + size(skill_dir / "approach.md")
    report.check(
        f"尺寸预算：SKILL.md + approach.md 合计 < {SIZE_CORE_DOCS}",
        core < SIZE_CORE_DOCS,
        f"实际 {core} 字节",
    )

    files = list_objective_files(skill_dir)
    oversized = [f"{p.name}={p.stat().st_size}" for p in files if p.stat().st_size >= SIZE_SINGLE_OBJECTIVE]
    report.check(
        f"尺寸预算：单个 objective < {SIZE_SINGLE_OBJECTIVE}",
        not oversized,
        "；".join(oversized),
    )
    total = sum(p.stat().st_size for p in files)
    report.check(
        f"尺寸预算：objectives/ 合计 < {SIZE_OBJECTIVES_TOTAL}",
        total < SIZE_OBJECTIVES_TOTAL,
        f"实际 {total} 字节",
    )

    metrics_dir = skill_dir / "metrics"
    metrics_total = (
        sum(p.stat().st_size for p in metrics_dir.iterdir() if p.is_file()) if metrics_dir.is_dir() else 0
    )
    report.check(
        f"尺寸预算：metrics/ 合计 < {SIZE_METRICS_TOTAL}",
        metrics_total < SIZE_METRICS_TOTAL,
        f"实际 {metrics_total} 字节",
    )


def check_metric_cross_refs(skill_dir: Path, refs: set[str], report: Report) -> None:
    if not refs:
        report.check("指标引用交叉校验", True, "objectives 未引用任何指标，跳过")
        return
    metrics_dir = skill_dir / "metrics"
    existing = {p.stem for p in metrics_dir.glob("METRIC-*.yaml")} if metrics_dir.is_dir() else set()
    missing = sorted(refs - existing)
    report.check(
        "指标引用交叉校验",
        not missing,
        f"以下指标 ID 在 metrics/ 目录无同名文件：{missing}" if missing else f"{len(refs)} 个指标引用全部闭合",
    )


def list_theme_dirs(skill_dir: Path) -> list[Path]:
    web_report = skill_dir / "report" / "web_report"
    if not web_report.is_dir():
        return []
    return sorted(p for p in web_report.iterdir() if p.is_dir())


def check_report_structure(skill_dir: Path, report: Report) -> None:
    report_dir = skill_dir / "report"
    if not report_dir.is_dir():
        report.check("report/ 目录结构", True, "无 report/ 目录，跳过")
        return
    problems: list[str] = []
    web_report = report_dir / "web_report"
    if not (report_dir / "text_report.md").is_file() and not web_report.is_dir():
        problems.append("report/ 下 text_report.md 与 web_report/ 必须至少存在其一")
    if web_report.is_dir():
        themes = list_theme_dirs(skill_dir)
        if not themes:
            problems.append("web_report/ 存在但无主题子目录")
        for theme in themes:
            missing = [name for name in WEB_REPORT_REQUIRED_FILES if not (theme / name).is_file()]
            if missing:
                problems.append(f"web_report/{theme.name}/ 缺必需文件：{missing}")
    report.check("report/ 目录结构", not problems, "；".join(problems))


def load_json(path: Path) -> dict | None:
    """读 JSON 文件，不存在或非法时返回 None（合法性由 validate_template.py 负责判 FAIL）。"""
    if not path.is_file():
        return None
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return None
    return data if isinstance(data, dict) else None


def iter_metric_names(metrics_path: Path) -> list[str]:
    """逐行解析 metrics.jsonl 取 name（非法行跳过，由 validate_template.py 负责判 FAIL）。"""
    names: list[str] = []
    for line in metrics_path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(obj, dict) and isinstance(obj.get("name"), str):
            names.append(obj["name"])
    return names


def count_direct_read_markers(schema: dict) -> int:
    """统计 schema 中 x-direct-read-allowed 标记节点数（properties/items 递归）。"""
    count = 0
    props = schema.get("properties")
    if isinstance(props, dict):
        for child in props.values():
            if isinstance(child, dict):
                count += (DIRECT_READ_MARKER in child) + count_direct_read_markers(child)
    items = schema.get("items")
    if isinstance(items, dict):
        count += (DIRECT_READ_MARKER in items) + count_direct_read_markers(items)
    return count


def check_report_linkage(skill_dir: Path, objectives_data: dict[int, ObjectiveData], report: Report) -> None:
    """报告联动契约 R1/R2/R3/R4：报告需要的素材必须能溯源到某个 objective 的门禁产出。"""
    report_dir = skill_dir / "report"
    if not report_dir.is_dir():
        report.check("报告联动契约（R1/R2/R3/R4）", True, "无 report/ 目录，跳过")
        return

    all_metric_refs = set().union(*(o.metric_refs for o in objectives_data.values()), set())
    all_report_metrics = set().union(*(o.report_metrics for o in objectives_data.values()), set())
    problems: list[str] = []

    def mark_exists(order: int, gate_id: str) -> bool:
        obj = objectives_data.get(order)
        return obj is not None and gate_id in obj.gate_ids

    def check_marks(text: str, label: str, rule: str) -> int:
        """提取并校验来源记号存在性，返回记号总数；断链判 FAIL。"""
        marks = SOURCE_MARK_RE.findall(text)
        broken = sorted({f"objective_{o}/{g}" for o, g in marks if not mark_exists(int(o), g)})
        if broken:
            problems.append(f"{rule}：{label} 来源记号指向不存在的 objective/gate：{broken}")
        return len(marks)

    # R1：metrics.jsonl 每个 name 必须被某 objective 的指标引用或 ext.report_metrics 覆盖；
    # metrics.jsonl 为例外通道（独立于分析链路的第二数据通道），存在即提醒确认必要性
    for theme in list_theme_dirs(skill_dir):
        metrics_path = theme / "metrics.jsonl"
        if not metrics_path.is_file():
            continue
        report.warn(
            f"R1：web_report/{theme.name}/metrics.jsonl 为例外通道（独立于分析链路的第二数据通道）："
            f"确认系用户点名实时查库/全量明细需求，且各 metric 口径与归属门禁一致"
        )
        uncovered = [
            name
            for name in iter_metric_names(metrics_path)
            if not ((METRIC_ID_RE.match(name) and name in all_metric_refs) or name in all_report_metrics)
        ]
        if uncovered:
            problems.append(
                f"R1：web_report/{theme.name}/metrics.jsonl 以下 metric 未被任何 objective 门禁覆盖"
                f"（metrics_deps/gates[].metrics 或 gates[].ext.report_metrics）：{uncovered}"
            )

    # R2 + R4：fill_guide 来源标注与直读源文件闭合（同一循环，schema/guide 各读一次）
    for theme in list_theme_dirs(skill_dir):
        schema = load_json(theme / "schema.json")
        props = [k for k in (schema or {}).get("properties", {}) if k not in SCHEMA_RESERVED_KEYS]
        guide_path = theme / "fill_guide.md"
        guide = guide_path.read_text(encoding="utf-8") if guide_path.is_file() else ""
        if guide:
            check_marks(guide, f"web_report/{theme.name}/fill_guide.md", "R2")
        # 按字段首次出现位置切段，段内无来源记号视为无标注
        positions = sorted((guide.find(p), p) for p in props if p in guide)
        segments = {
            name: guide[pos : positions[idx + 1][0] if idx + 1 < len(positions) else len(guide)]
            for idx, (pos, name) in enumerate(positions)
        }
        for prop in props:
            segment = segments.get(prop, "")
            if not SOURCE_MARK_RE.search(segment):
                report.warn(
                    f"R2：web_report/{theme.name}/schema.json 字段 {prop} 在 fill_guide.md 中"
                    f"无来源标注（统一记号：来源：objective_N/G<id>）"
                )

        # R4：直读记号与来源记号同行、文件闭合到所标 objective 的 deliverables
        read_mark_count = 0
        for line in guide.splitlines():
            files = DIRECT_READ_FILE_RE.findall(line)
            if not files:
                continue
            read_mark_count += len(files)
            mark = SOURCE_MARK_RE.search(line)
            if mark is None:
                problems.append(
                    f"R4：web_report/{theme.name}/fill_guide.md 直读记号所在行无来源记号"
                    f"（统一记号：来源：objective_N/G<id>（直读：{{文件名}}））：{line.strip()[:60]}"
                )
                continue
            order = int(mark.group(1))
            obj = objectives_data.get(order)
            if obj is None:
                problems.append(
                    f"R4：web_report/{theme.name}/fill_guide.md 直读记号指向不存在的 objective_{order}"
                )
                continue
            for file in files:
                if file not in obj.deliverables:
                    problems.append(
                        f"R4：web_report/{theme.name}/fill_guide.md 直读文件 {file} 不在"
                        f" objective_{order} 的 deliverables 中"
                    )
                elif not obj.deliverables[file]:
                    report.warn(
                        f"R4：objective_{order} 的直读源文件 {file} 未声明 columns"
                        f"（列名即直读契约，建议补齐四锁格式契约：文件名/列名/形状/格式）"
                    )
        marker_count = count_direct_read_markers(schema) if schema else 0
        if marker_count and not read_mark_count:
            report.warn(
                f"R4：web_report/{theme.name}/schema.json 含 {marker_count} 个 {DIRECT_READ_MARKER} 标记，"
                f"但 fill_guide.md 通篇无「直读：{{文件名}}」记号（建议逐字段写明预期源文件）"
            )

    # R3：text_report.md 素材清单逐项标注来源；断链 FAIL，通篇无标注 WARN
    text_path = report_dir / "text_report.md"
    if text_path.is_file():
        total = check_marks(text_path.read_text(encoding="utf-8"), "report/text_report.md", "R3")
        if total == 0:
            report.warn("R3：report/text_report.md 通篇无任何 来源：objective_N/G<id> 标注")

    report.check("报告联动契约（R1/R2/R3/R4）", not problems, "；".join(problems) or "联动引用全部闭合")


def check_web_report_templates(skill_dir: Path, report: Report) -> None:
    """逐主题调用同目录 validate_template.py 做模板包机器校验。"""
    themes = list_theme_dirs(skill_dir)
    if not themes:
        report.check("web_report 模板包机器校验", True, "无 web_report/ 主题，跳过")
        return
    validator = Path(__file__).resolve().parent / "validate_template.py"
    if not validator.is_file():
        report.check("web_report 模板包机器校验", False, f"校验器缺失：{validator}")
        return
    problems: list[str] = []
    for theme in themes:
        proc = subprocess.run(
            [sys.executable, str(validator), str(theme)],
            check=False, capture_output=True,
            text=True,
        )
        if proc.returncode != 0:
            output = (proc.stdout + proc.stderr).strip()
            fails = [line for line in output.splitlines() if line.startswith("[fail]")]
            summary = "；".join(fails) if fails else output[-300:]
            problems.append(f"web_report/{theme.name} 退出码 {proc.returncode}：{summary}")
    report.check(
        "web_report 模板包机器校验",
        not problems,
        "；".join(problems) or f"{len(themes)} 个主题全部通过 validate_template.py",
    )


def main() -> int:
    parser = argparse.ArgumentParser(description="quest 技能包格式校验")
    parser.add_argument("skill_dir", type=Path, help="技能包目录路径")
    args = parser.parse_args()

    skill_dir: Path = args.skill_dir
    if not skill_dir.is_dir():
        print(f"[FAIL] 技能包目录不存在：{skill_dir}")
        return 1

    report = Report()
    check_skill_md(skill_dir, report)
    check_objective_naming(skill_dir, report)
    refs, objectives_data = check_objective_contracts(skill_dir, report)
    check_depends_on_dag(objectives_data, report)
    check_size_budget(skill_dir, report)
    check_metric_cross_refs(skill_dir, refs, report)
    check_report_structure(skill_dir, report)
    check_report_linkage(skill_dir, objectives_data, report)
    check_web_report_templates(skill_dir, report)

    for ok, name, detail in report.results:
        mark = "PASS" if ok else "FAIL"
        suffix = f" —— {detail}" if detail else ""
        print(f"[{mark}] {name}{suffix}")
    for message in report.warnings:
        print(f"[WARN] {message}")

    total, failed = len(report.results), report.failed
    print(f"\n共 {total} 项检查：{total - failed} PASS / {failed} FAIL")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
