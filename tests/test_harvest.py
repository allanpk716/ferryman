"""用量采集：纯解析器与增量状态测试（设计 §3.7）。"""

import json
from pathlib import Path

from ferryman.accounts import Accounts
from ferryman.harvest import HarvestState, parse_usage_chunk


def _user_line(ts="2026-09-18T01:00:00Z", cwd="C:/proj"):
    return json.dumps({"type": "user", "timestamp": ts, "cwd": cwd,
                       "sessionId": "s1", "message": {"role": "user",
                                                      "content": "干活"}})


def _asst_line(ts="2026-09-18T01:00:05Z", model="glm-5.3", inp=100, cr=9000, cc=0, out=50):
    return json.dumps({"type": "assistant", "timestamp": ts,
                       "message": {"role": "assistant", "model": model,
                                   "content": [{"type": "text", "text": "好"}],
                                   "usage": {"input_tokens": inp,
                                             "cache_read_input_tokens": cr,
                                             "cache_creation_input_tokens": cc,
                                             "output_tokens": out}}})


def test_parse_extracts_assistant_usage_and_cwd():
    chunk = _user_line() + "\n" + _asst_line() + "\n"
    rows, title, cwd = parse_usage_chunk(chunk)
    assert cwd == "C:/proj"
    assert title == ""
    assert len(rows) == 1
    r = rows[0]
    assert r["input_tokens"] == 100 and r["cache_read_tokens"] == 9000
    assert r["cache_creation_tokens"] == 0 and r["output_tokens"] == 50
    assert r["model"] == "glm-5.3"
    from datetime import datetime
    expected = datetime.fromisoformat("2026-09-18T01:00:05+00:00").timestamp()
    assert r["ts"] == expected
    assert "content" not in r and "message" not in r  # 隐私：无消息内容


def test_parse_tracks_ai_title():
    title_line = json.dumps({"type": "ai-title", "aiTitle": "修登录bug"})
    chunk = title_line + "\n" + _asst_line() + "\n"
    rows, title, cwd = parse_usage_chunk(chunk)
    assert title == "修登录bug"
    assert len(rows) == 1


def test_parse_skips_malformed_and_bare_lines():
    chunk = ("{broken json\n" + _asst_line() + "\n"
             + json.dumps({"type": "user", "message": {"role": "user", "content": "x"}}) + "\n"
             + json.dumps({"type": "assistant", "message": {"role": "assistant",
                                                            "content": "无用量"}}) + "\n")
    rows, _t, _c = parse_usage_chunk(chunk)
    assert len(rows) == 1            # 只有带 usage 的 assistant 出行


def test_parse_skips_nondict_and_null_token_lines():
    """病态行防御：数组行/message 为字符串/token 为 null——跳过不抛且不伤及好行（审查 Nit）。"""
    chunk = ("[1, 2]\n"
             + json.dumps({"type": "assistant", "message": "字符串消息"}) + "\n"
             + json.dumps({"type": "assistant", "message": {"role": "assistant",
                                                            "usage": {"input_tokens": None}}}) + "\n"
             + _asst_line() + "\n")
    rows, _t, _c = parse_usage_chunk(chunk)
    assert len(rows) == 1 and rows[0]["output_tokens"] == 50


def _mk_session(p: Path):
    p.write_text(_user_line() + "\n" + _asst_line() + "\n", encoding="utf-8")


def test_incremental_and_offsets(tmp_path):
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    _mk_session(f)
    hs = HarvestState(acc)
    rows = hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    assert len(rows) == 1
    assert rows[0]["offset"] == f.stat().st_size      # 本批结束偏移
    assert rows[0]["title"] == "" and rows[0]["project"] == "C:/proj"
    # 无新增 → 空
    assert hs.maybe_harvest(f, f.stat().st_size, agent="cc") == []
    # 追加一条 assistant → 只有新增量
    with open(f, "a", encoding="utf-8") as fh:
        fh.write(_asst_line(ts="2026-09-18T01:01:00Z") + "\n")
    rows2 = hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    assert len(rows2) == 1 and rows2[0]["offset"] == f.stat().st_size


def test_partial_line_held_back(tmp_path):
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    f.write_text(_user_line() + "\n", encoding="utf-8")
    hs = HarvestState(acc)
    hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    half = _asst_line()[:30]                          # 无换行的半行
    with open(f, "a", encoding="utf-8") as fh:
        fh.write(half)
    assert hs.maybe_harvest(f, f.stat().st_size, agent="cc") == []
    with open(f, "a", encoding="utf-8") as fh:        # 补完
        fh.write(_asst_line()[30:] + "\n")
    rows = hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    assert len(rows) == 1


def test_title_carried_across_batches(tmp_path):
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    title_line = json.dumps({"type": "ai-title", "aiTitle": "起名了"})
    f.write_text(title_line + "\n", encoding="utf-8")
    hs = HarvestState(acc)
    hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    with open(f, "a", encoding="utf-8") as fh:
        fh.write(_asst_line() + "\n")
    rows = hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    assert rows and rows[0]["title"] == "起名了"       # 标题跨批次携带


def test_resume_from_accounts(tmp_path):
    """重启恢复：新 HarvestState 从账本行恢复偏移与标题，不重复采集。"""
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    f.write_text(json.dumps({"type": "ai-title", "aiTitle": "旧名"}) + "\n"
                 + _asst_line() + "\n", encoding="utf-8")
    hs = HarvestState(acc)
    for r in hs.maybe_harvest(f, f.stat().st_size, agent="cc"):
        acc.record("usage", ts=r["ts"], agent="cc", session_id="s1",
                   lineage_id="L", project=r["project"], model=r["model"],
                   title=r["title"], input_tokens=r["input_tokens"],
                   cache_read_tokens=r["cache_read_tokens"],
                   cache_creation_tokens=r["cache_creation_tokens"],
                   output_tokens=r["output_tokens"], offset=r["offset"])
    with open(f, "a", encoding="utf-8") as fh:        # 停机期间新增
        fh.write(_asst_line(ts="2026-09-18T02:00:00Z") + "\n")
    hs2 = HarvestState(acc)                            # "重启"
    rows = hs2.maybe_harvest(f, f.stat().st_size, agent="cc")
    assert len(rows) == 1                              # 只采新增
    assert rows[0]["title"] == "旧名"                  # 标题也已恢复


def test_shrink_rereads(tmp_path):
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    _mk_session(f)
    hs = HarvestState(acc)
    hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    f.write_text(_asst_line(ts="2026-09-18T03:00:00Z") + "\n", encoding="utf-8")
    rows = hs.maybe_harvest(f, f.stat().st_size, agent="cc")   # 收缩→从头重采
    assert len(rows) == 1


def test_resume_survives_null_offset_row(tmp_path):
    """账本毒药行防御：usage 行 offset 为 null——构造不抛，从 0 正常恢复（审查 Nit）。"""
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    _mk_session(f)
    with open(acc.dir / "209901.jsonl", "a", encoding="utf-8") as fh:   # 手写毒药行，不经 record()
        fh.write(json.dumps({"kind": "usage", "agent": "cc", "session_id": "s1",
                             "offset": None, "model": "glm-5.3",
                             "input_tokens": 1, "cache_read_tokens": 0,
                             "cache_creation_tokens": 0, "output_tokens": 0}) + "\n")
    hs = HarvestState(acc)                            # 不抛
    rows = hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    assert len(rows) == 1 and rows[0]["offset"] == f.stat().st_size


def _asst_mid(mid, ts="2026-09-18T05:00:00Z"):
    return json.dumps({"type": "assistant", "timestamp": ts,
                       "message": {"role": "assistant", "id": mid, "model": "glm-5.3",
                                   "content": [{"type": "text", "text": "x"}],
                                   "usage": {"input_tokens": 1, "cache_read_input_tokens": 2,
                                             "cache_creation_input_tokens": 0,
                                             "output_tokens": 3}}})


def test_duplicate_message_id_suppressed_in_batch(tmp_path):
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    f.write_text(_asst_mid("m1") + "\n" + _asst_mid("m1", ts="2026-09-18T05:00:04Z") + "\n",
                 encoding="utf-8")
    rows = HarvestState(acc).maybe_harvest(f, f.stat().st_size, agent="cc")
    assert len(rows) == 1 and "msg_id" not in rows[0]


def test_duplicate_message_id_suppressed_across_batches(tmp_path):
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    f.write_text(_asst_mid("m1") + "\n", encoding="utf-8")
    hs = HarvestState(acc)
    assert len(hs.maybe_harvest(f, f.stat().st_size, agent="cc")) == 1
    with open(f, "a", encoding="utf-8") as fh:      # 数秒后的重写
        fh.write(_asst_mid("m1", ts="2026-09-18T05:00:09Z") + "\n")
    assert hs.maybe_harvest(f, f.stat().st_size, agent="cc") == []


def test_distinct_or_absent_ids_pass(tmp_path):
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    f.write_text(_asst_mid("m1") + "\n" + _asst_mid("m2") + "\n" + _asst_line() + "\n",
                 encoding="utf-8")                  # m2 不同 id；第三条无 id
    rows = HarvestState(acc).maybe_harvest(f, f.stat().st_size, agent="cc")
    assert len(rows) == 3
