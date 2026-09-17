"""账本：append-only、按月滚动、白名单（隐私不变量）、过滤读取。"""
import time

import pytest

from ferryman.accounts import Accounts

AUG = time.mktime(time.strptime("2026-08-15 12:00:00", "%Y-%m-%d %H:%M:%S"))
SEP = time.mktime(time.strptime("2026-09-16 12:00:00", "%Y-%m-%d %H:%M:%S"))
MID = time.mktime(time.strptime("2026-09-01 12:00:00", "%Y-%m-%d %H:%M:%S"))  # 落在 (AUG, SEP) 内，since/until 断言与运行日期解耦


def rec_handoff(acc, ts=None, sid="s1", outcome="fresh", **kw):
    return acc.record("handoff", ts=ts, agent="cc", session_id=sid,
                      lineage_id=f"L-{sid}", project="C:/proj",
                      provider="glm", model="glm-5.3",
                      price_ver="glm@2026-09-17", prompt_tokens=100,
                      completion_tokens=50, outcome=outcome, wall_s=1.2, **kw)


def test_record_and_read(tmp_path):
    acc = Accounts(tmp_path)
    e = rec_handoff(acc)
    assert e["kind"] == "handoff" and e["v"] == 1 and e["ts"] > 0
    rows = acc.read()
    assert len(rows) == 1 and rows[0]["prompt_tokens"] == 100
    assert rows[0]["ts_iso"]  # 人读时间戳非空


def test_append_only_two_lines(tmp_path):
    acc = Accounts(tmp_path)
    rec_handoff(acc, sid="s1"); rec_handoff(acc, sid="s2")
    f = tmp_path / "accounts" / (time.strftime("%Y%m") + ".jsonl")
    assert len(f.read_text(encoding="utf-8").splitlines()) == 2   # 只增不改


def test_monthly_rollover(tmp_path):
    acc = Accounts(tmp_path)
    rec_handoff(acc, ts=AUG); rec_handoff(acc, ts=SEP)
    files = sorted(p.name for p in (tmp_path / "accounts").glob("*.jsonl"))
    assert files == ["202608.jsonl", "202609.jsonl"]


def test_privacy_whitelist(tmp_path):
    acc = Accounts(tmp_path)
    with pytest.raises(ValueError, match="隐私"):
        acc.record("handoff", agent="cc", session_id="s", lineage_id="L", project="p",
                   provider="x", model="m", price_ver=None,
                   prompt_tokens=1, completion_tokens=1, outcome="fresh",
                   wall_s=0.1, content="用户原话不应入账")
    with pytest.raises(ValueError, match="缺必填"):
        acc.record("block", agent="cc", session_id="s")   # 缺 prefix_tokens/idle_s


def test_unknown_kind(tmp_path):
    acc = Accounts(tmp_path)
    with pytest.raises(ValueError, match="未知科目"):
        acc.record("wage", agent="cc", session_id="s")


def test_filters(tmp_path):
    acc = Accounts(tmp_path)
    rec_handoff(acc, sid="s1", ts=MID)
    acc.record("block", ts=MID, agent="cc", session_id="s2", lineage_id="L2",
               project="C:/q", prefix_tokens=150_000, idle_s=2100)
    assert len(acc.read(kind="block")) == 1
    assert acc.read(kind="block")[0]["prefix_tokens"] == 150_000
    assert len(acc.read(session="s1")) == 1
    assert len(acc.read(project="C:/q")) == 1
    assert len(acc.read(since=SEP + 1)) == 0
    assert len(acc.read(until=AUG + 1)) == 0
    assert len(acc.read(lineage="L2")) == 1


def test_reserved_fields_stamped_not_passable(tmp_path):
    acc = Accounts(tmp_path)
    with pytest.raises(ValueError, match="保留字段"):
        rec_handoff(acc, v=2)
    with pytest.raises(ValueError, match="保留字段"):
        rec_handoff(acc, ts_iso="2020-01-01")


def test_read_skips_corrupt_tail_line(tmp_path):
    acc = Accounts(tmp_path)
    rec_handoff(acc)
    f = tmp_path / "accounts" / (time.strftime("%Y%m") + ".jsonl")
    with open(f, "a", encoding="utf-8") as fh:      # 模拟崩溃撕裂的尾行
        fh.write('{"v": 1, "kind": "handoff", TRUN')
    assert len(acc.read()) == 1                      # 好行仍在，坏行被跳过
