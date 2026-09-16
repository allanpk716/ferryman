"""台账（ledger）：session_id → 最后活动/大小/标题/项目/Agent，闲置判定唯一事实源。

DESIGN §4：
- 主键 agent + session_id；辅助键 transcript_path；
- 会话族系（lineage）：同一 transcript_path 出现新 session_id → 继承闲置史与交接关联；
  （首条 user 消息 hash 指纹经 E0a 校准偏弱——模板开场白碰撞多——仅作辅助信号，不进主判定）
- 时钟统一 UTC epoch 秒；闲置 = now − 台账.last_write（mtime 口径）；
- lookback=0：启动只登记不触发任何摆渡。
"""

from __future__ import annotations

import threading
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path, PureWindowsPath


def _norm_path(p: str) -> str:
    """路径键归一：统一分隔符 + 小写（Windows 不区分大小写）。"""
    return str(PureWindowsPath(p)).replace("\\", "/").lower()


def now_s() -> float:
    return datetime.now(timezone.utc).timestamp()


@dataclass
class SessionState:
    agent: str                       # "cc" | "codex"
    session_id: str
    transcript_path: str
    cwd: str = ""
    title: str | None = None
    last_write: float = 0.0          # UTC epoch 秒（文件 mtime 口径）
    size: int = 0
    peak_ctx: int = 0
    observed_active: bool = False    # daemon 启动后是否见过其活动（lookback=0 的摆渡闸）
    handed_off_at: float = 0.0       # 最近一次成功摆渡时间（防重复入队）
    enriched_write: float = -1.0     # 已富化(标题/峰值)到哪个 last_write 版本


class Ledger:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._by_key: dict[tuple[str, str], SessionState] = {}
        self._by_path: dict[str, SessionState] = {}
        self.last_transcript_write: float = 0.0   # 健康监控用（DESIGN §4）

    def touch(self, agent: str, session_id: str, transcript_path: str,
              *, mtime: float, size: int, cwd: str = "", title: str | None = None,
              peak_ctx: int = 0, daemon_started_at: float) -> SessionState:
        """登记/刷新一条会话。返回（可能经 lineage 继承的）状态。"""
        key = (agent, session_id)
        norm = _norm_path(transcript_path)
        with self._lock:
            self.last_transcript_write = max(self.last_transcript_write, mtime)
            st = self._by_key.get(key)
            if st is None:
                st = SessionState(agent=agent, session_id=session_id,
                                  transcript_path=transcript_path)
                # lineage：同 transcript_path 换了 session_id → 继承闲置史
                prev = self._by_path.get(norm)
                if prev is not None and prev.agent == agent:
                    st.last_write = prev.last_write
                    st.cwd = prev.cwd
                    st.title = prev.title
                    st.peak_ctx = prev.peak_ctx
                    st.handed_off_at = prev.handed_off_at
                self._by_key[key] = st
                self._by_path[norm] = st
            else:
                self._by_path[norm] = st
            st.size = size or st.size
            if cwd:
                st.cwd = cwd
            if title:
                st.title = title
            if peak_ctx:
                st.peak_ctx = peak_ctx
            if mtime > st.last_write:
                st.last_write = mtime
                if mtime >= daemon_started_at:
                    st.observed_active = True
            return st

    def get(self, agent: str, session_id: str) -> SessionState | None:
        with self._lock:
            return self._by_key.get((agent, session_id))

    def get_by_path(self, transcript_path: str) -> SessionState | None:
        with self._lock:
            return self._by_path.get(_norm_path(transcript_path))

    def all_sessions(self) -> list[SessionState]:
        with self._lock:
            return list(self._by_key.values())

    def mark_handed_off(self, st: SessionState) -> None:
        with self._lock:
            st.handed_off_at = now_s()
