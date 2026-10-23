#!/usr/bin/env python3
"""Standalone 报告模板包离线校验器（stdlib-only，jsonschema 可选增强）。

用法：
    python scripts/validate_template.py <模板包目录>

输出 [pass]/[warn]/[fail] 清单；存在 [fail] 时退出码为 1。
校验逻辑对齐平台加载模板包时的硬约束，并补充平台不自动校验的一致性项。
"""

import json
import re
import sys
from pathlib import Path

try:
    import tomllib
except ImportError:  # Python < 3.11 兜底：降级为正则提取关键键
    tomllib = None

PLACEHOLDER = "__REPORT_DATA__"
RT_DATA = "__RT_DATA__"
RESERVED_DATA_KEY = "metrics"

# <jarvis-metric name="..."> 引用（运行期由工程渲染，与平台模板实体解析器保持一致）
METRIC_REF_RE = re.compile(r"<jarvis-metric[^>]*\bname=\"([^\"]+)\"")
# 桥接别名：var D = window.__RT_DATA__ ...
ALIAS_RE = re.compile(rf"(?:var|let|const)\s+(\w+)\s*=\s*\(?\s*(?:window|globalThis)\.{RT_DATA}")
# 直接访问：(window.__RT_DATA__ || {}).field / window.__RT_DATA__.field
DIRECT_ACCESS_RES = [
    re.compile(rf"(?:window|globalThis)\.{RT_DATA}\s*\|\|\s*\{{\}}\s*\)\s*\.(\w+)"),
    re.compile(rf"(?:window|globalThis)\.{RT_DATA}\.(\w+)"),
]

results: list[str] = []


def report(level: str, msg: str) -> None:
    results.append(f"[{level}] {msg}")


def parse_manifest(text: str) -> dict:
    if tomllib is not None:
        return tomllib.loads(text)
    # 正则兜底只提取顶层 `key = "value"` 形式的字符串键
    report("warn", "Python < 3.11 无 tomllib，manifest 校验降级为正则解析（建议升级 Python）")
    manifest: dict = {}
    for m in re.finditer(r'^\s*(\w+)\s*=\s*"([^"]*)"', text, re.MULTILINE):
        manifest[m.group(1)] = m.group(2)
    return manifest


def check_manifest(tdir: Path) -> dict:
    path = tdir / "manifest.toml"
    if not path.is_file():
        report("fail", "manifest.toml 缺失")
        return {}
    try:
        manifest = parse_manifest(path.read_text(encoding="utf-8"))
    except Exception as e:  # noqa: BLE001
        report("fail", f"manifest.toml 解析失败: {e}")
        return {}
    if str(manifest.get("report_schema", "")).lower() != "standalone":
        report("fail", "manifest.toml: report_schema 必须为 standalone")
    else:
        report("pass", "manifest.toml: report_schema = standalone")
    if not manifest.get("schema_version"):
        report("fail", "manifest.toml: 缺 schema_version")
    else:
        report("pass", f"manifest.toml: schema_version = {manifest['schema_version']}")
    if not manifest.get("description"):
        report("warn", "manifest.toml: 建议补充 description")
    return manifest


def load_schema(tdir: Path) -> dict | None:
    path = tdir / "schema.json"
    if not path.is_file():
        report("fail", "schema.json 缺失")
        return None
    try:
        schema = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as e:
        report("fail", f"schema.json 非法 JSON: {e}")
        return None
    report("pass", "schema.json 是合法 JSON")
    try:
        import jsonschema

        jsonschema.validators.validator_for(schema).check_schema(schema)
        report("pass", "schema.json 通过 JSON Schema meta 校验")
    except ImportError:
        report("warn", "本机未装 jsonschema，跳过 meta-schema 校验")
    except Exception as e:  # noqa: BLE001
        report("fail", f"schema.json 不是合法 JSON Schema: {e}")
    props = schema.get("properties") or {}
    if RESERVED_DATA_KEY in props:
        report("fail", f"schema.json properties 声明了工程保留键 '{RESERVED_DATA_KEY}'")
    else:
        report("pass", f"schema.json properties 未占用保留键 '{RESERVED_DATA_KEY}'")
    return schema


def check_template(tdir: Path, schema: dict | None) -> str | None:
    path = tdir / "template.html"
    if not path.is_file():
        report("fail", "template.html 缺失")
        return None
    html = path.read_text(encoding="utf-8")

    count = html.count(PLACEHOLDER)
    if count == 1:
        report("pass", f"template.html 含唯一 {PLACEHOLDER} 注入点")
    else:
        report("fail", f"template.html 注入点数量为 {count}，要求恰好 1 个（注释中出现也计数）")

    if re.search(rf"(?:window|globalThis)\.{RT_DATA}\s*=\s*{PLACEHOLDER}", html):
        report("pass", f"存在 {RT_DATA} 桥接赋值（注入点处于 JS 表达式位置）")
    else:
        report("fail", f"未找到 `window.{RT_DATA} = {PLACEHOLDER}` 桥接，注入点可能不在 JS 表达式位置")

    # JS 顶层字段读取 与 schema properties 一致性静态检查
    if schema is not None:
        declared = set((schema.get("properties") or {}).keys()) | {RESERVED_DATA_KEY}
        accessed: set[str] = set()
        for alias in ALIAS_RE.findall(html):
            accessed.update(re.findall(rf"\b{re.escape(alias)}\.(\w+)", html))
        for pattern in DIRECT_ACCESS_RES:
            accessed.update(pattern.findall(html))
        accessed.discard(RT_DATA)
        unknown = sorted(accessed - declared)
        if unknown:
            report("fail", f"template.html 读取了 schema 未声明的顶层字段: {', '.join(unknown)}")
        else:
            report("pass", f"template.html 读取的顶层字段（{len(accessed)} 个）均在 schema 中声明或为 metrics")
        unused = sorted(declared - accessed - {RESERVED_DATA_KEY})
        if unused:
            report("warn", f"schema 声明但 template.html 未读取的字段: {', '.join(unused)}")
    return html


def check_metrics(tdir: Path, html: str | None) -> None:
    path = tdir / "metrics.jsonl"
    defined: set[str] = set()
    if path.is_file():
        bad_lines, dup = [], []
        for line_no, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                bad_lines.append(line_no)
                continue
            if not obj.get("name") or not obj.get("sql"):
                bad_lines.append(line_no)
                continue
            if obj["name"] in defined:
                dup.append(obj["name"])
            defined.add(obj["name"])
        if bad_lines:
            report("fail", f"metrics.jsonl 第 {bad_lines} 行非法（非 JSON 或缺 name/sql）")
        else:
            report("pass", f"metrics.jsonl {len(defined)} 个 metric 定义均合法")
        if dup:
            report("warn", f"metrics.jsonl 重名 metric（运行期保留首个）: {', '.join(dup)}")
    else:
        report("warn", "无 metrics.jsonl（纯 LLM 填充模板可忽略）")

    if html is not None:
        referenced = list(dict.fromkeys(METRIC_REF_RE.findall(html)))
        missing = [n for n in referenced if n not in defined]
        if missing:
            report("warn", f"<jarvis-metric> 引用未在 metrics.jsonl 定义（需会话内有 DATA-{{name}} cell）: {', '.join(missing)}")
        elif referenced:
            report("pass", f"<jarvis-metric> 引用的 {len(referenced)} 个 metric 均有定义")
        # JS 中 M.xxx / metrics.xxx 的消费与定义交叉核对
        js_used = set(re.findall(r"\bmetrics\.(\w+)|\bM\[\"(\w+)\"\]|\bM\.(\w+)", html))
        js_names = {n for tup in js_used for n in tup if n}
        unknown_js = sorted(n for n in js_names if defined and n not in defined)
        if unknown_js:
            report("warn", f"JS 消费了 metrics.jsonl 未定义的 metric: {', '.join(unknown_js)}")


def check_fill_guide(tdir: Path, schema: dict | None, manifest_version: str | None = None) -> None:
    path = tdir / "fill_guide.md"
    props = list((schema or {}).get("properties") or {})
    if not path.is_file():
        if props:
            report("warn", "混合模板缺 fill_guide.md，填充质量不可控")
        return
    guide = path.read_text(encoding="utf-8")
    uncovered = [p for p in props if p not in guide]
    if uncovered:
        report("warn", f"fill_guide.md 未覆盖字段: {', '.join(uncovered)}")
    else:
        report("pass", f"fill_guide.md 覆盖全部 {len(props)} 个 schema 字段")
    # 版本号同步：fill_guide 若提及 schema_version，须与 manifest 一致（迭代改版易落下）
    if manifest_version:
        m = re.search(r"schema_version\s*[:=：]?\s*\"?(\d+)", guide)
        if m and m.group(1) != str(manifest_version):
            report("warn", f"fill_guide.md 版本号（{m.group(1)}）与 manifest schema_version（{manifest_version}）不一致")
        elif m:
            report("pass", f"fill_guide.md 版本号与 manifest schema_version（{manifest_version}）一致")


def check_sample(tdir: Path, schema: dict | None) -> None:
    path = tdir / "sample_data.json"
    if not path.is_file():
        report("warn", "无 sample_data.json，无法离线预览回归")
        return
    try:
        sample = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as e:
        report("fail", f"sample_data.json 非法 JSON: {e}")
        return
    if schema is None:
        return
    try:
        import jsonschema

        # sample 含工程注入的 metrics 键，校验前剔除
        llm_part = {k: v for k, v in sample.items() if k != RESERVED_DATA_KEY}
        jsonschema.validate(instance=llm_part, schema=schema)
        report("pass", "sample_data.json（剔除 metrics 后）通过 schema 校验")
    except ImportError:
        report("warn", "本机未装 jsonschema，跳过 sample_data 校验")
    except Exception as e:  # noqa: BLE001
        report("fail", f"sample_data.json 未过 schema: {e}")


def check_direct_read_markers(schema: dict | None) -> None:
    """x-direct-read-allowed 标记合法性：标记须为对象，orient 取枚举、column_map 为字符串映射。"""
    if schema is None:
        return
    problems: list[str] = []
    count = 0

    def walk(node: object) -> None:
        nonlocal count
        if not isinstance(node, dict):
            return
        marker = node.get("x-direct-read-allowed")
        if marker is not None:
            count += 1
            if not isinstance(marker, dict):
                problems.append(f"第 {count} 个标记不是对象（可写空对象 {{}} 或 {{orient, column_map}}）")
            else:
                orient = marker.get("orient")
                if orient is not None and orient not in ("records", "columns", "matrix"):
                    problems.append(f"orient 取值非法：{orient!r}（合法：records/columns/matrix）")
                column_map = marker.get("column_map")
                if column_map is not None and (
                    not isinstance(column_map, dict)
                    or not all(isinstance(k, str) and isinstance(v, str) for k, v in column_map.items())
                ):
                    problems.append("column_map 必须是 源列名→字段名 的字符串映射")
        for child in (node.get("properties") or {}).values():
            walk(child)
        items = node.get("items")
        if isinstance(items, dict):
            walk(items)

    walk(schema)
    if problems:
        for p in problems:
            report("fail", f"x-direct-read-allowed 标记非法：{p}")
    elif count:
        report("pass", f"x-direct-read-allowed 标记 {count} 个均合法")


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__)
        return 2
    tdir = Path(sys.argv[1])
    if not tdir.is_dir():
        print(f"模板目录不存在: {tdir}")
        return 2

    manifest = check_manifest(tdir)
    schema = load_schema(tdir)
    check_direct_read_markers(schema)
    html = check_template(tdir, schema)
    check_metrics(tdir, html)
    check_fill_guide(tdir, schema, manifest.get("schema_version"))
    check_sample(tdir, schema)

    fails = sum(1 for r in results if r.startswith("[fail]"))
    warns = sum(1 for r in results if r.startswith("[warn]"))
    passes = sum(1 for r in results if r.startswith("[pass]"))
    print("\n".join(results))
    print(f"\n校验完成: {passes} pass / {warns} warn / {fails} fail")
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())
