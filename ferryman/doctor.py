"""ferryman doctor：一键体检。

2026-09-17 两类事故的直接解药——都是"静默失效"，出问题时表面毫无异常：
- CC Switch 切换/重写抹掉 settings.json 里的钩子（T21）；
- 生成文件时反斜杠转义被写成控制字符（\\a→BEL、\\f→FF），钩子指向不存在路径。

检查项全部纯函数化（路径/探针可注入）；run_doctor 聚合并打印 ✓/✗。
"""

from __future__ import annotations

import json
import re
import sqlite3
from pathlib import Path

from .install import CODEX_CONFIG, CODEX_HOOKS, CCSWITCH_DB, LAUNCHER_NAME

FERRY_EVENTS = ("UserPromptSubmit", "SessionStart", "SubagentStart", "SubagentStop")


def _repo() -> Path:
    return Path(__file__).resolve().parent.parent


def check_cc_hooks(settings_path: Path, repo: Path | None = None) -> tuple[bool, str]:
    """settings.json 四事件齐全、路径存在、restore 超时够自举。"""
    repo = repo or _repo()
    if not settings_path.exists():
        return False, f"{settings_path} 不存在"
    try:
        hooks = json.loads(settings_path.read_text(encoding="utf-8")).get("hooks", {})
    except ValueError as e:
        return False, f"settings.json 解析失败: {e}"
    missing = [evt for evt in FERRY_EVENTS
               if not any("ferryman" in json.dumps(e, ensure_ascii=False)
                          for e in hooks.get(evt, []))]
    if missing:
        return False, f"settings.json 缺 ferryman 钩子事件: {', '.join(missing)}" \
                      f"（CC Switch 切换会抹钩子——跑 install-cc / install-ccswitch）"
    # 路径存在性 + restore 超时（自举等待预算）
    for evt in FERRY_EVENTS:
        for entry in hooks[evt]:
            blob = json.dumps(entry, ensure_ascii=False)
            if "ferryman" not in blob:
                continue
            m = re.search(r'-File \\"(.*?)\\"', blob) or re.search(r'-File "(.*?)"', blob)
            if not m or not Path(m.group(1)).exists():
                return False, f"{evt}: 钩子脚本路径不存在（{m.group(1) if m else blob[:60]}）"
            if evt == "SessionStart" and entry.get("hooks", [{}])[0].get("timeout", 0) < 10:
                return False, "SessionStart 超时 <10s（含 daemon 自举等待会不够——重跑 install-cc）"
    return True, "settings.json 四钩子在位"


def check_hook_scripts(paths: list[Path]) -> list[tuple[bool, str]]:
    """BOM 在位（PS5.1 中文注释地雷）+ 无控制字符（\\a→BEL / \\f→FF 事故）。"""
    out = []
    for p in paths:
        if not p.exists():
            out.append((False, f"{p.name}: 不存在"))
            continue
        raw = p.read_bytes()
        if not raw.startswith(b"\xef\xbb\xbf"):
            out.append((False, f"{p.name}: 缺 UTF-8 BOM（PS5.1 按 ANSI 解析中文注释会炸）"))
            continue
        bad = [b for b in (0x07, 0x08, 0x0B, 0x0C, 0x1B) if bytes([b]) in raw]
        if bad:
            out.append((False, f"{p.name}: 含控制字符 {[hex(b) for b in bad]}"
                               f"（路径转义事故——重跑 install 修复）"))
            continue
        out.append((True, f"{p.name}: BOM/无控制字符"))
    return out


def check_ccswitch(db_path: Path = CCSWITCH_DB) -> tuple[bool, str]:
    """全部 claude 供应商快照都带 ferryman 钩子（切换=逐字写入，缺了就会被抹）。"""
    if not db_path.exists():
        return True, "未装 CC Switch（跳过）"
    try:
        conn = sqlite3.connect(db_path, timeout=5)
        rows = conn.execute(
            "SELECT name, settings_config FROM providers WHERE app_type='claude'"
        ).fetchall()
        conn.close()
    except sqlite3.Error as e:
        return False, f"cc-switch.db 读取失败: {e}"
    lacking = [name for name, raw in rows
               if not all(any("ferryman" in json.dumps(e, ensure_ascii=False)
                              for e in (json.loads(raw).get("hooks", {})
                                        .get(evt) or []))
                          for evt in FERRY_EVENTS)]
    if lacking:
        return False, f"供应商快照缺钩子: {', '.join(lacking)}（重跑 install-ccswitch）"
    return True, f"CC Switch {len(rows)} 个 claude 快照全带钩子"


def check_codex(hooks_path: Path = CODEX_HOOKS,
                config_path: Path = CODEX_CONFIG) -> tuple[bool, str]:
    """Codex：钩子条目在位 + [features] hooks = true（默认关，不开则整包静默失效）。"""
    problems = []
    if hooks_path.exists():
        try:
            hooks = json.loads(hooks_path.read_text(encoding="utf-8")).get("hooks", {})
            need = ("UserPromptSubmit", "SessionStart", "SubagentStart", "SubagentStop")
            missing = [e for e in need
                       if not any("ferryman" in json.dumps(x, ensure_ascii=False)
                                  for x in hooks.get(e, []))]
            if missing:
                problems.append(f"hooks.json 缺: {', '.join(missing)}（跑 install-codex）")
        except ValueError as e:
            problems.append(f"hooks.json 解析失败: {e}")
    else:
        problems.append("hooks.json 不存在（跑 install-codex）")
    toml = config_path.read_text(encoding="utf-8") if config_path.exists() else ""
    if not any(l.strip().replace(" ", "").startswith("hooks=true") for l in toml.splitlines()):
        problems.append("[features] hooks = true 未开（Codex 钩子默认关闭）")
    return (not problems, "；".join(problems) if problems else "Codex 钩子+旗标在位")


def check_daemon(probe, pid_file: Path) -> tuple[bool, str]:
    """probe() 返回 /stats dict（活）或 None（死）。"""
    st = probe()
    if st is None:
        return False, "daemon 未运行（钩子自举会拉起，或手动 start-daemon.cmd）"
    extra = ""
    try:
        pid = json.loads(pid_file.read_text(encoding="utf-8"))
        extra = f"（pid {pid.get('pid')}）"
    except (OSError, ValueError):
        pass
    alert = " · ⚠ 健康告警: 疑似钩子失效" if st.get("health_alert") else " · ok"
    return True, f"daemon 活着{extra}{alert}"


def check_ferry_provider(name: str, providers: dict) -> tuple[bool, str]:
    """摆渡 provider 已配置且在 [providers.*] 有定义（T39 去内置默认后的新静默失效点）。"""
    if not name:
        return False, ("[ferry] provider 未配置——摆渡永远降级骨架"
                       "（复制 config.example.toml 到 ~/ferryman/config.toml）")
    if name not in providers:
        return False, f"[ferry] provider '{name}' 未在 [providers.*] 定义"
    return True, f"摆渡 provider '{name}' 在位"


def check_launcher(path: Path, repo: Path | None = None) -> tuple[bool, str]:
    """点火脚本在位且其 python 路径有效（钩子自举的地基）。"""
    repo = repo or _repo()
    if not path.exists():
        return False, f"{path} 不存在（重跑 install-cc 生成点火脚本）"
    body = path.read_text(encoding="utf-8", errors="replace")
    m = re.search(r'"([^"]+python[w]?)\.exe"', body)
    if m and not Path(m.group(1) + ".exe").exists():
        return False, f"点火脚本 python 路径失效: {m.group(1)}.exe"
    return True, f"{LAUNCHER_NAME} 在位"


def run_doctor() -> int:
    repo = _repo()
    home = Path.home()
    data_dir = home / "ferryman"
    results: list[tuple[bool, str]] = []

    results.append(check_cc_hooks(home / ".claude" / "settings.json", repo=repo))
    results.append(check_launcher(data_dir / LAUNCHER_NAME, repo=repo))
    results.append(check_ccswitch())
    try:
        from . import config as config_mod
        from .ferry import load_config as load_providers
        cfg = config_mod.load()
        results.append(check_ferry_provider(cfg.ferry_provider, load_providers()))
    except Exception as e:  # noqa: BLE001 — 配置坏要让 doctor 报出来而非崩
        results.append((False, f"摆渡配置加载失败: {e}"))

    scripts = [repo / "hooks" / n for n in (
        "ferryman-gate.ps1", "ferryman-restore.ps1", "ferryman-subagent.ps1",
        "ferryman-ensure.ps1", "ferryman-gate-codex.ps1",
        "ferryman-restore-codex.ps1", "ferryman-subagent-codex.ps1")]
    results.extend(check_hook_scripts(scripts))
    results.append(check_codex())

    def real_probe():
        import urllib.error
        import urllib.request
        try:
            token = (data_dir / "daemon.token").read_text(encoding="utf-8").strip()
        except OSError:
            return None
        req = urllib.request.Request("http://127.0.0.1:7311/stats",
                                     headers={"Authorization": f"Bearer {token}"})
        try:
            with urllib.request.urlopen(req, timeout=2) as r:
                return json.loads(r.read().decode("utf-8"))
        except (OSError, ValueError):
            return None

    results.append(check_daemon(real_probe, data_dir / "daemon.pid"))

    fails = 0
    for ok, msg in results:
        print(("[OK]   " if ok else "[FAIL] ") + msg)
        fails += 0 if ok else 1
    print(f"\n体检结论: {len(results) - fails}/{len(results)} 通过"
          + ("——有问题见上" if fails else ""))
    return 1 if fails else 0
