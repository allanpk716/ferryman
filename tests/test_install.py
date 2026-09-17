"""T09 · install：追加不覆盖既有（Orca）、幂等、备份。"""

import json

from ferryman.install import install_cc


def _orca_settings(tmp_path):
    p = tmp_path / "settings.json"
    p.write_text(json.dumps({"hooks": {"UserPromptSubmit": [
        {"hooks": [{"type": "command", "command": "orca-hook.cmd", "timeout": 10}]},
    ]}}, ensure_ascii=False), encoding="utf-8")
    return p


def test_install_appends_keeps_orca_and_is_idempotent(tmp_path, capsys):
    p = _orca_settings(tmp_path)
    # 密闭性：显式指向不存在的 cc-switch.db，绝不碰本机真实 CC Switch 库
    install_cc(settings_path=p, ccswitch_db=tmp_path / "no-ccswitch.db")
    data = json.loads(p.read_text(encoding="utf-8"))

    ups = data["hooks"]["UserPromptSubmit"]
    assert any("orca-hook.cmd" in json.dumps(e, ensure_ascii=False) for e in ups)
    ferry = [e for e in ups if "ferryman" in json.dumps(e, ensure_ascii=False)]
    assert len(ferry) == 1
    assert "ferryman-gate.ps1" in json.dumps(ferry[0])
    assert ferry[0]["hooks"][0]["timeout"] == 3

    ss = data["hooks"]["SessionStart"]
    assert ss and ss[0]["matcher"] == "clear|startup"

    # T32：子代理生命周期钩子（SubagentStart/Stop → daemon 计数）
    for evt in ("SubagentStart", "SubagentStop"):
        entries = [e for e in data["hooks"].get(evt, [])
                   if "ferryman" in json.dumps(e, ensure_ascii=False)]
        assert len(entries) == 1
        assert "ferryman-subagent.ps1" in json.dumps(entries[0])

    install_cc(settings_path=p)                     # 幂等：ferryman 条目不重复
    data2 = json.loads(p.read_text(encoding="utf-8"))
    ups2 = data2["hooks"]["UserPromptSubmit"]
    assert len([e for e in ups2 if "ferryman" in json.dumps(e)]) == 1
    assert any("orca-hook.cmd" in json.dumps(e) for e in ups2)   # Orca 仍在

    # 备份存在（同秒内两次安装会覆盖同名备份，故只断言 ≥1）
    assert len(list(tmp_path.glob("settings.json.bak-ferryman-*"))) >= 1


# ---------- T36 · install-codex：hooks.json 注入 + 功能旗标 ----------

def test_install_codex_merges_and_enables_feature(tmp_path):
    from ferryman.install import install_codex
    hooks = tmp_path / "hooks.json"
    hooks.write_text(json.dumps({"hooks": {"Stop": [
        {"hooks": [{"type": "command", "command": "orca.cmd", "timeout": 10}]}]}},
        ensure_ascii=False), encoding="utf-8")
    cfg = tmp_path / "config.toml"
    cfg.write_text("[features]\ngoals = true\n", encoding="utf-8")

    n = install_codex(hooks_path=hooks, config_path=cfg)
    assert n == 2                                    # gate + restore 两条

    data = json.loads(hooks.read_text(encoding="utf-8"))
    ups = [e for e in data["hooks"]["UserPromptSubmit"]
           if "ferryman" in json.dumps(e)]
    assert len(ups) == 1 and "ferryman-gate-codex.ps1" in json.dumps(ups[0])
    assert any("orca.cmd" in json.dumps(e) for e in data["hooks"]["Stop"])  # Orca 保留
    ss = [e for e in data["hooks"]["SessionStart"] if "ferryman" in json.dumps(e)]
    assert ss[0]["hooks"][0]["timeout"] == 10        # 自举等待预算
    # 无控制字符（\a→BEL / \f→FF 事故的回归防线）
    assert not any(b in hooks.read_bytes() for b in (b"\x07", b"\x0c"))

    toml = cfg.read_text(encoding="utf-8")
    assert "hooks = true" in toml                    # 钩子默认关，必须开旗标


def test_install_codex_idempotent_and_creates_missing(tmp_path):
    from ferryman.install import install_codex
    hooks = tmp_path / "hooks.json"                  # 不存在 → 创建
    cfg = tmp_path / "config.toml"                   # 无 [features] 段 → 追加
    install_codex(hooks_path=hooks, config_path=cfg)
    install_codex(hooks_path=hooks, config_path=cfg)  # 幂等
    data = json.loads(hooks.read_text(encoding="utf-8"))
    assert sum("ferryman" in json.dumps(e)
               for e in data["hooks"]["UserPromptSubmit"]) == 1
    assert cfg.read_text(encoding="utf-8").count("hooks = true") == 1
