"""T38 · ferryman doctor：一键体检——今天的两类事故（CC Switch 抹钩子、控制字符坏路径）
都是静默失效，doctor 一跑现形。全部检查器纯函数化（路径/探针可注入，测试密闭）。"""

import json

from ferryman.doctor import (check_hook_scripts, check_cc_hooks,
                             check_ccswitch, check_codex, check_daemon,
                             check_launcher)


def _write_settings(p, hooks):
    p.write_text(json.dumps({"hooks": hooks}, ensure_ascii=False), encoding="utf-8")


def test_cc_hooks_all_present(tmp_path):
    from ferryman.install import install_cc
    p = tmp_path / "settings.json"
    p.write_text("{}", encoding="utf-8")
    install_cc(settings_path=p, ccswitch_db=tmp_path / "no.db", data_dir=tmp_path)
    ok, msg = check_cc_hooks(p, repo=None)
    assert ok, msg


def test_cc_hooks_missing_reported(tmp_path):
    p = tmp_path / "settings.json"
    _write_settings(p, {})                                   # 被 CC Switch 抹掉的样子
    ok, msg = check_cc_hooks(p, repo=None)
    assert not ok and "UserPromptSubmit" in msg


def test_hook_scripts_bom_and_control_chars(tmp_path):
    good = tmp_path / "good.ps1"
    good.write_bytes("\ufeff# ok\n".encode("utf-8"))
    nobom = tmp_path / "nobom.ps1"
    nobom.write_bytes(b"# bad\n")                            # 无 BOM（PS5.1 中文注释地雷）
    ctrl = tmp_path / "ctrl.ps1"
    ctrl.write_bytes("\ufeff$a = 'x\x0cy'\n".encode("utf-8"))  # FF 控制字符（今日事故）
    checks = check_hook_scripts([good, nobom, ctrl])
    assert checks[0][0] and not checks[1][0] and not checks[2][0]
    assert "BOM" in checks[1][1] and "控制字符" in checks[2][1]


def test_ccswitch_snapshot_coverage(tmp_path):
    import sqlite3
    db = tmp_path / "cc-switch.db"
    conn = sqlite3.connect(db)
    conn.execute("CREATE TABLE providers (id INTEGER PRIMARY KEY, app_type TEXT, "
                 "name TEXT, settings_config TEXT)")
    conn.execute("INSERT INTO providers VALUES (1,'claude','P1','{}')")   # 无钩子 → 缺
    full = {"hooks": {evt: [{"hooks": [{"command": "ferryman.ps1"}]}]
                      for evt in ("UserPromptSubmit", "SessionStart",
                                  "SubagentStart", "SubagentStop")}}
    conn.execute("INSERT INTO providers VALUES (2,'claude','P2',?)",
                 (json.dumps(full),))
    conn.commit()
    conn.close()
    ok, msg = check_ccswitch(db)
    assert not ok and "P1" in msg and "P2" not in msg        # 点名缺钩子的供应商


def test_codex_hooks_and_flag(tmp_path):
    hooks = tmp_path / "hooks.json"
    all4 = {evt: [{"hooks": [{"command": "ferryman.ps1"}]}]
            for evt in ("UserPromptSubmit", "SessionStart",
                        "SubagentStart", "SubagentStop")}
    hooks.write_text(json.dumps({"hooks": all4}), encoding="utf-8")
    cfg = tmp_path / "config.toml"
    cfg.write_text("[features]\nhooks = true\n", encoding="utf-8")
    ok, msg = check_codex(hooks, cfg)
    assert ok, msg
    cfg.write_text("[features]\n", encoding="utf-8")          # 旗标关
    ok, msg = check_codex(hooks, cfg)
    assert not ok and "hooks = true" in msg


def test_daemon_probe(tmp_path):
    ok_down, msg_down = check_daemon(probe=lambda: None, pid_file=tmp_path / "no.pid")
    assert not ok_down and "未运行" in msg_down
    ok_up, msg_up = check_daemon(probe=lambda: {"health_alert": False},
                                 pid_file=tmp_path / "no.pid")
    assert ok_up and "ok" in msg_up


def test_ferry_provider_check():
    from ferryman.doctor import check_ferry_provider
    from ferryman.ferry import Provider
    ok, msg = check_ferry_provider("", {})                       # 未配置
    assert not ok and "未配置" in msg
    ok, msg = check_ferry_provider("mine", {})                   # 名字无定义
    assert not ok and "未在" in msg
    ok, msg = check_ferry_provider(
        "mine", {"mine": Provider(name="mine", base_url="http://x", model="m")})
    assert ok, msg


def test_launcher(tmp_path):
    ok, msg = check_launcher(tmp_path / "no.cmd", repo=None)
    assert not ok
    from ferryman.install import ensure_launcher
    ensure_launcher(data_dir=tmp_path, repo=tmp_path)
    ok, msg = check_launcher(tmp_path / "start-daemon.cmd", repo=tmp_path)
    assert ok, msg
