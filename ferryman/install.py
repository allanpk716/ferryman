"""钩子安装器：把 Ferryman 钩子追加进 ~/.claude/settings.json（不动既有条目）。

DESIGN §3 地雷（2026-09-17 T21 三轮实测定案）：CC Switch 切换供应商 =
把 ~/.cc-switch/cc-switch.db 中该供应商的 settings_config 快照**逐字写入**
settings.json——不在快照里的键（hooks）每次切换 / Live 模式重写都会被抹。
故 install-cc 在检测到 cc-switch.db 时自动把钩子注进全部 claude 供应商快照
（inject_ccswitch：幂等、保留既有条目、改库前备份）。

幂等：先移除 command 含 "ferryman" 的旧条目再追加。
"""

from __future__ import annotations

import json
import shutil
import sqlite3
import time
from pathlib import Path

CCSWITCH_DB = Path.home() / ".cc-switch" / "cc-switch.db"


def _ferry_hook_entries(repo: Path) -> dict[str, list[dict]]:
    """四段钩子条目（settings.json 与 CC Switch 快照共用同一结构）。"""
    ps = 'powershell -NoProfile -ExecutionPolicy Bypass -File'
    return {
        "UserPromptSubmit": [{"hooks": [{
            "type": "command",
            "command": f'{ps} "{repo / "hooks" / "ferryman-gate.ps1"}"',
            "timeout": 3}]}],
        # SessionStart 仅 clear|startup 注入（resume/compact 不注入，DESIGN §5）
        "SessionStart": [{"matcher": "clear|startup", "hooks": [{
            "type": "command",
            "command": f'{ps} "{repo / "hooks" / "ferryman-restore.ps1"}"',
            "timeout": 3}]}],
        # T32：子代理生命周期（CC ≥2.1.273；旧版本不触发事件 → T31 悬空检测兜底）
        "SubagentStart": [{"hooks": [{
            "type": "command",
            "command": f'{ps} "{repo / "hooks" / "ferryman-subagent.ps1"}"',
            "timeout": 3}]}],
        "SubagentStop": [{"hooks": [{
            "type": "command",
            "command": f'{ps} "{repo / "hooks" / "ferryman-subagent.ps1"}"',
            "timeout": 3}]}],
    }


def install_cc(settings_path: Path | None = None,
               ccswitch_db: Path | None = None) -> int:
    repo = Path(__file__).resolve().parent.parent
    settings_path = settings_path or (Path.home() / ".claude" / "settings.json")
    if not settings_path.exists():
        settings_path.write_text("{}", encoding="utf-8")
    backup = settings_path.with_name(
        f"settings.json.bak-ferryman-{time.strftime('%Y%m%d_%H%M%S')}")
    shutil.copy2(settings_path, backup)

    data = json.loads(settings_path.read_text(encoding="utf-8"))
    hooks = data.setdefault("hooks", {})
    entries = _ferry_hook_entries(repo)

    def merge(event: str, new_entries: list[dict]) -> None:
        lst = hooks.setdefault(event, [])
        if isinstance(lst, list):
            hooks[event] = [e for e in lst
                            if "ferryman" not in json.dumps(e, ensure_ascii=False)]
            hooks[event].extend(new_entries)

    for event, entry_list in entries.items():
        merge(event, entry_list)

    settings_path.write_text(
        json.dumps(data, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"已追加 Ferryman 钩子到 {settings_path}（备份: {backup.name}）")
    print()

    # CC Switch 地雷（DESIGN §3）：检测到 → 钩子注进全部 claude 供应商快照
    n = inject_ccswitch(ccswitch_db or CCSWITCH_DB)
    if n == 0:
        print("[!] CC Switch 地雷（DESIGN §3）：切换供应商会全量覆盖 settings.json。")
        print("   未检测到 cc-switch.db——若日后安装 CC Switch，重跑本命令或 install-ccswitch。")
        print("   手动方案（四段钩子同步进各供应商模板）：")
        for evt, entry in entries.items():
            print(f"# {evt}")
            print(json.dumps(entry, ensure_ascii=False, indent=2))
    return 0


def inject_ccswitch(db_path: Path | None = None) -> int:
    """把 ferryman 钩子注进 cc-switch.db 全部 claude 供应商快照。

    - 幂等：剔除快照里旧 ferryman 条目后追加最新版（Orca 等既有条目原样保留）；
    - 改库前备份 cc-switch.db.bak-ferryman-<ts>；
    - 新增供应商后需重跑（`ferryman install-ccswitch`）。
    返回注入的供应商数；无 DB / 任何 sqlite 故障 → 0（绝不影响 settings.json 安装）。
    """
    db_path = Path(db_path) if db_path else CCSWITCH_DB
    if not db_path.exists():
        print(f"未检测到 CC Switch（{db_path} 不存在），跳过供应商快照注入。")
        return 0
    repo = Path(__file__).resolve().parent.parent
    entries = _ferry_hook_entries(repo)
    bak = db_path.with_name(f"{db_path.name}.bak-ferryman-{time.strftime('%Y%m%d_%H%M%S')}")
    try:
        shutil.copy2(db_path, bak)
    except OSError as e:
        print(f"[!] cc-switch.db 备份失败，跳过注入（安全起见）: {e}")
        return 0
    try:
        conn = sqlite3.connect(db_path, timeout=10)
        cur = conn.cursor()
        rows = cur.execute(
            "SELECT id, name, settings_config FROM providers WHERE app_type='claude'"
        ).fetchall()
        for pid, name, raw in rows:
            try:
                cfg = json.loads(raw)
                if not isinstance(cfg, dict):
                    cfg = {}
            except ValueError:
                cfg = {}
            old: dict = cfg.get("hooks") if isinstance(cfg.get("hooks"), dict) else {}
            merged: dict[str, list] = {}
            for evt in set(old) | set(entries):
                kept = [e for e in old.get(evt, [])
                        if isinstance(e, dict)
                        and "ferryman" not in json.dumps(e, ensure_ascii=False)]
                merged[evt] = kept + entries.get(evt, [])
            cfg["hooks"] = merged
            cur.execute("UPDATE providers SET settings_config=? WHERE id=?",
                        (json.dumps(cfg, ensure_ascii=False, indent=2), pid))
            print(f"[ccswitch] ✓ {name}: ferryman 四钩子已入快照"
                  f"（既有条目保留，备份 {bak.name}）")
        conn.commit()
        conn.close()
        return len(rows)
    except sqlite3.Error as e:
        print(f"[!] CC Switch 快照注入失败（{e}）——CC Switch 可能正持有库锁。"
              f"稍后重跑 `ferryman install-ccswitch`。")
        return 0
