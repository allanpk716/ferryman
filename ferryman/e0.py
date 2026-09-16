"""E0a：CC/GLM 链路缓存 TTL 全量实测。

方法：对 ~/.claude/projects 全部会话取相邻 assistant usage 轮次，统计
（间隔 gap, 下一轮 cache_read 占比）的分桶中位数；对 15–60 分钟区间细分
（拦截阈值定位），并按月份分层（检测历史路由切换对曲线的干扰）。
附带 lineage 校准：首条 user 消息 hash 碰撞 = resume 复制历史的信号。

口径：命中率 = cache_read ÷ (cache_read + input)（不计 cache_creation，与预演一致）。
输出：reports/e0a-cc-glm.md。设计依据：docs/DESIGN.md §7。
"""

from __future__ import annotations

import time
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path
from statistics import median

from .transcripts import assistant_turns, first_user_message_hash

# (下界秒, 上界秒, 标签) —— 15–60 分钟细分用于拐点定位
BUCKETS: list[tuple[float, float, str]] = [
    (0, 60, "<1m"),
    (60, 300, "1-5m"),
    (300, 900, "5-15m"),
    (900, 1500, "15-25m"),
    (1500, 1800, "25-30m"),
    (1800, 2100, "30-35m"),
    (2100, 2400, "35-40m"),
    (2400, 2700, "40-45m"),
    (2700, 3000, "45-50m"),
    (3000, 3600, "50-60m"),
    (3600, 5400, "60-90m"),
    (5400, 10800, "90m-3h"),
    (10800, float("inf"), ">3h"),
]
BIG_CTX = 50_000          # 大会话分层：前一轮上下文 ≥50K
MIN_GAP = 5.0             # 跳过同轮多事件（gap≈0）
MIN_PREV_CTX = 10_000     # 前轮上下文过小时命中率噪声大
TIME_BUDGET = 280.0       # 自保：到点停止扫描，标记 partial


def _bucket_label(gap: float) -> str | None:
    for lo, hi, label in BUCKETS:
        if lo <= gap < hi:
            return label
    return None


def collect(projects_dir: Path) -> dict:
    files = sorted(projects_dir.glob("**/*.jsonl"))
    t0 = time.time()
    pairs: list[tuple[float, float, float, int]] = []  # (gap, ratio, t2_ts, prev_ctx)
    hash_hits: dict[str, list[str]] = defaultdict(list)
    n_files = 0
    partial = False
    for p in files:
        if time.time() - t0 > TIME_BUDGET:
            partial = True
            break
        n_files += 1
        turns = assistant_turns(p)
        for i in range(1, len(turns)):
            t1, t2 = turns[i - 1], turns[i]
            gap = t2.ts - t1.ts
            if gap < MIN_GAP or t1.ctx_tokens < MIN_PREV_CTX:
                continue
            denom = t2.cache_read + t2.input_tokens
            if denom <= 0:
                continue
            pairs.append((gap, t2.cache_read / denom, t2.ts, t1.ctx_tokens))
        h = first_user_message_hash(p)
        if h:
            hash_hits[h].append(p.name)
    return {
        "total_files": len(files),
        "n_files": n_files,
        "pairs": pairs,
        "hash_hits": hash_hits,
        "partial": partial,
    }


def _bucket_table(pairs: list[tuple[float, float, float, int]]) -> list[tuple[str, int, float | None, int, float | None]]:
    by_all: dict[str, list[float]] = defaultdict(list)
    by_big: dict[str, list[float]] = defaultdict(list)
    for gap, ratio, _ts, prev_ctx in pairs:
        label = _bucket_label(gap)
        if label is None:
            continue
        by_all[label].append(ratio)
        if prev_ctx >= BIG_CTX:
            by_big[label].append(ratio)
    rows = []
    for _lo, _hi, label in BUCKETS:
        a, b = by_all.get(label, []), by_big.get(label, [])
        rows.append((
            label,
            len(a),
            median(a) if a else None,
            len(b),
            median(b) if b else None,
        ))
    return rows


def _monthly_table(pairs: list[tuple[float, float, float, int]]) -> list[tuple[str, int, float | None]]:
    """间隔 15–60 分钟的按月分层——检测历史路由切换是否使拐点漂移。"""
    by_month: dict[str, list[float]] = defaultdict(list)
    for gap, ratio, ts, _ctx in pairs:
        if not (900 <= gap < 3600):
            continue
        month = datetime.fromtimestamp(ts, tz=timezone.utc).strftime("%Y-%m")
        by_month[month].append(ratio)
    return [
        (m, len(v), median(v))
        for m, v in sorted(by_month.items())
        if len(v) >= 15
    ]


def _fmt(x: float | None) -> str:
    return "n/a" if x is None else f"{x:.3f}"


def build_report(data: dict, out_path: Path) -> str:
    pairs = data["pairs"]
    rows = _bucket_table(pairs)
    months = _monthly_table(pairs)
    collisions = {h: names for h, names in data["hash_hits"].items() if len(names) > 1}
    n_collision_files = sum(len(v) for v in collisions.values())

    # 数据驱动建议：≥50K 分层里 15m 之后第一个中位数跌破 0.5 的桶，下界+5min
    knee = None
    for lo, _hi, label in BUCKETS:
        if lo < 900:
            continue
        row = next(r for r in rows if r[0] == label)
        if row[4] is not None and row[4] < 0.5:
            knee = (lo, label, row)
            break
    suggestion = (
        f"拐点桶 **{knee[1]}**（≥50K 分层中位 {_fmt(knee[2][4])}）；"
        f"数据建议拦截阈值默认 ≈ **{int(knee[0] // 60) + 5} 分钟**（拐点下界 + 5min 余量）"
        if knee
        else "≥50K 分层未观察到跌破 0.5 的拐点，需人工复核曲线"
    )

    now = datetime.now().strftime("%Y-%m-%d %H:%M")
    lines = [
        "# E0a · CC/GLM 缓存 TTL 实测报告",
        "",
        f"- 生成：{now}（工具：`ferryman e0`）",
        f"- 数据：{data['n_files']}/{data['total_files']} 个会话文件（~/.claude/projects），"
        f"{len(pairs)} 对相邻轮次（前轮上下文 ≥10K、间隔 ≥5s）",
        "- 口径：命中率 = 下一轮 cache_read ÷ (cache_read + input)，中位数；"
        "≥50K 列 = 前轮上下文 ≥50K 的大会话分层",
        "",
        "## 分桶曲线",
        "",
        "| 间隔 | n | 中位命中率 | n(≥50K) | 中位(≥50K) |",
        "|---|---:|---:|---:|---:|",
    ]
    lines += [f"| {l} | {na} | {_fmt(ma)} | {nb} | {_fmt(mb)} |" for l, na, ma, nb, mb in rows]
    lines += [
        "",
        "## 按月分层（间隔 15–60 分钟，n≥15）",
        "",
        "| 月份 | n | 中位命中率 |",
        "|---|---:|---:|",
    ]
    lines += [f"| {m} | {n} | {_fmt(med)} |" for m, n, med in months] or ["| （无满足 n≥15 的月份） | | |"]
    lines += [
        "",
        "## lineage 校准（resume 行为信号）",
        "",
        f"- 首条 user 消息 hash 碰撞：{len(collisions)} 组 / 涉及 {n_collision_files} 个文件",
        "- 碰撞 = resume/复制历史产生新文件的信号（供会话族系判定校准；"
        "碰撞少则 resume 多为同文件续写，lineage 主键以 transcript_path 为准）",
        "",
        "## 结论",
        "",
        f"- {suggestion}",
        "- 阈值终值由人工复核本曲线后拍板（设计初值 30min，见 docs/DESIGN.md §6.1）",
        f"- partial={data['partial']}（True 表示因时间预算未扫完全部文件）",
        "",
    ]
    text = "\n".join(lines)
    out_path.write_text(text, encoding="utf-8")
    return text


def run() -> int:
    projects_dir = Path.home() / ".claude" / "projects"
    out_path = Path(__file__).resolve().parent.parent / "reports" / "e0a-cc-glm.md"
    out_path.parent.mkdir(exist_ok=True)
    data = collect(projects_dir)
    build_report(data, out_path)
    print(f"e0 done files={data['n_files']}/{data['total_files']} "
          f"pairs={len(data['pairs'])} partial={data['partial']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(run())
