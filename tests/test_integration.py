"""T10-T15, T31 · 集成测试：全链路 / skeleton 降级 / 墙钟强杀 / 队列背压 / 懒富化 / 鉴权健康 / 悬空 tool_use 推迟。

全离线：摆渡模型用 monkeypatch 假件替代（不连 4090x2、不出网）。
"""

import queue
import time
import urllib.error
import urllib.parse
from pathlib import Path

import pytest

import ferryman.daemon as daemon_mod
from ferryman.config import Config, ThresholdCfg
from ferryman.daemon import Watcher
from ferryman.ledger import now_s
from helpers import Harness, free_port, write_session


# ---------- T10 全链路 ----------

def test_t10_full_loop(h):
    proj = str(h.tmp / "proj")
    sid = "integ-0001"
    f = write_session(h.projects, sid, proj)
    body = {"agent": "cc", "session_id": sid, "transcript_path": str(f),
            "cwd": proj, "prompt": "继续"}

    # 守望→富化→入队→假摆渡→交接落盘
    assert h.wait_for(lambda: any(e["session_id"] == sid
                                  for e in h.store._index["handoffs"])), "摆渡未完成"
    # 闲置达阈值后 gate 分支5
    assert h.wait_for(lambda: h.gate(body)["decision"] == "block", timeout=10)
    r = h.gate(body)
    assert r["handoff_path"] and "交接" in r["reason"]
    # 归还注入（含不可信声明 + 注入层 + 待续 prompt）
    ctx = h.get(f"/restore?agent=cc&cwd={urllib.parse.quote(proj)}&session_id=new-1")
    assert ctx["context"] and "不可信" in ctx["context"] and "注入层" in ctx["context"]
    assert "继续" in ctx["context"]                       # 待续 prompt 已随交接注入
    # 健康计数
    stats = h.get("/stats")
    assert stats["gate_calls_total"] >= 2 and stats["health_alert"] is False


# ---------- T11 skeleton 降级 ----------

def test_t11_skeleton_on_ferry_failure(tmp_path, monkeypatch):
    def exploding(path, provider):
        raise RuntimeError("provider down")

    harness = Harness(tmp_path, monkeypatch, fake_ferry=exploding)
    try:
        proj = str(tmp_path / "proj")
        sid = "integ-0002"
        f = write_session(harness.projects, sid, proj)
        assert harness.wait_for(lambda: any(
            e["session_id"] == sid and e["status"] == "skeleton"
            for e in harness.store._index["handoffs"])), "骨架降级未发生"
        body = {"agent": "cc", "session_id": sid, "transcript_path": str(f),
                "cwd": proj, "prompt": "x"}
        assert harness.wait_for(lambda: harness.gate(body)["decision"] == "block")
    finally:
        harness.stop()


# ---------- T12 墙钟强杀 → 降级 ----------

def test_t12_wall_clock_kill(tmp_path, monkeypatch):
    monkeypatch.setattr(daemon_mod, "FERRY_WALL_TIMEOUT_S", 1.0)

    def sleeping(path, provider):
        time.sleep(30)
        return "never", {}

    harness = Harness(tmp_path, monkeypatch, fake_ferry=sleeping)
    try:
        proj = str(tmp_path / "proj")
        sid = "integ-0003"
        write_session(harness.projects, sid, proj)
        assert harness.wait_for(lambda: any(
            e["session_id"] == sid and e["status"] == "skeleton"
            for e in harness.store._index["handoffs"]), timeout=20), "墙钟超时未降级"
        assert harness.tasks.empty()                     # 工人没被卡死
    finally:
        harness.stop()


# ---------- T13 队列背压 ----------

def test_t13_queue_backpressure_delays_not_dies(h):
    from helpers import write_session
    proj = str(h.tmp / "proj")
    f = write_session(h.projects, "bp-1", proj)          # 真实合成会话，任务字段完整
    item = {"transcript_path": str(f), "agent": "cc", "session_id": "bp-1",
            "cwd": proj}
    h.tasks.put_nowait(dict(item))                        # 占满（maxsize=1）
    with pytest.raises(queue.Full):                       # 满 → 调用方语义=延迟(False)
        h.tasks.put_nowait(dict(item))
    assert h.wait_for(lambda: any(e["session_id"] == "bp-1"
                                  for e in h.store._index["handoffs"]), timeout=10)
    h.tasks.put_nowait(dict(item))                        # 排空后可再入


# ---------- T14 懒富化单次性 ----------

def test_t14_enrich_once_per_version(tmp_path, monkeypatch):
    import ferryman.extract as extract_mod
    calls = {"n": 0}
    orig = extract_mod.extract

    def counting(path):
        calls["n"] += 1
        return orig(path)

    monkeypatch.setattr(extract_mod, "extract", counting)

    cfg = Config()
    cfg.thresholds = ThresholdCfg(summarize_s=0.1, block_s=1.0,
                                  min_ctx_tokens=10_000_000)   # 峰值永远不够 → 只富化不入队
    from ferryman.ledger import Ledger
    led = Ledger()
    proj = str(tmp_path / "proj")
    f = write_session(tmp_path / "projects", "enrich-1", proj)
    st = led.touch("cc", "enrich-1", str(f), mtime=now_s(), size=10, cwd=proj,
                   daemon_started_at=0)
    watcher = Watcher.__new__(Watcher)                   # 只测 _maybe_enqueue/_enrich
    watcher.cfg = cfg
    watcher.ledger = led
    watcher.store = None
    watcher.enqueue = lambda st: True
    watcher.started_at = 0
    st.last_write -= 5                                    # 造闲置
    for _ in range(3):
        watcher._maybe_enqueue(st)
    assert calls["n"] == 1                               # 同一 last_write 版本只读盘一次


# ---------- T31 悬空 tool_use 推迟入队 ----------

def _dangling_session(projects: Path, sid: str, cwd: str) -> Path:
    """写一个尾部悬空 tool_use（工具/子代理运行中）的会话。"""
    import json
    lines = [
        {"type": "user", "timestamp": "2026-09-16T12:00:00.000Z", "cwd": cwd,
         "sessionId": sid, "message": {"role": "user", "content": "跑个长任务"}},
        {"type": "assistant", "timestamp": "2026-09-16T12:00:02.000Z",
         "message": {"role": "assistant", "content": [
             {"type": "text", "text": "好"},
             {"type": "tool_use", "id": "toolu_dangle1", "name": "Bash", "input": {}}],
             "usage": {"input_tokens": 2000, "cache_read_input_tokens": 100,
                        "cache_creation_input_tokens": 0, "output_tokens": 5}}},
    ]
    f = projects / "C--proj" / f"{sid}.jsonl"
    f.parent.mkdir(parents=True, exist_ok=True)
    f.write_text("\n".join(json.dumps(x, ensure_ascii=False) for x in lines) + "\n",
                 encoding="utf-8")
    return f


def test_t31_dangling_defers_enqueue(tmp_path):
    """达阈值的 CC 会话若尾部悬空 tool_use → 不入队；对照组正常会话 → 入队。"""
    from ferryman.ledger import Ledger

    cfg = Config()
    cfg.thresholds = ThresholdCfg(summarize_s=0.1, block_s=1.0, min_ctx_tokens=10)
    led = Ledger()
    proj = str(tmp_path / "proj")
    enqueued: list[str] = []

    watcher = Watcher.__new__(Watcher)
    watcher.cfg, watcher.ledger, watcher.store = cfg, led, None
    watcher.enqueue = lambda st: enqueued.append(st.session_id) or True
    watcher.started_at = 0

    f1 = _dangling_session(tmp_path / "projects", "dangle-1", proj)
    f2 = write_session(tmp_path / "projects", "calm-2", proj)   # 对照：无悬空
    for sid, path in (("dangle-1", f1), ("calm-2", f2)):
        st = led.touch("cc", sid, str(path), mtime=now_s(), size=10, cwd=proj,
                       daemon_started_at=0)
        st.last_write -= 5                                        # 造闲置
        for _ in range(3):
            watcher._maybe_enqueue(st)
    assert "calm-2" in enqueued
    assert "dangle-1" not in enqueued                            # 运行中 → 每轮都推迟


# ---------- T15 鉴权与健康 ----------

def test_t15_auth_and_health(h):
    with pytest.raises(urllib.error.HTTPError) as ei:
        h.get("/stats", token="wrong-token")
    assert ei.value.code == 401
    assert h.get("/stats")["gate_calls_total"] >= 0      # 正确 token 通

    d = h.daemon
    d.started_at = now_s() - 601                         # 场景前提：已过启动宽限期
    d.stats.total = 0                                    # 1h 内有写入但 gate 零调用
    h.ledger.last_transcript_write = now_s()
    assert d.health()["health_alert"] is True
    d.stats.hit("cc")                                    # 一旦有调用 → 解除
    assert d.health()["health_alert"] is False


# ---------- 健康误报：daemon 重启宽限期 ----------
# 真实场景（2026-09-16 夜间实测）：daemon 重启后计数器归零，而自主运行的会话
# 仍在写 transcript（无人发 prompt → UserPromptSubmit 不触发 → gate 零调用），
# 旧逻辑立即误报"钩子失效"。T26 验收标准含"健康告警是否误报"。

def test_health_grace_period_after_daemon_restart(h):
    d = h.daemon
    d.stats.total = 0
    h.ledger.last_transcript_write = now_s()
    assert d.health()["health_alert"] is False        # 启动 <10min：宽限，不误报

    d.started_at = now_s() - 601                      # 宽限期已过，同条件才告警
    assert d.health()["health_alert"] is True
