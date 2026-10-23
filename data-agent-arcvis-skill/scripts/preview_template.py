#!/usr/bin/env python3
"""Standalone 模板预览生成器：将 sample_data.json 注入 template.html 生成 preview.html。

用法：
    python scripts/preview_template.py <模板包目录> [output.html]

复刻平台运行期注入语义：
JSON 安全转义（</ → <\\/、U+2028/U+2029）+ 唯一注入点替换 + {{current_time}} 替换。
"""

import json
import sys
from datetime import datetime
from pathlib import Path

PLACEHOLDER = "__REPORT_DATA__"


def escape_inline_json(data_json: str) -> str:
    return (
        data_json.replace("</", "<\\/")
        .replace("\u2028", "\\u2028")
        .replace("\u2029", "\\u2029")
    )


def main() -> int:
    if len(sys.argv) not in (2, 3):
        print(__doc__)
        return 2
    tdir = Path(sys.argv[1])
    template_path = tdir / "template.html"
    sample_path = tdir / "sample_data.json"
    for path in (template_path, sample_path):
        if not path.is_file():
            print(f"缺少文件: {path}")
            return 2

    html = template_path.read_text(encoding="utf-8")
    count = html.count(PLACEHOLDER)
    if count != 1:
        print(f"template.html 含 {count} 个 {PLACEHOLDER} 注入点，要求恰好 1 个")
        return 1

    data = json.loads(sample_path.read_text(encoding="utf-8"))
    data_json = json.dumps(data, ensure_ascii=False)
    html = html.replace(PLACEHOLDER, escape_inline_json(data_json))
    html = html.replace("{{current_time}}", datetime.now().strftime("%Y-%m-%d %H:%M:%S"))

    out = Path(sys.argv[2]) if len(sys.argv) == 3 else tdir / "preview.html"
    out.write_text(html, encoding="utf-8")
    print(f"预览已生成: {out}")
    print("注意: preview.html 是本地预览产物，不推荐提交/上传（平台加载时会忽略该文件）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
