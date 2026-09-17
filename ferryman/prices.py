# ferryman/prices.py
"""价格表：~/ferryman/config.toml [prices.*] → 版本化单价（设计 §1.1，Q1/Q16）。

- 每条流水钉死记账时的 price_tag（"key@effective_from"），改价不重算旧账；
- p_cache 允许缺省（None = 无缓存经济：策略计算器拒算，report 标注不可算）。
"""

from __future__ import annotations

import tomllib
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path


@dataclass(frozen=True)
class PriceVersion:
    effective_from: str            # "YYYY-MM-DD"，生效日 UTC 零点起
    p_in: float
    p_cache: float | None          # None = 未公布/无缓存价
    p_out: float


@dataclass(frozen=True)
class PriceBook:
    key: str                       # 对应 [prices.<key>]
    unit: str                      # 自述单位（如 "智谱积分"）
    per: int                       # 每 N token 一个计价块（万=10000，M=1000000）
    versions: tuple[PriceVersion, ...]   # effective_from 升序

    def at(self, ts: float) -> PriceVersion | None:
        best = None
        for v in self.versions:      # 升序，取最后一个 effective_from ≤ ts
            day = datetime.strptime(v.effective_from, "%Y-%m-%d") \
                .replace(tzinfo=timezone.utc).timestamp()
            if day <= ts:
                best = v
        return best


def price_tag(book_key: str, pv: PriceVersion) -> str:
    return f"{book_key}@{pv.effective_from}"


def load_prices(path: Path | None = None) -> dict[str, PriceBook]:
    """读 [prices.*]；无文件/无节 → 空 dict。"""
    p = path or Path.home() / "ferryman" / "config.toml"
    if not p.exists():
        return {}
    data = tomllib.loads(p.read_text(encoding="utf-8"))
    books: dict[str, PriceBook] = {}
    for key, blk in (data.get("prices") or {}).items():
        vers = []
        for vb in blk.get("versions", []):
            vers.append(PriceVersion(
                effective_from=str(vb["effective_from"]),
                p_in=float(vb["p_in"]),
                p_cache=(float(vb["p_cache"])
                         if vb.get("p_cache") is not None else None),
                p_out=float(vb["p_out"]),
            ))
        vers.sort(key=lambda v: v.effective_from)
        books[key] = PriceBook(key=key, unit=str(blk.get("unit", "")),
                               per=int(blk.get("per", 10_000)),
                               versions=tuple(vers))
    return books
