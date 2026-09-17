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
import os
import shutil
import sqlite3
import time
from pathlib import Path

CCSWITCH_DB = Path.home() / ".cc-switch" / "cc-switch.db"
LAUNCHER_NAME = "start-daemon.cmd"


def ensure_launcher(data_dir: Path | None = None, repo: Path | None = None) -> Path:
    """生成守护进程点火脚本 ~/ferryman/start-daemon.cmd（钩子自举用，DESIGN §3）。

    思路：不做开机自启——任意 agent 的任意钩子（gate/restore/subagent/codex）
    POST 前探测 :7311，不在则隐藏窗口拉起本脚本（含绝对 venv python 路径，
    输出重定向到 serve.{out,err}.log，进程独立于钩子存活）。
    """
    data_dir = data_dir or (Path.home() / "ferryman")
    repo = repo or Path(__file__).resolve().parent.parent
    data_dir.mkdir(parents=True, exist_ok=True)
    py = repo / ".venv" / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
    win = os.name == "nt"
    if py.exists():
        start = f'"{py}" -m ferryman serve'
    else:
        start = f'uv --directory "{repo}" run ferryman serve'
    if win:
        body = (f'@echo off\r\n'
                f'rem Ferryman 守护进程点火脚本（install-cc 自动生成，勿手改）\r\n'
                f'cd /d "{repo}"\r\n'
                f'{start} >> "%USERPROFILE%\\ferryman\\serve.out.log"'
                f' 2>> "%USERPROFILE%\\ferryman\\serve.err.log"\r\n')
    else:
        body = (f'#!/bin/sh\n'
                f'# Ferryman 守护进程点火脚本（install-cc 自动生成，勿手改）\n'
                f'cd "{repo}"\n'
                f'{start} >> "$HOME/ferryman/serve.out.log"'
                f' 2>> "$HOME/ferryman/serve.err.log"\n')
    launcher = data_dir / LAUNCHER_NAME
    launcher.write_text(body, encoding="utf-8")
    print(f"[ensure] 点火脚本就绪: {launcher}（钩子自举 = agent 启动会话即拉起 daemon）")
    return launcher


def _ferry_hook_entries(repo: Path) -> dict[str, list[dict]]:
    """四段钩子条目（settings.json 与 CC Switch 快照共用同一结构）。"""
    ps = 'powershell -NoProfile -ExecutionPolicy Bypass -File'
    return {
        "UserPromptSubmit": [{"hooks": [{
            "type": "command",
            "command": f'{ps} "{repo / "hooks" / "ferryman-gate.ps1"}"',
            "timeout": 3}]}],
        # SessionStart 仅 clear|startup 注入（resume/compact 不注入，DESIGN §5）；
        # 超时 10s：钩子内含 daemon 自举（最坏 ~2.5s 等就绪）+ POST
        "SessionStart": [{"matcher": "clear|startup", "hooks": [{
            "type": "command",
            "command": f'{ps} "{repo / "hooks" / "ferryman-restore.ps1"}"',
            "timeout": 10}]}],
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
               ccswitch_db: Path | None = None,
               data_dir: Path | None = None) -> int:
    repo = Path(__file__).resolve().parent.parent
    ensure_launcher(data_dir=data_dir, repo=repo)          # 钩子自举点火脚本（唯一化前提）
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
                cfg: dict = json.loads(raw)
                if not isinstance(cfg, dict):
                    cfg = {}
            except ValueError:
                cfg = {}
            hooks_cfg = cfg.get("hooks")
            old: dict = hooks_cfg if isinstance(hooks_cfg, dict) else {}
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
