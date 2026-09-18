"""T51 票03 · 心跳调度器：两道验＋全局串行＋observe 零网络＋三态熔断＋记账＋RLock。

调度语义（spec 决策 4-7）：开窗排计划（开窗瞬间不跳）；每跳到期先两道验
（①台账 last_write 版本章无新写入 ②复 stat 转录 mtime+size 与开窗快照
一致），任一不符取消本跳并作废剩余计划；全局同时最多 1 跳在途（跨会话
串行）；observe 零网络（NoopSender），真实 sender 可注入（enforce 本票
只落接口占位，Q14 段二/三前不实现 HTTP）；三态 HIT/MISS/ERROR 与熔断
（连续 2 MISS 降级 enforce→observe＋告警、连续 3 ERROR 暂停当前窗口
剩余跳＋告警、ERROR 不计入也不重置 MISS 连击）；每跳（含 observe 演练）
逐条入 beat 科目。调度与台账共享 RLock（顺手吸收票02 评审的两窗互斥
TOCTOU）。一切异常吞掉不外抛（T48 同款纪律）。
"""

from __future__ import annotations

import json
import os
import threading
import time
from pathlib import Path

import ferryman.config as config_mod
from ferryman.accounts import Accounts
from ferryman.beat import (BeatBreaker, BeatResult, BeatSender, NoopSender,
                           classify)
from ferryman.config import (Config, FERRY_WALL_TIMEOUT_S, QuestionWatchCfg,
                             ThresholdCfg, validate)
from ferryman.daemon import Watcher
from ferryman.ledger import Ledger, now_s
from ferryman.server import FerryDaemon

# ---------- 语料（与票02 同款） ----------

_SURGE = "\n".join(f"❓ **Q{i}** - **标题{i}**：这个问题该怎么答？" for i in range(1, 9))


def _line(d: dict) -> str:
    return json.dumps(d, ensure_ascii=False)


def _assistant(text: str, mid: str = "msg_1") -> dict:
    return {"type": "assistant", "timestamp": "2026-09-18T12:00:00.000Z",
            "message": {"id": mid, "role": "assistant",
                        "content": [{"type": "text", "text": text}],
                        "usage": {"input_tokens": 2000,
                                  "cache_read_input_tokens": 100,
                                  "cache_creation_input_tokens": 0,
                                  "output_tokens": 5}}}


def _write_transcript(projects: Path, sid: str, text: str) -> Path:
    d = projects / "C--proj"
    d.mkdir(parents=True, exist_ok=True)
    f = d / f"{sid}.jsonl"
    lines = [{"type": "user", "timestamp": "2026-09-18T12:00:00.000Z",
              "message": {"role": "user", "content": "答你的问题"}},
             _assistant(text)]
    f.write_text("\n".join(_line(x) for x in lines) + "\n", encoding="utf-8")
    return f


# ---------- 装配 ----------

def _qw_cfg(mode: str = "observe", **qw_kw) -> Config:
    cfg = Config()
    cfg.thresholds = ThresholdCfg(summarize_s=10, block_s=100, min_ctx_tokens=1000)
    cfg.question_watch = QuestionWatchCfg(mode=mode, ferry_deadline_lead_s=40, **qw_kw)
    return cfg


def _watcher(cfg: Config, led: Ledger, accounts=None, ferry_daemon=None,
             sender=None) -> Watcher:
    w = Watcher.__new__(Watcher)
    w.cfg, w.ledger, w.store = cfg, led, None
    w.enqueue = lambda st: True
    w.started_at = 0
    w.accounts = accounts
    w.ferry_daemon = ferry_daemon
    w._qwatch_seen = {}
    w._qwatch_hit_seen = {}                 # 票04：命中事件版本去重章
    w.qwatch_stats = None                   # 票04：未接计数器（旧用例零改动）
    w._enrich = lambda st: setattr(st, "peak_ctx", 50_000)   # 条件④确定性
    w._beat_sender = sender
    w._beat_in_flight = False
    w._breaker = BeatBreaker()
    w._noop_sender = NoopSender()
    w._beat_enforce_warned = False
    return w


def _touch(led: Ledger, projects: Path, sid: str, f: Path):
    # mtime 用文件真实 st_mtime（与生产 _poll_cc 同口径）——两道验②比对的就是它
    st = led.touch("cc", sid, str(f), mtime=f.stat().st_mtime,
                   size=f.stat().st_size, cwd=str(projects), daemon_started_at=0)
    assert st.observed_active
    return st


def _bare_session(led: Ledger, tmp_path: Path, sid: str):
    """不经开窗谓词的裸会话（调度器单测直接摆窗口字段）。"""
    f = tmp_path / f"{sid}.jsonl"
    f.write_text("{}\n", encoding="utf-8")
    st = led.touch("cc", sid, str(f), mtime=f.stat().st_mtime,
                   size=f.stat().st_size, cwd=str(tmp_path), daemon_started_at=0)
    return st, f


def _open(st, interval: float = 420.0, beats: int = 2):
    """手工摆成已开窗态（计划/快照与 _maybe_qwatch 开窗路径同形状）。"""
    t0 = now_s()
    st.qwatch_opened_ts = t0
    st.qwatch_beats_fired = 0
    st.qwatch_plan = [t0 + i * interval for i in range(1, beats + 1)]
    st.qwatch_snapshot = (st.last_write, st.size)
    return st


class RecordingSender:
    """fake sender：记录收到的 BeatPlan，按脚本吐 BeatResult（默认 HIT）。"""

    def __init__(self, results: list[BeatResult] | None = None):
        self.plans: list = []
        self.results = list(results or [])

    def send(self, plan):
        self.plans.append(plan)
        if self.results:
            return self.results.pop(0)
        return BeatResult(sent=True, ok=True, input_tokens=100,
                          cache_read_tokens=1900, provider="cc-proxy",
                          model="glm-4", cost_actual=0.01)


class BlockingSender:
    """fake sender：阻塞在 send 里直到放行（观测全局串行）。"""

    def __init__(self):
        self.entered = threading.Event()
        self.release = threading.Event()
        self.count = 0

    def send(self, plan):
        self.count += 1
        self.entered.set()
        self.release.wait(5)
        return BeatResult(sent=True, ok=True, input_tokens=100,
                          cache_read_tokens=1900)


_HIT = BeatResult(sent=True, ok=True, input_tokens=100, cache_read_tokens=1900)
_MISS = BeatResult(sent=True, ok=True, input_tokens=2000, cache_read_tokens=0)
_ERR = BeatResult(sent=True, ok=False, err="429-after-retry")


# ---------- 计划生成（验收①） ----------

def test_plan_scheduled_on_open_no_immediate_fire(tmp_path):
    """开窗排计划：max_beats 跳按 beat_interval_s 排定；首跳在 t0+interval。"""
    projects = tmp_path / "projects"
    led = Ledger()
    w = _watcher(_qw_cfg(beat_interval_s=300.0, max_beats=3), led)
    f = _write_transcript(projects, "plan-1", _SURGE)
    st = _touch(led, projects, "plan-1", f)
    w._maybe_qwatch(st)
    assert st.qwatch_opened_ts is not None
    assert st.qwatch_plan == [st.qwatch_opened_ts + 300.0,
                              st.qwatch_opened_ts + 600.0,
                              st.qwatch_opened_ts + 900.0]
    assert all(t > now_s() for t in st.qwatch_plan)     # 开窗瞬间不跳


def test_nothing_due_no_fire(tmp_path):
    """未到期的跳不发；到期恰发一跳（出队＋beats_fired 计数）。"""
    led = Ledger()
    w = _watcher(_qw_cfg(mode="enforce"), led, sender=RecordingSender())
    st, _f = _bare_session(led, tmp_path, "due-1")
    _open(st, interval=9999.0)
    calls: list[float] = []
    w._send_beat = lambda st, ts: calls.append(ts) or BeatResult(sent=False)
    w._maybe_fire_beats(st)
    assert calls == []                                  # 全在未来：不发
    future = now_s() + 999
    st.qwatch_plan = [now_s() - 1, future]
    w._maybe_fire_beats(st)
    assert len(calls) == 1                              # 一轮至多一发（串行节奏）
    assert st.qwatch_beats_fired == 1
    assert st.qwatch_plan == [future]                   # 未到期跳保留


# ---------- 两道验（验收②） ----------

def test_two_check_file_stat_changed_no_send_voids_plan(tmp_path):
    """两道验②：预检通过但转录 mtime/size 变 → 不发并作废剩余计划。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    w = _watcher(_qw_cfg(), led, accounts=accts)
    st, f = _bare_session(led, tmp_path, "tc-1")
    _open(st)
    st.qwatch_plan = [now_s() - 1, now_s() + 999]
    f.write_text("{}\n{}\n", encoding="utf-8")          # 用户提交：文件变 + mtime 变
    new_t = st.last_write + 5
    os.utime(f, (new_t, new_t))
    w._maybe_fire_beats(st)
    assert accts.read(kind="beat") == []                # 未发
    assert st.qwatch_plan == []                         # 剩余计划作废
    assert st.qwatch_beats_fired == 0
    assert st.qwatch_opened_ts is not None              # 窗留给 touch 关（不越权）


def test_two_check_ledger_version_moved_no_send(tmp_path):
    """两道验①：台账 last_write 版本章前进（快照基线后见过新写入）→ 不发作废。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    w = _watcher(_qw_cfg(), led, accounts=accts)
    st, _f = _bare_session(led, tmp_path, "tc-2")
    _open(st)
    st.qwatch_plan = [now_s() - 1]
    st.last_write = st.qwatch_snapshot[0] + 5           # 模拟：写已入账、窗未及关
    w._maybe_fire_beats(st)
    assert accts.read(kind="beat") == []
    assert st.qwatch_plan == []


def test_new_write_cancels_pending_beats(tmp_path):
    """任何新写入 → 关窗＋剩余跳全部作废（守望轮询粒度内生效）。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    w = _watcher(_qw_cfg(), led, accounts=accts)
    st, f = _bare_session(led, tmp_path, "cc-1")
    _open(st)
    st.qwatch_plan = [now_s() + 999]                    # 未来跳
    f.write_text("{}\n{}\n", encoding="utf-8")
    led.touch("cc", "cc-1", str(f), mtime=now_s() + 5, size=f.stat().st_size,
              cwd=str(tmp_path), daemon_started_at=0)   # 新写入 → 关窗清计划
    w._maybe_fire_beats(st)
    assert st.qwatch_opened_ts is None
    assert st.qwatch_plan == []
    assert accts.read(kind="beat") == []                # 一跳都没发


# ---------- 全局串行（验收④） ----------

def test_global_serial_one_beat_in_flight(tmp_path):
    """两窗同刻到期：sender 阻塞期间第二窗不进（全局同时最多 1 跳在途）。"""
    led = Ledger()
    blocker = BlockingSender()
    w = _watcher(_qw_cfg(mode="enforce"), led, sender=blocker)
    st_a, _ = _bare_session(led, tmp_path, "ser-a")
    st_b, _ = _bare_session(led, tmp_path, "ser-b")
    _open(st_a)
    _open(st_b)
    due = now_s() - 1
    st_a.qwatch_plan = [due]
    st_b.qwatch_plan = [due]
    th = threading.Thread(target=w._maybe_fire_beats, args=(st_a,), daemon=True)
    th.start()
    assert blocker.entered.wait(5)                      # A 已进 sender（在途）
    w._maybe_fire_beats(st_b)                           # B 到期但 A 在途 → 跳过
    assert blocker.count == 1
    blocker.release.set()
    th.join(5)
    w._maybe_fire_beats(st_b)                           # A 出场后 B 下轮照发
    assert blocker.count == 2
    blocker.release.set()


# ---------- observe 零网络（验收⑤） ----------

def test_observe_mode_never_calls_injected_sender(tmp_path):
    """mode=observe：注入的 sender 不被调（零网络）；演练跳照落账（标 observe）。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    rec = RecordingSender()
    w = _watcher(_qw_cfg(mode="observe"), led, accounts=accts, sender=rec)
    st, _f = _bare_session(led, tmp_path, "ob-1")
    _open(st)
    st.qwatch_plan = [now_s() - 1]
    w._maybe_fire_beats(st)
    assert rec.plans == []                              # 零网络
    rows = accts.read(kind="beat")
    assert len(rows) == 1 and rows[0]["outcome"] == "observe"
    assert rows[0]["cache_read"] == 0
    assert st.qwatch_beats_fired == 1


def test_enforce_without_real_sender_drills_with_warning(tmp_path, capsys):
    """enforce 未注入真实 sender（Q14 前）：按 observe 演练记账＋告警一次。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    w = _watcher(_qw_cfg(mode="enforce"), led, accounts=accts, sender=None)
    st, _f = _bare_session(led, tmp_path, "en-1")
    _open(st)
    st.qwatch_plan = [now_s() - 1]
    w._maybe_fire_beats(st)
    st.qwatch_plan = [now_s() - 1]
    w._maybe_fire_beats(st)
    out = capsys.readouterr().out
    assert out.count("observe 演练") == 1               # 只告警一次
    rows = accts.read(kind="beat")
    assert [r["outcome"] for r in rows] == ["observe", "observe"]


# ---------- 三态（验收⑥） ----------

def test_classify_three_states():
    assert classify(BeatResult(sent=False)) == "observe"
    assert classify(BeatResult(sent=True, ok=False, err="429")) == "error"
    assert classify(BeatResult(sent=True, ok=True, input_tokens=100,
                               cache_read_tokens=900)) == "hit"          # 0.9
    assert classify(BeatResult(sent=True, ok=True, input_tokens=100,
                               cache_read_tokens=100)) == "hit"          # 0.5 边界
    assert classify(BeatResult(sent=True, ok=True, input_tokens=2000,
                               cache_read_tokens=0)) == "miss"           # ≈0
    assert classify(BeatResult(sent=True, ok=True)) == "miss"             # 全零
    assert classify(BeatResult(sent=True, ok=True, input_tokens=600,
                               cache_read_tokens=400)) == "miss"         # 0.4 < 0.5


def test_three_states_booked_from_fake_sender(tmp_path):
    """fake sender 注入三态：每跳 outcome/实收逐条入既有 beat 科目。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    rec = RecordingSender(results=[
        _HIT,
        BeatResult(sent=True, ok=True, input_tokens=1500, cache_read_tokens=100,
                   provider="cc-proxy", model="glm-4"),
        BeatResult(sent=True, ok=False, err="5xx"),
    ])
    w = _watcher(_qw_cfg(mode="enforce"), led, accounts=accts, sender=rec)
    st, _f = _bare_session(led, tmp_path, "tri-1")
    _open(st)
    for _ in range(3):
        st.qwatch_plan = [now_s() - 1]                  # 逐轮补一枚到期跳
        w._maybe_fire_beats(st)
    rows = accts.read(kind="beat")
    assert [r["outcome"] for r in rows] == ["hit", "miss", "error"]
    assert rows[0]["cache_read"] == 1900
    assert rows[1]["provider"] == "cc-proxy" and rows[1]["model"] == "glm-4"
    assert rows[0]["cost_actual"] == 0.0 and rows[2]["cache_read"] == 0
    assert st.qwatch_beats_fired == 3
    assert len(rec.plans) == 3                           # 发送器收到计划快照
    assert rec.plans[0].session_id == "tri-1"
    assert rec.plans[0].beat_index == 1                  # 第几跳（1 起）
    assert not any("content" in r for r in rows)         # 账面无内容字段（白名单）


# ---------- 熔断（验收⑦） ----------

def test_breaker_two_miss_demotes_enforce_to_observe(tmp_path, capsys):
    """连续 2 MISS → mode 自动 enforce→observe＋告警；剩余跳不作废（转演练）。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    rec = RecordingSender(results=[_MISS, _MISS])
    w = _watcher(_qw_cfg(mode="enforce"), led, accounts=accts, sender=rec)
    st, _f = _bare_session(led, tmp_path, "bm-1")
    _open(st)
    future = now_s() + 999
    st.qwatch_plan = [now_s() - 1, future]
    w._maybe_fire_beats(st)
    assert w.cfg.question_watch.mode == "enforce"        # 1 次 MISS 不降
    st.qwatch_plan = [now_s() - 1, future]               # 再补一枚到期跳
    w._maybe_fire_beats(st)
    assert w.cfg.question_watch.mode == "observe"        # 连续 2 → 降级
    assert "MISS" in capsys.readouterr().out             # 告警落控制台
    assert st.qwatch_plan == [future]                    # 剩余跳保留（转 observe 演练）


def test_breaker_error_not_counted_into_miss_streak(tmp_path):
    """MISS、ERROR、MISS：ERROR 不计入也不重置 MISS 连击 → 仍连续 2 MISS 降级。"""
    led = Ledger()
    rec = RecordingSender(results=[_MISS, _ERR, _MISS])
    w = _watcher(_qw_cfg(mode="enforce"), led, sender=rec)
    st, _f = _bare_session(led, tmp_path, "bm-2")
    for _ in range(3):
        _open(st)
        st.qwatch_plan = [now_s() - 1]
        w._maybe_fire_beats(st)
    assert w.cfg.question_watch.mode == "observe"


def test_breaker_hit_resets_miss_streak(tmp_path):
    """MISS、HIT、MISS：真实命中打断连击 → 不降级。"""
    led = Ledger()
    rec = RecordingSender(results=[_MISS, _HIT, _MISS])
    w = _watcher(_qw_cfg(mode="enforce"), led, sender=rec)
    st, _f = _bare_session(led, tmp_path, "bm-3")
    for _ in range(3):
        _open(st)
        st.qwatch_plan = [now_s() - 1]
        w._maybe_fire_beats(st)
    assert w.cfg.question_watch.mode == "enforce"


def test_breaker_three_errors_pause_window_only(tmp_path, capsys):
    """连续 3 ERROR → 暂停当前窗口剩余跳＋告警；mode 不动；他窗不累及。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    rec = RecordingSender(results=[_ERR, _ERR, _ERR])
    w = _watcher(_qw_cfg(mode="enforce"), led, accounts=accts, sender=rec)
    st_a, _ = _bare_session(led, tmp_path, "be-a")
    st_b, _ = _bare_session(led, tmp_path, "be-b")
    other = now_s() + 999
    _open(st_b)
    st_b.qwatch_plan = [other]
    for _ in range(3):
        _open(st_a)
        st_a.qwatch_plan = [now_s() - 1, now_s() + 999]
        w._maybe_fire_beats(st_a)
    assert st_a.qwatch_plan == []                        # 本窗剩余跳全停
    assert st_b.qwatch_plan == [other]                   # 他窗不累及
    assert w.cfg.question_watch.mode == "enforce"        # ERROR 不降级模式
    assert "ERROR" in capsys.readouterr().out
    rows = accts.read(kind="beat")
    assert [r["outcome"] for r in rows] == ["error", "error", "error"]


def test_breaker_unit_transparent_error_and_reset():
    """计数器纯逻辑：ERROR 对 MISS 连击透明；HIT/MISS 清 ERROR 连击；
    observe 演练不动任何连击。"""
    b = BeatBreaker()
    assert b.record("miss") == "" and b.miss_streak == 1
    assert b.record("error") == "" and b.miss_streak == 1   # 不计入
    assert b.record("miss") == "demote" and b.miss_streak == 0
    b2 = BeatBreaker()
    assert [b2.record("error") for _ in range(3)] == ["", "", "pause"]
    b3 = BeatBreaker()
    b3.record("error")
    b3.record("hit")
    assert b3.error_streak == 0                             # 成功清 ERROR 连击
    b4 = BeatBreaker()
    b4.record("miss")
    b4.record("observe")
    assert b4.miss_streak == 1                              # 演练不动连击


# ---------- RLock 串行化（验收⑧） ----------

def test_ledger_lock_reentrant_and_shared():
    """台账锁可重入：持锁临界区内嵌套 get/touch 同线程不卡死。"""
    led = Ledger()
    with led.lock:
        led.touch("cc", "rl-0", "C:/x/rl-0.jsonl", mtime=1, size=1,
                  daemon_started_at=0)
        assert led.get("cc", "rl-0") is not None
        assert led.get_by_path("C:/x/rl-0.jsonl") is not None


def test_rlock_parking_first_no_double_window(tmp_path):
    """两窗互斥 TOCTOU 归零：等答复窗临界区持锁期间停车窗被并发打开
    → 锁内复验发现已开，不重开（旧代码两窗并存）。"""
    projects = tmp_path / "projects"
    led = Ledger()
    d = FerryDaemon(_qw_cfg(), led, None, lambda st: True)
    w = _watcher(_qw_cfg(), led, ferry_daemon=d)
    sid = "mx-9"
    f = _write_transcript(projects, sid, _SURGE)
    st = _touch(led, projects, sid, f)
    got_lock = threading.Event()

    def hold_then_open_parking():
        with led.lock:                       # 模拟 HTTP 线程先抓住台账锁
            got_lock.set()                   # 已持锁（主线程此后才进临界区）
            time.sleep(0.2)                  # 锁内先开停车窗，制造并发时序
            d._windows[("cc", sid)] = {"opened_ts": now_s()}
    th = threading.Thread(target=hold_then_open_parking, daemon=True)
    th.start()
    assert got_lock.wait(5)                  # 确定化：锁已被线程抓走
    w._maybe_qwatch(st)                      # 临界区等锁 → 拿到后复验停车窗已开
    th.join(5)
    assert st.qwatch_opened_ts is None       # 等答复窗没开
    assert ("cc", sid) in d._windows         # 停车窗开着（先开者赢）


def test_rlock_subagent_open_waits_on_ledger_lock():
    """停车窗开窗 check-then-act 在台账锁内：锁被持时不判不开，释放后照常。"""
    led = Ledger()
    d = FerryDaemon(Config(), led, None, lambda st: True)
    with led.lock:
        th = threading.Thread(
            target=lambda: d.subagent({"event": "start", "agent": "cc",
                                       "session_id": "sb-9"}), daemon=True)
        th.start()
        time.sleep(0.15)                     # 若不在锁内，停车窗这会儿已开
        assert ("cc", "sb-9") not in d._windows
    th.join(5)
    assert ("cc", "sb-9") in d._windows      # 锁释放后照常开窗


# ---------- 记账与契约（验收⑨） ----------

def test_beat_sender_protocol_shape():
    """BeatSender 协议与 NoopSender 契约：NoopSender 恒返回未真发结果。"""
    assert hasattr(BeatSender, "send")
    r = NoopSender().send(None)              # 零网络：不需要真计划
    assert r.sent is False and r.ok is False
    assert classify(r) == "observe"


def test_beat_row_privacy_no_message_content(tmp_path):
    """账面字段白名单：只有元数据与金额，无任何消息内容字段。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    rec = RecordingSender()
    w = _watcher(_qw_cfg(mode="enforce"), led, accounts=accts, sender=rec)
    st, _f = _bare_session(led, tmp_path, "pv-1")
    _open(st)
    st.qwatch_plan = [now_s() - 1]
    w._maybe_fire_beats(st)
    (row,) = accts.read(kind="beat")
    assert set(row) <= {"v", "ts", "ts_iso", "kind", "agent", "session_id",
                        "lineage_id", "project", "provider", "model",
                        "price_ver", "prefix_tokens", "cache_read",
                        "outcome", "cost_pred", "cost_actual"}


def test_scheduler_exception_swallowed(tmp_path):
    """调度任何异常吞掉不外抛（守望主路径不受累）。"""
    led = Ledger()
    w = _watcher(_qw_cfg(), led)

    def boom(st, ts):
        raise RuntimeError("boom")
    w._send_beat = boom
    st, _f = _bare_session(led, tmp_path, "x-1")
    _open(st)
    st.qwatch_plan = [now_s() - 1]
    w._maybe_fire_beats(st)                  # 不外抛
    assert st.qwatch_beats_fired == 1        # 已出队（发送 attempted）
    assert w._beat_in_flight is False        # 在途旗必还


# ---------- 配置补强（票02 评审转来）：死线余量告警 ----------

def _val_cfg(mode="observe", lead=480.0, summarize_s=25 * 60, block_s=35 * 60):
    c = Config()
    c.thresholds = ThresholdCfg(summarize_s=summarize_s, block_s=block_s)
    c.question_watch = QuestionWatchCfg(mode=mode, ferry_deadline_lead_s=lead)
    return c


def test_config_wall_timeout_warn_on_equal(capsys):
    """lead == 摆渡墙钟（默认 480）→ 告警"骨架可能贴线"（不改默认值）。"""
    assert FERRY_WALL_TIMEOUT_S == 480
    c = _val_cfg(lead=480)
    validate(c)
    assert c.question_watch.ferry_deadline_lead_s == 480     # 默认值未动
    assert "骨架可能贴线" in capsys.readouterr().out


def test_config_wall_timeout_warn_on_below(capsys):
    c = _val_cfg(lead=300)
    validate(c)
    assert "骨架可能贴线" in capsys.readouterr().out


def test_config_wall_timeout_silent_when_lead_over_wall(capsys):
    c = _val_cfg(lead=600)                   # 1500+600=2100 ≤ block_s，且 > 480
    validate(c)
    assert "骨架可能贴线" not in capsys.readouterr().out


def test_config_wall_timeout_silent_when_off(capsys):
    c = _val_cfg(mode="off", lead=100, summarize_s=10, block_s=200)
    validate(c)
    assert "骨架可能贴线" not in capsys.readouterr().out


def test_config_load_warns_on_default_lead(tmp_path, monkeypatch, capsys):
    """load() 端到端：默认 lead=480 触发告警，默认值本身不被改。"""
    f = tmp_path / "c.toml"
    f.write_text('[question_watch]\nmode = "observe"\n', encoding="utf-8")
    monkeypatch.setenv("FERRYMAN_CONFIG", str(f))
    cfg = config_mod.load()
    assert cfg.question_watch.ferry_deadline_lead_s == 480
    assert "骨架可能贴线" in capsys.readouterr().out
