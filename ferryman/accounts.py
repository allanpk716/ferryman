"""账本：accounts/YYYYMM.jsonl append-only 流水（ADR-0002，设计 §1.2/§1.3）。

铁律：
- 只记元数据与金额（字段白名单），永不落消息内容——隐私不变量；
- append-only，report 只读不改写；节省在 report 层由版本化公式重算；
- 按月滚动（本地时区）；每行带 schema 版本 v。
"""

from __future__ import annotations

import json
import sys
import threading
import time
from datetime import datetime
from pathlib import Path

SCHEMA_V = 1

_KIND_FIELDS: dict[str, set[str]] = {
    # 摆渡的每次模型调用（含分块/重试/失败）
    "handoff": {"provider", "model", "price_ver", "prompt_tokens",
                "completion_tokens", "outcome", "wall_s"},
    # 心跳（T41 占坑，T51 票03 起有生产者）：三态 outcome ∈ hit|miss|error
    # + observe（演练跳未真发）。T41 的 hit 布尔被三态取代（当时无生产者）。
    "beat": {"provider", "model", "price_ver", "prefix_tokens", "cache_read",
             "outcome", "cost_pred", "cost_actual"},
    "block": {"prefix_tokens", "idle_s"},
    "inject": {"tokens", "handoff_id"},
    "bypass": {"prefix_tokens"},
    "window": {"opened_ts", "closed_ts", "dur_s", "prefix_tokens", "close_reason"},
    # 问询守望事件（T51 票04，spec 决策 8）：命中/开窗/关窗走本通道（每跳
    # 复用上方 beat 科目）。命中行带复核证据形态（unit_count＋breakdown 三桶
    # ＋转录绝对路径；session_id/命中时间是公共字段）——只记元数据与计数，
    # 消息正文永不入账（隐私铁律，测试 test_hit_event_privacy_no_message_body
    # 守着）。
    "qwatch_hit": {"unit_count", "marker_lines", "qmark_lines",
                   "numbered_lines", "transcript_path"},
    "qwatch_open": {"unit_count", "prefix_tokens"},
    "qwatch_close": {"opened_ts", "closed_ts", "dur_s", "beats_fired",
                     "close_reason"},
    # 逐次请求的用量遥测（设计 §3.7；会话文件 30 天清理后的审计地基）
    "usage": {"model", "title", "input_tokens", "cache_read_tokens",
              "cache_creation_tokens", "output_tokens", "offset"},
}
_COMMON = {"ts", "ts_iso", "kind", "v", "agent", "session_id", "lineage_id", "project"}
KINDS = tuple(_KIND_FIELDS)


class Accounts:
    def __init__(self, data_dir: Path) -> None:
        self.dir = data_dir / "accounts"
        self.dir.mkdir(parents=True, exist_ok=True)
        self._lock = threading.Lock()

    def record(self, kind: str, *, ts: float | None = None, **fields) -> dict:
        if kind not in _KIND_FIELDS:
            raise ValueError(f"未知科目: {kind!r}（可选 {KINDS}）")
        bad = set(fields) - _COMMON - _KIND_FIELDS[kind]
        if bad:
            raise ValueError(f"账本不落这些字段（隐私不变量）: {sorted(bad)}")
        missing = _KIND_FIELDS[kind] - set(fields)
        if missing:
            raise ValueError(f"{kind} 缺必填字段: {sorted(missing)}")
        reserved = {"v", "ts_iso"} & set(fields)
        if reserved:
            raise ValueError(f"保留字段由模块盖章，不可传入: {sorted(reserved)}")
        ts = ts if ts is not None else time.time()
        entry = {"v": SCHEMA_V, "kind": kind, "ts": round(ts, 3),
                 "ts_iso": datetime.fromtimestamp(ts).astimezone()
                 .strftime("%Y-%m-%dT%H:%M:%S%z"),
                 "agent": fields.pop("agent", ""),
                 "session_id": fields.pop("session_id", ""),
                 "lineage_id": fields.pop("lineage_id", ""),
                 "project": fields.pop("project", "")}
        entry.update(fields)
        fname = datetime.fromtimestamp(ts).strftime("%Y%m") + ".jsonl"
        with self._lock:
            with open(self.dir / fname, "a", encoding="utf-8") as f:
                f.write(json.dumps(entry, ensure_ascii=False) + "\n")
        return entry

    def read(self, *, since: float | None = None, until: float | None = None,
             project: str | None = None, session: str | None = None,
             lineage: str | None = None, kind: str | None = None) -> list[dict]:
        out: list[dict] = []
        for f in sorted(self.dir.glob("*.jsonl")):
            for i, line in enumerate(f.read_text(encoding="utf-8").splitlines(), 1):
                if not line.strip():
                    continue
                try:
                    e = json.loads(line)
                except ValueError:
                    # 终审#1：告警走 stderr——read() 的调用方（report --json）
                    # 把 stdout 当机器可解析载荷，告警混入会撕裂输出。
                    print(f"[accounts] 跳过损坏行 {f.name}:{i}",
                          file=sys.stderr, flush=True)
                    continue
                if since is not None and e.get("ts", 0) < since:
                    continue
                if until is not None and e.get("ts", 0) > until:
                    continue
                if project and e.get("project") != project:
                    continue
                if session and e.get("session_id") != session:
                    continue
                if lineage and e.get("lineage_id") != lineage:
                    continue
                if kind and e.get("kind") != kind:
                    continue
                out.append(e)
        return out
