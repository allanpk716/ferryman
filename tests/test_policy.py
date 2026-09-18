# tests/test_policy.py
"""策略计算器：闭式公式，断言锚定实验报告 §4 的 GLM 实测数字。"""
import math

import pytest

from ferryman.policy import (
    HeartbeatPolicy, NoCachePriceError, heartbeat_policy, strategy_costs, tier_for,
)
from ferryman.prices import PriceBook, PriceVersion

BOOK = PriceBook(key="glm", unit="智谱积分", per=10_000,
                 versions=(PriceVersion("2026-09-17", 6.9, 1.7, 24),))
PV = BOOK.versions[0]


def test_glm_150k_report_numbers():
    pol = heartbeat_policy(BOOK, PV, ttl_s=600, prefix_tokens=150_000)
    assert pol.tau_s == pytest.approx(480)                      # 0.8×10min
    assert pol.per_beat_cost == pytest.approx(26.2, abs=0.1)    # 25.5 + 0.72
    assert pol.expire_cost == pytest.approx(103.5)              # 15×6.9
    assert pol.worthwhile_cap_s == pytest.approx(1428, rel=0.03)  # ~24min（报告口径 25–28 带）
    assert pol.grace_s == 300 and pol.min_prefix_tokens == 30_000


def test_small_prefix_shrinks_cap():
    big = heartbeat_policy(BOOK, PV, 600, 150_000)
    small = heartbeat_policy(BOOK, PV, 600, 30_000)
    assert small.worthwhile_cap_s < big.worthwhile_cap_s   # 300×P_out 占比变大


def test_no_cache_price_refuses():
    nb = PriceBook("x", "元", 1_000_000, (PriceVersion("2026-09-01", 1.0, None, 2.0),))
    with pytest.raises(NoCachePriceError):
        heartbeat_policy(nb, nb.versions[0], 600, 100_000)


def test_bad_ttl():
    with pytest.raises(ValueError, match="ttl_s"):
        heartbeat_policy(BOOK, PV, 0, 100_000)


def test_tiers():
    pol = heartbeat_policy(BOOK, PV, 600, 150_000)
    assert tier_for(pol, 400) == "none"       # ≤τ：缓存必活
    assert tier_for(pol, 900) == "beat"       # τ..cap
    assert tier_for(pol, 10_000) == "expire"  # >cap


def test_strategy_costs():
    sc = strategy_costs(BOOK, PV, ttl_s=600, wait_s=900, prefix_tokens=150_000)
    assert sc["none"] == pytest.approx(103.5)          # 900s > TTL 600s → 全量重付
    assert sc["beat"] == pytest.approx(2 * 26.2, abs=0.2)   # ⌈900/480⌉ = 2 跳
    assert sc["expire"] == pytest.approx(103.5)
    assert sc["expire_compact"] == pytest.approx(0.25 * 103.5)
    sc2 = strategy_costs(BOOK, PV, 600, 300, 150_000)
    assert sc2["none"] == 0.0                          # 300s ≤ TTL：白等
    assert sc2["beat"] == pytest.approx(1 * 26.2, abs=0.1)
