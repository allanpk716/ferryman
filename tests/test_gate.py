"""T07 · 闸门状态机全分支（DESIGN §6.10 单一权威定义）。"""

import json
import time
from pathlib import Path

import pytest

from ferryman.accounts import Accounts
from ferryman.config import Config, ThresholdCfg
from ferryman.ledger import Ledger
from ferryman.server import PARK_EXPIRE_S, FerryDaemon
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


# ---- 缺口A：机器等机器豁免（rev2 规格，验收 A1-A10 除 A6 人工项） ----
# 规格：docs/superpowers/specs/20260918-antfeedinglog子代理等待场景-consensus.rev2.md

def _mk_dangling(p: Path) -> Path:
    """悬空 jsonl：末条 tool_use 无 tool_result（真实悬空形态）。"""
    line = json.dumps({"type": "assistant", "message": {"content": [
        {"type": "tool_use", "id": "t1", "name": "Task", "input": {}}]}})
    p.write_text(line + "\n", encoding="utf-8")
    return p


def _mk_resolved(p: Path) -> Path:
    """自愈 jsonl：tool_use 与 tool_result 都在。"""
    tu = json.dumps({"type": "assistant", "message": {"content": [
        {"type": "tool_use", "id": "t1", "name": "Task", "input": {}}]}})
    tr = json.dumps({"type": "user", "message": {"content": [
        {"type": "tool_result", "tool_use_id": "t1", "content": "ok"}]}})
    p.write_text(tu + "\n" + tr + "\n", encoding="utf-8")
    return p


def test_A1_subagent_active_exempts(env):
    """A1：计数>0 → 放行 + 说明（含卡死恢复出路）+ 不置 pending + 不入队 + 计数不变。"""
    d, led, store, enqueued, tmp = env
    proj = str(tmp / "projA1")
    _reg(led, "a1", "C:/no-such-a1.jsonl", proj, idle_s=BLOCK + 5)
    led.subagent_event("cc", "a1", "start")
    r = d.gate(_body("a1", "C:/no-such-a1.jsonl", proj))
    assert r["decision"] == "allow" and r["reason"] == "machine-waiting"
    assert "放行" in r["additional_context"] and "Esc" in r["additional_context"]
    assert d.pending.get(("cc", "a1")) is None            # 不置 pending
    assert enqueued == []                                 # 不入队摆渡（分支7不可达）
    assert led.subagent_active("cc", "a1")                # 计数不受影响


def test_A2_dangling_only_exempts(env):
    """A2：仅悬空（计数=0）→ 同样放行（两道 OR 的直接验证）。"""
    d, led, store, enqueued, tmp = env
    f = _mk_dangling(tmp / "a2.jsonl")
    proj = str(tmp / "projA2")
    _reg(led, "a2", str(f), proj, idle_s=BLOCK + 5)
    r = d.gate(_body("a2", str(f), proj))
    assert r["decision"] == "allow" and r["reason"] == "machine-waiting"
    assert d.pending.get(("cc", "a2")) is None
    assert enqueued == []


def test_A4_exemption_beats_observe_warn_and_enqueue(env):
    """A4：observe 模式下豁免同样生效（不警告不入队，mid-wait 摆渡堵死）；
    对照组证明非豁免路径零回归。"""
    d, led, store, enqueued, tmp = env
    d.cfg.gate_cc = "observe"
    proj = str(tmp / "projA4")
    _reg(led, "a4", "C:/no-such-a4.jsonl", proj, idle_s=BLOCK + 5)
    led.subagent_event("cc", "a4", "start")
    r = d.gate(_body("a4", "C:/no-such-a4.jsonl", proj))
    assert r["decision"] == "allow" and r["reason"] == "machine-waiting"
    assert enqueued == []
    _reg(led, "a4b", "C:/no-such-a4b.jsonl", proj, idle_s=BLOCK + 5)
    r2 = d.gate(_body("a4b", "C:/no-such-a4b.jsonl", proj))
    assert r2["decision"] == "allow" and r2.get("additional_context")
    assert enqueued == ["a4b"]                             # 既有 observe 行为零回归


def test_A7_count_leak_recovers_gate(env):
    """A7：计数泄漏期过后恢复拦截能力（前置：tool_result 已落盘=无悬空）。"""
    d, led, store, enqueued, tmp = env
    f = _mk_resolved(tmp / "a7.jsonl")
    proj = str(tmp / "projA7")
    _reg(led, "a7", str(f), proj, idle_s=BLOCK + 5)
    led.subagent_event("cc", "a7", "start")                # Stop 丢失：计数停 1
    led._subagents[("cc", "a7")] = (1, time.time() - 4000)  # 泄漏期已过
    r = d.gate(_body("a7", str(f), proj))
    assert r["decision"] == "allow"                        # 正常路径（分支7 警告）
    assert r.get("reason") != "machine-waiting"
    assert enqueued == ["a7"]                              # 入队恢复=拦截能力恢复


def test_A8_pending_preserved_across_exemption(env):
    """A8：先置 pending → 子代理在飞提交=放行且 pending 原样（不清、不 bump
    blocks 计数）→ 子代理结束后下一次提交按 pending 状态机走（分支6）。"""
    d, led, store, enqueued, tmp = env
    f = _mk_resolved(tmp / "a8.jsonl")
    proj = str(tmp / "projA8")
    _reg(led, "a8", str(f), proj, idle_s=BLOCK + 5)
    d.gate(_body("a8", str(f), proj))                      # 分支7：置 pending
    assert d.pending.get(("cc", "a8")) is not None
    led.subagent_event("cc", "a8", "start")
    r1 = d.gate(_body("a8", str(f), proj))                 # 豁免放行
    assert r1["reason"] == "machine-waiting"
    assert d.pending.t[("cc", "a8")]["blocks"] == 0        # 计数不变（未 bump）
    led.subagent_event("cc", "a8", "stop")                 # 计数归零
    r2 = d.gate(_body("a8", str(f), proj))
    assert r2["decision"] == "block" and "强续" in r2["reason"]   # pending 状态机照走


def test_A9_dangling_persistent_is_design(env):
    """A9：计数=0 且悬空=真 → 连续多次提交均放行（宁可不拦方向的显式验收）。"""
    d, led, store, enqueued, tmp = env
    f = _mk_dangling(tmp / "a9.jsonl")
    proj = str(tmp / "projA9")
    _reg(led, "a9", str(f), proj, idle_s=BLOCK + 5)
    for _ in range(3):
        r = d.gate(_body("a9", str(f), proj))
        assert r["decision"] == "allow" and r["reason"] == "machine-waiting"


def test_A10_dangling_selfheals(env):
    """A10：悬空期放行 → tool_result 落盘 → 下一次提交恢复正常闸门路径。"""
    d, led, store, enqueued, tmp = env
    f = _mk_dangling(tmp / "a10.jsonl")
    proj = str(tmp / "projA10")
    _reg(led, "a10", str(f), proj, idle_s=BLOCK + 5)
    assert d.gate(_body("a10", str(f), proj))["reason"] == "machine-waiting"
    _mk_resolved(f)                                        # tool_result 落盘
    r = d.gate(_body("a10", str(f), proj))
    assert r["decision"] == "allow" and r.get("reason") != "machine-waiting"
    assert enqueued == ["a10"]                             # 分支7 正常入队


def test_A5_branch6_copy_deterministic_escape(env):
    """A5：分支6 新文案——确定性出路（每次强续都放行）；第4次降级不变。
    （A3 口径：分支5/7 既有断言不动；分支6 文案断言随本条更新。）"""
    d, led, store, enqueued, tmp = env
    proj = str(tmp / "projA5")
    _reg(led, "a5", "C:/no-such-a5.jsonl", proj, idle_s=BLOCK + 5)
    r0 = d.gate(_body("a5", "C:/no-such-a5.jsonl", proj))  # 分支7：警告+置 pending
    assert r0["decision"] == "allow" and r0.get("additional_context")
    for i in (1, 2, 3):
        r = d.gate(_body("a5", "C:/no-such-a5.jsonl", proj))
        assert r["decision"] == "block" and f"第 {i} 次" in r["reason"]
        assert "每次以「强续」开头" in r["reason"]          # 确定性出路
        assert "已保存" in r["reason"]                      # 原话下落明确
    r4 = d.gate(_body("a5", "C:/no-such-a5.jsonl", proj))
    assert r4["decision"] == "allow"                        # 第4次降级（行为不变）


# ---- T48 票02：等待窗口停表停车状态机（async 停车/latch/宽限/过期/豁免） ----
# 设计参照：docs/superpowers/plans/2026-09-18-t48-async-wait-signal.rev1.md Task 2
# + xcheck 附录必改 #1/#3/#5/#7/#10/#12/#13（以票面验收标准为准）。

PARK_EXPIRE_S_REF = PARK_EXPIRE_S   # 附录#12：import server 真常量，不复制 3600 字面量


@pytest.fixture
def wenv(tmp_path):
    """带真 Accounts 的 daemon（停车状态机用例要读 window 流水断 close_reason）。"""
    led = Ledger()
    store = Store(tmp_path / "data")
    acc = Accounts(tmp_path / "acc")
    cfg = Config()
    cfg.gate_cc = "enforce"
    cfg.thresholds = ThresholdCfg(summarize_s=SUMMARIZE, block_s=BLOCK,
                                  min_ctx_tokens=MIN_CTX)
    d = FerryDaemon(cfg, led, store, lambda st: True, accounts=acc)
    return d, led, acc, tmp_path


def _write_session(led, tmp, sid, blocks, idle_s=0):
    """写转录 jsonl + 登台账（路径真实存在，尾判 has_async_launch 可读）。"""
    p = tmp / f"{sid}.jsonl"
    p.write_text("\n".join(json.dumps(b) for b in blocks) + "\n", encoding="utf-8")
    led.touch("cc", sid, str(p), mtime=time.time() - idle_s, size=10,
              cwd="C:/proj", peak_ctx=150000, daemon_started_at=0)
    return p


def _append_blocks(p, blocks):
    """两阶段交错用例（附录#1）：向已有转录追加块——一次性预写会使 stop 时
    尾判读到 sync 块、latch 永不触发，测试实现后仍红（e2 实验已证）。"""
    with open(p, "a", encoding="utf-8") as f:
        for b in blocks:
            f.write(json.dumps(b) + "\n")


def _async_blocks():
    return [
        {"type": "assistant", "message": {"role": "assistant", "content": [
            {"type": "tool_use", "id": "t1", "name": "Task",
             "input": {"prompt": "干活", "run_in_background": True}}]}},
        {"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": "t1",
             "content": "Async agent launched successfully"}]}},
    ]


def _sync_blocks():
    return [
        {"type": "assistant", "message": {"role": "assistant", "content": [
            {"type": "tool_use", "id": "t2", "name": "Task",
             "input": {"prompt": "短活"}}]}},
        {"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": "t2", "content": "done"}]}},
    ]


def _start_stop(d, sid):
    assert d.subagent({"event": "start", "agent": "cc", "session_id": sid})["ok"]
    assert d.subagent({"event": "stop", "agent": "cc", "session_id": sid})["ok"]


def test_async_stop_parks_until_main_resumes(wenv):
    """核心案：async 派发 stop 后不闭窗；主会话恢复调用才闭（main_resumed）。"""
    d, led, acc, tmp = wenv
    sid = "as1"
    _write_session(led, tmp, sid, _async_blocks())
    assert d.subagent({"event": "start", "agent": "cc", "session_id": sid})["ok"]
    time.sleep(0.05)                                # dur_s > 0 可断言
    assert d.subagent({"event": "stop", "agent": "cc", "session_id": sid})["ok"]
    assert _window_rows(acc, sid) == []             # 不再秒闭（旧 bug：dur 0.6min 废数据）
    assert d.window_wait("cc", sid) is True         # 机器等待在停
    d.note_usage("cc", sid, ts=time.time() + 200)   # 恢复调用（晚于停表+90s 宽限）
    rows = _window_rows(acc, sid)
    assert len(rows) == 1
    assert rows[0]["close_reason"] == "main_resumed"
    assert rows[0]["dur_s"] > 0
    assert d.window_wait("cc", sid) is False


def test_ack_turn_within_grace_does_not_close(wenv):
    """★ 宽限：stop 后 ack 确认回合（90s 内的 usage 行）不闭窗。

    当天实测 ack 均落在 stop 前（钩子时序），时序反转时靠宽限兜底
    （round 0 评审 #10/#11 的廉价保险）。"""
    d, led, acc, tmp = wenv
    sid = "as1b"
    _write_session(led, tmp, sid, _async_blocks())
    _start_stop(d, sid)
    d.note_usage("cc", sid, ts=time.time() + 5)     # ack 行：stop 后 5s（宽限内）
    assert _window_rows(acc, sid) == []
    assert d.window_wait("cc", sid) is True
    d.note_usage("cc", sid, ts=time.time() + 200)   # 真恢复
    assert _window_rows(acc, sid)[0]["close_reason"] == "main_resumed"


def test_sync_stop_closes_immediately(wenv):
    """同步派发语义不变：stop 即闭（既有行为回归锁）。"""
    d, led, acc, tmp = wenv
    sid = "sy1"
    _write_session(led, tmp, sid, _sync_blocks())
    _start_stop(d, sid)
    rows = _window_rows(acc, sid)
    assert len(rows) == 1 and rows[0]["close_reason"] == "subagents_done"
    assert d.window_wait("cc", sid) is False


def test_interleave_sync_after_async_keeps_park(wenv):
    """★ latch（附录#1 两阶段写文件）：async 停车后再派 sync，sync 的 stop 不再
    走即闭——交错派发不丢 async 等待（round 0 e2 实验：一次性预写会使 stop 时
    尾判读到 sync、latch 永不触发，故先 async 停车、再追加 sync 块）。"""
    d, led, acc, tmp = wenv
    sid = "ix1"
    p = _write_session(led, tmp, sid, _async_blocks())
    _start_stop(d, sid)                             # 第一段 async 的 stop → 停车
    assert _window_rows(acc, sid) == []
    _append_blocks(p, _sync_blocks())               # 再派 sync：追加转录块
    d.subagent({"event": "start", "agent": "cc", "session_id": sid})   # 续窗
    d.subagent({"event": "stop", "agent": "cc", "session_id": sid})    # sync 的 stop
    assert _window_rows(acc, sid) == []             # 仍不闭（latch 生效）
    assert d.window_wait("cc", sid) is True
    d.note_usage("cc", sid, ts=time.time() + 200)
    assert _window_rows(acc, sid)[0]["close_reason"] == "main_resumed"


def test_overlap_seq_parks_even_when_sync_stop_seen_last(wenv):
    """★ 重叠序（附录#7）：async start→sync start→async stop→sync stop。

    停车判定只在计数归零时跑，届时尾部最后派发已是 sync——须靠 start 事件
    处理时的尾判置 latch，否则 sync 的 stop 会误闭 async 等待。"""
    d, led, acc, tmp = wenv
    sid = "ov1"
    p = _write_session(led, tmp, sid, _async_blocks())
    d.subagent({"event": "start", "agent": "cc", "session_id": sid})   # async start
    _append_blocks(p, _sync_blocks())
    d.subagent({"event": "start", "agent": "cc", "session_id": sid})   # sync start（计数=2）
    d.subagent({"event": "stop", "agent": "cc", "session_id": sid})    # async 早到 stop（计数=1，无判定）
    d.subagent({"event": "stop", "agent": "cc", "session_id": sid})    # sync stop（归零）
    assert _window_rows(acc, sid) == []             # 最终仍停车（不误闭）
    assert d.window_wait("cc", sid) is True


def test_parked_window_exempts_gate_and_prompt_keeps_park(wenv):
    """停车窗期间用户输消息 → 缺口 A 放行（machine-waiting），且窗口不因
    prompt 闭——async 真身还在跑，等待没结束（今天会误拦/误闭的场景）。"""
    d, led, acc, tmp = wenv
    sid = "as4"
    p = _write_session(led, tmp, sid, _async_blocks(), idle_s=9999)  # 闲置远超 block 线
    _start_stop(d, sid)
    r = d.gate(_body(sid, str(p), "C:/proj"))
    assert r["decision"] == "allow" and r["reason"] == "machine-waiting"
    assert d.window_wait("cc", sid) is True
    assert _window_rows(acc, sid) == []             # 停车窗不因 prompt 闭


def test_parked_window_expires(wenv):
    """停表超 PARK_EXPIRE_S → 懒过期闭窗记 expired，豁免随之失效。"""
    d, led, acc, tmp = wenv
    sid = "as5"
    _write_session(led, tmp, sid, _async_blocks())
    _start_stop(d, sid)
    d._windows[("cc", sid)]["stop_ts"] = time.time() - (PARK_EXPIRE_S_REF + 400)
    assert d.window_wait("cc", sid) is False
    rows = _window_rows(acc, sid)
    assert rows and rows[-1]["close_reason"] == "expired"


def test_window_wait_never_raises(wenv, monkeypatch):
    """★ 异常边界（附录#5 收窄 patch 面）：记账路径炸了，谓词只返回 False 不
    外抛；patch 只打窗口记账内部（_record_window），闸门主路径不受影响照常决策。
    且记账失败不丢窗（先记后 pop，附录#10）。"""
    d, led, acc, tmp = wenv
    sid = "as6"
    p = _write_session(led, tmp, sid, _async_blocks(), idle_s=9999)
    _start_stop(d, sid)
    d._windows[("cc", sid)]["stop_ts"] = time.time() - (PARK_EXPIRE_S_REF + 400)

    def boom(*a, **k):
        raise RuntimeError("记账坏了")
    monkeypatch.setattr(d, "_record_window", boom)
    assert d.window_wait("cc", sid) is False        # 不抛、按 False
    assert ("cc", sid) in d._windows                # 记账失败窗保留（未 pop）
    r = d.gate(_body(sid, str(p), "C:/proj"))       # 闸门主路径不炸、照常出决策
    assert r["decision"] in ("allow", "block")


def test_note_usage_record_failure_keeps_window(wenv, monkeypatch):
    """★ 附录#10：note_usage 记账炸 → 不抛，且绝不"先 pop 后记账失败丢窗"。"""
    d, led, acc, tmp = wenv
    sid = "as7"
    _write_session(led, tmp, sid, _async_blocks())
    _start_stop(d, sid)

    def boom(*a, **k):
        raise RuntimeError("记账坏了")
    monkeypatch.setattr(d, "_record_window", boom)
    d.note_usage("cc", sid, ts=time.time() + 200)   # 不抛
    assert d.window_wait("cc", sid) is True         # 窗未丢（仍停车，下轮可重试）


def test_reanchor_expired_park_records_expired_no_future_ts(wenv):
    """重锚旧停车窗（附录#3/#13）：按距 stop_ts 超 PARK_EXPIRE_S 判过期（非
    opened_ts）；closed_ts=min(stop+PARK_EXPIRE_S, now)——绝不出现未来时刻。"""
    d, led, acc, tmp = wenv
    sid = "ix2"
    _write_session(led, tmp, sid, _async_blocks())
    _start_stop(d, sid)
    stop = time.time() - (PARK_EXPIRE_S_REF + 400)
    w = d._windows[("cc", sid)]
    w["stop_ts"] = stop
    w["opened_ts"] = stop - 300      # opened 更早：超 LEAK 阈值 → 下个 start 走重锚
    assert d.subagent({"event": "start", "agent": "cc", "session_id": sid})["ok"]
    rows = _window_rows(acc, sid)
    assert len(rows) == 1 and rows[0]["close_reason"] == "expired"
    assert rows[0]["closed_ts"] <= time.time() + 1            # 无未来时刻
    assert abs(rows[0]["closed_ts"] - (stop + PARK_EXPIRE_S_REF)) < 5
    assert d._windows[("cc", sid)]["stop_ts"] is None         # 新窗在跑


def test_prompt_still_closes_open_window(wenv):
    """未停车的窗口遇 prompt 照旧闭（R1 既有语义回归锁）。"""
    d, led, acc, tmp = wenv
    sid = "sy6"
    p = _write_session(led, tmp, sid, _sync_blocks())
    assert d.subagent({"event": "start", "agent": "cc", "session_id": sid})["ok"]
    d.gate(_body(sid, str(p), "C:/proj"))
    rows = _window_rows(acc, sid)
    assert len(rows) == 1 and rows[0]["close_reason"] == "prompt"
