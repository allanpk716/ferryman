#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""票01 · 快照脚本:真实账本 → stats.data.js

只读遍历账本 JSONL(零网络),按 spec《成本成效账视图》「聚合数据契约」「成本
口径」两节聚合,产出与本脚本同目录的 stats.data.js(JSONP:
``window.STATS_DATA = {...};``,票02 mock 页直接加载)。

口径单源(与 daemon /report 同法;对照 internal/daemon/query_report.go、
internal/report/report.go、internal/prices/prices.go,均为只读参考):
- econ 表选取:books=[prices.*];econKey=[ferry] provider;命中取之,否则仅一本
  取唯一本,否则 nil(不可算,不造数)。
- 可算性文案:与 daemon savingsComputability 逐字同款。
- 节省额公式 v1(逐行行局部):毛节省=block 行 prefix_tokens/per*(p_in−p_cache)
  (econBook 非 nil 且行 ts 版本含 p_cache);注入成本=inject 行 tokens/per*p_in;
  净=毛−注入−handoff 成本。econBook=nil 时毛/注入/净不可算(null),事件计数
  始终可算。
- handoff 金额独立于 econBook:按行 price_ver("@" 前段查 book,再按
  effective_from 精确匹配版本)折算;查不到 book/版本→unpriced(按行 provider
  去重排序,与 SavingsV1/cost_report unpriced 同源)。
- 日界=本地时区自然日;日桶=行 ts_iso 前 10 字符(账本自身本地 ISO)。
- 请求数=逐 usage 行计数(含子代理行,与 /report 同源)。
- 明细抽样绝不含 title 字段(F4);会话列=session_id 截断 ≤12 字符(F9)。

对账锚点(内置断言,任一不符→打印全部失败项、非零退出、不写产物):
- 2026-09 已封账,精确相等:五类事件 10/2/50/778/760,requests=76,245。
- 2026-10 未封账且账本活增长(2026-10-08 21:45 第0环基线拍板后当晚仍在新增
  handoff/window 行,实测已漂移),锚点取基线为下界:67/15/63/442/216——
  低于下界=分桶丢行,判失败;高于基线=活账本自然增长,放行并提示。
- 数据起点 2026-07-30,days 连续无空洞直至今天。
- KPI 公式抽查:四列 token/请求数/五类事件 days 求和==KPI;命中率公式;
  明细行数 ≤200、无 title、session ≤12 字符、时间倒序;产物 JSON 可解析。

金额舍入:6 位小数(daemon mathx.Round 同水位);净额公式内 4 位(report
SavingsV1 同款)。

用法:python -B widget/ui/mock/stats/gen_snapshot.py [--accounts DIR] [--config FILE]
产物:脚本同目录 stats.data.js
"""

from __future__ import annotations

import argparse
import json
import re
import sys
import tomllib
from collections import Counter, deque
from datetime import date, datetime, timezone
from pathlib import Path

try:  # Windows 控制台 GBK 兜底:输出统一按 UTF-8
    sys.stdout.reconfigure(encoding="utf-8")
    sys.stderr.reconfigure(encoding="utf-8")
except Exception:
    pass

DATA_START = "2026-07-30"  # 数据起点(D3 契约:自适应时间窗从此日画到今天)
DETAIL_N = 200             # 明细抽样条数(最近 N 条 usage 行,倒序)
KINDS = ("block", "bypass", "inject", "handoff", "window")
TOKEN_KEYS = (  # (输出短名, 账本字段名)
    ("input", "input_tokens"),
    ("cache_read", "cache_read_tokens"),
    ("cache_creation", "cache_creation_tokens"),
    ("output", "output_tokens"),
)

# ── 对账锚点(来源:spec「mock 评审通道」节+第0环 exp/f1_day_counts.json)────
ANCHOR_MONTH_EXACT = {  # 已封账月份:精确相等
    "2026-09": {"block": 10, "bypass": 2, "inject": 50, "handoff": 778, "window": 760},
}
ANCHOR_REQUEST_EXACT = {"2026-09": 76245}
ANCHOR_MONTH_FLOOR = {  # 未封账月份:下界(低于即丢行);基线=2026-10-08 21:45
    "2026-10": {"block": 67, "bypass": 15, "inject": 63, "handoff": 442, "window": 216},
}

DAY_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
DEFAULT_ACCOUNTS = Path.home() / "ferryman" / "accounts"
DEFAULT_CONFIG = Path.home() / "ferryman" / "config.toml"


def _num(v) -> bool:
    """数值判定(prices.go isNumeric 同法:bool 不算数)。"""
    return isinstance(v, (int, float)) and not isinstance(v, bool)


def _f(v) -> float:
    return float(v) if _num(v) else 0.0


# ── 价格表(internal/prices/prices.go LoadPrices 同法)─────────────────────────
def load_books(config_path: Path) -> dict:
    """[prices.*] → {key: book}。无文件/坏 TOML→空表(打警告,daemon 容错同款);
    版本缺 p_in/p_out 跳过;p_cache 键缺失→None(0 是合法价);per 缺省 10000。"""
    try:
        raw = config_path.read_bytes()
    except OSError:
        print(f"[gen_snapshot] 警告:config 不可读({config_path}),按空价格表处理", file=sys.stderr)
        return {}
    try:
        data = tomllib.loads(raw.decode("utf-8"))
    except (tomllib.TOMLDecodeError, UnicodeDecodeError) as exc:
        print(f"[gen_snapshot] 警告:config 解析失败({exc}),按空价格表处理", file=sys.stderr)
        return {}
    books: dict = {}
    for key, blk in (data.get("prices") or {}).items():
        if not isinstance(blk, dict):
            continue
        versions = []
        for vb in blk.get("versions") or []:
            if not isinstance(vb, dict) or not _num(vb.get("p_in")) or not _num(vb.get("p_out")):
                ef = vb.get("effective_from", "?") if isinstance(vb, dict) else "?"
                print(f"[prices] 版本缺必填键,跳过: {key}@{ef}", file=sys.stderr)
                continue
            ef = vb.get("effective_from")
            if isinstance(ef, datetime):
                ef = ef.date().isoformat()
            elif isinstance(ef, date):
                ef = ef.isoformat()
            elif not isinstance(ef, str):
                ef = ""
            pc = vb.get("p_cache")
            versions.append({
                "effective_from": ef,
                "p_in": float(vb["p_in"]),
                "p_cache": float(pc) if _num(pc) else None,
                "p_out": float(vb["p_out"]),
            })
        versions.sort(key=lambda v: v["effective_from"])  # 升序,稳定
        unit = blk.get("unit")
        per = blk.get("per")
        books[key] = {
            "key": key,
            "unit": unit if isinstance(unit, str) else "",
            "per": per if _num(per) else 10000,
            "versions": versions,
        }
    return books


def book_for(books: dict, key: str):
    """econ 表选取(prices.BookFor 同法):精确命中;否则仅一本取唯一本;再否则 None。"""
    if not books:
        return None
    if key:
        hit = books.get(key)
        if hit is not None:
            return hit
    if len(books) == 1:
        return next(iter(books.values()))
    return None


def book_at(book: dict, ts: float):
    """PriceBook.At 同法:最后一个 effective_from(UTC 零点)≤ ts 的版本;全未生效→None。"""
    best = None
    for v in book["versions"]:
        try:
            day = datetime.strptime(v["effective_from"], "%Y-%m-%d").replace(
                tzinfo=timezone.utc).timestamp()
        except ValueError:
            continue
        if day <= ts:
            best = v
    return best


def savings_computability(book) -> tuple[bool, str]:
    """可算性标注(query_report.go savingsComputability 逐字同款文案)。"""
    if book is None:
        return False, "节省额不可算：无可用品价格表（[prices.*]）"
    if not book["versions"]:
        return False, f"节省额不可算：{book['key']} 无价格版本"
    if book["versions"][-1]["p_cache"] is None:
        return False, f"节省额不可算：{book['key']} 无 p_cache（不硬算）"
    return True, ""


def handoff_row_cost(e: dict, books: dict):
    """report.HandoffCost 同法:按行钉死 price_ver 折算;不可价→None(不造数)。"""
    tag = e.get("price_ver")
    if not isinstance(tag, str) or not tag:
        return None
    key, sep, ver = tag.partition("@")
    if not sep:
        return None
    book = books.get(key)
    if book is None:
        return None
    pv = next((v for v in book["versions"] if v["effective_from"] == ver), None)
    if pv is None:
        return None
    per = book["per"] or 10000
    return (_f(e.get("prompt_tokens")) / per * pv["p_in"]
            + _f(e.get("completion_tokens")) / per * pv["p_out"])


def usage_row_cost(e: dict, econ_book) -> float | None:
    """明细行成本(仅在 econ 可算时调用):版本按行 ts 取;p_cache 缺→None
    (缓存写无价、不硬造,daemon 四列金额红线同款)。"""
    if econ_book is None:
        return None
    pv = book_at(econ_book, _f(e.get("ts")))
    if pv is None or pv["p_cache"] is None:
        return None
    per = econ_book["per"] or 10000
    return (_f(e.get("input_tokens")) * pv["p_in"]
            + _f(e.get("cache_read_tokens")) * pv["p_cache"]
            + _f(e.get("output_tokens")) * pv["p_out"]) / per


def day_of(e: dict) -> str:
    """行本地日:ts_iso 前 10 字符;缺失/畸形回落行 ts 的本地时区日期。"""
    iso = e.get("ts_iso")
    if isinstance(iso, str):
        head = iso[:10]
        if DAY_RE.match(head):
            return head
    return datetime.fromtimestamp(_f(e.get("ts"))).strftime("%Y-%m-%d")


# ── 账本扫描(只读)───────────────────────────────────────────────────────────
def scan_ledger(accounts_dir: Path, books: dict, econ_book) -> dict:
    files = sorted(accounts_dir.glob("*.jsonl"))
    if not files:
        raise SystemExit(f"[gen_snapshot] 错误:账本目录无 *.jsonl({accounts_dir})")

    kind_total = Counter()
    month_kind: dict[str, Counter] = {}
    month_requests: Counter = Counter()
    tokens_total = {short: 0 for short, _ in TOKEN_KEYS}
    requests_total = 0
    first_day = None
    bad_lines = 0
    total_lines = 0

    unpriced = set()
    handoff_cost_total = 0.0
    gross_total = 0.0
    inject_total = 0.0
    cost_total = 0.0  # usage 行 token 级成本(仅 econ 可算时累计)

    days: dict[str, dict] = {}
    details: deque = deque(maxlen=DETAIL_N)  # (ts, dict),扫描序≈时间序,收尾再排序

    def day_bucket(d: str) -> dict:
        b = days.get(d)
        if b is None:
            b = {
                "tokens": {short: 0 for short, _ in TOKEN_KEYS},
                "requests": 0,
                "events": Counter(),
                "handoff_cost": 0.0,
                "gross": 0.0,
                "inject": 0.0,
            }
            days[d] = b
        return b

    for fp in files:
        with open(fp, "r", encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                total_lines += 1
                try:
                    e = json.loads(line)
                except ValueError:
                    bad_lines += 1
                    continue
                if not isinstance(e, dict):
                    bad_lines += 1
                    continue
                kind = e.get("kind")
                d = day_of(e)
                if first_day is None or d < first_day:
                    first_day = d
                b = day_bucket(d)

                if kind == "usage":
                    requests_total += 1
                    b["requests"] += 1
                    month_requests[d[:7]] += 1
                    for short, field in TOKEN_KEYS:
                        n = e.get(field)
                        if _num(n):
                            tokens_total[short] += int(n)
                            b["tokens"][short] += int(n)
                    if econ_book is not None:
                        c = usage_row_cost(e, econ_book)
                        if c is not None:
                            cost_total += c
                    else:
                        c = None
                    details.append((_f(e.get("ts")), {
                        "ts_iso": e.get("ts_iso", ""),
                        "project": e.get("project", ""),
                        "session": str(e.get("session_id") or "")[:12],
                        "model": e.get("model", ""),
                        # 明细行 token 键名=账本原字段名(冻结 schema;days 才用短名)
                        **{field: (int(e.get(field)) if _num(e.get(field)) else 0)
                           for _, field in TOKEN_KEYS},
                    }, c))  # 第三位=行成本(econ 可算时已算好;None=不可价)
                elif kind in KINDS:
                    kind_total[kind] += 1
                    b["events"][kind] += 1
                    mk = month_kind.setdefault(d[:7], Counter())
                    mk[kind] += 1
                    if kind == "handoff":
                        c = handoff_row_cost(e, books)
                        if c is None:
                            prov = e.get("provider")
                            unpriced.add(prov if isinstance(prov, str) and prov else "?")
                        else:
                            handoff_cost_total += c
                            b["handoff_cost"] += c
                    elif kind == "block" and econ_book is not None:
                        pv = book_at(econ_book, _f(e.get("ts")))
                        if pv is not None and pv["p_cache"] is not None:
                            g = _f(e.get("prefix_tokens")) / (econ_book["per"] or 10000) \
                                * (pv["p_in"] - pv["p_cache"])
                            gross_total += g
                            b["gross"] += g
                    elif kind == "inject" and econ_book is not None:
                        pv = book_at(econ_book, _f(e.get("ts")))
                        if pv is not None:
                            ic = _f(e.get("tokens")) / (econ_book["per"] or 10000) * pv["p_in"]
                            inject_total += ic
                            b["inject"] += ic

    return {
        "files": files,
        "total_lines": total_lines,
        "bad_lines": bad_lines,
        "first_day": first_day,
        "kind_total": kind_total,
        "month_kind": month_kind,
        "month_requests": month_requests,
        "tokens_total": tokens_total,
        "requests_total": requests_total,
        "unpriced": sorted(unpriced),
        "handoff_cost_total": handoff_cost_total,
        "gross_total": gross_total,
        "inject_total": inject_total,
        "cost_total": cost_total,
        "days": days,
        "details": details,
    }


# ── 组装输出(冻结 schema,票02 依赖此形状)────────────────────────────────────
def build_data(agg: dict, econ_book, generated_at: str, source: str) -> dict:
    computable, note = savings_computability(econ_book)
    unit = econ_book["unit"] if econ_book else "智谱积分"
    hit = agg["tokens_total"]["cache_read"]
    base = hit + agg["tokens_total"]["input"]

    today = date.today().isoformat()
    start = min(DATA_START, agg["first_day"] or DATA_START)
    sd = date.fromisoformat(start)
    ed = date.fromisoformat(today)

    days_out = []
    cursor = sd
    while cursor <= ed:
        d = cursor.isoformat()
        b = agg["days"].get(d)
        if b is None:
            b = {"tokens": {short: 0 for short, _ in TOKEN_KEYS}, "requests": 0,
                 "events": Counter(), "handoff_cost": 0.0, "gross": 0.0, "inject": 0.0}
        gross = round(b["gross"], 6) if computable else None
        inject = round(b["inject"], 6) if computable else None
        hcost = round(b["handoff_cost"], 6)
        net = None
        if computable:
            net = round(round(b["gross"], 4) - round(b["inject"], 4) - hcost, 4)
        days_out.append({
            "date": d,
            **{short: b["tokens"][short] for short, _ in TOKEN_KEYS},
            "requests": b["requests"],
            "events": {k: int(b["events"].get(k, 0)) for k in KINDS},
            "savings": {
                "computable": computable,
                "gross": gross,
                "inject_cost": inject,
                "handoff_cost": hcost,
                "net": net,
            },
            "neg": bool(net is not None and net < 0),
        })
        cursor = date.fromordinal(cursor.toordinal() + 1)

    cost_value = round(agg["cost_total"], 6) if computable else None
    net_total = None
    if computable:
        net_total = round(round(agg["gross_total"], 4) - round(agg["inject_total"], 4)
                          - round(agg["handoff_cost_total"], 6), 4)

    details_out = []
    for _ts, row, row_cost in sorted(agg["details"], key=lambda x: x[0], reverse=True):
        row = dict(row)
        row["cost"] = round(row_cost, 6) if (computable and row_cost is not None) else None
        details_out.append(row)

    return {
        "generated_at": generated_at,
        "source": source,
        "kpi": {
            "requests": agg["requests_total"],
            "tokens": {short: agg["tokens_total"][short] for short, _ in TOKEN_KEYS},
            "cache_hit": {
                "hit": hit,
                "base": base,
                "rate": (hit / base) if base > 0 else None,
            },
            "cost": {"computable": computable, "note": note, "unit": unit,
                     "value": cost_value},
            "savings": {
                "computable": computable,
                "note": note,
                "unit": unit,
                "gross": round(agg["gross_total"], 6) if computable else None,
                "inject_cost": round(agg["inject_total"], 6) if computable else None,
                "handoff_cost": round(agg["handoff_cost_total"], 6),
                "net": net_total,
                "counts": {k: int(agg["kind_total"].get(k, 0)) for k in KINDS},
                "unpriced": agg["unpriced"],
            },
        },
        "days": days_out,
        "details": details_out,
    }


# ── 对账锚点与自检(不符即非零退出、不写产物)─────────────────────────────────
def run_checks(data: dict, agg: dict) -> list[str]:
    fails: list[str] = []

    def mk(month: str) -> Counter:
        return agg["month_kind"].get(month, Counter())

    for month, exp in ANCHOR_MONTH_EXACT.items():
        for k in KINDS:
            got = int(mk(month).get(k, 0))
            if got != exp[k]:
                fails.append(f"锚点失败(精确): {month} {k}={got},期望 {exp[k]}")
    for month, exp in ANCHOR_REQUEST_EXACT.items():
        got = int(agg["month_requests"].get(month, 0))
        if got != exp:
            fails.append(f"锚点失败(精确): {month} requests={got},期望 {exp}")
    for month, exp in ANCHOR_MONTH_FLOOR.items():
        for k in KINDS:
            got = int(mk(month).get(k, 0))
            if got < exp[k]:
                fails.append(f"锚点失败(下界): {month} {k}={got},低于基线 {exp[k]}(分桶丢行)")
            elif got > exp[k]:
                print(f"[gen_snapshot] 提示: {month} {k}={got} 高于基线 {exp[k]}"
                      f"(活账本自基线后自然增长,放行)")

    # 数据起点与连续性
    days = data["days"]
    if not days:
        fails.append("days 为空")
    else:
        if days[0]["date"] != DATA_START:
            fails.append(f"days 起点失败: {days[0]['date']},期望 {DATA_START}")
        today = date.today().isoformat()
        if days[-1]["date"] != today:
            fails.append(f"days 终点失败: {days[-1]['date']},期望今天 {today}")
        for a, b in zip(days, days[1:]):
            if date.fromisoformat(a["date"]).toordinal() + 1 != date.fromisoformat(b["date"]).toordinal():
                fails.append(f"days 不连续: {a['date']} → {b['date']}")

    # KPI 公式抽查:days 求和 == KPI
    tok_sum = {short: sum(d[short] for d in days) for short, _ in TOKEN_KEYS}
    for short, _ in TOKEN_KEYS:
        if tok_sum[short] != data["kpi"]["tokens"][short]:
            fails.append(f"KPI 抽查失败: tokens.{short} days 求和 {tok_sum[short]}"
                         f" != KPI {data['kpi']['tokens'][short]}")
    req_sum = sum(d["requests"] for d in days)
    if req_sum != data["kpi"]["requests"]:
        fails.append(f"KPI 抽查失败: requests days 求和 {req_sum} != KPI {data['kpi']['requests']}")
    for k in KINDS:
        ev_sum = sum(d["events"][k] for d in days)
        if ev_sum != data["kpi"]["savings"]["counts"][k]:
            fails.append(f"KPI 抽查失败: events.{k} days 求和 {ev_sum}"
                         f" != KPI counts {data['kpi']['savings']['counts'][k]}")

    # 命中率公式
    ch = data["kpi"]["cache_hit"]
    if ch["base"] > 0:
        if ch["rate"] is None or abs(ch["rate"] - ch["hit"] / ch["base"]) > 1e-12:
            fails.append("KPI 抽查失败: cache_hit.rate 与 hit/base 不符")
    elif ch["rate"] is not None:
        fails.append("KPI 抽查失败: base=0 时 rate 应为 null")

    # 明细约束:≤200 条、无 title、session ≤12 字符、时间倒序
    det = data["details"]
    if len(det) > DETAIL_N:
        fails.append(f"明细抽查失败: {len(det)} 条 > {DETAIL_N}")
    if any("title" in r for r in det):
        fails.append("明细抽查失败: 详情行含 title 字段(隐私红线 F4)")
    if any(len(r["session"]) > 12 for r in det):
        fails.append("明细抽查失败: session 超 12 字符")
    for a, b in zip(det, det[1:]):
        if a["ts_iso"] < b["ts_iso"]:
            fails.append("明细抽查失败: 非时间倒序")
            break

    # 负值日机制存在性:标记位字段在且为 bool
    if any(not isinstance(d["neg"], bool) for d in days):
        fails.append("days 抽查失败: neg 非 bool")

    # 产物可解析(JSON round-trip)
    try:
        json.loads(json.dumps(data, ensure_ascii=False))
    except (TypeError, ValueError) as exc:
        fails.append(f"产物 JSON 化失败: {exc}")

    return fails


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(
        description="真实账本 → stats.data.js 快照(票01;只读账本,零网络)")
    ap.add_argument("--accounts", default=str(DEFAULT_ACCOUNTS),
                    help=f"账本目录(缺省 {DEFAULT_ACCOUNTS})")
    ap.add_argument("--config", default=str(DEFAULT_CONFIG),
                    help=f"config.toml 路径(缺省 {DEFAULT_CONFIG})")
    args = ap.parse_args(argv)

    accounts_dir = Path(args.accounts)
    config_path = Path(args.config)
    if not accounts_dir.is_dir():
        raise SystemExit(f"[gen_snapshot] 错误:账本目录不存在({accounts_dir})")

    books = load_books(config_path)
    econ_key = ""
    try:
        raw = config_path.read_bytes()
        cfg = tomllib.loads(raw.decode("utf-8"))
        ferry = cfg.get("ferry")
        if isinstance(ferry, dict) and isinstance(ferry.get("provider"), str):
            econ_key = ferry["provider"]
    except (OSError, tomllib.TOMLDecodeError, UnicodeDecodeError):
        pass  # books 已按空表兜底,econ_key 留空即同水位
    econ_book = book_for(books, econ_key)

    generated_at = datetime.now().astimezone().isoformat(timespec="seconds")
    source = str(accounts_dir).replace("\\", "/")
    print(f"[gen_snapshot] econKey={econ_key or '(空)'} books={sorted(books)} "
          f"econBook={'nil' if econ_book is None else econ_book['key']}")

    agg = scan_ledger(accounts_dir, books, econ_book)
    if agg["bad_lines"]:
        print(f"[gen_snapshot] 警告: 坏行 {agg['bad_lines']}/{agg['total_lines']}(跳过,"
              f"计数锚点会兜底)", file=sys.stderr)
    print(f"[gen_snapshot] 扫描 {len(agg['files'])} 个文件,共 {agg['total_lines']} 行,"
          f"起点 {agg['first_day']},requests={agg['requests_total']}")

    data = build_data(agg, econ_book, generated_at, source)
    fails = run_checks(data, agg)
    if fails:
        for f in fails:
            print(f"[gen_snapshot] {f}", file=sys.stderr)
        print(f"[gen_snapshot] 对账断言未过({len(fails)} 项),不写产物,退出码 1",
              file=sys.stderr)
        return 1

    out_path = Path(__file__).resolve().parent / "stats.data.js"
    header = (
        "// stats.data.js — 真实账本快照(票01 产物;票02 mock 页直接加载)\n"
        f"// 生成时间: {generated_at}\n"
        f"// 源账本: {source}\n"
        f"// 生成器: widget/ui/mock/stats/gen_snapshot.py\n"
        "// 勿手改;重跑 gen_snapshot.py 刷新。\n"
    )
    payload = json.dumps(data, ensure_ascii=False, indent=1)
    out_path.write_text(header + "window.STATS_DATA = " + payload + ";\n",
                        encoding="utf-8")

    k = data["kpi"]
    print(f"[gen_snapshot] KPI: requests={k['requests']} "
          f"tokens={k['tokens']} hit_rate={k['cache_hit']['rate']}")
    print(f"[gen_snapshot] KPI: counts={k['savings']['counts']} "
          f"handoff_cost={k['savings']['handoff_cost']} unpriced={k['savings']['unpriced']}")
    print(f"[gen_snapshot] KPI: cost.computable={k['cost']['computable']} "
          f"savings.computable={k['savings']['computable']} note={k['savings']['note']}")
    print(f"[gen_snapshot] days={len(data['days'])}({data['days'][0]['date']}~"
          f"{data['days'][-1]['date']}) details={len(data['details'])}")
    print(f"[gen_snapshot] 产物已写: {out_path}({out_path.stat().st_size} 字节)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
