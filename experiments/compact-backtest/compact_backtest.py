"""CC /compact 节律回测：真实账本 → 标定 Y → 节律仿真 → 节省网格。

三步（只读 ~/ferryman/accounts/*.jsonl，可用 --data 换目录，不写任何文件）：
1. 按 lineage 重放请求前缀 P = input+cache_read+creation，识别真实 compact 事件
   （前缀高位暴跌 + cache_read≈0 + 重进远小于旧 P），标定 compact 后前缀 Y 的分布；
   同场识别闲置复活（input≥80% 旧 P 且 cr≈0 且间隔≥30min）。
2. 对每个会话重放请求序列，比较输入侧总花费（cache_read + input + creation）：
   基线（真实行累加） vs 策略(P→Y)（仿真前缀超阈值即 compact：付压缩调用
   旧前缀×P_cache + 摘要 3000×P_out，重进 Y×P_in，前缀重置 Y）。
3. 网格 P×Y 汇总节省积分/百分比、compact 触发次数、P 与 Y 的敏感性，
   并对代表会话逐段展示基线与最优档的分叉点。

假设（结果为无质量损失上界）：Y 恒定；压缩调用走同价目；零质量税；
台账行即计费口径（与 cost-structure 同口径，含台账自身的连排行）。
基准读数与用法见同目录 README.md。
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
DEFAULT_GRID = "100000,150000,200000,300000|63000,20000"  # P 列表 | Y 列表（63k=实测 p50）

# compact 事件识别（2026-07-30~09-18 账本实测：cr 直方图在 45k 以上为空，
# 30~45k 一档是 compact 后系统+工具前缀（~34k）仍在 TTL 内的缓存命中，同为 compact）
COMPACT_MIN_PREV = 80_000     # 旧前缀至少 80k，滤掉会话起步噪声
COMPACT_CR_MAX = 45_000       # cache_read 近零（残留命中=系统+工具前缀，实测 ≤44k）
COMPACT_RATIO_MAX = 0.5       # 新前缀 ≤ 50% 旧前缀（实测事件 0.11~0.32）
COMPACT_NEW_MAX = 100_000     # 重进 = 系统+工具+摘要+首条消息，实测 45~87k
REVIVAL_MIN_PREV = 50_000     # 复活识别的旧前缀下限
REVIVAL_CR_MAX = 10_000       # 与 cost-structure 全款重付同口径
REVIVAL_RATIO = 0.8           # input ≥ 80% 旧 P
REVIVAL_GAP = 1800.0          # 闲置 ≥30min（TTL≈10min，30min 后确定性死）
SUMMARY_OUT_TOKENS = 3000     # 压缩调用写摘要的输出量（任务设定）


def load_usage(pattern: str) -> list[dict]:
    rows: list[dict] = []
    for f in sorted(glob.glob(pattern)):
        for i, line in enumerate(open(f, encoding="utf-8"), 1):
            line = line.strip()
            if not line:
                continue
            try:
                e = json.loads(line)
            except ValueError:
                print(f"[compact-backtest] 跳过损坏行 {os.path.basename(f)}:{i}",
                      file=sys.stderr)
                continue
            if e.get("kind") != "usage":
                continue
            e["_in"] = e.get("input_tokens") or 0
            e["_cr"] = e.get("cache_read_tokens") or 0
            e["_cc"] = e.get("cache_creation_tokens") or 0
            e["_out"] = e.get("output_tokens") or 0
            e["_p"] = e["_in"] + e["_cr"] + e["_cc"]   # 当时前缀（完整请求输入）
            e["_n"] = e["_in"] + e["_cc"]              # 全新内容（无缓存命中部分）
            if e["_p"] == 0:
                continue  # 报错/合成行不带 usage，跳过
            rows.append(e)
    return rows


def group_by_lineage(rows: list[dict]) -> list[list[dict]]:
    groups: dict[str, list[dict]] = collections.defaultdict(list)
    for idx, e in enumerate(rows):
        e["_seq"] = idx
        groups[e.get("lineage_id") or e.get("session_id") or "?"].append(e)
    out = list(groups.values())
    for g in out:
        g.sort(key=lambda e: (e["ts"], e["_seq"]))
    out.sort(key=lambda g: -sum(e["_cr"] for e in g))  # 重族系在前，便于代表会话选取
    return out


def is_compact(a: dict, b: dict) -> bool:
    return (a["_p"] >= COMPACT_MIN_PREV and b["_cr"] < COMPACT_CR_MAX
            and b["_p"] <= COMPACT_RATIO_MAX * a["_p"] and b["_p"] <= COMPACT_NEW_MAX)


def is_revival(a: dict, b: dict) -> bool:
    gap = b["ts"] - a["ts"]
    return (a["_p"] >= REVIVAL_MIN_PREV and b["_cr"] < REVIVAL_CR_MAX
            and b["_n"] >= REVIVAL_RATIO * a["_p"] and gap >= REVIVAL_GAP)


def pct(vals: list[int], q: float) -> int:
    if not vals:
        return 0
    s = sorted(vals)
    return s[min(len(s) - 1, int(len(s) * q))]


def replay(g: list[dict], p_th: int, y_val: int, trace: bool = False) -> dict:
    """单会话策略(P→Y)仿真。返回输入侧成本（积分）、compact 次数、可选逐行轨迹。"""
    cost = 0.0
    compacts = 0
    sim_p = 0.0            # 仿真前缀（上一请求结束后的上下文占用）
    just_compacted = False
    trace_rows = [] if trace else None
    for t, e in enumerate(g):
        if t == 0:
            cost += e["_n"] * P_IN + e["_cr"] * P_CACHE
            sim_p += e["_n"] + e["_out"]
            if trace:
                trace_rows.append((t, e, 0, cost, "start"))
            continue
        prev = g[t - 1]
        rev = is_revival(prev, e)
        # 复活前不点火：缓存马上要整段重付，压缩只会白付一次重进
        if sim_p > p_th and not rev:
            cost += sim_p * P_CACHE + SUMMARY_OUT_TOKENS * P_OUT  # 压缩调用
            cost += y_val * P_IN                                  # 重进
            sim_p = float(y_val)
            compacts += 1
            just_compacted = True
            if trace:
                trace_rows.append((t, e, sim_p, cost, "COMPACT"))
        else:
            just_compacted = False
        if rev:
            # 闲置复活照真实发生计费（前缀取仿真值：只涨真实增量，不吃回旧前缀）
            cost += e["_n"] * P_IN + e["_cr"] * P_CACHE
            sim_p += max(0, e["_p"] - prev["_p"])
            if trace:
                trace_rows.append((t, e, sim_p, cost, "revival"))
        elif just_compacted:
            # 重进请求本身无缓存可读：Y 已在 compact 处按 input 价付过，这里只付新内容
            cost += e["_n"] * P_IN
            sim_p += e["_n"] + e["_out"]
            if trace:
                trace_rows.append((t, e, sim_p, cost, "re-enter"))
        else:
            cost += sim_p * P_CACHE + e["_n"] * P_IN
            sim_p += e["_n"] + e["_out"]
            if trace:
                trace_rows.append((t, e, sim_p, cost, ""))
    return {"cost": cost, "compacts": compacts, "trace": trace_rows}


def main() -> None:
    global P_IN, P_CACHE, P_OUT, COMPACT_CR_MAX
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--data", default=os.path.expanduser("~/ferryman/accounts"),
                    help="账本目录（默认 ~/ferryman/accounts）")
    ap.add_argument("--prices", nargs=4, type=float, metavar=("P_IN", "P_CACHE", "P_OUT", "PER"),
                    default=DEFAULT_PRICES, help="价格表口径（默认 GLM 6.9 1.7 24 10000）")
    ap.add_argument("--grid", default=DEFAULT_GRID,
                    help="网格 'P列表|Y列表'，token 可写 k 后缀（默认 100000,150000,200000,300000|63000,20000）")
    ap.add_argument("--compact-cr-max", type=int, default=COMPACT_CR_MAX,
                    help="compact 识别的 cache_read 上限")
    ap.add_argument("--top", type=int, default=2, help="代表会话展示前 N 个")
    args = ap.parse_args()

    P_IN, P_CACHE, P_OUT, PER = args.prices
    # 归一为「每 token 积分」，后续全部 token×单价 即积分
    P_IN, P_CACHE, P_OUT = P_IN / PER, P_CACHE / PER, P_OUT / PER

    def parse_tokens(s: str) -> list[int]:
        s = s.strip()
        return [int(float(x[:-1]) * 1000) if x[-1] in "kK" else int(x)
                for x in s.split(",") if x.strip()]

    p_list, y_list = args.grid.split("|")
    p_grid, y_grid = parse_tokens(p_list), parse_tokens(y_list)
    COMPACT_CR_MAX = args.compact_cr_max

    rows = load_usage(os.path.join(args.data, "*.jsonl"))
    if not rows:
        print("无 usage 行——检查 --data 目录")
        return

    groups = group_by_lineage(rows)
    span_days = (rows[-1]["ts"] - rows[0]["ts"]) / 86400 if len(rows) > 1 else 0

    # ---- 步骤 1：真实 compact 事件 + Y 标定 + 复活计数 ----
    compacts: list[dict] = []
    revivals = 0
    for g in groups:
        for a, b in zip(g, g[1:]):
            if is_revival(a, b):
                revivals += 1
            elif is_compact(a, b):
                compacts.append({"ts": b["ts"], "lin": g[0]["lineage_id"],
                                 "prev": a["_p"], "new": b["_p"]})
    ys = sorted(c["new"] for c in compacts)
    y_p50 = ys[len(ys) // 2] if ys else 0
    y_p90 = pct(ys, 0.9)

    print(f"请求 {len(rows):,} 行 · {len(groups)} 个会话 · 跨度 {span_days:.1f} 天 "
          f"· 价格 in {P_IN*PER:.1f}/cache {P_CACHE*PER:.1f}/out {P_OUT*PER:.1f} 每万 token")
    print(f"\n[1] 真实 compact 事件（旧前缀≥{COMPACT_MIN_PREV//1000}k · cr<{args.compact_cr_max//1000}k · "
          f"新前缀≤{COMPACT_RATIO_MAX:.0%} 旧且 ≤{COMPACT_NEW_MAX//1000}k）：{len(compacts)} 次")
    for c in sorted(compacts, key=lambda c: c["ts"]):
        t = time.strftime("%m-%d %H:%M", time.localtime(c["ts"]))
        print(f"    {t}  {c['prev']//1000:>4}k → {c['new']//1000:>3}k  ({c['new']/c['prev']:.0%})  {c['lin'][-16:]}")
    if ys:
        print(f"    compact 后前缀 Y：p50 {y_p50/1000:.0f}k · p90 {y_p90/1000:.0f}k · "
              f"min {ys[0]//1000}k · max {ys[-1]//1000}k")
    print(f"    闲置复活（input≥80% 旧 P · cr<{REVIVAL_CR_MAX//1000}k · 间隔≥30min）：{revivals} 次")

    # ---- 步骤 2：基线 + 网格仿真 ----
    base_in = 0.0
    out_tokens = 0
    for e in rows:
        base_in += e["_n"] * P_IN + e["_cr"] * P_CACHE
        out_tokens += e["_out"]
    const_out = out_tokens * P_OUT
    total_spend = base_in + const_out

    print(f"\n[2] 基线输入侧 {base_in:,.0f} · output 常量 {const_out:,.0f} · 总花费 {total_spend:,.0f}")
    print(f"    策略(P→Y) 网格，摘要写 {SUMMARY_OUT_TOKENS} tok/次：")

    results = {}
    print(f"    {'P→Y':>12s} {'策略输入侧':>14s} {'省积分':>12s} {'对输入侧':>9s} {'对总花费':>9s} {'compact':>8s} {'会话级p50/p90/max':>18s}")
    for p_th in p_grid:
        for y_val in y_grid:
            tot_cost = 0.0
            tot_cpt = 0
            per_lin_cpt = []
            for g in groups:
                r = replay(g, p_th, y_val)
                tot_cost += r["cost"]
                tot_cpt += r["compacts"]
                if r["compacts"]:
                    per_lin_cpt.append(r["compacts"])
            saved = base_in - tot_cost
            results[(p_th, y_val)] = (tot_cost, saved, tot_cpt, per_lin_cpt)
            print(f"    {p_th//1000:>4}k→{y_val//1000:<3}k {tot_cost:>14,.0f} {saved:>12,.0f} "
                  f"{saved/base_in*100:>8.1f}% {saved/total_spend*100:>8.1f}% "
                  f"{tot_cpt:>7} {pct(per_lin_cpt, .5):>6}/{pct(per_lin_cpt, .9):<3}/{max(per_lin_cpt, default=0)}")

    # ---- 敏感性：P 与 Y 谁搬动节省更多 ----
    y_ref = y_p50 if y_p50 in y_grid else y_grid[0]
    p_ref = 150_000 if 150_000 in p_grid else p_grid[0]
    p_span = max(results[(p, y_ref)][1] for p in p_grid) - min(results[(p, y_ref)][1] for p in p_grid)
    y_span = max(results[(p_ref, y)][1] for y in y_grid) - min(results[(p_ref, y)][1] for y in y_grid)

    print(f"\n[3] 敏感性（节省积分极差）：P {p_grid[0]//1000}k~{p_grid[-1]//1000}k（Y={y_ref//1000}k）"
          f" 摆动 {p_span:,.0f} · Y {y_grid[0]//1000}k~{y_grid[-1]//1000}k（P={p_ref//1000}k）"
          f" 摆动 {y_span:,.0f} → 最敏感参数：{'P' if p_span >= y_span else 'Y'}")

    # ---- 代表会话逐段对比：基线 vs 最优档 ----
    best_key = max(results, key=lambda k: results[k][1])
    best_p, best_y = best_key
    print(f"\n[4] 代表会话（最优档 P={best_p//1000}k→Y={best_y//1000}k，逐段累计输入侧积分）：")
    for g in groups[: args.top]:
        base_lin = sum(e["_n"] * P_IN + e["_cr"] * P_CACHE for e in g)
        r = replay(g, best_p, best_y, trace=True)
        title = (g[0].get("title") or "")[:30]
        print(f"\n    {g[0]['lineage_id'][-20:]} 「{title}」 {len(g)} 行 · "
              f"基线 {base_lin:,.0f} → 策略 {r['cost']:,.0f}（省 {(base_lin - r['cost'])/base_lin*100:.0f}%，"
              f"compact {r['compacts']} 次）")
        tr = r["trace"]
        marks = [i for i, row in enumerate(tr) if row[4] in ("COMPACT", "revival")]
        if len(marks) > 44:                       # 分叉点太多时抽样，保证可读
            marks = marks[:: -(-len(marks) // 44)]
        shown = sorted(set(range(0, len(tr), max(1, len(tr) // 22))) | set(marks))
        seen_t = set()
        disp = []
        for i in shown:
            row = tr[i]
            if row[0] in seen_t and row[4] == "re-enter":
                disp[-1] = (disp[-1][0], disp[-1][1], row[2], row[3], "COMPACT")  # 合并 compact+重进
                continue
            if row[0] in seen_t:
                continue
            seen_t.add(row[0])
            disp.append((row[0], row[1], row[2], row[3], row[4]))
        scale = max((d[3] for d in disp), default=1) or 1
        for t, e, sim_p, cum, mark in disp:
            bar_len = int(cum / scale * 40)
            tag = {"COMPACT": " ◀compact", "revival": " ◀复活",
                   "start": " ◀会话起步"}.get(mark, "")
            print(f"      #{t:<5d} 真实P {e['_p']//1000:>4}k 仿真P {int(sim_p)//1000:>4}k "
                  f"策略累计 {cum:>9,.0f} {'#' * bar_len}{tag}")

    print("\n假设：Y 恒定 / 压缩调用同价目 / 零质量税——结果为无质量损失上界，现实要打折；"
          "复活按真实计费（保守）。")


if __name__ == "__main__":
    main()
