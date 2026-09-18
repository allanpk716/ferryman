"""账本成本结构分解：usage 流水 → 各分量份额 / 前缀分布 / 全款重付事件 / 族系集中度。

只读 ~/ferryman/accounts/*.jsonl（可用 --data 换目录），不写任何文件。
基准读数与动机见同目录 README.md（2026-09-18：cache_read 占 82%）。
"""

from __future__ import annotations

import argparse
import collections
import glob
import json
import os
import sys
import time

# Windows 控制台默认 GBK，中文报表会打成乱码——强制 UTF-8 输出（reconfigure 仅 io.TextIOWrapper 有）
_reconfigure = getattr(sys.stdout, "reconfigure", None)
if callable(_reconfigure):
    _reconfigure(encoding="utf-8", errors="replace")

# GLM v2026-09-17（config.example.toml [prices.glm]）：积分 / 万 token
DEFAULT_PRICES = (6.9, 1.7, 24.0, 10000.0)


def load_usage(pattern: str, since: float | None) -> list[dict]:
    rows: list[dict] = []
    for f in sorted(glob.glob(pattern)):
        for i, line in enumerate(open(f, encoding="utf-8"), 1):
            line = line.strip()
            if not line:
                continue
            try:
                e = json.loads(line)
            except ValueError:
                print(f"[cost-structure] 跳过损坏行 {os.path.basename(f)}:{i}",
                      file=sys.stderr)
                continue
            if e.get("kind") != "usage":
                continue
            if since is not None and (e.get("ts") or 0) < since:
                continue
            rows.append(e)
    return rows


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--data", default=os.path.expanduser("~/ferryman/accounts"),
                    help="账本目录（默认 ~/ferryman/accounts）")
    ap.add_argument("--since", default=None,
                    help="只统计该日期起（YYYY-MM-DD，本地时区）")
    ap.add_argument("--prices", nargs=4, type=float, metavar=("P_IN", "P_CACHE", "P_OUT", "PER"),
                    default=DEFAULT_PRICES, help="价格表口径（默认 GLM 6.9 1.7 24 10000）")
    ap.add_argument("--top", type=int, default=5, help="族系集中度显示前 N")
    args = ap.parse_args()

    since = None
    if args.since:
        since = time.mktime(time.strptime(args.since, "%Y-%m-%d"))
    p_in, p_cache, p_out, per = args.prices

    rows = load_usage(os.path.join(args.data, "*.jsonl"), since)
    if not rows:
        print("无 usage 行——检查 --data 目录")
        return

    comp = collections.Counter()
    per_lin = collections.defaultdict(lambda: {"cr": 0, "rows": 0})
    cr_sorted: list[int] = []
    cold = cold_cost = 0
    models = collections.Counter()
    for e in rows:
        i = e.get("input_tokens") or 0
        cr = e.get("cache_read_tokens") or 0
        cc = e.get("cache_creation_tokens") or 0
        o = e.get("output_tokens") or 0
        comp["input"] += i
        comp["cache_read"] += cr
        comp["creation"] += cc
        comp["output"] += o
        models[e.get("model") or "?"] += 1
        cr_sorted.append(cr)
        lin = per_lin[e.get("lineage_id") or "?"]
        lin["cr"] += cr
        lin["rows"] += 1
        # 全款重付：大量新输入 + 几乎无命中 = 闲置复活/冷启动（压缩与心跳共同关心的事件）
        if i >= 50000 and cr < 10000:
            cold += 1
            cold_cost += i / per * p_in

    c_cr = comp["cache_read"] / per * p_cache
    c_in = (comp["input"] + comp["creation"]) / per * p_in
    c_out = comp["output"] / per * p_out
    total = c_cr + c_in + c_out
    n = len(rows)
    cr_sorted.sort()
    span_days = (rows[-1].get("ts", 0) - rows[0].get("ts", 0)) / 86400 if n > 1 else 0

    print(f"请求 {n:,} 次 · 跨度 {span_days:.1f} 天 · 模型 {dict(models.most_common(3))}")
    print(f"均值: cache_read {comp['cache_read']/n/1000:.0f}k · input {comp['input']/n/1000:.1f}k"
          f" · output {comp['output']/n/1000:.1f}k")
    print(f"\n成本结构（合计 {total:,.0f}）:")
    for label, v in (("cache_read 重复读历史", c_cr), ("input+creation 新内容", c_in), ("output 输出", c_out)):
        print(f"  {label:24s} {v:>14,.0f}  ({v/total*100:.0f}%)")
    print(f"全款重付事件 {cold} 次 ≈ {cold_cost:,.0f}（占 {cold_cost/total*100:.1f}%）")
    print(f"\n单请求 cache_read: p50 {cr_sorted[n//2]//1000}k · p90 {cr_sorted[int(n*.9)]//1000}k"
          f" · p99 {cr_sorted[int(n*.99)]//1000}k · max {cr_sorted[-1]//1000}k")
    top = sorted(per_lin.items(), key=lambda kv: -kv[1]["cr"])[: args.top]
    share = sum(v["cr"] for _, v in top) / comp["cache_read"] * 100 if comp["cache_read"] else 0
    print(f"\ncache_read 前 {len(top)} 族系占 {share:.0f}%:")
    for lin, c in top:
        print(f"  {lin[:36]:38s} rows={c['rows']:>6}  cache_read={c['cr']/1e6:>8.1f}M")


if __name__ == "__main__":
    main()
