"""T51 票02 · 等答复窗口：台账态＋命中谓词＋摆渡推迟/死线＋两窗互斥＋配置校验。

窗口语义（spec 决策 2/3）：命中谓词四条件全真才开窗；任何新写入关窗
（Ledger.touch 钩子）；关窗后末条又是提问潮 → 重开新窗重新计时；窗口期间
摆渡入队推迟、最迟 block_s − ferry_deadline_lead_s 强制入队（死线不再因
悬空让步——含 AskUserQuestion，被拦 ⇒ 交接必已存在）；与停车窗互斥、
先开者赢。闸门语义零改动。
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

import ferryman.daemon as daemon_mod
from ferryman.config import (Config, QuestionWatchCfg, ThresholdCfg, load,
                             validate)
from ferryman.daemon import Watcher
from ferryman.ledger import Ledger, SessionState, now_s
from ferryman.server import FerryDaemon

# ---------- 语料 ----------

_SURGE = "\n".join(f"❓ **Q{i}** - **标题{i}**：这个问题该怎么答？" for i in range(1, 9))


def _line(d: dict) -> str:
    return json.dumps(d, ensure_ascii=False)


def _assistant(text: str, tools: tuple[tuple[str, str], ...] = (),
               mid: str = "msg_1", usage_input: int = 2000) -> dict:
    blocks = [{"type": "text", "text": text}]
    blocks += [{"type": "tool_use", "id": tid, "name": name, "input": {}}
               for tid, name in tools]
    return {"type": "assistant", "timestamp": "2026-09-18T12:00:00.000Z",
            "message": {"id": mid, "role": "assistant", "content": blocks,
                        "usage": {"input_tokens": usage_input,
                                  "cache_read_input_tokens": 100,
                                  "cache_creation_input_tokens": 0,
                                  "output_tokens": 5}}}


def _write_transcript(projects: Path, sid: str, text: str,
                      tools: tuple[tuple[str, str], ...] = ()) -> Path:
    d = projects / "C--proj"
    d.mkdir(parents=True, exist_ok=True)
    f = d / f"{sid}.jsonl"
    lines = [{"type": "user", "timestamp": "2026-09-18T12:00:00.000Z",
              "message": {"role": "user", "content": "答你的问题"}},
             _assistant(text, tools)]
    f.write_text("\n".join(_line(x) for x in lines) + "\n", encoding="utf-8")
    return f


# ---------- 装配 ----------

def _qw_cfg(mode: str = "observe", **qw_kw) -> Config:
    cfg = Config()
    cfg.thresholds = ThresholdCfg(summarize_s=10, block_s=100, min_ctx_tokens=1000)
    cfg.question_watch = QuestionWatchCfg(mode=mode,
                                          ferry_deadline_lead_s=40, **qw_kw)
    return cfg                   # 死线 = block_s − lead = 60s


def _watcher(cfg: Config, led: Ledger, ferry_daemon=None) -> Watcher:
    w = Watcher.__new__(Watcher)
    w.cfg, w.ledger, w.store = cfg, led, None
    w.enqueue = lambda st: True
    w.started_at = 0
    w.ferry_daemon = ferry_daemon
    w._qwatch_seen = {}
    w._enrich = lambda st: setattr(st, "peak_ctx", 50_000)   # 条件④确定性：富化即达标
    return w


def _session(led: Ledger, projects: Path, sid: str, f: Path) -> SessionState:
    st = led.touch("cc", sid, str(f), mtime=now_s(), size=42,
                   cwd=str(projects), daemon_started_at=0)
    assert st.observed_active
    return st


def _opened(st: SessionState) -> bool:
    return st.qwatch_opened_ts is not None


# ---------- 台账窗口态：开窗字段 + 新写入关窗 ----------

def test_t51_window_fields_on_open(tmp_path):
    """四条件全真开窗：opened_ts/beats_fired=0/plan 空/snapshot=(mtime,size)。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    f = _write_transcript(projects, "win-1", _SURGE)
    st = _session(led, projects, "win-1", f)
    w._maybe_qwatch(st)
    assert _opened(st)
    assert st.qwatch_beats_fired == 0
    assert st.qwatch_plan == []                     # 计划由票03调度器填充
    assert st.qwatch_snapshot == (st.last_write, st.size)


def test_t51_new_write_closes_window(tmp_path):
    """任何新写入关窗（台账 touch 路径挂钩）：窗口字段整体复位。"""
    led = Ledger()
    f = tmp_path / "w.jsonl"
    f.write_text("{}\n", encoding="utf-8")
    st = led.touch("cc", "wc-1", str(f), mtime=1000, size=10, daemon_started_at=0)
    st.qwatch_opened_ts = 900.0
    st.qwatch_beats_fired = 2
    st.qwatch_plan = [100.0, 200.0]
    st.qwatch_snapshot = (900.0, 10)
    led.touch("cc", "wc-1", str(f), mtime=1005, size=20, daemon_started_at=0)
    assert st.qwatch_opened_ts is None
    assert st.qwatch_beats_fired == 0
    assert st.qwatch_plan == []
    assert st.qwatch_snapshot is None


def test_t51_stale_mtime_does_not_close_window(tmp_path):
    """旧 mtime（乱序到达）不算新写入：窗口保留。"""
    led = Ledger()
    f = tmp_path / "w2.jsonl"
    f.write_text("{}\n", encoding="utf-8")
    st = led.touch("cc", "wc-2", str(f), mtime=1000, size=10, daemon_started_at=0)
    st.qwatch_opened_ts = 900.0
    led.touch("cc", "wc-2", str(f), mtime=990, size=11, daemon_started_at=0)
    assert _opened(st)


# ---------- 命中谓词四条件真值表 ----------

def test_t51_predicate_all_four_true_opens(tmp_path):
    """靶场景：提问潮 + AskUserQuestion 悬空 → 开窗（悬空⊆{AQ} 直用 Verdict）。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    f = _write_transcript(projects, "pred-1", _SURGE,
                          tools=(("t1", "AskUserQuestion"),))
    st = _session(led, projects, "pred-1", f)
    w._maybe_qwatch(st)
    assert _opened(st)


def test_t51_predicate_not_surge_no_open(tmp_path):
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    f = _write_transcript(projects, "pred-2", "干完了，没有问题。")
    st = _session(led, projects, "pred-2", f)
    w._maybe_qwatch(st)
    assert not _opened(st)


def test_t51_predicate_dangling_other_tool_no_open(tmp_path):
    """悬空含非 AskUserQuestion 工具（条件②假）→ 不开窗。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    f = _write_transcript(projects, "pred-3", _SURGE, tools=(("t1", "Bash"),))
    st = _session(led, projects, "pred-3", f)
    w._maybe_qwatch(st)
    assert not _opened(st)


def test_t51_predicate_subagent_active_no_open_then_opens_after_stop(tmp_path):
    """条件③子代理在飞 → 不开窗（瞬态：不盖版本章，stop 后同版本仍可开）。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    f = _write_transcript(projects, "pred-4", _SURGE)
    st = _session(led, projects, "pred-4", f)
    led.subagent_event("cc", "pred-4", "start")
    w._maybe_qwatch(st)
    assert not _opened(st)
    led.subagent_event("cc", "pred-4", "stop")
    w._maybe_qwatch(st)
    assert _opened(st)


def test_t51_predicate_ctx_below_min_no_open(tmp_path):
    """条件④前缀 < min_ctx_tokens → 不开窗（结论性：盖版本章）。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    w._enrich = lambda st: setattr(st, "peak_ctx", 100)
    f = _write_transcript(projects, "pred-5", _SURGE)
    st = _session(led, projects, "pred-5", f)
    w._maybe_qwatch(st)
    assert not _opened(st)
    assert w._qwatch_seen[("cc", "pred-5")] == st.last_write


def test_t51_mode_off_never_opens(tmp_path):
    """mode=off 功能整体关闭：条件全真也不开窗（默认零开销）。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(mode="off"), led)
    f = _write_transcript(projects, "pred-6", _SURGE)
    st = _session(led, projects, "pred-6", f)
    w._maybe_qwatch(st)
    assert not _opened(st)


def test_t51_detection_cached_per_write_version(tmp_path):
    """同一写入版本只判一次（不逐轮读盘）；新写入才重判。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    f = _write_transcript(projects, "pred-7", "干完了，没有问题。")
    st = _session(led, projects, "pred-7", f)
    calls = {"n": 0}
    real = daemon_mod.detect

    def counting(path, min_questions=5, **kw):
        calls["n"] += 1
        return real(path, min_questions=min_questions, **kw)

    daemon_mod.detect = counting
    try:
        w._maybe_qwatch(st)
        w._maybe_qwatch(st)
        w._maybe_qwatch(st)
        assert calls["n"] == 1
    finally:
        daemon_mod.detect = real


# ---------- 两窗互斥（先开者赢） ----------

class _FakeParking:
    def __init__(self, open_: bool = False):
        self.open_ = open_

    def parking_open(self, agent, session_id):
        return self.open_


def test_t51_parking_window_open_blocks_qwatch_open(tmp_path):
    """停车窗开着 → 不开等答复窗（先开者赢；瞬态阻塞不盖版本章）。"""
    projects = tmp_path / "projects"
    led = Ledger()
    fake = _FakeParking(open_=True)
    w = _watcher(_qw_cfg(), led, ferry_daemon=fake)
    f = _write_transcript(projects, "mutex-1", _SURGE)
    st = _session(led, projects, "mutex-1", f)
    w._maybe_qwatch(st)
    assert not _opened(st)
    fake.open_ = False                              # 停车窗闭 → 同版本仍可开
    w._maybe_qwatch(st)
    assert _opened(st)


def test_t51_parking_probe_leak_guard_and_never_raises(tmp_path):
    """parking_open 探测：泄漏期旧窗视同已闭；异常按未开。"""
    d = FerryDaemon(Config(), Ledger(), None, lambda st: True)
    assert d.parking_open("cc", "s") is False
    d._windows[("cc", "s")] = {"opened_ts": now_s()}
    assert d.parking_open("cc", "s") is True
    d._windows[("cc", "s")] = {"opened_ts": now_s() - 3601}
    assert d.parking_open("cc", "s") is False       # 泄漏口径（SUBAGENT_EVENT_LEAK_S）


def test_t51_qwatch_window_blocks_parking_open():
    """/subagent start 不开停车窗（"反之亦然"侧）；等答复窗不受 start/stop 影响。"""
    led = Ledger()
    f = Path("C:/nonexistent/mx.jsonl")
    st = led.touch("cc", "mx-1", str(f), mtime=now_s(), size=1, daemon_started_at=0)
    st.qwatch_opened_ts = now_s()
    d = FerryDaemon(Config(), led, None, lambda st: True)
    d.subagent({"event": "start", "agent": "cc", "session_id": "mx-1"})
    assert ("cc", "mx-1") not in d._windows
    assert led.subagent_active("cc", "mx-1") is True
    d.subagent({"event": "stop", "agent": "cc", "session_id": "mx-1"})
    assert _opened(st)                              # 只有新写入关窗


def test_t51_parking_opens_normally_without_qwatch_window():
    """无等答复窗时停车窗照开（既有 T41 行为零改动）。"""
    led = Ledger()
    d = FerryDaemon(Config(), led, None, lambda st: True)
    d.subagent({"event": "start", "agent": "cc", "session_id": "mx-2"})
    assert ("cc", "mx-2") in d._windows


# ---------- 摆渡推迟与死线 ----------

def test_t51_window_defers_regular_enqueue(tmp_path):
    """窗口期间：闲置达 summarize 也不入队（推迟）；无窗对照照常入队。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    enq: list[str] = []
    w.enqueue = lambda st: enq.append(st.session_id) or True
    f1 = _write_transcript(projects, "defer-1", _SURGE)
    st1 = _session(led, projects, "defer-1", f1)
    w._maybe_qwatch(st1)
    assert _opened(st1)
    st1.last_write -= 30                            # ≥ summarize(10)，< 死线(60)
    w._maybe_enqueue(st1)
    assert enq == []
    f2 = _write_transcript(projects, "defer-2", "干完了，没有问题。")
    st2 = _session(led, projects, "defer-2", f2)    # 对照：无窗
    st2.last_write -= 30
    w._maybe_enqueue(st2)
    assert enq == ["defer-2"]


def test_t51_deadline_forces_enqueue_past_dangling(tmp_path):
    """死线（idle ≥ block_s − lead）：强制入队，不再因悬空（含 AQ）让步。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    enq: list[str] = []
    w.enqueue = lambda st: enq.append(st.session_id) or True
    f = _write_transcript(projects, "dl-1", _SURGE,
                          tools=(("t1", "AskUserQuestion"),))
    st = _session(led, projects, "dl-1", f)
    w._maybe_qwatch(st)
    st.last_write = now_s() - 70                    # ≥ 死线 60
    w._maybe_enqueue(st)
    assert enq == ["dl-1"]
    assert st.handed_off_at > 0                     # 入队即记（防重复入队）


def test_t51_deadline_boundary_exact_idle_is_due(tmp_path):
    """idle 恰等于死线（block_s − lead）→ 已到，强制入队。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    enq: list[str] = []
    w.enqueue = lambda st: enq.append(st.session_id) or True
    f = _write_transcript(projects, "dl-2", _SURGE)
    st = _session(led, projects, "dl-2", f)
    w._maybe_qwatch(st)
    st.last_write = now_s() - 60
    w._maybe_enqueue(st)
    assert enq == ["dl-2"]


def test_t51_no_window_dangling_defers_unchanged(tmp_path):
    """无窗会话语义零改动：悬空照旧推迟、摆渡阈值照旧。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    enq: list[str] = []
    w.enqueue = lambda st: enq.append(st.session_id) or True
    f = _write_transcript(projects, "old-1", _SURGE, tools=(("t1", "Bash"),))
    st = _session(led, projects, "old-1", f)
    st.last_write -= 70
    w._maybe_enqueue(st)
    assert enq == []                                # 悬空推迟仍在（无窗不受死线豁免）


def test_t51_qwatch_deadline_helper_truth_table(tmp_path):
    """_qwatch_deadline 二元组真值表：无窗 (F,F)；窗内未到 (F,T)；到线 (T,T)。"""
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    f = tmp_path / "h.jsonl"
    f.write_text("{}\n", encoding="utf-8")
    st = led.touch("cc", "h-1", str(f), mtime=now_s(), size=1, daemon_started_at=0)
    th = w.cfg.threshold_for("cc")
    assert w._qwatch_deadline(st, th) == (False, False)     # 无窗
    st.qwatch_opened_ts = now_s()
    st.last_write = now_s() - 59
    assert w._qwatch_deadline(st, th) == (False, True)      # 窗内未到死线
    st.last_write = now_s() - 60
    assert w._qwatch_deadline(st, th) == (True, True)       # 死线已到


# ---------- 异常吞掉（T48 同款纪律） ----------

def test_t51_detect_exception_swallowed(tmp_path, monkeypatch, capsys):
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    f = _write_transcript(projects, "boom-1", _SURGE)
    st = _session(led, projects, "boom-1", f)
    monkeypatch.setattr(daemon_mod, "detect",
                        lambda *a, **k: (_ for _ in ()).throw(RuntimeError("boom")))
    w._maybe_qwatch(st)                             # 不外抛
    assert not _opened(st)


def test_t51_parking_probe_exception_swallowed(tmp_path):
    """parking_open 抛错 → 按未开处理路径吞掉，不影响守望。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led, ferry_daemon=None)
    w.ferry_daemon = type("X", (), {"parking_open": staticmethod(
        lambda a, s: (_ for _ in ()).throw(RuntimeError("boom")))})()
    f = _write_transcript(projects, "boom-2", _SURGE)
    st = _session(led, projects, "boom-2", f)
    w._maybe_qwatch(st)                             # 不外抛（外层兜底）
    assert not _opened(st)


# ---------- 关窗后重开（重新计时） ----------

def test_t51_reopen_after_write_resets_timer(tmp_path):
    """新写入关窗 → 同轮末条仍是提问潮 → 重开新窗（opened_ts/snapshot 刷新）。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    f = _write_transcript(projects, "re-1", _SURGE)
    st = _session(led, projects, "re-1", f)
    w._maybe_qwatch(st)
    t1 = st.qwatch_opened_ts
    snap1 = st.qwatch_snapshot
    new_mtime = now_s() + 10
    st2 = led.touch("cc", "re-1", str(f), mtime=new_mtime, size=99,
                    cwd=str(projects), daemon_started_at=0)   # 用户提交 → 关窗
    assert st2 is st and not _opened(st)
    w._maybe_qwatch(st)                             # 同轮判定：末条仍是提问潮
    assert _opened(st)
    assert st.qwatch_opened_ts >= t1                # 重新计时
    assert st.qwatch_snapshot != snap1
    assert st.qwatch_snapshot == (st.last_write, st.size)


def test_t51_close_resumes_regular_ferry_scheduling(tmp_path):
    """关窗即恢复常规摆渡调度：窗口推迟解除，达 summarize 照常入队。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    enq: list[str] = []
    w.enqueue = lambda st: enq.append(st.session_id) or True
    f = _write_transcript(projects, "rs-1", _SURGE)
    st = _session(led, projects, "rs-1", f)
    w._maybe_qwatch(st)
    st.last_write -= 30
    w._maybe_enqueue(st)
    assert enq == []                                # 窗口期间推迟
    with f.open("a", encoding="utf-8") as fh:       # 用户新一轮落盘（末条非提问潮）
        fh.write(_line(_assistant("收到，逐条答如下。没有问题。", mid="msg_2")) + "\n")
    led.touch("cc", "rs-1", str(f), mtime=now_s() + 5, size=f.stat().st_size,
              cwd=str(projects), daemon_started_at=0)   # 新写入 → 关窗
    w._maybe_qwatch(st)
    assert not _opened(st)                          # 末条非提问潮不重开
    st.last_write = now_s() - 30                    # 重新攒闲置达 summarize
    w._maybe_enqueue(st)
    assert enq == ["rs-1"]                          # 关窗后恢复常规入队


def test_t51_close_then_calm_no_reopen(tmp_path):
    """关窗后末条非提问潮 → 不重开（恢复常规摆渡调度）。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(), led)
    f = _write_transcript(projects, "re-2", _SURGE)
    st = _session(led, projects, "re-2", f)
    w._maybe_qwatch(st)
    assert _opened(st)
    with f.open("a", encoding="utf-8") as fh:       # 用户作答落盘（末条非提问潮）
        fh.write(_line(_assistant("收到，逐条答如下。没有问题。", mid="msg_2")) + "\n")
    led.touch("cc", "re-2", str(f), mtime=now_s() + 5, size=f.stat().st_size,
              cwd=str(projects), daemon_started_at=0)
    w._maybe_qwatch(st)
    assert not _opened(st)


# ---------- 配置（spec 决策 9） ----------

def _val_cfg(mode="observe", lead=480.0, summarize_s=25 * 60, block_s=35 * 60):
    c = Config()
    c.thresholds = ThresholdCfg(summarize_s=summarize_s, block_s=block_s)
    c.question_watch = QuestionWatchCfg(mode=mode, ferry_deadline_lead_s=lead)
    return c


def test_t51_config_mode_validated():
    with pytest.raises(ValueError, match="question_watch.mode"):
        validate(_val_cfg(mode="bogus"))


def test_t51_config_lead_below_floor_clamped_with_warning(capsys):
    c = _val_cfg(lead=10)
    validate(c)
    assert c.question_watch.ferry_deadline_lead_s == 60     # 夹取下限
    assert "ferry_deadline_lead_s" in capsys.readouterr().out


def test_t51_config_lead_over_block_rejected():
    """summarize_s + lead > block_s → 拒绝（死线必须赶在闸门拦截之前）。"""
    with pytest.raises(ValueError, match="ferry_deadline_lead_s"):
        validate(_val_cfg(lead=700))                # 1500 + 700 > 2100


def test_t51_config_lead_exact_upper_bound_passes():
    validate(_val_cfg(lead=600))                    # 1500 + 600 = 2100 ≤ block_s


def test_t51_config_mode_off_skips_lead_checks():
    """mode=off 功能关闭：lead 不校验（存量小阈值配置零影响）。"""
    c = _val_cfg(mode="off", lead=99999, summarize_s=10, block_s=200)
    validate(c)
    assert c.question_watch.ferry_deadline_lead_s == 99999


def test_t51_config_load_parses_question_watch(tmp_path, monkeypatch):
    f = tmp_path / "my.toml"
    f.write_text(
        '[question_watch]\nmode = "observe"\nmin_questions = 3\n'
        "beat_interval_s = 300\nmax_beats = 1\nferry_deadline_lead_s = 100\n",
        encoding="utf-8")
    monkeypatch.setenv("FERRYMAN_CONFIG", str(f))
    cfg = load()
    qw = cfg.question_watch
    assert (qw.mode, qw.min_questions, qw.beat_interval_s, qw.max_beats,
            qw.ferry_deadline_lead_s) == ("observe", 3, 300.0, 1, 100.0)


def test_t51_config_defaults_off():
    qw = QuestionWatchCfg()
    assert qw.mode == "off" and qw.min_questions == 5 \
        and qw.beat_interval_s == 420 and qw.max_beats == 2 \
        and qw.ferry_deadline_lead_s == 480


# ---------- 接线 ----------

def test_t51_harness_wires_ferry_daemon_for_mutex(h):
    """Harness/serve 接线：守望持有 FerryDaemon 引用（互斥探测用）。"""
    assert h.watcher.ferry_daemon is h.daemon
