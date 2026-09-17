# ferryman/report.py
"""ferryman account report：族系账单 + 成效账 v1 + 策略对比 + 复算附录。

ADR-0002：节省在 report 层由版本化公式现算，原始流水不改写。
block 侧价格在 report 时经 --provider 指定（daemon 不知道被拦会话的 provider，
v1 已知简化，复算附录披露）；handoff 侧用流水钉死的 price_ver。
"""
from __future__ import annotations

import json
import time
from datetime import datetime

from . import config as config_mod
from .accounts import Accounts
from .policy import NoCachePriceError, strategy_costs
from .prices import PriceBook, load_prices

SAVINGS_FORMULA = "v1"
COMPACT_RATIO = 0.25          # 公式内常数（policy.strategy_costs 默认值同源）


def _book_for(books: dict[str, PriceBook], key: str | None) -> PriceBook | None:
    if not books:
        return None
    if key and key in books:
        return books[key]
    if len(books) == 1:
        return next(iter(books.values()))
    return None


def _pv_at(book: PriceBook, ts: float):
    return book.at(ts)


def handoff_cost(e: dict, books: dict[str, PriceBook]) -> float | None:
    """按流水钉死的 price_ver 折算；无价格/无版本 → None（不可算，不造数）。"""
    tag = e.get("price_ver")
    if not tag or "@" not in tag:
        return None
    key, _, ver = tag.partition("@")
    book = books.get(key)
    pv = next((v for v in book.versions if v.effective_from == ver), None) \
        if book else None
    if pv is None:
        return None
    return (e.get("prompt_tokens", 0) / book.per * pv.p_in
            + e.get("completion_tokens", 0) / book.per * pv.p_out)


def savings_v1(entries: list[dict], books: dict[str, PriceBook],
               econ_book: PriceBook | None) -> dict:
    lines: dict[str, dict] = {}
    unpriced: set[str] = set()

    def row(lid: str) -> dict:
        return lines.setdefault(lid, {
            "lineage_id": lid, "project": "", "blocks": 0, "bypass": 0,
            "injects": 0, "handoffs": 0, "windows": 0,
            "handoff_cost": 0.0, "gross": 0.0, "inject_cost": 0.0})

    for e in entries:
        r = row(e.get("lineage_id") or e.get("session_id", "?"))
        r["project"] = r["project"] or e.get("project", "")
        k = e["kind"]
        if k == "block":
            r["blocks"] += 1
            if econ_book is not None:
                pv = _pv_at(econ_book, e.get("ts", 0))
                if pv and pv.p_cache is not None:
                    r["gross"] += e.get("prefix_tokens", 0) / econ_book.per \
                        * (pv.p_in - pv.p_cache)
        elif k == "bypass":
            r["bypass"] += 1
        elif k == "inject":
            r["injects"] += 1
            if econ_book is not None:
                pv = _pv_at(econ_book, e.get("ts", 0))
                if pv:
                    r["inject_cost"] += e.get("tokens", 0) / econ_book.per * pv.p_in
        elif k == "handoff":
            r["handoffs"] += 1
            c = handoff_cost(e, books)
            if c is None:
                unpriced.add(e.get("provider", "?"))
            else:
                r["handoff_cost"] += c
        elif k == "window":
            r["windows"] += 1
    for r in lines.values():                      # 净节省最后一步：毛 − 注入 − handoff
        r["net"] = round(r["gross"] - r["inject_cost"] - r["handoff_cost"], 4)
    totals = {kk: sum(r[kk] for r in lines.values())
              for kk in ("blocks", "bypass", "injects", "handoffs", "windows",
                         "handoff_cost", "gross", "inject_cost")}
    totals["net"] = round(totals["gross"] - totals["inject_cost"]
                          - totals["handoff_cost"], 4)
    return {"formula": SAVINGS_FORMULA, "lineages": sorted(lines.values(),
            key=lambda r: -r["net"]), "totals": totals,
            "unpriced": sorted(unpriced)}


def strategy_table(window_entries: list[dict], books: dict[str, PriceBook],
                   ttl_s: float, econ_key: str | None) -> dict:
    book = _book_for(books, econ_key)
    skipped: list[str] = []
    if ttl_s <= 0:
        skipped.append("ttl 未配置（[heartbeat] ttl_s）")
    if book is None:
        skipped.append("无可用品价格表（--provider / [prices.*]）")
    elif not book.versions:
        skipped.append(f"{book.key} 无价格版本")
        book = None
    elif book.versions[-1].p_cache is None:
        skipped.append(f"{book.key} 无 p_cache：节省额不可算（Q16）")
        book = None
    rows = []
    if book is not None and ttl_s > 0:
        for e in window_entries:
            try:
                sc = strategy_costs(book, book.at(e.get("ts", 0)) or book.versions[-1],
                                    ttl_s, e.get("dur_s", 0.0),
                                    e.get("prefix_tokens", 0),
                                    compact_ratio=COMPACT_RATIO)
            except NoCachePriceError:
                skipped.append(f"{book.key} 无 p_cache")
                break
            sc["dur_s"] = e.get("dur_s", 0.0)
            sc["prefix_tokens"] = e.get("prefix_tokens", 0)
            sc["best"] = min(("none", "beat", "expire", "expire_compact"),
                             key=lambda kk: sc[kk])
            rows.append(sc)
    return {"rows": rows, "skipped": skipped}


def _accounts_for(cfg) -> Accounts:
    return Accounts(cfg.data_dir)


def _parse_date(s: str | None) -> float | None:
    if not s:
        return None
    return datetime.strptime(s, "%Y-%m-%d").timestamp()


def render_text(s: dict, st: dict, books: dict[str, PriceBook],
                econ_key: str | None, filters: dict) -> str:
    unit = ""
    eb = _book_for(books, econ_key)
    if eb:
        unit = f"（单位：{eb.unit}）"
    L = ["# Ferryman 账本报表", "",
         f"- 成效公式：{SAVINGS_FORMULA} · 策略常数 compact_ratio={COMPACT_RATIO}"
         f" · 经济价格表：{econ_key or '（未指定）'}{unit}",
         f"- 过滤：{filters or '（无）'} · 生成：{time.strftime('%Y-%m-%d %H:%M')}", "",
         "## 按族系（lineage）", "",
         "| lineage | 项目 | block | bypass | inject | handoff | window "
         "| handoff成本 | 毛节省 | 注入成本 | 净节省 |", "|---|---|---:|---:|---:|---:|---:"
         "|---:|---:|---:|---:|"]
    for r in s["lineages"]:
        L.append(f"| {r['lineage_id'][:24]} | {r['project'][:20]} | {r['blocks']} "
                 f"| {r['bypass']} | {r['injects']} | {r['handoffs']} | {r['windows']} "
                 f"| {r['handoff_cost']:.2f} | {r['gross']:.2f} "
                 f"| {r['inject_cost']:.2f} | {r['net']:.2f} |")
    t = s["totals"]
    L += ["", f"**总计**：block {t['blocks']} · bypass {t['bypass']}（对照组，不计节省）"
         f" · 净节省 **{t['net']:.2f}**{unit}", ""]
    if s["unpriced"]:
        L.append(f"- 无价格 provider（token 已记、金额不可算）：{', '.join(s['unpriced'])}")
    if st["rows"]:
        L += ["## 等待窗口策略对比（事后，可复算）", "",
              "| dur_s | 前缀 | 不作为 | 心跳 | 放任 | 放任+compact | 最优 |",
              "|---:|---:|---:|---:|---:|---:|---|"]
        for r in st["rows"]:
            L.append(f"| {r['dur_s']:.0f} | {r['prefix_tokens']} | {r['none']:.2f} "
                     f"| {r['beat']:.2f} | {r['expire']:.2f} "
                     f"| {r['expire_compact']:.2f} | {r['best']} |")
    for skip in st["skipped"]:
        L.append(f"- 策略对比跳过：{skip}")
    L += ["", "## 复算附录", "",
          "- 公式（v1）：净节省 = Σ block S×(P_in−P_cache)/per − Σ inject tok×P_in/per"
          " − Σ handoff (prompt×P_in + completion×P_out)/per",
          "- handoff 侧价格取流水钉死的 price_ver（改价不重算旧账）；"
          "block 侧取本表头经济价格表（daemon 不知被拦会话 provider，v1 简化）",
          "- 复现：ferryman account report --json（同过滤参数）",
          ""]
    return "\n".join(L)


def run(args) -> int:
    cfg = config_mod.load()
    acc = _accounts_for(cfg)
    books = load_prices()
    econ_key = getattr(args, "provider", None) or cfg.ferry_provider or None
    filters = {"since": args.since, "until": args.until, "project": args.project,
               "session": args.session, "kind": args.kind}
    entries = acc.read(since=_parse_date(args.since), until=_parse_date(args.until),
                       project=args.project, session=args.session, kind=args.kind)
    econ_book = _book_for(books, econ_key)
    s = savings_v1(entries, books, econ_book)
    st = strategy_table([e for e in entries if e["kind"] == "window"],
                        books, ttl_s=cfg.heartbeat.ttl_s, econ_key=econ_key)
    if getattr(args, "json", False):
        print(json.dumps({"savings": s, "strategy": st,
                          "econ_provider": econ_key}, ensure_ascii=False, indent=2))
    else:
        print(render_text(s, st, books, econ_key,
                          {kk: vv for kk, vv in filters.items() if vv}))
    return 0
