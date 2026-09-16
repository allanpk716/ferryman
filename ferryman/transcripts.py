"""Claude Code 会话 jsonl 的防御式读取器。

行内格式属 CC 内部实现、版本间会变（官方明示不稳定）：本模块只取需要的字段，
任何坏行/缺字段一律静默跳过，绝不向调用方抛错。台账闲置判定另有 mtime 兜底路径。
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path


@dataclass(frozen=True)
class Turn:
    """一条带 usage 的 assistant 轮次（时间已统一为 UTC epoch 秒）。"""

    ts: float
    cache_read: int
    cache_creation: int
    input_tokens: int

    @property
    def ctx_tokens(self) -> int:
        return self.cache_read + self.cache_creation + self.input_tokens


def _ts_to_epoch(v: object) -> float | None:
    if not isinstance(v, str) or not v:
        return None
    try:
        dt = datetime.fromisoformat(v.replace("Z", "+00:00"))
    except ValueError:
        return None
    if dt.tzinfo is None:  # 无时区的按 UTC 处理，避免与 mtime 双时钟错位
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.timestamp()


def assistant_turns(path: Path) -> list[Turn]:
    """会话内全部 assistant usage 轮次，按时间升序。"""
    turns: list[Turn] = []
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                if '"usage"' not in line:
                    continue
                try:
                    d = json.loads(line)
                except ValueError:
                    continue
                if d.get("type") != "assistant":
                    continue
                u = (d.get("message") or {}).get("usage") or {}
                try:
                    cr = int(u.get("cache_read_input_tokens") or 0)
                    cc = int(u.get("cache_creation_input_tokens") or 0)
                    inp = int(u.get("input_tokens") or 0)
                except (TypeError, ValueError):
                    continue
                ts = _ts_to_epoch(d.get("timestamp"))
                if ts is None or cr + cc + inp <= 0:
                    continue
                turns.append(Turn(ts, cr, cc, inp))
    except OSError:
        return []
    turns.sort(key=lambda t: t.ts)
    return turns


def ai_title(path: Path) -> str | None:
    """会话的自动生成标题（取最后一个 ai-title 行；该类行本身无 timestamp 字段）。"""
    title: str | None = None
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                if '"ai-title"' not in line:
                    continue
                try:
                    d = json.loads(line)
                except ValueError:
                    continue
                t = d.get("aiTitle")
                if isinstance(t, str) and t.strip():
                    title = t.strip()
    except OSError:
        return None
    return title


def first_user_message_hash(path: Path) -> str | None:
    """首条 user 消息正文的 sha256（会话族系 lineage 指纹）。

    resume 若产生新文件且复制历史，此 hash 会与原会话相同——
    这是 lineage 校准（E0a 附带项）的判定信号。
    """
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                if '"type":"user"' not in line:
                    continue
                try:
                    d = json.loads(line)
                except ValueError:
                    continue
                if d.get("type") != "user":
                    continue
                content = (d.get("message") or {}).get("content")
                if isinstance(content, str):
                    text = content
                elif isinstance(content, list):
                    text = "".join(
                        b.get("text", "")
                        for b in content
                        if isinstance(b, dict) and b.get("type") == "text"
                    )
                else:
                    continue
                if text.strip():
                    return hashlib.sha256(text.strip().encode("utf-8")).hexdigest()
    except OSError:
        return None
    return None
