"""E0b：Codex（ChatGPT 订阅）链路缓存 TTL 实测。

方法与 E0a 同构：对 ~/.codex/sessions 全部 rollout 取相邻 token_count 轮次，
统计（间隔, 下一轮 cached/input 占比）分桶中位数；15–60 分钟细分 + 按月分层。
口径差异：Codex 的 input_tokens 已含 cached（见 codex_transcripts.py 语义校准），
故命中率 = cached ÷ input。OpenAI 官方 API 缓存 TTL 先验 = 30 分钟。

输出：reports/e0b-codex.md。设计依据：docs/DESIGN.md §7。
"""

from __future__ import annotations

import time
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path
from statistics import median

from .codex_transcripts import token_count_turns

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
BIG_CTX = 50_000
MIN_GAP = 5.0
MIN_PREV_CTX = 10_000
MIN_KNEE_N = 5  # 拐点桶最小样本数（≥50K 分层），低于此不判定拐点
TIME_BUDGET = 280.0


def _bucket_label(gap: float) -> str | None:
    for lo, hi, label in BUCKETS:
        if lo <= gap < hi:
            return label
    return None


def collect(sessions_dir: Path) -> dict:
    files = sorted(sessions_dir.glob("**/rollout-*.jsonl"))
    t0 = time.time()
    pairs: list[tuple[float, float, float, int]] = []  # (gap, ratio, t2_ts, prev_ctx)
    n_files = 0
    n_disjoint_violations = 0  # cached > input 出现次数（≠0 说明语义漂移到 CC 式）
    partial = False
    for p in files:
        if time.time() - t0 > TIME_BUDGET:
            partial = True
            break
        n_files += 1
        turns = token_count_turns(p)
        n_disjoint_violations += sum(1 for t in turns if t.cached > t.input_tokens)
        for i in range(1, len(turns)):
            t1, t2 = turns[i - 1], turns[i]
            gap = t2.ts - t1.ts
            if gap < MIN_GAP or t1.input_tokens < MIN_PREV_CTX:
                continue
            if t2.input_tokens <= 0:
                continue
            pairs.append((gap, t2.cached / t2.input_tokens, t2.ts, t1.input_tokens))
    return {
        "total_files": len(files),
        "n_files": n_files,
        "pairs": pairs,
        "n_disjoint_violations": n_disjoint_violations,
        "partial": partial,
    }


def _bucket_table(pairs):
    by_all: dict[str, list[float]] = defaultdict(list)
    by_big: dict[str, list[float]] = defaultdict(list)
    for gap, ratio, _ts, prev_ctx in pairs:
        label = _bucket_label(gap)
        if label is None:
            continue
        by_all[label].append(ratio)
        if prev_ctx >= BIG_CTX:
            by_big[label].append(ratio)
    return [
        (
            label,
            len(by_all.get(label, [])),
            median(by_all[label]) if by_all.get(label) else None,
            len(by_big.get(label, [])),
            median(by_big[label]) if by_big.get(label) else None,
        )
        for _lo, _hi, label in BUCKETS
    ]


def _monthly_table(pairs):
    by_month: dict[str, list[float]] = defaultdict(list)
    for gap, ratio, ts, _ctx in pairs:
        if not (900 <= gap < 3600):
            continue
        month = datetime.fromtimestamp(ts, tz=timezone.utc).strftime("%Y-%m")
        by_month[month].append(ratio)
    return [(m, len(v), median(v)) for m, v in sorted(by_month.items()) if len(v) >= 10]


def _fmt(x: float | None) -> str:
    return "n/a" if x is None else f"{x:.3f}"


def build_report(data: dict, out_path: Path) -> None:
    pairs = data["pairs"]
    rows = _bucket_table(pairs)
    months = _monthly_table(pairs)

    knee = None
    for lo, _hi, label in BUCKETS:
        if lo < 900:
            continue
        row = next(r for r in rows if r[0] == label)
        if row[3] >= MIN_KNEE_N and row[4] is not None and row[4] < 0.5:
            knee = (lo, label, row)
            break
    suggestion = (
        f"拐点桶 **{knee[1]}**（≥50K 分层 n={knee[2][3]}，中位 {_fmt(knee[2][4])}）；"
        f"数据建议 Codex 拦截阈值 ≈ **{int(knee[0] // 60) + 5} 分钟**（拐点下界 + 5min 余量）"
        if knee
        else ("≥50K 分层在 25–90 分钟窗口内**没有足够样本定位拐点**（本机 Codex 长间隔轮次太少）。"
              "已知：40-45m 处仍热（n=1，不可靠）、>90m 已冷（n=1）。先验取 OpenAI 官方 30m；"
              "维持 gate_mode=off，靠 daemon 台账持续积累后重跑本工具，或做一次受控闲置实验（20/30/45/60m 各探一轮）")
    )

    now = datetime.now().strftime("%Y-%m-%d %H:%M")
    lines = [
        "# E0b · Codex（ChatGPT 订阅）缓存 TTL 实测报告",
        "",
        f"- 生成：{now}（工具：`ferryman e0b`）",
        f"- 数据：{data['n_files']}/{data['total_files']} 个 rollout（~/.codex/sessions），"
        f"{len(pairs)} 对相邻轮次（前轮 input ≥10K、间隔 ≥5s）",
        "- 口径：命中率 = 下一轮 cached_input_tokens ÷ input_tokens（input 已含 cached，"
        "语义经实测校准，见 ferryman/codex_transcripts.py；"
        f"cached>input 语义漂移计数 = {data['n_disjoint_violations']}，非 0 则公式需重审）",
        "- 先验：OpenAI 官方 API 缓存 TTL = 30 分钟（订阅链路待本数据对照）",
        "",
        "## 分桶曲线",
        "",
        "| 间隔 | n | 中位命中率 | n(≥50K) | 中位(≥50K) |",
        "|---|---:|---:|---:|---:|",
    ]
    lines += [f"| {l} | {na} | {_fmt(ma)} | {nb} | {_fmt(mb)} |" for l, na, ma, nb, mb in rows]
    lines += [
        "",
        "## 按月分层（间隔 15–60 分钟，n≥10）",
        "",
        "| 月份 | n | 中位命中率 |",
        "|---|---:|---:|",
    ]
    lines += [f"| {m} | {n} | {_fmt(med)} |" for m, n, med in months] or ["| （无满足 n≥10 的月份） | | |"]
    lines += [
        "",
        "## 结论",
        "",
        f"- {suggestion}",
        "- Codex gate_mode 升级路径：off → observe（先用 observe 观察拦截判定命中情况）→ enforce（DESIGN §6.1-2）",
        f"- partial={data['partial']}",
        "",
    ]
    out_path.write_text("\n".join(lines), encoding="utf-8")


def run() -> int:
    sessions_dir = Path.home() / ".codex" / "sessions"
    out_path = Path(__file__).resolve().parent.parent / "reports" / "e0b-codex.md"
    out_path.parent.mkdir(exist_ok=True)
    data = collect(sessions_dir)
    build_report(data, out_path)
    print(f"e0b done files={data['n_files']}/{data['total_files']} "
          f"pairs={len(data['pairs'])} violations={data['n_disjoint_violations']} "
          f"partial={data['partial']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(run())
