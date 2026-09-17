"""T34 · CC Switch 快照注入：检测到 cc-switch.db → 钩子进全部 claude 供应商快照。

机制（2026-09-17 T21 三轮实测定案，DESIGN §3）：CC Switch 切换供应商 =
把 providers.settings_config 快照**逐字写入** ~/.claude/settings.json——
不在快照里的键（hooks）每次切换 / Live 模式重写都会被抹（Orca 同样会灭）。
修复 = ferryman 钩子注进全部 claude 供应商快照：幂等、保留既有条目（Orca 共存）、
改库前自动备份。新增供应商后需重跑 `ferryman install-ccswitch`。
"""

import json
import sqlite3

from ferryman.install import inject_ccswitch


def _make_db(path, providers):
    """providers: list of (app_type, name, settings_config_dict)。"""
    conn = sqlite3.connect(path)
    conn.execute("CREATE TABLE providers (id INTEGER PRIMARY KEY, app_type TEXT, "
                 "name TEXT, settings_config TEXT)")
    for app, name, cfg in providers:
        conn.execute("INSERT INTO providers (app_type, name, settings_config) VALUES (?,?,?)",
                     (app, name, json.dumps(cfg, ensure_ascii=False)))
    conn.commit()
    conn.close()


ORCA_ENTRY = {"hooks": [{"type": "command",
                         "command": "C:/Users/allan716/.orca/agent-hooks/claude-hook.cmd || echo {}",
                         "timeout": 10}]}

FERRY_EVENTS = ("UserPromptSubmit", "SessionStart", "SubagentStart", "SubagentStop")


def test_inject_all_claude_providers_preserves_existing(tmp_path):
    db = tmp_path / "cc-switch.db"
    _make_db(db, [
        ("claude", "有Orca的", {"env": {"A": "1"}, "hooks": {
            "UserPromptSubmit": [json.loads(json.dumps(ORCA_ENTRY))],
            "Stop": [json.loads(json.dumps(ORCA_ENTRY))]}}),
        ("claude", "干净的", {"env": {"B": "2"}}),
        ("codex", "Codex家", {"auth": {}, "config": "x = 1"}),
    ])
    n = inject_ccswitch(db)
    assert n == 2                                            # 只动 claude

    conn = sqlite3.connect(db)
    rows = dict(conn.execute("SELECT name, settings_config FROM providers "
                             "WHERE app_type='claude'").fetchall())
    codex_raw = conn.execute("SELECT settings_config FROM providers "
                             "WHERE name='Codex家'").fetchone()[0]
    conn.close()

    for name, raw in rows.items():
        cfg = json.loads(raw)
        assert "env" in cfg                                  # 原有内容不动
        hooks = cfg["hooks"]
        for evt in FERRY_EVENTS:
            ferry = [e for e in hooks[evt] if "ferryman" in json.dumps(e, ensure_ascii=False)]
            assert len(ferry) == 1, (name, evt)

    # Orca 共存（同一事件数组里两条并存）
    cfg = json.loads(rows["有Orca的"])
    ups = cfg["hooks"]["UserPromptSubmit"]
    assert any("orca" in json.dumps(e) for e in ups)
    assert any("ferryman" in json.dumps(e) for e in ups)
    # 不归我们管的事件（Stop）原样保留
    assert len(cfg["hooks"]["Stop"]) == 1 and "orca" in json.dumps(cfg["hooks"]["Stop"])
    # SessionStart 带 matcher
    ss = [e for e in cfg["hooks"]["SessionStart"]
          if "ferryman" in json.dumps(e, ensure_ascii=False)]
    assert ss[0]["matcher"] == "clear|startup"
    # codex 供应商不动
    assert "ferryman" not in codex_raw


def test_inject_idempotent(tmp_path):
    db = tmp_path / "cc-switch.db"
    _make_db(db, [("claude", "P1", {"env": {}})])
    inject_ccswitch(db)
    inject_ccswitch(db)                                      # 第二次不重复
    conn = sqlite3.connect(db)
    raw = conn.execute("SELECT settings_config FROM providers").fetchone()[0]
    conn.close()
    hooks = json.loads(raw)["hooks"]
    assert sum("ferryman" in json.dumps(e) for e in hooks["SubagentStart"]) == 1


def test_inject_replaces_stale_ferryman_entries(tmp_path):
    """快照里已有旧 ferryman 条目（如手工注入过）→ 替换为最新版，不叠加。"""
    db = tmp_path / "cc-switch.db"
    stale = {"env": {}, "hooks": {"SubagentStart": [
        {"hooks": [{"type": "command", "command": "ferryman-old.ps1", "timeout": 9}]}]}}
    _make_db(db, [("claude", "P1", stale)])
    inject_ccswitch(db)
    conn = sqlite3.connect(db)
    raw = conn.execute("SELECT settings_config FROM providers").fetchone()[0]
    conn.close()
    subs = json.loads(raw)["hooks"]["SubagentStart"]
    assert len(subs) == 1 and "ferryman-subagent.ps1" in json.dumps(subs)


def test_no_db_is_silent_noop(tmp_path, capsys):
    n = inject_ccswitch(tmp_path / "nope.db")
    assert n == 0
    assert "未检测到" in capsys.readouterr().out


def test_backup_created_before_modifying(tmp_path):
    db = tmp_path / "cc-switch.db"
    _make_db(db, [("claude", "P1", {"env": {}})])
    inject_ccswitch(db)
    baks = list(tmp_path.glob("cc-switch.db.bak-ferryman-*"))
    assert len(baks) == 1
    # 备份是修改前的内容（无 ferryman）
    import sqlite3 as sq
    bak_raw = sq.connect(baks[0]).execute("SELECT settings_config FROM providers").fetchone()[0]
    assert "ferryman" not in bak_raw


def test_install_cc_auto_injects_ccswitch(tmp_path):
    from ferryman.install import install_cc
    db = tmp_path / "cc-switch.db"
    _make_db(db, [("claude", "P1", {"env": {}})])
    p = tmp_path / "settings.json"
    p.write_text("{}", encoding="utf-8")
    install_cc(settings_path=p, ccswitch_db=db)
    raw = sqlite3.connect(db).execute("SELECT settings_config FROM providers").fetchone()[0]
    assert "ferryman" in raw
