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
    install_cc(settings_path=p)
    data = json.loads(p.read_text(encoding="utf-8"))

    ups = data["hooks"]["UserPromptSubmit"]
    assert any("orca-hook.cmd" in json.dumps(e, ensure_ascii=False) for e in ups)
    ferry = [e for e in ups if "ferryman" in json.dumps(e, ensure_ascii=False)]
    assert len(ferry) == 1
    assert "ferryman-gate.ps1" in json.dumps(ferry[0])
    assert ferry[0]["hooks"][0]["timeout"] == 3

    ss = data["hooks"]["SessionStart"]
    assert ss and ss[0]["matcher"] == "clear|startup"

    install_cc(settings_path=p)                     # 幂等：ferryman 条目不重复
    data2 = json.loads(p.read_text(encoding="utf-8"))
    ups2 = data2["hooks"]["UserPromptSubmit"]
    assert len([e for e in ups2 if "ferryman" in json.dumps(e)]) == 1
    assert any("orca-hook.cmd" in json.dumps(e) for e in ups2)   # Orca 仍在

    # 备份存在（同秒内两次安装会覆盖同名备份，故只断言 ≥1）
    assert len(list(tmp_path.glob("settings.json.bak-ferryman-*"))) >= 1
