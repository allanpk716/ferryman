"""T51×T48 合并专属回归：两窗互斥移植进停车状态机后的双向互斥＋死线口径。

合并语义锚定（本文件钉住 merge 时手工移植的三处，机械取侧必丢）：
- 互斥复验移植进 server.subagent 停车状态机的首开与重锚共用点——新窗落表
  在 _wlock 外层 → ledger.lock 内层复验等答复窗之后（锁序与既有路径一致）；
- parking_open 是无副作用无锁探针：委托 T48 窗口状态（活跃窗按泄漏口径、
  停车窗按停表过期口径 PARK_EXPIRE_S 判——与 window_wait 豁免口径一致但
  不做懒过期闭账记账）；
- _maybe_enqueue 三道推迟并存：等答复窗推迟＋悬空推迟＋window_wait 推迟；
  死线到线只跳过悬空让步，不跳过 window_wait（保守——两窗互斥成立时
  "死线遇停车窗"本不可达，此为防御性口径）。
"""

from __future__ import annotations

import json
import time
from pathlib import Path

from ferryman.accounts import Accounts
from ferryman.config import Config, QuestionWatchCfg, ThresholdCfg
from ferryman.daemon import Watcher
from ferryman.ledger import Ledger, now_s
from ferryman.server import PARK_EXPIRE_S, SUBAGENT_EVENT_LEAK_S, FerryDaemon

_SURGE = "\n".join(f"❓ **Q{i}** - **标题{i}**：这个问题该怎么答？" for i in range(1, 9))


def _line(d: dict) -> str:
    return json.dumps(d, ensure_ascii=False)


def _assistant(text, tools=(), mid="msg_1"):
    blocks = [{"type": "text", "text": text}]
    blocks += [{"type": "tool_use", "id": tid, "name": name, "input": {}}
               for tid, name in tools]
    return {"type": "assistant", "timestamp": "2026-09-18T12:00:00.000Z",
            "message": {"id": mid, "role": "assistant", "content": blocks,
                        "usage": {"input_tokens": 2000,
                                  "cache_read_input_tokens": 100,
                                  "cache_creation_input_tokens": 0,
                                  "output_tokens": 5}}}


def _write_transcript(projects: Path, sid: str, text, tools=()) -> Path:
    d = projects / "C--proj"
    d.mkdir(parents=True, exist_ok=True)
    f = d / f"{sid}.jsonl"
    lines = [{"type": "user", "timestamp": "2026-09-18T12:00:00.000Z",
              "message": {"role": "user", "content": "答你的问题"}},
             _assistant(text, tools)]
    f.write_text("\n".join(_line(x) for x in lines) + "\n", encoding="utf-8")
    return f


def _qw_cfg() -> Config:
    cfg = Config()
    cfg.thresholds = ThresholdCfg(summarize_s=10, block_s=100, min_ctx_tokens=1000)
    cfg.question_watch = QuestionWatchCfg(mode="observe",
                                          ferry_deadline_lead_s=40)
    return cfg                   # 死线 = block_s − lead = 60s


def _watcher(cfg: Config, led: Ledger, ferry_daemon=None) -> Watcher:
    w = Watcher.__new__(Watcher)
    w.cfg, w.ledger, w.store = cfg, led, None
    w.enqueue = lambda st: True
    w.started_at = 0
    w.ferry_daemon = ferry_daemon
    w._qwatch_seen = {}
    w._qwatch_hit_seen = {}
    w.qwatch_stats = None
    w.accounts = None
    w._enrich = lambda st: setattr(st, "peak_ctx", 50_000)
    return w


def _session(led: Ledger, projects: Path, sid: str, f: Path):
    st = led.touch("cc", sid, str(f), mtime=now_s(), size=42,
                   daemon_started_at=0)
    assert st.observed_active
    return st


# ---------- 方向一：等答复窗开着 → 停车窗不可开（首开与重锚两点） ----------

def test_merge_qwatch_open_blocks_first_open_and_reanchor(tmp_path):
    """等答复窗开着时 /subagent start 不落停车窗：首开挡住；重锚照常如实闭账
    旧停车窗（记 expired、无未来时刻）但不落新窗；写入关窗后 start 照常开。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    d = FerryDaemon(_qw_cfg(), led, None, lambda st: True, accounts=accts)
    f = _write_transcript(tmp_path / "projects", "mmx-1", _SURGE)
    st = _session(led, tmp_path / "projects", "mmx-1", f)
    st.qwatch_opened_ts = now_s()

    # 首开被挡：无停车窗行，但台账计数照常（豁免道仍由计数兜底）
    d.subagent({"event": "start", "agent": "cc", "session_id": "mmx-1"})
    assert ("cc", "mmx-1") not in d._windows
    assert led.subagent_active("cc", "mmx-1") is True

    # 重锚点被挡：塞一条已泄漏的旧停车窗，再来 start——旧窗如实闭账记
    # expired，新窗仍不落（互斥优先于记账完整性，与分支语义一致）
    d._windows[("cc", "mmx-1")] = {"opened_ts": now_s() - (SUBAGENT_EVENT_LEAK_S + 600),
                                   "stop_ts": now_s() - (SUBAGENT_EVENT_LEAK_S + 500),
                                   "saw_async": True}
    t0 = now_s()
    d.subagent({"event": "start", "agent": "cc", "session_id": "mmx-1"})
    assert ("cc", "mmx-1") not in d._windows
    rows = accts.read(kind="window", session="mmx-1")
    assert [r["close_reason"] for r in rows] == ["expired"]
    assert rows[0]["closed_ts"] <= t0 + 5            # 封顶 now，无未来时刻

    # 写入关等答复窗后：start 照常开停车窗（互斥解除自愈）
    mtime = now_s()
    f.write_text(f.read_text(encoding="utf-8"), encoding="utf-8")   # 触发新写入口径
    led.touch("cc", "mmx-1", str(f), mtime=mtime, size=f.stat().st_size,
              daemon_started_at=0)
    assert st.qwatch_opened_ts is None
    d.subagent({"event": "start", "agent": "cc", "session_id": "mmx-1"})
    assert ("cc", "mmx-1") in d._windows


# ---------- 方向二：停车窗开着 → 等答复窗不可开（真探针，非替身） ----------

def test_merge_parked_window_blocks_qwatch_open(tmp_path):
    """真 FerryDaemon 停车窗（停表未过期）→ 守望不开等答复窗；停车窗闭后
    同写入版本仍可开（瞬态阻塞不盖版本章）。"""
    led = Ledger()
    d = FerryDaemon(_qw_cfg(), led, None, lambda st: True)
    w = _watcher(_qw_cfg(), led, ferry_daemon=d)
    projects = tmp_path / "projects"
    f = _write_transcript(projects, "mmx-2", _SURGE)
    st = _session(led, projects, "mmx-2", f)
    d._windows[("cc", "mmx-2")] = {"opened_ts": now_s() - 120,
                                   "stop_ts": now_s() - 10, "saw_async": True}
    w._maybe_qwatch(st)
    assert st.qwatch_opened_ts is None               # 先开者赢

    d.note_usage("cc", "mmx-2", ts=now_s() + 200)    # 主会话恢复 → 停车窗闭
    assert d.parking_open("cc", "mmx-2") is False
    w._maybe_qwatch(st)
    assert st.qwatch_opened_ts is not None           # 解除后同版本仍可开


# ---------- 探针口径：委托 T48 停车状态、无副作用 ----------

def test_merge_parking_probe_delegates_to_parking_state(tmp_path):
    """探针按窗口状态分口径：活跃窗按泄漏口径；停车窗按停表过期口径——
    opened_ts 早已过泄漏期但停表新鲜的停车窗仍算开（合并语义，旧探针漏判）；
    过期停车窗视同已闭且**不产生闭账记账**（window_wait 才有懒过期副作用）。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    d = FerryDaemon(_qw_cfg(), led, None, lambda st: True, accounts=accts)
    key = ("cc", "mmx-3")
    d._windows[key] = {"opened_ts": now_s() - 120, "stop_ts": None,
                       "saw_async": False}
    assert d.parking_open("cc", "mmx-3") is True          # 活跃窗

    d._windows[key] = {"opened_ts": now_s() - (SUBAGENT_EVENT_LEAK_S + 300),
                       "stop_ts": now_s() - 30, "saw_async": True}
    assert d.parking_open("cc", "mmx-3") is True          # 停车新鲜（opened_ts 再旧也算开）

    d._windows[key]["stop_ts"] = now_s() - (PARK_EXPIRE_S + 30)
    assert d.parking_open("cc", "mmx-3") is False         # 停表过期视同已闭
    assert accts.read(kind="window") == []                # 探针零副作用（不闭账）
    assert d.window_wait("cc", "mmx-3") is False          # window_wait 才做懒过期
    rows = accts.read(kind="window")
    assert [r["close_reason"] for r in rows] == ["expired"]   # 副作用在正规道收口


# ---------- 死线口径：只跳悬空让步，不跳 window_wait ----------

def test_merge_deadline_skips_dangling_but_not_window_wait(tmp_path):
    """死线到线＋悬空 AskUserQuestion：无停车窗 → 强制入队（跳过悬空让步）；
    同状态下若停车窗在停（防御性并存态）→ window_wait 仍推迟（死线不豁免它）。"""
    led = Ledger()
    d = FerryDaemon(_qw_cfg(), led, None, lambda st: True)
    w = _watcher(_qw_cfg(), led, ferry_daemon=d)
    enq: list[str] = []
    w.enqueue = lambda st: enq.append(st.session_id) or True
    projects = tmp_path / "projects"
    f = _write_transcript(projects, "mmx-4", _SURGE,
                          tools=(("t1", "AskUserQuestion"),))
    st = _session(led, projects, "mmx-4", f)
    st.qwatch_opened_ts = now_s()
    st.last_write = now_s() - 60                        # 恰到死线（block 100 − lead 40）

    d._windows[("cc", "mmx-4")] = {"opened_ts": now_s() - 120,
                                   "stop_ts": now_s() - 5, "saw_async": True}
    w._maybe_enqueue(st)
    assert enq == []                                    # window_wait 推迟不被死线豁免

    d._windows.pop(("cc", "mmx-4"), None)               # 停车窗撤（防御态解除）
    w._maybe_enqueue(st)
    assert enq == ["mmx-4"]                             # 死线强制入队，跳过悬空
    assert st.handed_off_at > 0
