"""T51 票04 · 事件、证据形态、/stats 与一键停。

事件语义（spec 决策 8）：命中/开窗/关窗三类新事件走既有台账科目通道
（accounts.jsonl 追加行；每跳早已入 beat 科目——票03），viewer 时间线
零大改即可显。命中事件带复核证据形态五字段（session_id＋命中时间＋
unit_count＋breakdown＋转录绝对路径），隐私回归：事件内容永不含消息
正文。/stats 增问询守望计数器（命中/开窗/跳数/四道 outcome/累计实收
花费/当前 mode——QWatchStats 纯内存计数，账本数据不反推跳数，spec
决策 7）。一键停＝POST /qwatch_stop：mode 置 off＋取消全部在飞计划与
未关窗口，集成级验证。另含票04 评审转来的 M5（beat_interval_s ≤0 拒绝）。
"""

from __future__ import annotations

import json
import os
import time
from pathlib import Path

import pytest

import ferryman.config as config_mod
from ferryman.accounts import Accounts
from ferryman.beat import BeatBreaker, BeatResult, NoopSender, QWatchStats
from ferryman.config import Config, QuestionWatchCfg, ThresholdCfg, validate
from ferryman.daemon import Watcher
from ferryman.ledger import Ledger, now_s
from ferryman.server import FerryDaemon

# ---------- 语料（与票02/03 同款） ----------

_SURGE = "\n".join(f"❓ **Q{i}** - **标题{i}**：这个问题该怎么答？" for i in range(1, 9))
_MARKER = "隐私正文绝不入账XYZZY"      # 隐私回归：消息正文里的独特标记


def _line(d: dict) -> str:
    return json.dumps(d, ensure_ascii=False)


def _assistant(text: str, tools: tuple[tuple[str, str], ...] = (),
               mid: str = "msg_1") -> dict:
    blocks = [{"type": "text", "text": text}]
    blocks += [{"type": "tool_use", "id": tid, "name": name, "input": {}}
               for tid, name in tools]
    return {"type": "assistant", "timestamp": "2026-09-18T12:00:00.000Z",
            "message": {"id": mid, "role": "assistant", "content": blocks,
                        "usage": {"input_tokens": 2000,
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
    cfg.question_watch = QuestionWatchCfg(mode=mode, ferry_deadline_lead_s=40, **qw_kw)
    return cfg


def _watcher(cfg: Config, led: Ledger, accounts=None, ferry_daemon=None,
             stats: QWatchStats | None = None, projects: Path | None = None) -> Watcher:
    w = Watcher.__new__(Watcher)
    w.cfg, w.ledger, w.store = cfg, led, None
    w.enqueue = lambda st: True
    w.started_at = 0
    w.accounts = accounts
    w.ferry_daemon = ferry_daemon
    w._qwatch_seen = {}
    w._qwatch_hit_seen = {}                    # 票04：命中事件版本去重
    w.qwatch_stats = stats
    w._enrich = lambda st: setattr(st, "peak_ctx", 50_000)   # 条件④确定性
    w._beat_sender = None
    w._beat_in_flight = False
    w._breaker = BeatBreaker()
    w._noop_sender = NoopSender()
    w._beat_enforce_warned = False
    if projects is not None:                   # 走真实 _poll_cc 的用例需要
        w.cc_dir, w.cx_dirs = projects, []
    return w


class RecordingSender:
    """fake sender：记录收到的 BeatPlan，按脚本吐 BeatResult（默认 HIT）。"""

    def __init__(self, results=None):
        self.plans: list = []
        self.results = list(results or [])

    def send(self, plan):
        self.plans.append(plan)
        if self.results:
            return self.results.pop(0)
        return BeatResult(sent=True, ok=True, input_tokens=100,
                          cache_read_tokens=1900, provider="cc-proxy",
                          model="glm-4", cost_actual=0.01)


def _bare_session(led: Ledger, tmp_path: Path, sid: str):
    """不经开窗谓词的裸会话（直接摆窗口字段）。"""
    f = tmp_path / f"{sid}.jsonl"
    f.write_text("{}\n", encoding="utf-8")
    st = led.touch("cc", sid, str(f), mtime=f.stat().st_mtime,
                   size=f.stat().st_size, cwd=str(tmp_path), daemon_started_at=0)
    return st, f


def _open(st, interval: float = 420.0, beats: int = 2):
    """手工摆成已开窗态（与 _maybe_qwatch 开窗路径同形状）。"""
    t0 = now_s()
    st.qwatch_opened_ts = t0
    st.qwatch_beats_fired = 0
    st.qwatch_plan = [t0 + i * interval for i in range(1, beats + 1)]
    st.qwatch_snapshot = (st.last_write, st.size)
    return st


_HIT = BeatResult(sent=True, ok=True, input_tokens=100, cache_read_tokens=1900,
                  cost_actual=0.01)
_MISS = BeatResult(sent=True, ok=True, input_tokens=2000, cache_read_tokens=0)


# ---------- 命中事件：证据形态五字段（验收②） ----------

def test_hit_event_booked_once_per_version_with_evidence(tmp_path):
    """命中事件随写入版本去重；证据形态五字段齐：session_id＋命中时间（ts）＋
    unit_count＋breakdown 三桶＋转录绝对路径。"""
    projects = tmp_path / "projects"
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    stats = QWatchStats()
    w = _watcher(_qw_cfg(), led, accounts=accts, stats=stats)
    f = _write_transcript(projects, "hit-1", _SURGE,
                          tools=(("tu_aq", "AskUserQuestion"),))
    st = led.touch("cc", "hit-1", str(f), mtime=f.stat().st_mtime,
                   size=f.stat().st_size, cwd=str(projects), daemon_started_at=0)
    w._maybe_qwatch(st)
    w._maybe_qwatch(st)                        # 同版本重判：不重复记
    rows = accts.read(kind="qwatch_hit")
    assert len(rows) == 1
    r = rows[0]
    assert r["session_id"] == "hit-1"          # 证据① session_id
    assert isinstance(r["ts"], float)          # 证据② 命中时间
    assert r["unit_count"] == 8                # 证据③ unit_count
    assert (r["marker_lines"] + r["qmark_lines"]
            + r["numbered_lines"]) == 8        # 证据④ breakdown 三桶合计
    assert r["transcript_path"] == str(f)      # 证据⑤ 转录绝对路径
    assert Path(r["transcript_path"]).is_absolute()
    assert stats.snapshot()["hits"] == 1


def test_hit_event_dedup_across_transient_rechecks(tmp_path):
    """瞬态阻塞（子代理在飞）不盖版本章、逐轮重判——命中事件靠版本去重只记一次；
    解除后开窗也不补记。"""
    projects = tmp_path / "projects"
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    stats = QWatchStats()
    w = _watcher(_qw_cfg(), led, accounts=accts, stats=stats)
    f = _write_transcript(projects, "hit-2", _SURGE,
                          tools=(("tu_aq", "AskUserQuestion"),))
    st = led.touch("cc", "hit-2", str(f), mtime=f.stat().st_mtime,
                   size=f.stat().st_size, cwd=str(projects), daemon_started_at=0)
    led.subagent_event("cc", "hit-2", "start")
    w._maybe_qwatch(st)
    w._maybe_qwatch(st)
    w._maybe_qwatch(st)                        # 子代理在飞：反复重判
    assert st.qwatch_opened_ts is None         # 条件③阻塞
    assert len(accts.read(kind="qwatch_hit")) == 1
    led.subagent_event("cc", "hit-2", "stop")
    w._maybe_qwatch(st)
    assert st.qwatch_opened_ts is not None     # 解除后开窗
    assert len(accts.read(kind="qwatch_hit")) == 1   # 不补记
    assert stats.snapshot()["windows_opened"] == 1


def test_hit_event_privacy_no_message_body(tmp_path):
    """隐私回归（验收②硬线）：命中事件原始 JSON 行不含消息正文标记；
    字段集合不超白名单（只有元数据与计数）。"""
    projects = tmp_path / "projects"
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    w = _watcher(_qw_cfg(), led, accounts=accts)
    surge = _SURGE + f"\n{_MARKER}（这一句只在消息正文里）"
    f = _write_transcript(projects, "pv-9", surge,
                          tools=(("tu_aq", "AskUserQuestion"),))
    st = led.touch("cc", "pv-9", str(f), mtime=f.stat().st_mtime,
                   size=f.stat().st_size, cwd=str(projects), daemon_started_at=0)
    w._maybe_qwatch(st)
    rows = accts.read(kind="qwatch_hit")
    assert rows, "前提：命中事件已落账"
    allowed = {"v", "ts", "ts_iso", "kind", "agent", "session_id", "lineage_id",
               "project", "unit_count", "marker_lines", "qmark_lines",
               "numbered_lines", "transcript_path"}
    for raw_file in accts.dir.glob("*.jsonl"):
        for raw in raw_file.read_text(encoding="utf-8").splitlines():
            if '"qwatch_hit"' not in raw:
                continue
            assert _MARKER not in raw          # 正文标记不入账
            assert set(json.loads(raw)) <= allowed


# ---------- 开窗/关窗事件（验收①） ----------

def test_open_and_close_events_booked_via_poll_loop(tmp_path):
    """走真实 _poll_cc：开窗落 qwatch_open（unit_count＋prefix_tokens）；
    新写入关窗落 qwatch_close（opened_ts/closed_ts/dur_s/beats_fired/
    close_reason）；无写入的轮次不重复记关窗。"""
    projects = tmp_path / "projects"
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    stats = QWatchStats()
    w = _watcher(_qw_cfg(), led, accounts=accts, stats=stats, projects=projects)
    f = _write_transcript(projects, "oc-1", _SURGE,
                          tools=(("tu_aq", "AskUserQuestion"),))
    w._poll_cc()
    st = led.get("cc", "oc-1")
    assert st.qwatch_opened_ts is not None
    (open_row,) = accts.read(kind="qwatch_open")
    assert open_row["unit_count"] == 8
    assert open_row["prefix_tokens"] == 50_000
    assert accts.read(kind="qwatch_close") == []

    opened_ts = st.qwatch_opened_ts
    with f.open("a", encoding="utf-8") as fh:  # 用户提交：非提问潮 assistant 落盘
        fh.write(_line(_assistant("收到，开工。", mid="msg_2")) + "\n")
    new_t = time.time() + 5
    os.utime(f, (new_t, new_t))
    w._poll_cc()
    assert st.qwatch_opened_ts is None         # 新写入关窗（既有语义）
    (close_row,) = accts.read(kind="qwatch_close")
    assert close_row["opened_ts"] == round(opened_ts, 3)
    assert close_row["closed_ts"] == round(st.last_write, 3)
    assert close_row["dur_s"] >= 0
    assert close_row["beats_fired"] == 0
    assert close_row["close_reason"] == "write"

    w._poll_cc()                               # 无写入：不重复记
    assert len(accts.read(kind="qwatch_close")) == 1
    assert stats.snapshot()["windows_opened"] == 1


def test_observe_drill_beats_counted_not_billed(tmp_path):
    """每跳事件沿用 beat 科目（票03 已落）；这里验 /stats 侧：observe 演练跳
    进 observe 桶、实收花费不计。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    stats = QWatchStats()
    w = _watcher(_qw_cfg(), led, accounts=accts, stats=stats)
    st, _f = _bare_session(led, tmp_path, "ob-s")
    _open(st)
    st.qwatch_plan = [now_s() - 1]
    w._maybe_fire_beats(st)
    rows = accts.read(kind="beat")
    assert len(rows) == 1 and rows[0]["outcome"] == "observe"   # 既有科目通道
    snap = stats.snapshot()
    assert snap["beats_fired"] == 1
    assert snap["beats_by_outcome"]["observe"] == 1
    assert snap["cost_actual"] == 0.0          # 演练跳零花费


def test_beat_stats_counters_by_outcome(tmp_path):
    """enforce fake sender：hit/miss 两跳 → 跳数、四道 outcome 计数、累计实收。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    stats = QWatchStats()
    rec = RecordingSender(results=[_HIT, _MISS])
    w = _watcher(_qw_cfg(mode="enforce"), led, accounts=accts, stats=stats)
    w._beat_sender = rec
    st, _f = _bare_session(led, tmp_path, "tri-s")
    _open(st)
    for _ in range(2):
        st.qwatch_plan = [now_s() - 1]
        w._maybe_fire_beats(st)
    snap = stats.snapshot()
    assert snap["beats_fired"] == 2
    assert snap["beats_by_outcome"] == {"hit": 1, "miss": 1,
                                        "error": 0, "observe": 0}
    assert abs(snap["cost_actual"] - 0.01) < 1e-9


# ---------- /stats（验收③） ----------

def test_health_reports_qwatch_section():
    """health() 带 qwatch 节：计数器快照＋当前 mode（读配置活值）。"""
    led = Ledger()
    cfg = _qw_cfg(mode="observe")
    stats = QWatchStats()
    stats.record_hit()
    stats.record_beat("miss", 0.5)
    d = FerryDaemon(cfg, led, None, lambda st: True, qwatch_stats=stats)
    q = d.health()["qwatch"]
    assert q["mode"] == "observe"
    assert q["hits"] == 1
    assert q["windows_opened"] == 0
    assert q["beats_fired"] == 1
    assert q["beats_by_outcome"]["miss"] == 1
    assert q["cost_actual"] == 0.5
    # 未注入计数器（旧调用零改动）→ 全零占位，mode 照报
    d2 = FerryDaemon(_qw_cfg(mode="enforce"), led, None, lambda st: True)
    q2 = d2.health()["qwatch"]
    assert q2["mode"] == "enforce"
    assert q2["beats_fired"] == 0 and q2["hits"] == 0 and q2["cost_actual"] == 0.0


# ---------- 一键停（验收④） ----------

def test_qwatch_stop_clears_windows_and_disables_reopen(tmp_path):
    """一键停：mode→off＋全部在飞计划/未关窗口取消（关窗事件 close_reason=
    stop 落账）；置 off 后不再开窗。"""
    projects = tmp_path / "projects"
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    cfg = _qw_cfg()
    d = FerryDaemon(cfg, led, None, lambda st: True, accounts=accts)
    w = _watcher(cfg, led, accounts=accts, ferry_daemon=d)
    f = _write_transcript(projects, "stop-1", _SURGE,
                          tools=(("tu_aq", "AskUserQuestion"),))
    st_a = led.touch("cc", "stop-1", str(f), mtime=f.stat().st_mtime,
                     size=f.stat().st_size, cwd=str(projects), daemon_started_at=0)
    w._maybe_qwatch(st_a)
    assert st_a.qwatch_opened_ts is not None
    st_b, _f = _bare_session(led, tmp_path, "stop-2")
    _open(st_b)
    out = d.qwatch_stop()
    assert out == {"ok": True, "mode": "off", "cancelled": 2}
    assert cfg.question_watch.mode == "off"
    assert st_a.qwatch_opened_ts is None and st_a.qwatch_plan == []
    assert st_b.qwatch_opened_ts is None and st_b.qwatch_plan == []
    close_rows = accts.read(kind="qwatch_close")
    assert {r["session_id"] for r in close_rows} == {"stop-1", "stop-2"}
    assert {r["close_reason"] for r in close_rows} == {"stop"}
    # 置 off 后：即使新写入且末条仍提问潮，也不再开窗
    st_a.last_write += 5
    w._maybe_qwatch(st_a)
    assert st_a.qwatch_opened_ts is None
    assert d.health()["qwatch"]["mode"] == "off"


def test_qwatch_stop_integration_over_http(h):
    """集成级（真实守望线程＋HTTP）：写提问潮 jsonl → 自然开窗 → POST
    /qwatch_stop → 在飞计划取消、不再开窗、/stats 报 off。"""
    h.cfg.question_watch = QuestionWatchCfg(mode="observe", beat_interval_s=3600.0,
                                            ferry_deadline_lead_s=1.0)
    h.cfg.thresholds.block_s = 600.0           # 死线远在测试窗口之外
    d = h.projects                             # 守望的 cc_projects_dir
    surge = "\n".join(f"❓ **Q{i}** 这该如何取舍？" for i in range(1, 8))
    proj = d / "C--proj"
    proj.mkdir(parents=True, exist_ok=True)
    f = proj / "stop-http.jsonl"
    blocks = [{"type": "text", "text": surge},
              {"type": "tool_use", "id": "tu_aq", "name": "AskUserQuestion",
               "input": {}}]
    lines = [{"type": "user", "timestamp": "2026-09-18T12:00:00.000Z",
              "cwd": str(h.tmp), "sessionId": "stop-http",
              "message": {"role": "user", "content": "开始干"}},
             {"type": "assistant", "timestamp": "2026-09-18T12:00:00.000Z",
              "message": {"id": "msg_1", "role": "assistant", "content": blocks,
                          "usage": {"input_tokens": 2000,
                                    "cache_read_input_tokens": 100,
                                    "cache_creation_input_tokens": 0,
                                    "output_tokens": 5}}}]
    f.write_text("\n".join(json.dumps(x, ensure_ascii=False) for x in lines) + "\n",
                 encoding="utf-8")
    os.utime(f, None)
    assert h.wait_for(lambda: (h.ledger.get("cc", "stop-http") is not None
                               and h.ledger.get("cc", "stop-http").qwatch_opened_ts
                               is not None)), "前提：守望线程自然开窗"
    st = h.ledger.get("cc", "stop-http")
    assert st.qwatch_plan                      # 在飞计划已排定
    assert h.accounts.read(kind="beat") == []  # 未来跳：一跳未发

    out = h.post("/qwatch_stop")
    assert out["ok"] is True and out["mode"] == "off" and out["cancelled"] == 1
    st = h.ledger.get("cc", "stop-http")
    assert st.qwatch_opened_ts is None and st.qwatch_plan == []
    (close_row,) = h.accounts.read(kind="qwatch_close")
    assert close_row["close_reason"] == "stop"     # 一键停关窗也落事件
    stats_body = h.get("/stats")["qwatch"]
    assert stats_body["mode"] == "off"
    assert stats_body["windows_opened"] == 1
    assert stats_body["beats_fired"] == 0

    # 置 off 后再写入：不开窗、不复发
    with f.open("a", encoding="utf-8") as fh:
        fh.write(json.dumps({"type": "user", "timestamp": "2026-09-18T12:00:01.000Z",
                             "message": {"role": "user", "content": "继续"}},
                            ensure_ascii=False) + "\n")
    os.utime(f, None)
    h.wait_for(lambda: False, timeout=1.5)     # 让守望跑几轮
    assert h.ledger.get("cc", "stop-http").qwatch_opened_ts is None
    assert h.get("/stats")["qwatch"]["windows_opened"] == 1
    assert h.accounts.read(kind="beat") == []


# ---------- M5（票04 评审转来）：beat_interval_s ≤ 0 拒绝 ----------

def test_config_rejects_zero_beat_interval():
    c = _qw_cfg()
    c.question_watch.beat_interval_s = 0
    with pytest.raises(ValueError, match="beat_interval_s"):
        validate(c)


def test_config_rejects_negative_beat_interval():
    c = _qw_cfg(mode="off")                    # off 也不放行：排出的计划全是过去跳
    c.question_watch.beat_interval_s = -1
    with pytest.raises(ValueError, match="beat_interval_s"):
        validate(c)


def test_config_load_rejects_bad_beat_interval(tmp_path, monkeypatch):
    f = tmp_path / "c.toml"
    f.write_text('[question_watch]\nmode = "observe"\nbeat_interval_s = 0\n',
                 encoding="utf-8")
    monkeypatch.setenv("FERRYMAN_CONFIG", str(f))
    with pytest.raises(ValueError, match="beat_interval_s"):
        config_mod.load()
