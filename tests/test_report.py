# tests/test_report.py
"""report：族系账单、成效公式 v1、策略对比、复算附录。"""
import json
import time

import pytest

from ferryman import report
from ferryman.accounts import Accounts
from ferryman.prices import load_prices

TOML = """
[prices.glm]
unit = "智谱积分"
per = 10000
[[prices.glm.versions]]
effective_from = "2026-09-01"
p_in = 6.9
p_cache = 1.7
p_out = 24

[heartbeat]
ttl_s = 600
"""


@pytest.fixture
def env(tmp_path, monkeypatch):
    (tmp_path / "config.toml").write_text(TOML, encoding="utf-8")
    monkeypatch.setenv("FERRYMAN_CONFIG", str(tmp_path / "config.toml"))
    acc = Accounts(tmp_path / "data")
    # 族系 L1：一次 block(S=150k) + 一次 inject(2200) + 一次 handoff(50k in/2k out)
    acc.record("block", agent="cc", session_id="s1", lineage_id="L1",
               project="C:/p", prefix_tokens=150_000, idle_s=2100)
    acc.record("inject", agent="cc", session_id="s2", lineage_id="L1",
               project="C:/p", tokens=2200, handoff_id="h1")
    acc.record("handoff", agent="cc", session_id="s1", lineage_id="L1",
               project="C:/p", provider="glm", model="glm-5.3",
               price_ver="glm@2026-09-01", prompt_tokens=50_000,
               completion_tokens=2_000, outcome="fresh", wall_s=9.9)
    acc.record("bypass", agent="cc", session_id="s3", lineage_id="L2",
               project="C:/p", prefix_tokens=150_000)
    return acc, load_prices(tmp_path / "config.toml"), tmp_path


def test_savings_v1_math(env):
    acc, books, _ = env
    s = report.savings_v1(acc.read(), books, books["glm"])
    row = [r for r in s["lineages"] if r["lineage_id"] == "L1"][0]
    assert row["blocks"] == 1 and row["injects"] == 1 and row["handoffs"] == 1
    assert row["gross"] == pytest.approx(78.0)            # 15×(6.9−1.7)
    assert row["inject_cost"] == pytest.approx(1.518)     # 0.22×6.9
    assert row["handoff_cost"] == pytest.approx(39.3)     # 5×6.9 + 0.2×24
    assert row["net"] == pytest.approx(78.0 - 1.518 - 39.3)
    assert s["totals"]["bypass"] == 1                     # 对照组单列
    assert s["formula"] == "v1"


def test_unpriced_provider_listed(env):
    acc, books, tmp_path = env
    acc.record("handoff", agent="cc", session_id="s9", lineage_id="L9",
               project="C:/p", provider="whoever", model="m",
               price_ver=None, prompt_tokens=1, completion_tokens=1,
               outcome="fresh", wall_s=0.1)
    s = report.savings_v1(acc.read(), books, books["glm"])
    assert "whoever" in s["unpriced"]


def test_strategy_table(env):
    acc, books, _ = env
    now = time.time()
    acc.record("window", agent="cc", session_id="s1", lineage_id="L1",
               project="C:/p", opened_ts=now - 900, closed_ts=now,
               dur_s=900, prefix_tokens=150_000, close_reason="subagents_done")
    t = report.strategy_table(acc.read(kind="window"), books, ttl_s=600,
                              econ_key="glm")
    assert len(t["rows"]) == 1
    r = t["rows"][0]
    assert r["none"] == pytest.approx(103.5)      # 900s > TTL
    assert r["beat"] == pytest.approx(2 * 26.2, abs=0.2)
    assert r["expire_compact"] == pytest.approx(25.875)
    assert r["best"] == "expire_compact"


def test_strategy_table_skips_without_ttl_or_cache(env):
    acc, books, _ = env
    t = report.strategy_table(acc.read(kind="window"), books, ttl_s=0,
                              econ_key="glm")
    assert t["rows"] == [] and t["skipped"]


def test_run_json_output(env, tmp_path, monkeypatch):
    acc, books, tmp = env
    monkeypatch.setattr(report, "_accounts_for", lambda cfg_: acc)
    class A:  # 最小 args namespace
        since = until = project = session = kind = None
        json = True; provider = None
    assert report.run(A()) == 0


# ---------- 终审修复：--until 含当日 ----------

SEP30_2300 = time.mktime(time.strptime("2026-09-30 23:00:00",
                                       "%Y-%m-%d %H:%M:%S"))


def test_until_day_inclusive_read(env):
    """终审：--until 2026-09-30 必须含 9-30 当天 23:00 的流水（月账主形态）。"""
    acc, books, _ = env
    acc.record("block", ts=SEP30_2300, agent="cc", session_id="sE",
               lineage_id="LE", project="C:/p", prefix_tokens=150_000, idle_s=60)
    rows = acc.read(until=report._parse_date("2026-09-30", end_of_day=True))
    assert any(r["lineage_id"] == "LE" for r in rows)
    # 旧口径（午夜截断）确实会把当日排掉——锁定行为差异真实存在
    assert not any(r["lineage_id"] == "LE"
                   for r in acc.read(until=report._parse_date("2026-09-30")))


def test_run_json_until_includes_last_day(env, monkeypatch, capsys):
    """终审：report.run 的 --until 走含当日口径（JSON 端到端）。"""
    acc, books, _ = env
    acc.record("bypass", ts=SEP30_2300, agent="cc", session_id="sE",
               lineage_id="LE", project="C:/p", prefix_tokens=150_000)
    monkeypatch.setattr(report, "_accounts_for", lambda cfg_: acc)

    class A:
        since = "2026-09-01"; until = "2026-09-30"
        project = session = kind = None
        json = True; provider = None

    assert report.run(A()) == 0
    payload = json.loads(capsys.readouterr().out)
    lids = {r["lineage_id"] for r in payload["savings"]["lineages"]}
    assert "LE" in lids, "--until 末日 23:00 的流水被排除"


# ---------- 终审修复：策略口径披露 ----------

def _add_window(acc):
    now = time.time()
    acc.record("window", agent="cc", session_id="s1", lineage_id="L1",
               project="C:/p", opened_ts=now - 900, closed_ts=now,
               dur_s=900, prefix_tokens=150_000, close_reason="subagents_done")


def test_strategy_table_stamps_formula_and_ratio(env):
    acc, books, _ = env
    _add_window(acc)
    t = report.strategy_table(acc.read(kind="window"), books, ttl_s=600,
                              econ_key="glm")
    assert t["formula"] == report.STRATEGY_FORMULA == "v1"
    assert t["compact_ratio"] == report.COMPACT_RATIO == 0.25


def test_strategy_caveats_in_json_and_text(env, monkeypatch, capsys):
    """终审：口径披露两行进入 --json payload 与文本报表；公式在 render 头部盖章。"""
    acc, books, _ = env
    _add_window(acc)
    monkeypatch.setattr(report, "_accounts_for", lambda cfg_: acc)
    monkeypatch.setattr(report, "load_prices", lambda: books)  # 隔离 ~/ferryman 默认路径

    class A:  # 最小 args namespace
        since = until = project = session = kind = None
        json = True; provider = None

    assert report.run(A()) == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["strategy"]["formula"] == "v1"
    assert payload["strategy"]["compact_ratio"] == 0.25
    assert len(payload["strategy_caveats"]) == 2
    assert any("口径披露" in c for c in payload["strategy_caveats"])
    assert any("不得作为心跳授权依据" in c for c in payload["strategy_caveats"])

    text = report.render_text(report.savings_v1(acc.read(), books, books["glm"]),
                              payload["strategy"], books, "glm", {})
    assert "口径披露" in text and "不得作为心跳授权依据" in text
    assert f"策略公式：{report.STRATEGY_FORMULA}" in text
