"""T32 · 子代理生命周期钩子：SubagentStart/Stop → daemon 计数 → 摆渡推迟；subagents/ 转录排除。

背景（2026-09-16 夜间探针实测，CC 2.1.273 + 临时钩子 + claude -p）：
- SubagentStart/Stop 对前台 / 后台异步完成 / 嵌套两层均触发；payload 含主会话 session_id、
  agent_id、agent_type、agent_transcript_path；
- 子代理 transcript 落在 <projects>/<proj>/<session-id>/subagents/agent-*.jsonl ——
  守望 glob 之前把它们当独立会话摆渡（daemon 日志 cc/agent-a6 系列即此）；
- 计数仅内存：daemon 重启丢计数 → T31 悬空检测兜底；Stop 丢失（崩溃/强杀）→ 泄漏防护。
"""

import json
import urllib.error
import urllib.request

import pytest

from ferryman.config import Config, ThresholdCfg, WatchCfg
from ferryman.daemon import Watcher
from ferryman.ledger import Ledger, now_s
from helpers import write_session


# ---------- 计数生命周期 ----------

def test_subagent_count_lifecycle():
    led = Ledger()
    assert led.subagent_active("cc", "s1") is False            # 未知会话不炸
    led.subagent_event("cc", "s1", "start")
    assert led.subagent_active("cc", "s1") is True
    led.subagent_event("cc", "s1", "stop")
    assert led.subagent_active("cc", "s1") is False
    led.subagent_event("cc", "s1", "stop")                     # 重复 stop（重启丢 start）→ 钳 0
    assert led.subagent_active("cc", "s1") is False


def test_subagent_nested_two_levels():
    """探针实测：嵌套两层各自 Start/Stop，同属主会话 → 纯计数即可，无需 parent 链。"""
    led = Ledger()
    led.subagent_event("cc", "s1", "start")                    # 外层
    led.subagent_event("cc", "s1", "start")                    # 内层
    assert led.subagent_active("cc", "s1") is True
    led.subagent_event("cc", "s1", "stop")                     # 内层先停
    assert led.subagent_active("cc", "s1") is True             # 外层仍在
    led.subagent_event("cc", "s1", "stop")
    assert led.subagent_active("cc", "s1") is False


def test_subagent_leak_guard():
    """Stop 丢失（CC 崩溃/强杀会话）→ 计数不得永久卡死摆渡：1h 无新事件视为 0。"""
    led = Ledger()
    led.subagent_event("cc", "s1", "start")
    led._subagents[("cc", "s1")] = (1, now_s() - 3601)         # 伪造最后事件在 1h 前
    assert led.subagent_active("cc", "s1") is False
    assert ("cc", "s1") not in led._subagents                  # 判定即清理


# ---------- 守望：subagents/ 路径排除 ----------

def test_watcher_skips_subagent_transcript_paths(tmp_path):
    """<session-id>/subagents/agent-*.jsonl 不登记为独立会话（真实目录结构，探针实测）。"""
    projects = tmp_path / "projects"
    write_session(projects, "main-1", str(tmp_path / "proj"))
    sub = projects / "C--proj" / "main-1" / "subagents" / "agent-a1234.jsonl"
    sub.parent.mkdir(parents=True, exist_ok=True)
    sub.write_text('{"type": "user", "message": {"role": "user", "content": "子任务"}}\n',
                   encoding="utf-8")

    led = Ledger()
    w = Watcher.__new__(Watcher)
    w.cfg, w.ledger, w.store = Config(), led, None
    w.started_at = 0
    w.cc_dir, w.cx_dirs = projects, [tmp_path / "no-codex"]
    w._poll_cc()

    ids = {st.session_id for st in led.all_sessions()}
    assert "main-1" in ids
    assert not any(i.startswith("agent-") for i in ids)        # 子代理转录不再被摆渡


# ---------- 守望：计数优先于 T31 文件兜底 ----------

def test_maybe_enqueue_defers_while_subagent_active(tmp_path):
    """子代理计数 > 0 → 推迟摆渡（内存判定，先于 T31 悬空检测的磁盘扫描）。"""
    cfg = Config()
    cfg.thresholds = ThresholdCfg(summarize_s=0.1, block_s=1.0, min_ctx_tokens=10)
    led = Ledger()
    enqueued: list[str] = []

    w = Watcher.__new__(Watcher)
    w.cfg, w.ledger, w.store = cfg, led, None
    w.enqueue = lambda st: enqueued.append(st.session_id) or True
    w.started_at = 0

    f = write_session(tmp_path / "projects", "sub-run-1", str(tmp_path / "proj"))
    st = led.touch("cc", "sub-run-1", str(f), mtime=now_s(), size=10,
                   cwd=str(tmp_path), daemon_started_at=0)
    st.last_write -= 5                                         # 造闲置达阈值

    led.subagent_event("cc", "sub-run-1", "start")
    for _ in range(3):
        w._maybe_enqueue(st)
    assert enqueued == []                                      # 运行中 → 每轮推迟

    led.subagent_event("cc", "sub-run-1", "stop")
    w._maybe_enqueue(st)
    assert enqueued == ["sub-run-1"]                           # 停了 → 正常摆渡


# ---------- HTTP /subagent 端点 ----------

def _post(h, body: dict, token: str | None = None) -> dict:
    req = urllib.request.Request(
        f"http://127.0.0.1:{h.port}/subagent",
        data=json.dumps(body, ensure_ascii=False).encode("utf-8"),
        headers={"Authorization": f"Bearer {token or h.token}",
                 "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=5) as r:
        return json.loads(r.read().decode("utf-8"))


def test_subagent_endpoint_roundtrip(h):
    r = _post(h, {"agent": "cc", "session_id": "ep-1", "event": "start"})
    assert r["active"] is True
    assert h.get("/stats")["subagents_active"] == 1            # 可观测性（T26 观察）
    assert h.get("/stats")["subagent_events_total"] >= 1       # 累计事件数（端到端验证用）
    r = _post(h, {"agent": "cc", "session_id": "ep-1", "event": "stop"})
    assert r["active"] is False
    assert h.get("/stats")["subagents_active"] == 0
    assert h.get("/stats")["subagent_events_total"] >= 2


def test_subagent_endpoint_auth_and_validation(h):
    with pytest.raises(urllib.error.HTTPError) as ei:
        _post(h, {"agent": "cc", "session_id": "ep-2", "event": "start"},
              token="wrong-token")
    assert ei.value.code == 401
    with pytest.raises(urllib.error.HTTPError) as ei:
        _post(h, {"agent": "cc", "session_id": "ep-2", "event": "boom"})
    assert ei.value.code == 400


# ---------- 守望：多 codex 会话目录（Orca CODEX_HOME 重定向，2026-09-17 实测） ----------

def test_codex_watch_dirs_auto_detects_orca_runtime(tmp_path):
    """Orca 运行时目录存在时自动追加（经 Orca 启动的 codex rollout 写在那里）。"""
    from ferryman.config import WatchCfg
    from ferryman.daemon import codex_watch_dirs
    cfg = WatchCfg(codex_sessions_dir=str(tmp_path / "main"))

    assert codex_watch_dirs(cfg, home=tmp_path) == [tmp_path / "main"]  # 无 orca → 只有一个

    orca = tmp_path / "AppData" / "Roaming" / "orca" / "codex-runtime-home" / "home" / "sessions"
    orca.mkdir(parents=True)
    dirs = codex_watch_dirs(cfg, home=tmp_path)
    assert dirs == [tmp_path / "main", orca]

    cfg2 = WatchCfg(codex_sessions_dir="", codex_extra_dirs=[str(tmp_path / "x")])
    assert codex_watch_dirs(cfg2, home=tmp_path) == [tmp_path / ".codex" / "sessions",
                                                     tmp_path / "x", orca]


def test_watcher_polls_all_codex_dirs(tmp_path):
    """两个目录里的 rollout 都要登记（Orca 会话不再漏摆渡）。"""
    cfg = Config()
    cfg.watch = WatchCfg(codex_sessions_dir=str(tmp_path / "a"))
    led = Ledger()
    w = Watcher.__new__(Watcher)
    w.cfg, w.ledger, w.store = cfg, led, None
    w.started_at = 0
    w.cc_dir = tmp_path / "no-cc"
    w.cx_dirs = [tmp_path / "a", tmp_path / "orca-home"]
    for d, sid in ((w.cx_dirs[0], "aaa111"), (w.cx_dirs[1], "bbb222")):
        f = d / "2026" / "09" / "17" / f"rollout-2026-09-17T10-00-00-{sid}.jsonl"
        f.parent.mkdir(parents=True, exist_ok=True)
        f.write_text('{"type":"session_meta","payload":{"cwd":"C:/x"}}\n', encoding="utf-8")
    w._poll_codex()
    ids = {st.session_id for st in led.all_sessions() if st.agent == "codex"}
    assert ids == {"aaa111", "bbb222"}


def test_watcher_dedupes_same_sid_across_codex_dirs(tmp_path):
    """~/.codex/sessions 与 Orca runtime 目录互为副本（2026-09-17 实测同 uuid 两份）
    → 同 sid 只登记一次，路径取主目录（在前）。"""
    led = Ledger()
    w = Watcher.__new__(Watcher)
    w.cfg, w.ledger, w.store = Config(), led, None
    w.started_at = 0
    w.cc_dir = tmp_path / "no-cc"
    a, b = tmp_path / "main", tmp_path / "orca"
    name = "rollout-2026-09-17T10-00-00-dup111.jsonl"
    for d in (a, b):
        f = d / "2026" / "09" / "17" / name
        f.parent.mkdir(parents=True, exist_ok=True)
        f.write_text("{}\n", encoding="utf-8")
    w.cx_dirs = [a, b]
    w._poll_codex()
    st = [s for s in led.all_sessions() if s.agent == "codex"]
    assert len(st) == 1 and st[0].session_id == "dup111"
    assert st[0].transcript_path.startswith(str(a))     # 路径稳定取主目录
