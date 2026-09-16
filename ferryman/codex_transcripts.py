"""Codex CLI rollout（jsonl）的防御式读取器。

与 transcripts.py 同一防御纪律：坏行/缺字段静默跳过，绝不抛错。

语义（经 2026-06 会话实测校准）：token_count.last_token_usage 里
input_tokens = 完整 prompt（**已包含** cached_input_tokens，OpenAI 惯例；
input 随对话单调增长、cached ≤ input 恒成立），因此：
命中率 = cached ÷ input；上下文规模 ≈ input。
若未来版本语义变化（cached > input 出现即 disjoint/CC 式），调用方需切换公式。
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path


@dataclass(frozen=True)
class Turn:
    """一条 token_count 轮次（时间已统一为 UTC epoch 秒）。"""

    ts: float
    input_tokens: int  # 完整 prompt（含 cached）
    cached: int
    cache_write: int


def _ts_to_epoch(v: object) -> float | None:
    if not isinstance(v, str) or not v:
        return None
    try:
        dt = datetime.fromisoformat(v.replace("Z", "+00:00"))
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.timestamp()


def token_count_turns(path: Path) -> list[Turn]:
    """会话内全部 token_count 轮次（last_token_usage 口径），按时间升序。"""
    turns: list[Turn] = []
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                if '"token_count"' not in line:
                    continue
                try:
                    d = json.loads(line)
                except ValueError:
                    continue
                if d.get("type") != "event_msg":
                    continue
                payload = d.get("payload") or {}
                if payload.get("type") != "token_count":
                    continue
                u = (payload.get("info") or {}).get("last_token_usage") or {}
                try:
                    inp = int(u.get("input_tokens") or 0)
                    cached = int(u.get("cached_input_tokens") or 0)
                    w = int(u.get("cache_write_input_tokens") or 0)
                except (TypeError, ValueError):
                    continue
                ts = _ts_to_epoch(d.get("timestamp"))
                if ts is None or inp <= 0:
                    continue
                turns.append(Turn(ts, inp, cached, w))
    except OSError:
        return []
    turns.sort(key=lambda t: t.ts)
    return turns
