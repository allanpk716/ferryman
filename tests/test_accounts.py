"""账本：append-only、按月滚动、白名单（隐私不变量）、过滤读取。"""
import time

import pytest

from ferryman.accounts import Accounts
from helpers import MIN_CTX, write_session

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


def test_read_skips_corrupt_tail_line(tmp_path, capsys):
    acc = Accounts(tmp_path)
    rec_handoff(acc)
    f = tmp_path / "accounts" / (time.strftime("%Y%m") + ".jsonl")
    with open(f, "a", encoding="utf-8") as fh:      # 模拟崩溃撕裂的尾行
        fh.write('{"v": 1, "kind": "handoff", TRUN')
    assert len(acc.read()) == 1                      # 好行仍在，坏行被跳过
    captured = capsys.readouterr()
    assert "跳过损坏行" in captured.err               # 终审：告警走 stderr——不污染 --json 的 stdout
    assert captured.out == ""                        # stdout 保持机器可解析（无撕裂尾线）


def test_ferry_completion_books_handoff(h):
    """端到端：合成会话达总结阈值 → fake 摆渡 → 账本出现 handoff 流水。"""
    write_session(h.projects, "acct1", "C:/proj")
    assert h.wait_for(lambda: any(e["kind"] == "handoff"
                                  for e in h.accounts.read()))
    e = h.accounts.read(kind="handoff")[0]
    assert e["session_id"] == "acct1"
    assert e["lineage_id"].endswith("acct1.jsonl")     # lineage = 归一化 transcript 路径
    assert e["provider"] == "fake"
    assert e["outcome"] in ("fresh", "skeleton", "failed")
    assert "content" not in e and "md" not in e        # 隐私不变量：无正文


def test_failed_ferry_books_exactly_one_row(tmp_path, monkeypatch):
    """终审：失败摆渡只记一行 outcome=failed——骨架产物不另记行（append-only 从零起账，
    双行无法事后修复）。"""
    from helpers import Harness

    def exploding(path, provider, agent="cc"):
        raise RuntimeError("provider down")

    harness = Harness(tmp_path, monkeypatch, fake_ferry=exploding)
    sid = "acct-fail1"
    try:
        write_session(harness.projects, sid, "C:/proj")
        assert harness.wait_for(lambda: any(
            e["session_id"] == sid and e["status"] == "skeleton"
            for e in harness.store._index["handoffs"])), "骨架降级未发生"
        assert harness.wait_for(lambda: any(
            e["session_id"] == sid
            for e in harness.accounts.read(kind="handoff"))), "失败行未入账"
        time.sleep(0.5)                      # 留出潜在第二行落盘的窗口（RED 期双行必现）
        rows = [e for e in harness.accounts.read(kind="handoff")
                if e["session_id"] == sid]
        assert len(rows) == 1, f"失败摆渡应只记一行，实记 {len(rows)} 行"
        assert rows[0]["outcome"] == "failed"
    finally:
        harness.stop()


def test_booking_failure_never_breaks_ferry(h, monkeypatch):
    """记账抛异常（坏价格表）时摆渡照常产出交接——骨架兜底不变量优先。"""
    import ferryman.daemon as daemon_mod
    from ferryman.accounts import Accounts

    def boom(self, *a, **k):
        raise RuntimeError("坏 [prices.*] TOML")

    monkeypatch.setattr(daemon_mod.FerryWorker, "_book_handoff", boom)
    write_session(h.projects, "acct-boom", "C:/proj")
    assert h.wait_for(lambda: h.store.restore_candidates("cc", "C:/proj"))
    # 摆渡产物存在（fresh 或 skeleton），且 worker 线程未死
    assert h.worker.is_alive()


def _stale_blocked_session(h):
    """造一个已达拦截阈值、有有效交接的会话（enforce 下必被拦）。"""
    write_session(h.projects, "acct2", "C:/proj")
    assert h.wait_for(lambda: h.store.restore_candidates("cc", "C:/proj"))
    body = {"agent": "cc", "session_id": "acct2",
            "transcript_path": str(h.projects / "C--proj" / "acct2.jsonl"),
            "cwd": "C:/proj", "prompt": "继续干活"}
    return body


def test_block_books_entry(h):
    body = _stale_blocked_session(h)
    r = h.gate(body)
    if r["decision"] == "allow":        # observe/骨架时序兜底：等到 block 为止
        assert h.wait_for(lambda: h.gate(body)["decision"] == "block")
    e = h.accounts.read(kind="block")[-1]
    assert e["session_id"] == "acct2"
    assert e["prefix_tokens"] >= MIN_CTX or e["prefix_tokens"] == 0  # peak_ctx 尽力而为
    assert e["idle_s"] > 0


def test_bypass_books_entry(h):
    body = _stale_blocked_session(h)
    r = h.gate({**body, "prompt": "强续 无论如何继续"})
    assert r["decision"] == "allow"
    e = h.accounts.read(kind="bypass")[-1]
    assert e["session_id"] == "acct2"


def test_restore_books_inject(h):
    body = _stale_blocked_session(h)
    assert h.wait_for(lambda: h.gate(body)["decision"] == "block")
    r = h.get(f"/restore?agent=cc&cwd=C:/proj&session_id=newsid")
    assert r["context"]
    e = h.accounts.read(kind="inject")[-1]
    assert e["session_id"] == "newsid"
    assert e["tokens"] > 0 and e["handoff_id"]
    blk = h.accounts.read(kind="block")[-1]        # R9：inject 与 block 同谱系（Q7 因果链）
    assert e["lineage_id"] == blk["lineage_id"]


def test_window_books_on_subagent_cycle(h):
    h.sub({"event": "start", "agent": "cc", "session_id": "acct3"})
    h.sub({"event": "start", "agent": "cc", "session_id": "acct3"})   # 嵌套：计数 2
    h.sub({"event": "stop", "agent": "cc", "session_id": "acct3"})
    h.sub({"event": "stop", "agent": "cc", "session_id": "acct3"})    # 计数归零 → 闭窗
    assert h.wait_for(lambda: h.accounts.read(kind="window"))
    e = h.accounts.read(kind="window")[0]
    assert e["session_id"] == "acct3"
    assert e["dur_s"] >= 0 and e["close_reason"] == "subagents_done"
    assert set(e) >= {"opened_ts", "closed_ts", "dur_s", "prefix_tokens"}


def test_window_closes_on_prompt(h):
    h.sub({"event": "start", "agent": "cc", "session_id": "acct4"})
    r = h.gate({"agent": "cc", "session_id": "acct4", "prompt": "人回来了"})
    assert r["decision"] == "allow"
    e = h.accounts.read(kind="window")[-1]
    assert e["close_reason"] == "prompt"
    # 窗已闭：后续 stop 不再产生第二条
    h.sub({"event": "stop", "agent": "cc", "session_id": "acct4"})
    assert len([x for x in h.accounts.read(kind="window")
                if x["session_id"] == "acct4"]) == 1


def test_window_closes_on_bypass_prompt(h):
    """R1：强续/bypass 亦是主会话恢复写入——等待窗口同样闭窗（钩子须在 bypass 分支之前）。"""
    h.sub({"event": "start", "agent": "cc", "session_id": "acct5"})
    r = h.gate({"agent": "cc", "session_id": "acct5", "prompt": "强续 继续"})
    assert r["decision"] == "allow" and r["reason"] == "bypass"
    e = h.accounts.read(kind="window")[-1]
    assert e["session_id"] == "acct5" and e["close_reason"] == "prompt"


def test_window_reanchors_after_leak_gap(h):
    from ferryman.ledger import SUBAGENT_EVENT_LEAK_S, now_s
    h.sub({"event": "start", "agent": "cc", "session_id": "acct6"})
    # 模拟 Stop 丢失 + 泄漏超时后再次 start：旧窗不沿用（否则 dur_s 虚高跨越泄漏间隙）
    h.daemon._windows[("cc", "acct6")] = {"opened_ts": now_s() - SUBAGENT_EVENT_LEAK_S - 60}
    h.sub({"event": "start", "agent": "cc", "session_id": "acct6"})
    h.sub({"event": "stop", "agent": "cc", "session_id": "acct6"})
    h.sub({"event": "stop", "agent": "cc", "session_id": "acct6"})
    e = [x for x in h.accounts.read(kind="window") if x["session_id"] == "acct6"][-1]
    assert e["dur_s"] < SUBAGENT_EVENT_LEAK_S


def test_usage_kind_roundtrip(tmp_path):
    acc = Accounts(tmp_path)
    e = acc.record("usage", ts=1758150005.0, agent="cc", session_id="s1",
                   lineage_id="L1", project="C:/proj", model="glm-5.3",
                   title="修登录bug", input_tokens=100, cache_read_tokens=9000,
                   cache_creation_tokens=0, output_tokens=50, offset=2048)
    assert e["kind"] == "usage" and e["offset"] == 2048
    with pytest.raises(ValueError, match="不落这些字段"):
        acc.record("usage", agent="cc", session_id="s1", model="m", title="",
                   input_tokens=1, cache_read_tokens=0, cache_creation_tokens=0,
                   output_tokens=0, offset=1, message_content="泄漏")
