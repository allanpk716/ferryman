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
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path, PureWindowsPath


def _norm_path(p: str) -> str:
    """路径键归一：统一分隔符 + 小写（Windows 不区分大小写）。"""
    return str(PureWindowsPath(p)).replace("\\", "/").lower()


def now_s() -> float:
    return datetime.now(timezone.utc).timestamp()


SUBAGENT_EVENT_LEAK_S = 3600     # 子代理计数泄漏防护：1h 无新事件视为已结束（Stop 丢失场景）


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
    # T51 等答复窗口（问询守望）：opened_ts None=无窗。开窗在 Watcher._maybe_qwatch
    # （命中谓词四条件），关窗只在下面的新写入分支（任何新写入=用户已作答）；
    # plan 开窗即排定（票03调度器：max_beats 跳 × beat_interval_s，开窗瞬间
    # 不跳），snapshot=(last_write,size) 供两道验新鲜度比对。
    qwatch_opened_ts: float | None = None
    qwatch_beats_fired: int = 0
    qwatch_plan: list[float] = field(default_factory=list)
    qwatch_snapshot: tuple[float, int] | None = None


class Ledger:
    def __init__(self) -> None:
        # RLock（T51 票03）：心跳两道验与两窗互斥开窗的 check-then-act 临界区
        # 与台账读写共用一把可重入锁——同线程嵌套（临界区内再调 get/touch）
        # 不卡死，跨线程 check-then-act 与台账更新严格串行。
        self._lock = threading.RLock()
        self._by_key: dict[tuple[str, str], SessionState] = {}
        self._by_path: dict[str, SessionState] = {}
        self.last_transcript_write: float = 0.0   # 健康监控用（DESIGN §4）
        # T32 子代理计数：(agent, session_id) → (运行数, 最后事件时刻)。仅内存——
        # daemon 重启丢计数由 T31 悬空检测兜底；Stop 丢失由泄漏防护兜底。
        self._subagents: dict[tuple[str, str], tuple[int, float]] = {}

    @property
    def lock(self) -> threading.RLock:
        """共享可重入锁（T51 票03）：调度器两道验/开窗判定与停车窗开窗的
        check-then-act 临界区用它包住，与台账读写串行（TOCTOU 归零）。"""
        return self._lock

    def subagent_event(self, agent: str, session_id: str, event: str) -> int:
        """SubagentStart/Stop 事件计数（嵌套各计一次，探针实测同属主会话）。返回当前运行数。"""
        with self._lock:
            key = (agent, session_id)
            count, _last = self._subagents.get(key, (0, 0.0))
            count = count + 1 if event == "start" else max(0, count - 1)
            if count == 0:
                self._subagents.pop(key, None)
            else:
                self._subagents[key] = (count, now_s())
            return count

    def subagent_active(self, agent: str, session_id: str) -> bool:
        """该会话是否有子代理运行中（含泄漏防护：事件超 1h 未更新 → 视为 0 并清理）。"""
        with self._lock:
            key = (agent, session_id)
            ent = self._subagents.get(key)
            if ent is None:
                return False
            count, last = ent
            if now_s() - last > SUBAGENT_EVENT_LEAK_S:
                del self._subagents[key]
                return False
            return count > 0

    def subagents_active_count(self) -> int:
        with self._lock:
            cutoff = now_s() - SUBAGENT_EVENT_LEAK_S
            return sum(c for c, last in self._subagents.values() if last > cutoff)

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
                if st.qwatch_opened_ts is not None:   # T51：任何新写入关窗
                    st.qwatch_opened_ts = None
                    st.qwatch_beats_fired = 0
                    st.qwatch_plan = []
                    st.qwatch_snapshot = None
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
