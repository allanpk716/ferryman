"""钩子安装器：把 Ferryman 钩子追加进 ~/.claude/settings.json（不动既有条目）。

DESIGN §3 地雷：CC Switch 切换供应商会全量覆盖 settings.json ——
本安装器完成本地追加后必须打印 CC Switch 模板同步提示（人工步骤）。
幂等：先移除 command 含 "ferryman" 的旧条目再追加。
"""

from __future__ import annotations

import json
import shutil
import time
from pathlib import Path


def install_cc(settings_path: Path | None = None) -> int:
    repo = Path(__file__).resolve().parent.parent
    settings_path = settings_path or (Path.home() / ".claude" / "settings.json")
    if not settings_path.exists():
        settings_path.write_text("{}", encoding="utf-8")
    backup = settings_path.with_name(
        f"settings.json.bak-ferryman-{time.strftime('%Y%m%d_%H%M%S')}")
    shutil.copy2(settings_path, backup)

    data = json.loads(settings_path.read_text(encoding="utf-8"))
    hooks = data.setdefault("hooks", {})

    def merge(event: str, entry: dict) -> None:
        lst = hooks.setdefault(event, [])
        if isinstance(lst, list):
            hooks[event] = [e for e in lst
                            if "ferryman" not in json.dumps(e, ensure_ascii=False)]
            hooks[event].append(entry)

    ps = 'powershell -NoProfile -ExecutionPolicy Bypass -File'
    merge("UserPromptSubmit", {"hooks": [{
        "type": "command",
        "command": f'{ps} "{repo / "hooks" / "ferryman-gate.ps1"}"',
        "timeout": 3}]})
    # SessionStart 仅 clear|startup 注入（resume/compact 不注入，DESIGN §5）
    merge("SessionStart", {
        "matcher": "clear|startup",
        "hooks": [{"type": "command",
                   "command": f'{ps} "{repo / "hooks" / "ferryman-restore.ps1"}"',
                   "timeout": 3}]})
    # T32：子代理生命周期（CC ≥2.1.273；旧版本不触发事件 → T31 悬空检测兜底）
    for evt in ("SubagentStart", "SubagentStop"):
        merge(evt, {"hooks": [{
            "type": "command",
            "command": f'{ps} "{repo / "hooks" / "ferryman-subagent.ps1"}"',
            "timeout": 3}]})

    settings_path.write_text(
        json.dumps(data, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"已追加 Ferryman 钩子到 {settings_path}（备份: {backup.name}）")
    print()
    print("[!] CC Switch 地雷（DESIGN §3）：切换供应商会全量覆盖 settings.json。")
    print("   请把以下钩子段同步进 CC Switch 的供应商模板，否则切一次钩子就没了：")
    for evt in ("UserPromptSubmit", "SessionStart", "SubagentStart", "SubagentStop"):
        entries = [e for e in hooks.get(evt, [])
                   if "ferryman" in json.dumps(e, ensure_ascii=False)]
        if entries:
            print(f"# {evt}")
            print(json.dumps(entries[-1], ensure_ascii=False, indent=2))
    return 0
