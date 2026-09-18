"""T51 票06 · 端到端集成：Harness 全链＋漏检观测＋回归挂点。

验收层（票 01-05 已就位的组装验证）：
- Harness 全链（自然闲置驱动）：提问潮会话走真实守望线程/真实 poll 循环，
  产生 开窗→observe 演练跳→关窗 完整事件序列；用户提交后窗口关、剩余跳取消、
  常规摆渡调度恢复；对照会话（非提问潮）零问询守望事件（守望在跑且判据不误触）。
  时序全部由真实流转产生：summarize_s 调小驱动闲置判定，beat_interval_s 调小
  驱动到期跳，绝不手改台账内部状态变量（last_write 等）。唯一"伪造"的是转录
  行内 timestamp——那是用量账本的 ts 数据源（harvest 原样读取），不参与闲置
  判定（mtime 口径）；漏检关联的 ≥TTL 间隙只有靠它才能在秒级测试里铺出来。
- 漏检观测（xcheck 附录第 10 条，observe 期粗粒度信号）：纯计数关联
  "全量重付的闲置复活请求"×"此前 30 分钟内末条疑似提问命中且其间无真跳保温"，
  入 /stats qwatch 节 miss_signals 字段。只做计数，不接 LLM、不做正文级判定
  （隐私不变量：计数与 bool，永不落消息内容）。
- 回归挂点：beat 请求前缀形状锁定（spec 决策 4 保形硬要求）——Q14 段二产物
  （真实捕获模板＋BeatBuilder）接入前以 skip 占位。
"""

from __future__ import annotations

import json
import os
import time
from datetime import datetime, timezone
from pathlib import Path

import pytest

from ferryman.accounts import Accounts
from ferryman.config import Config, QuestionWatchCfg, ThresholdCfg
from ferryman.ledger import Ledger, now_s
from ferryman.qwatch import MISS_IDLE_S, MISS_LOOKBACK_S, correlate_miss_signals
from ferryman.server import FerryDaemon
from helpers import now_iso, write_session

# ---------- Harness 全链（自然闲置，勿手改台账状态） ----------


def test_qwatch_e2e_full_chain(h):
    """全链：自然开窗→observe 演练跳→用户提交关窗（剩余跳取消）→常规摆渡恢复
    →/stats 计数与漏检信号→事件序列完整。"""
    h.cfg.question_watch = QuestionWatchCfg(
        mode="observe", beat_interval_s=1.2, max_beats=3,
        ferry_deadline_lead_s=8.0)          # 死线 60−8=52s：测试窗外，窗口期摆渡确被推迟
    h.cfg.thresholds.summarize_s = 0.5      # 自然闲置：写入后 0.5s 即达摆渡线
    h.cfg.thresholds.block_s = 60.0
    sid = "e2e-surge"
    t0 = time.time()

    def _st():
        return h.ledger.get("cc", sid)

    # 提问潮落盘：末条 assistant 纯文本 8 个问题单元（无 tool_use——条件②空集
    # 真空真；也让窗口期的摆渡推迟只能来自窗口本身，不与悬空推迟混淆）。
    surge = "\n".join(f"❓ **Q{i}** 这该如何取舍？" for i in range(1, 9))
    lines = [
        {"type": "user", "timestamp": now_iso(), "cwd": str(h.tmp), "sessionId": sid,
         "message": {"role": "user", "content": "开始"}},
        {"type": "assistant", "timestamp": now_iso(),
         "message": {"role": "assistant",
                     "content": [{"type": "text", "text": surge}],
                     "usage": {"input_tokens": 2000,
                               "cache_read_input_tokens": 100,
                               "cache_creation_input_tokens": 0,
                               "output_tokens": 5}}},
    ]
    f = h.projects / "C--proj" / f"{sid}.jsonl"
    f.parent.mkdir(parents=True, exist_ok=True)
    f.write_text("\n".join(json.dumps(x, ensure_ascii=False) for x in lines) + "\n",
                 encoding="utf-8")
    os.utime(f, None)

    # ① 守望线程自然开窗（真实 poll 循环流转）
    assert h.wait_for(lambda: _st() is not None
                      and _st().qwatch_opened_ts is not None), "守望线程自然开窗"
    (hit_row,) = h.accounts.read(kind="qwatch_hit", session=sid)
    (open_row,) = h.accounts.read(kind="qwatch_open", session=sid)
    assert hit_row["unit_count"] == 8                    # 证据形态随事件落账
    assert open_row["prefix_tokens"] == 2100             # 真实懒富化：2000+100

    # ② observe 演练跳自然到期（开窗瞬间不跳，首跳在 +1.2s；零网络）
    assert h.wait_for(lambda: h.accounts.read(kind="beat", session=sid))
    # 窗口开着且闲置已过摆渡线：常规摆渡被推迟（无悬空——推迟只能来自窗口）
    assert h.wait_for(lambda: now_s() - _st().last_write >= 1.6)
    assert sid not in h.enqueued_ok
    assert not any(e["session_id"] == sid for e in h.store._index["handoffs"])

    # ③ 用户提交（作答）：answer（user 行）＋assistant 回应（非提问潮）一次落盘。
    #    回应行 timestamp 前移 +1200s——只作用量账本 ts（漏检关联的 ≥TTL 间隙
    #    原料），闲置判定走 mtime 不受影响。回应 usage 缓存零命中＝全量重付。
    resp_iso = datetime.fromtimestamp(t0 + 1200, timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%S.000Z")
    submit = [
        {"type": "user", "timestamp": now_iso(), "cwd": str(h.tmp), "sessionId": sid,
         "message": {"role": "user", "content": "逐条答完了"}},
        {"type": "assistant", "timestamp": resp_iso,
         "message": {"role": "assistant",
                     "content": [{"type": "text", "text": "收到，继续执行。"}],
                     "usage": {"input_tokens": 2000,
                               "cache_read_input_tokens": 0,
                               "cache_creation_input_tokens": 0,
                               "output_tokens": 5}}},
    ]
    with f.open("a", encoding="utf-8") as fh:
        fh.write("\n".join(json.dumps(x, ensure_ascii=False) for x in submit) + "\n")
    os.utime(f, None)

    # ④ 关窗：剩余跳取消、关窗事件落账（beats_fired=窗口实发数）
    assert h.wait_for(lambda: h.accounts.read(kind="qwatch_close", session=sid))
    assert _st().qwatch_opened_ts is None and _st().qwatch_plan == []
    (close_row,) = h.accounts.read(kind="qwatch_close", session=sid)
    fired = len(h.accounts.read(kind="beat", session=sid))
    assert close_row["beats_fired"] == fired >= 1
    assert close_row["close_reason"] == "write"
    h.wait_for(lambda: False, timeout=1.5)               # 让守望再跑几轮
    assert len(h.accounts.read(kind="beat", session=sid)) == fired   # 剩余跳未再发
    assert _st().qwatch_opened_ts is None                # 不重开（末条已非提问潮）
    assert len(h.accounts.read(kind="qwatch_hit", session=sid)) == 1

    # ⑤ 恢复常规摆渡调度：闲置过线 → 入队 → 假摆渡出交接
    assert h.wait_for(lambda: sid in h.enqueued_ok)
    assert h.wait_for(lambda: any(e["session_id"] == sid
                                  for e in h.store._index["handoffs"]))

    # ⑥ /stats：qwatch 节全链计数＋漏检关联信号（observe 演练不保温 → 计 1）
    q = h.get("/stats")["qwatch"]
    assert q["mode"] == "observe"
    assert q["windows_opened"] == 1
    assert q["beats_fired"] == fired
    assert q["beats_by_outcome"]["observe"] == fired
    assert q["miss_signals"] == 1

    # ⑦ 事件完整序列（账本行序＝时序）：hit → open → beat×N → close
    kinds = [r["kind"] for r in h.accounts.read(session=sid)
             if r["kind"] in ("qwatch_hit", "qwatch_open", "beat", "qwatch_close")]
    assert kinds == ["qwatch_hit", "qwatch_open"] + ["beat"] * fired + ["qwatch_close"]


def test_control_session_zero_qwatch_events(h):
    """对照：非提问潮会话照常被守望摆渡（守望确实在跑），但零问询守望事件——
    命中/开窗/跳/关窗四类事件与 beat 全空，/stats 计数全零。"""
    h.cfg.question_watch = QuestionWatchCfg(mode="observe", beat_interval_s=3600.0,
                                            ferry_deadline_lead_s=1.0)
    h.cfg.thresholds.block_s = 600.0
    sid = "e2e-calm"
    write_session(h.projects, sid, str(h.tmp / "proj"))   # 普通会话：无提问潮
    assert h.wait_for(lambda: any(e["session_id"] == sid
                                  for e in h.store._index["handoffs"])), \
        "前提：对照会话照常摆渡（守望在跑）"
    h.wait_for(lambda: False, timeout=1.0)                # 再让守望跑几轮
    for kind in ("qwatch_hit", "qwatch_open", "qwatch_close", "beat"):
        assert h.accounts.read(kind=kind) == []
    st = h.ledger.get("cc", sid)
    assert st is not None and st.qwatch_opened_ts is None and st.qwatch_plan == []
    q = h.get("/stats")["qwatch"]
    assert (q["hits"], q["windows_opened"], q["beats_fired"]) == (0, 0, 0)
    assert q["miss_signals"] == 0


# ---------- 漏检观测（TDD；纯计数，隐私不变量：只有计数与元数据字段） ----------

_T0 = 1_800_000_000.0                     # 纯函数不吃系统钟：任意固定锚点


def _hit(sid: str, ts: float) -> dict:
    return {"kind": "qwatch_hit", "session_id": sid, "ts": ts}


def _usage(sid: str, ts: float, *, cache_read: int, inp: int = 30_000) -> dict:
    return {"kind": "usage", "session_id": sid, "ts": ts, "input_tokens": inp,
            "cache_read_tokens": cache_read, "cache_creation_tokens": 0,
            "output_tokens": 5}


def _beat(sid: str, ts: float, outcome: str) -> dict:
    return {"kind": "beat", "session_id": sid, "ts": ts, "outcome": outcome}


def test_miss_signal_counts_surge_then_full_repay_revival():
    """正例：末条疑似提问命中 → observe 演练零保温 → ≥TTL 闲置后复活请求
    缓存零命中（全量重付）→ 计 1；两会话各自独立计。"""
    rows = [_hit("s1", _T0),
            _usage("s1", _T0, cache_read=100),           # 提问潮落盘（缓存刚写）
            _beat("s1", _T0 + 420, "observe"),           # 演练跳：未真发，不保温
            _usage("s1", _T0 + 1200, cache_read=0)]      # 20min 后复活：全量重付
    assert correlate_miss_signals(rows) == 1
    rows += [_hit("s2", _T0 + 5),
             _usage("s2", _T0 + 5, cache_read=100),
             _usage("s2", _T0 + 5 + 900, cache_read=0)]
    assert correlate_miss_signals(rows) == 2             # 会话间独立


def test_miss_signal_negative_controls():
    """反例逐条不计：无命中／缓存命中／间隙不足／首条用量行／其间有真跳／
    命中超出 30 分钟回看窗。"""
    gap = MISS_IDLE_S + 60
    # ① 无命中可对照（检测没见过疑似提问）——真正的判据漏检无正文级证据，粗粒度不计
    assert correlate_miss_signals(
        [_usage("a", _T0, cache_read=100),
         _usage("a", _T0 + gap, cache_read=0)]) == 0
    # ② 复活但缓存命中（没全量重付）
    assert correlate_miss_signals(
        [_hit("b", _T0), _usage("b", _T0, cache_read=100),
         _usage("b", _T0 + gap, cache_read=28_000)]) == 0
    # ③ 间隙不足 MISS_IDLE_S（TTL 内密集交互，非闲置复活）
    assert correlate_miss_signals(
        [_hit("c", _T0), _usage("c", _T0, cache_read=100),
         _usage("c", _T0 + 60, cache_read=0)]) == 0
    # ④ 会话首条用量行（冷启动，无前驱间隙可言）
    assert correlate_miss_signals(
        [_hit("d", _T0), _usage("d", _T0 + gap, cache_read=0)]) == 0
    # ⑤ 命中与重付之间有真跳保温（检测与执行已尽职——其后重付归 TTL 漂移/死区
    #    取舍，由熔断与 D4 遥测管辖，不计漏检）
    assert correlate_miss_signals(
        [_hit("e", _T0), _usage("e", _T0, cache_read=100),
         _beat("e", _T0 + 420, "hit"),
         _usage("e", _T0 + gap, cache_read=0)]) == 0
    # ⑥ 命中超出 30 分钟回看窗（与本次重付无关联）
    assert correlate_miss_signals(
        [_hit("f", _T0 - MISS_LOOKBACK_S - 1),
         _usage("f", _T0 - MISS_LOOKBACK_S - 1, cache_read=100),
         _usage("f", _T0, cache_read=0)]) == 0


def _qw_cfg(mode: str = "observe") -> Config:
    cfg = Config()
    cfg.thresholds = ThresholdCfg(summarize_s=10, block_s=100, min_ctx_tokens=1000)
    cfg.question_watch = QuestionWatchCfg(mode=mode, ferry_deadline_lead_s=40)
    return cfg


def test_health_reports_miss_signals_from_accounts(tmp_path):
    """/stats qwatch 节 miss_signals：真实账本行（Accounts 落盘再读）驱动现算；
    accounts 未接线（旧调用零改动）→ 0 占位。"""
    led = Ledger()
    accts = Accounts(tmp_path / "data")
    t0 = time.time() - 1200
    common = dict(agent="cc", project="C:/p")
    accts.record("qwatch_hit", session_id="m-1", ts=t0, unit_count=8,
                 marker_lines=8, qmark_lines=0, numbered_lines=0,
                 transcript_path="C:/x/m-1.jsonl", **common)
    accts.record("usage", session_id="m-1", ts=t0, model="", title="",
                 input_tokens=30_000, cache_read_tokens=100,
                 cache_creation_tokens=0, output_tokens=5, offset=10, **common)
    accts.record("usage", session_id="m-1", ts=t0 + 1200, model="", title="",
                 input_tokens=30_000, cache_read_tokens=0,
                 cache_creation_tokens=0, output_tokens=5, offset=20, **common)
    d = FerryDaemon(_qw_cfg(), led, None, lambda st: True, accounts=accts)
    assert d.health()["qwatch"]["miss_signals"] == 1
    d2 = FerryDaemon(_qw_cfg(), led, None, lambda st: True)   # 未接线 → 全零占位
    assert d2.health()["qwatch"]["miss_signals"] == 0


# ---------- 回归挂点（Q14 段二产物接入后启用） ----------

@pytest.mark.skip(reason="Q14 段二产物（~/ferryman/captures/ 真实请求捕获模板＋"
                         "BeatBuilder）接入后启用：届时加载捕获模板，断言 beat "
                         "请求被缓存前缀字段（system/tools/messages/cache_control）"
                         "与 CC 真实请求逐字段 diff 全等（spec 决策 4 保形硬要求；"
                         "采样参数不在等价范围——max_tokens=1 封顶不参与比对）")
def test_beat_request_shape_lock():
    """回归挂点：beat 请求前缀形状锁定（spec 决策 4 保形验收锚定，Q14 段二
    验收的一部分）。接入后以真实捕获模板替换本占位实现。"""
    raise NotImplementedError("Q14 段二接入后以真实捕获模板实现（见 skip reason）")
