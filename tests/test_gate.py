"""T07 · 闸门状态机全分支（DESIGN §6.10 单一权威定义）。"""

import json
import time
from pathlib import Path

import pytest

from ferryman.accounts import Accounts
from ferryman.config import Config, ThresholdCfg
from ferryman.ledger import Ledger
from ferryman.server import FerryDaemon
from ferryman.store import Store

SUMMARIZE, BLOCK, MIN_CTX = 10, 30, 100


@pytest.fixture
def env(tmp_path):
    led = Ledger()
    store = Store(tmp_path / "data")
    enqueued: list[str] = []
    cfg = Config()
    cfg.gate_cc = "enforce"
    cfg.thresholds = ThresholdCfg(summarize_s=SUMMARIZE, block_s=BLOCK,
                                  min_ctx_tokens=MIN_CTX)
    d = FerryDaemon(cfg, led, store,
                    lambda st: (enqueued.append(st.session_id), True)[1])
    return d, led, store, enqueued, tmp_path


def _reg(led, sid, path, cwd, *, idle_s, peak=99999):
    led.touch("cc", sid, path, mtime=time.time() - idle_s, size=10, cwd=cwd,
              peak_ctx=peak, daemon_started_at=0)


def _body(sid, path, cwd, prompt="继续"):
    return {"agent": "cc", "session_id": sid, "transcript_path": path,
            "cwd": cwd, "prompt": prompt}


# ---- 前置分支 ----

def test_bypass_prefix(env):
    d, *_ = env
    r = d.gate(_body("s", "p", "c", prompt="!!我知道缓存死了，继续"))
    assert r == {"decision": "allow", "reason": "bypass"}
    assert d.stats.bypass == 1


def test_bypass_prefix_qiangxu(env):
    """「强续」前缀放行——CC 下 !! 不可达（! 首字符即触发 bash 模式），换可达关键词。"""
    d, *_ = env
    r = d.gate(_body("s", "p", "c", prompt="强续我知道缓存死了，继续"))
    assert r == {"decision": "allow", "reason": "bypass"}
    assert d.stats.bypass == 1


def test_no_ledger(env):
    d, *_ = env
    r = d.gate(_body("ghost", "C:/nope.jsonl", "C:/any"))
    assert r == {"decision": "allow", "reason": "no-ledger"}


def test_mode_off(env):
    d, led, *_ = env
    d.cfg.gate_cc = "off"
    _reg(led, "s1", "C:/p1.jsonl", "C:/proj", idle_s=9999)
    assert d.gate(_body("s1", "C:/p1.jsonl", "C:/proj"))["reason"] == "mode-off"


def test_not_in_window_allows_plainly(env):
    d, led, *_ = env
    _reg(led, "s1", "C:/p1.jsonl", "C:/proj", idle_s=BLOCK - 10)
    r = d.gate(_body("s1", "C:/p1.jsonl", "C:/proj"))
    assert r["decision"] == "allow" and "additional_context" not in r


def test_observe_mode_warns_not_blocks(env):
    d, led, store, enqueued, tmp = env
    d.cfg.gate_cc = "observe"
    _reg(led, "s1", "C:/p1.jsonl", str(tmp / "proj"), idle_s=BLOCK + 5)
    r = d.gate(_body("s1", "C:/p1.jsonl", str(tmp / "proj")))
    assert r["decision"] == "allow" and r["additional_context"]
    # 2026-09-18 文案修复：observe 永不拦，不得再发"将被拦"空头支票
    assert "只提醒不拦" in r["additional_context"]
    assert "将被拦" not in r["additional_context"]
    assert enqueued == ["s1"]                    # observe 也触发摆渡补交接


# ---- enforce：分支 5（有效交接 → block）----

def test_branch5_block_with_valid_handoff(env):
    d, led, store, enqueued, tmp = env
    from datetime import datetime, timezone
    covers = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
    proj = str(tmp / "proj")
    _reg(led, "s1", "C:/p1.jsonl", proj, idle_s=BLOCK + 5)
    e = store.save_handoff(session_id="s1", agent="cc", cwd=proj, title="t",
                           covers_until_iso=covers, status="fresh", handoff_md="md")
    r = d.gate(_body("s1", "C:/p1.jsonl", proj, prompt="被拦的原话"))
    assert r["decision"] == "block"
    assert r["suppressOriginalPrompt"] is True
    assert r["handoff_path"] == e["path"] and "交接" in r["reason"]
    assert store.pop_pending_prompt("s1") == "被拦的原话"    # 待续 prompt 保管
    idx = json.loads(Path(store.index_path).read_text(encoding="utf-8"))
    assert idx["handoffs"][0]["blocked_at"]                  # block 事件入 index
    assert enqueued == []                                    # 分支5 不再入队


def test_branch5_copy_guides_post_clear(env):
    """block 文案须自带三步指引（/clear 会抹掉文案，空屏后用户无任何提示）。"""
    d, led, store, *_ = env
    from datetime import datetime, timezone
    covers = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
    proj = str(env[4] / "proj")
    _reg(led, "s1", "C:/p1.jsonl", proj, idle_s=BLOCK + 5)
    store.save_handoff(session_id="s1", agent="cc", cwd=proj, title="t",
                       covers_until_iso=covers, status="fresh", handoff_md="md")
    r = d.gate(_body("s1", "C:/p1.jsonl", proj, prompt="被拦"))
    assert r["decision"] == "block"
    assert "随便发一个字" in r["reason"]                       # /clear 后怎么续（关键痛点）
    assert "强续" in r["reason"] and "!!" not in r["reason"]   # 可达的逃生关键词


# ---- enforce：分支 7 → 6 → 降级 ----

def test_branch7_warn_then_branch6_blocks_then_degrade(env):
    d, led, store, enqueued, tmp = env
    proj2 = str(tmp / "proj2")                               # 无交接的 cwd
    _reg(led, "s2", "C:/p2.jsonl", proj2, idle_s=BLOCK + 5)
    key = ("cc", "s2")

    r1 = d.gate(_body("s2", "C:/p2.jsonl", proj2))           # 分支7：警告一次
    assert r1["decision"] == "allow" and r1["additional_context"]
    assert d.pending.get(key) is not None and enqueued == ["s2"]

    for i in (1, 2, 3):                                      # 分支6：连续 3 次 block
        r = d.gate(_body("s2", "C:/p2.jsonl", proj2))
        assert r["decision"] == "block" and f"第 {i} 次" in r["reason"]
        assert "强续" in r["reason"]                          # 逃生关键词可达
    r4 = d.gate(_body("s2", "C:/p2.jsonl", proj2))           # 第 4 次：降级放行
    assert r4["decision"] == "allow" and r4["additional_context"]
    assert d.pending.get(key) is None                        # pending 清除


def test_branch7_no_enqueue_below_min_ctx(env):
    d, led, store, enqueued, tmp = env
    proj = str(tmp / "small")
    _reg(led, "s3", "C:/p3.jsonl", proj, idle_s=BLOCK + 5, peak=MIN_CTX - 1)
    r = d.gate(_body("s3", "C:/p3.jsonl", proj))
    assert r["decision"] == "allow" and r["additional_context"]   # 仍警告+置 pending
    assert "将被拦" in r["additional_context"]                    # enforce 下承诺为真
    assert enqueued == []                                         # 但小会话不入队


# ---- pending 生命周期 ----

def test_pending_cleared_on_new_idle_cycle(env):
    d, led, *_ = env
    proj = "C:/projX"
    _reg(led, "s4", "C:/p4.jsonl", proj, idle_s=BLOCK + 5)
    key = ("cc", "s4")
    d.gate(_body("s4", "C:/p4.jsonl", proj))                 # 分支7 → pending
    assert d.pending.get(key) is not None
    # 模拟用户回来又离开 summarize 时长（pending 早于新 last_write）
    st = led.get("cc", "s4")
    st.last_write = time.time() - (SUMMARIZE + 5)
    d.pending.t[key]["set_at"] = st.last_write - 10
    r = d.gate(_body("s4", "C:/p4.jsonl", proj))
    assert r["decision"] == "allow"                          # 未到 block，普通放行
    assert d.pending.get(key) is None                        # 新周期已清 pending


def test_pending_ttl_24h(env):
    d, *_ = env
    d.pending.set(("cc", "s9"))
    key = ("cc", "s9")
    d.pending.t[key]["set_at"] = time.time() - 25 * 3600
    assert d.pending.get(key) is None


# ---- T44b：缓存死线纯提醒（12min 信息条；不拦、不摆渡、0=关）----

def test_cache_info_at_15min(env):
    """闲置 12–35min 区间：纯提醒信息条——含"全价计费/无需操作"，非"交接生成中"。"""
    d, led, _, enqueued, _ = env
    d.cfg.thresholds = ThresholdCfg()            # 真实默认：warn 720s / block 2100s
    _reg(led, "s5", "C:/p5.jsonl", "C:/proj", idle_s=900)
    r = d.gate(_body("s5", "C:/p5.jsonl", "C:/proj"))
    assert r["decision"] == "allow"
    assert "全价计费" in r["additional_context"]
    assert "无需操作" in r["additional_context"]
    assert "交接生成中" not in r["additional_context"]
    assert enqueued == []                        # 纯提醒：不触发摆渡
    d.cfg.gate_cc = "observe"                    # observe 放行路径同样带信息条
    r2 = d.gate(_body("s5", "C:/p5.jsonl", "C:/proj"))
    assert r2["decision"] == "allow" and "全价计费" in r2["additional_context"]


def test_no_info_below_12min(env):
    d, led, *_ = env
    d.cfg.thresholds = ThresholdCfg()
    _reg(led, "s6", "C:/p6.jsonl", "C:/proj", idle_s=400)
    r = d.gate(_body("s6", "C:/p6.jsonl", "C:/proj"))
    assert r == {"decision": "allow"}            # 未到死线：无 additional_context


def test_no_info_at_block_window(env):
    """≥block 窗口走既有警告而非信息条——"建议 /clear" vs "无需操作"互斥可辨。"""
    d, led, *_ = env
    d.cfg.gate_cc = "observe"
    _reg(led, "s7", "C:/p7.jsonl", "C:/proj", idle_s=BLOCK + 5)
    r = d.gate(_body("s7", "C:/p7.jsonl", "C:/proj"))
    assert r["decision"] == "allow" and r["additional_context"]
    assert "闲置" in r["additional_context"]              # 既有警告
    assert "建议 /clear" in r["additional_context"]
    assert "无需操作" not in r["additional_context"]      # 非信息条专属文案


def test_cache_info_disabled(env):
    d, led, *_ = env
    d.cfg.thresholds = ThresholdCfg(cache_warn_s=0)
    _reg(led, "s8", "C:/p8.jsonl", "C:/proj", idle_s=900)
    r = d.gate(_body("s8", "C:/p8.jsonl", "C:/proj"))
    assert r == {"decision": "allow"}            # 0=关：无 additional_context


# ---- T46：窗口前缀懒富化（peak_ctx 缺位时回落账本 usage 实报值）----

def _daemon_acc(tmp_path):
    """带真 Accounts 的 daemon（env fixture 的 accounts=None，回落用例需要真账本）。"""
    led = Ledger()
    cfg = Config()
    cfg.gate_cc = "enforce"
    cfg.thresholds = ThresholdCfg(summarize_s=SUMMARIZE, block_s=BLOCK,
                                  min_ctx_tokens=MIN_CTX)
    acc = Accounts(tmp_path)
    d = FerryDaemon(cfg, led, Store(tmp_path / "data"), lambda st: True,
                    accounts=acc)
    return d, led, acc


def _usage(acc, ts, sid, i, cr, cc):
    return acc.record("usage", ts=ts, agent="cc", session_id=sid,
                      lineage_id=f"L-{sid}", project="C:/proj", model="glm-5.3",
                      title="", input_tokens=i, cache_read_tokens=cr,
                      cache_creation_tokens=cc, output_tokens=10, offset=0)


def _open_close(d, sid):
    assert d.subagent({"event": "start", "agent": "cc", "session_id": sid})["ok"]
    assert d.subagent({"event": "stop", "agent": "cc", "session_id": sid})["ok"]


def _window_rows(acc, sid):
    return [e for e in acc.read(kind="window") if e["session_id"] == sid]


def test_window_prefix_prefers_peak_ctx(tmp_path):
    """peak_ctx 非零 → 照旧直接用，不回落账本。"""
    d, led, acc = _daemon_acc(tmp_path)
    led.touch("cc", "w1", "C:/p1.jsonl", mtime=time.time(), size=10, cwd="C:/proj",
              peak_ctx=5000, daemon_started_at=0)
    _usage(acc, time.time() - 60, "w1", 999, 149000, 0)
    _open_close(d, "w1")
    rows = _window_rows(acc, "w1")
    assert len(rows) == 1 and rows[0]["prefix_tokens"] == 5000


def test_window_prefix_falls_back_to_usage(tmp_path):
    """peak_ctx=0 → 回落开窗前最后一条 usage 的 input+cache_read+cache_creation。"""
    d, led, acc = _daemon_acc(tmp_path)
    led.touch("cc", "w2", "C:/p2.jsonl", mtime=time.time(), size=10, cwd="C:/proj",
              peak_ctx=0, daemon_started_at=0)
    t0 = time.time()
    _usage(acc, t0 - 300, "w2", 100, 120000, 300)
    _usage(acc, t0 - 200, "w2", 200, 130000, 400)
    _usage(acc, t0 - 100, "w2", 800, 140000, 9200)     # 开窗前最后一条 → 150000
    _open_close(d, "w2")
    rows = _window_rows(acc, "w2")
    assert len(rows) == 1 and rows[0]["prefix_tokens"] == 150000


def test_window_prefix_ignores_rows_after_open(tmp_path):
    """开窗后（ts > opened_ts）写入的更大行不采纳——仍取 ≤opened_ts 的最后一条。"""
    d, led, acc = _daemon_acc(tmp_path)
    led.touch("cc", "w3", "C:/p3.jsonl", mtime=time.time(), size=10, cwd="C:/proj",
              peak_ctx=0, daemon_started_at=0)
    _usage(acc, time.time() - 100, "w3", 800, 140000, 9200)   # 开窗前 → 150000
    assert d.subagent({"event": "start", "agent": "cc", "session_id": "w3"})["ok"]
    time.sleep(0.01)                                   # 避开 round(ts,3) 与开窗时刻同毫秒
    _usage(acc, time.time(), "w3", 50000, 0, 0)        # 窗口期间别处写入的更大行
    assert d.subagent({"event": "stop", "agent": "cc", "session_id": "w3"})["ok"]
    rows = _window_rows(acc, "w3")
    assert len(rows) == 1 and rows[0]["prefix_tokens"] == 150000


def test_window_prefix_clock_skew_takes_latest_any(tmp_path):
    """无 ≤opened_ts 行（时钟毛刺）→ 退取该会话任意最后一条 usage。"""
    d, led, acc = _daemon_acc(tmp_path)
    led.touch("cc", "w4", "C:/p4.jsonl", mtime=time.time(), size=10, cwd="C:/proj",
              peak_ctx=0, daemon_started_at=0)
    assert d.subagent({"event": "start", "agent": "cc", "session_id": "w4"})["ok"]
    _usage(acc, time.time() + 5, "w4", 800, 140000, 9200)     # 毛刺：ts 落在开窗后
    assert d.subagent({"event": "stop", "agent": "cc", "session_id": "w4"})["ok"]
    rows = _window_rows(acc, "w4")
    assert len(rows) == 1 and rows[0]["prefix_tokens"] == 150000


def test_window_prefix_zero_when_no_usage(tmp_path):
    """账本里全无该会话 usage → 0（窗口行仍要落，不能丢）。"""
    d, led, acc = _daemon_acc(tmp_path)
    led.touch("cc", "w5", "C:/p5.jsonl", mtime=time.time(), size=10, cwd="C:/proj",
              peak_ctx=0, daemon_started_at=0)
    _open_close(d, "w5")
    rows = _window_rows(acc, "w5")
    assert len(rows) == 1 and rows[0]["prefix_tokens"] == 0


def test_window_none_accounts_no_crash(env):
    """accounts=None（旧测试形态）→ 闭窗整条跳过，全程不炸。"""
    d, led, *_ = env
    led.touch("cc", "w6", "C:/p6.jsonl", mtime=time.time(), size=10, cwd="C:/proj",
              peak_ctx=0, daemon_started_at=0)
    _open_close(d, "w6")
    assert d._windows == {}                            # 窗已 pop，无行可记也不炸
