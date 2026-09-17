"""T07 · 闸门状态机全分支（DESIGN §6.10 单一权威定义）。"""

import json
import time
from pathlib import Path

import pytest

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
