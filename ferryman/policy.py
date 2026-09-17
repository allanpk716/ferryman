# ferryman/policy.py
"""策略计算器：价格表 + 实测 TTL → 心跳参数与分档（闭式推导，设计 §2）。

公式出处：docs/20260917_1630 实验报告 §4；金额单位 = 价格表自述单位（按 per 归一）。
p_cache 缺省 → NoCachePriceError（宁可不算，不造数，Q16）。
"""

from __future__ import annotations

import math
from dataclasses import dataclass

from .prices import PriceBook, PriceVersion


class NoCachePriceError(ValueError):
    """无 p_cache：无缓存经济，拒绝推导心跳参数。"""


@dataclass(frozen=True)
class HeartbeatPolicy:
    ttl_s: float
    tau_s: float                 # 0.8 × T
    per_beat_cost: float         # 单次心跳成本
    expire_cost: float           # 放任过期代价
    worthwhile_cap_s: float      # 心跳划算的等待上限
    grace_s: float
    min_prefix_tokens: int


def heartbeat_policy(book: PriceBook, pv: PriceVersion, ttl_s: float,
                     prefix_tokens: int, *, beat_out_tokens: int = 300,
                     safety: float = 0.8, grace_s: float = 300.0,
                     min_prefix_tokens: int = 30_000) -> HeartbeatPolicy:
    if ttl_s <= 0:
        raise ValueError("ttl_s 须 > 0（先跑 experiments/cache-ttl 套件实测）")
    if pv.p_cache is None:
        raise NoCachePriceError(f"{book.key} 无 p_cache，拒绝推导心跳参数")
    pin = prefix_tokens / book.per * pv.p_in
    pcache = prefix_tokens / book.per * pv.p_cache
    per_beat = pcache + beat_out_tokens / book.per * pv.p_out
    tau = safety * ttl_s
    cap = tau * (pin - pcache) / per_beat if per_beat > 0 else math.inf
    return HeartbeatPolicy(ttl_s=ttl_s, tau_s=tau, per_beat_cost=per_beat,
                           expire_cost=pin, worthwhile_cap_s=cap,
                           grace_s=grace_s, min_prefix_tokens=min_prefix_tokens)


def tier_for(pol: HeartbeatPolicy, wait_s: float) -> str:
    if wait_s <= pol.tau_s:
        return "none"
    if wait_s <= pol.worthwhile_cap_s:
        return "beat"
    return "expire"


def strategy_costs(book: PriceBook, pv: PriceVersion, ttl_s: float,
                   wait_s: float, prefix_tokens: int, *,
                   beat_out_tokens: int = 300,
                   compact_ratio: float = 0.25) -> dict:
    """缓存经济学三策略 + compact 变体；handoff 策略由 report 按账本均值另算（§3.4）。

    compact_ratio 为公式内常数（v1 取 0.25，复算附录可见）。
    """
    pol = heartbeat_policy(book, pv, ttl_s, prefix_tokens,
                           beat_out_tokens=beat_out_tokens)
    beats = math.ceil(wait_s / pol.tau_s) if wait_s > 0 else 0
    return {"none": pol.expire_cost if wait_s > ttl_s else 0.0,
            "beat": beats * pol.per_beat_cost,
            "expire": pol.expire_cost,
            "expire_compact": prefix_tokens * compact_ratio / book.per * pv.p_in}
