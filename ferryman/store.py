"""交接库（store）：handoffs/ + index.json v1 —— 唯一权威源，原子写。

DESIGN §6.15：{handoffs: [{handoff_id, session_id, agent, cwd, title, created_at,
covers_until, status: fresh|skeleton|stale|pending, path, blocked_at, injected: []}],
pending_prompts: [...]}；同 session 新交接覆盖；30 天归档（TODO：定期清理任务）。
"""

from __future__ import annotations

import json
import os
import threading
import time
import uuid
from datetime import datetime
from pathlib import Path

FRESH_WINDOW_S = 24 * 3600      # 归还注入新鲜度（DESIGN §6.9，可配留 TODO）
COVERS_TOLERANCE_S = 60         # covers_until 与 last_write 的容差（transcript 异步落盘）


def _atomic_write(path: Path, text: str) -> None:
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(text, encoding="utf-8")
    os.replace(tmp, path)


def _parse_iso_utc(s: str | None) -> float:
    if not s:
        return 0.0
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp()
    except ValueError:
        return 0.0


class Store:
    def __init__(self, data_dir: Path) -> None:
        self.dir = data_dir / "handoffs"
        self.dir.mkdir(parents=True, exist_ok=True)
        self.index_path = data_dir / "index.json"
        self._lock = threading.Lock()
        self._index: dict = {"handoffs": [], "pending_prompts": []}
        self._load()

    def _load(self) -> None:
        if self.index_path.exists():
            try:
                self._index = json.loads(self.index_path.read_text(encoding="utf-8"))
            except (ValueError, OSError):
                self._index = {"handoffs": [], "pending_prompts": []}

    def _flush(self) -> None:
        _atomic_write(self.index_path,
                      json.dumps(self._index, ensure_ascii=False, indent=2))

    # ---------- 摆渡写入 ----------

    def save_handoff(self, *, session_id: str, agent: str, cwd: str,
                     title: str | None, covers_until_iso: str | None,
                     status: str, handoff_md: str) -> dict:
        """写入交接 MD + index（同 session 覆盖旧记录）。返回 index 条目。"""
        with self._lock:
            ts = time.strftime("%Y%m%d_%H%M%S")
            hid = f"{ts}_{uuid.uuid4().hex[:6]}"
            fname = f"{hid}.md"
            (self.dir / fname).write_text(handoff_md, encoding="utf-8")
            entry = {
                "handoff_id": hid,
                "session_id": session_id,
                "agent": agent,
                "cwd": str(Path(cwd).resolve()) if cwd else "",
                "title": title or "",
                "created_at": time.strftime("%Y-%m-%d %H:%M:%S"),
                "covers_until": covers_until_iso or "",
                "covers_until_s": _parse_iso_utc(covers_until_iso),
                "status": status,
                "path": str(self.dir / fname),
                "blocked_at": None,
                "injected": [],
            }
            self._index["handoffs"] = [
                e for e in self._index["handoffs"]
                if not (e.get("session_id") == session_id and e.get("agent") == agent)
            ] + [entry]
            self._flush()
            return entry

    # ---------- 闸门查询 ----------

    def valid_handoff(self, agent: str, cwd: str, last_write: float) -> dict | None:
        """DESIGN §6.10-5：同 agent+cwd、status∈{fresh,skeleton}、
        covers_until ≥ last_write（含 60s 容差）、在新鲜度窗口内。"""
        if not cwd:
            return None
        norm = str(Path(cwd).resolve()).lower()
        now = time.time()
        best: dict | None = None
        with self._lock:
            for e in self._index["handoffs"]:
                if e.get("agent") != agent or e.get("status") not in ("fresh", "skeleton"):
                    continue
                if str(e.get("cwd", "")).lower() != norm:
                    continue
                if now - (e.get("covers_until_s") or 0) > FRESH_WINDOW_S:
                    continue
                if (e.get("covers_until_s") or 0) + COVERS_TOLERANCE_S < last_write:
                    continue
                if best is None or (e.get("covers_until_s") or 0) > (best.get("covers_until_s") or 0):
                    best = e
        return best

    def mark_blocked(self, handoff_id: str) -> None:
        with self._lock:
            for e in self._index["handoffs"]:
                if e["handoff_id"] == handoff_id:
                    e["blocked_at"] = time.strftime("%Y-%m-%d %H:%M:%S")
            self._flush()

    # ---------- 待续 prompt（单源：index 内嵌，DESIGN §6.7） ----------

    def save_pending_prompt(self, session_id: str, prompt: str, *, cap: int = 500) -> None:
        from .extract import token_estimate  # 局部导入避免环
        if token_estimate(prompt) > cap:
            prompt = prompt[:cap] + "…(超长截断)"
        with self._lock:
            kept = [p for p in self._index["pending_prompts"]
                    if p.get("session_id") != session_id]
            kept.append({"session_id": session_id, "prompt": prompt,
                         "blocked_at": time.strftime("%Y-%m-%d %H:%M:%S"),
                         "consumed_by": None})
            self._index["pending_prompts"] = kept
            self._flush()

    def pop_pending_prompt(self, session_id: str, consume_for: str | None = None) -> str | None:
        """取未消费的待续 prompt；传 consume_for 时同时标记消费（注入一次后不再给）。"""
        with self._lock:
            for p in self._index["pending_prompts"]:
                if p.get("session_id") == session_id and not p.get("consumed_by"):
                    prompt = p["prompt"]
                    if consume_for:
                        p["consumed_by"] = consume_for
                        self._flush()
                    return prompt
            return None

    # ---------- 归还 ----------

    def restore_candidates(self, agent: str, cwd: str) -> list[dict]:
        """DESIGN §6.9：agent+cwd 双键过滤，时间降序（最新在前）。"""
        if not cwd:
            return []
        norm = str(Path(cwd).resolve()).lower()
        now = time.time()
        with self._lock:
            cands = [e for e in self._index["handoffs"]
                     if e.get("agent") == agent
                     and str(e.get("cwd", "")).lower() == norm
                     and (e.get("covers_until_s") or 0) > now - FRESH_WINDOW_S
                     and e.get("status") in ("fresh", "skeleton")]
        cands.sort(key=lambda e: -(e.get("covers_until_s") or 0))
        return cands

    def mark_injected(self, handoff_id: str, session_id: str) -> None:
        with self._lock:
            for e in self._index["handoffs"]:
                if e["handoff_id"] == handoff_id and session_id not in e["injected"]:
                    e["injected"].append(session_id)
            self._flush()

    def read_handoff(self, entry: dict) -> str:
        try:
            return Path(entry["path"]).read_text(encoding="utf-8")
        except (OSError, KeyError):
            return ""
