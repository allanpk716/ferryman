"""T10-T15, T31, T48 · 集成测试：全链路 / skeleton 降级 / 墙钟强杀 / 队列背压 / 懒富化 / 鉴权健康 / 悬空 tool_use 推迟 / 异步等待全链路差异断言。

全离线：摆渡模型用 monkeypatch 假件替代（不连真实推理网关、不出网）。
"""

import json
import queue
import time
import urllib.error
import urllib.parse
from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest

import ferryman.daemon as daemon_mod
from ferryman.config import Config, ThresholdCfg
from ferryman.daemon import Watcher
from ferryman.ledger import now_s
from helpers import SUMMARIZE, Harness, free_port, now_iso, write_session


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
    def exploding(path, provider, agent="cc"):
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

    def sleeping(path, provider, agent="cc"):
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


def test_t14b_enrich_once_even_when_queue_full(tmp_path, monkeypatch):
    """终审 I1 防回潮：队满（enqueue 恒 False）时版本章仍生效——
    盖章行若误入分支内部（CC 永不盖章），队满场景每轮轮询整文件重跑 extract。"""
    import ferryman.extract as extract_mod
    calls = {"n": 0}
    orig = extract_mod.extract

    def counting(path):
        calls["n"] += 1
        return orig(path)

    monkeypatch.setattr(extract_mod, "extract", counting)

    cfg = Config()
    cfg.thresholds = ThresholdCfg(summarize_s=0.1, block_s=1.0, min_ctx_tokens=10)
    from ferryman.ledger import Ledger
    led = Ledger()
    proj = str(tmp_path / "proj")
    f = write_session(tmp_path / "projects", "enrich-2", proj)
    st = led.touch("cc", "enrich-2", str(f), mtime=now_s(), size=10, cwd=proj,
                   daemon_started_at=0)
    watcher = Watcher.__new__(Watcher)                   # 只测 _maybe_enqueue/_enrich
    watcher.cfg = cfg
    watcher.ledger = led
    watcher.store = None
    watcher.enqueue = lambda st: False                   # 队满：入队永远失败
    watcher.started_at = 0
    st.last_write -= 5                                   # 造闲置
    for _ in range(4):
        watcher._maybe_enqueue(st)
    assert calls["n"] == 1                               # 入队失败不回滚版本章 → 只读盘一次


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


# ---------- T39 provider 未配置：醒目警告（不再依赖内置默认） ----------

def test_t39_unconfigured_provider_warns(tmp_path, monkeypatch, capsys):
    from ferryman.store import Store
    monkeypatch.setattr(daemon_mod, "load_providers", lambda: {})
    cfg = Config()
    cfg.ferry_provider = ""                              # 未配置
    daemon_mod.FerryWorker(cfg, Store(tmp_path / "d"), queue.Queue())
    out = capsys.readouterr().out
    assert "未配置" in out and "config.toml" in out

    cfg2 = Config()
    cfg2.ferry_provider = "ghost"                        # 配了名字但无定义
    daemon_mod.FerryWorker(cfg2, Store(tmp_path / "d"), queue.Queue())
    assert "ghost" in capsys.readouterr().out


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


# ---------- T42 用量采集：守望接线 ----------

def test_usage_harvested_to_accounts(h):
    """T42：守望把会话用量落账为 usage 行（含标题/cwd），追加只采增量。"""
    import json as _json
    sid = "usid-0001"
    write_session(h.projects, sid, "C:/proj", usage_input=1234)
    assert h.wait_for(
        lambda: h.accounts.read(kind="usage", session=sid))
    rows = h.accounts.read(kind="usage", session=sid)
    assert len(rows) == 1
    r = rows[0]
    assert r["agent"] == "cc" and r["session_id"] == sid
    assert r["input_tokens"] == 1234 and r["cache_read_tokens"] == 100
    assert r["model"] == "" and r["title"] == "集成测试会话"
    assert r["lineage_id"] and r["offset"] > 0
    # 追加一条 assistant → 只 +1 行
    f = h.projects / "C--proj" / f"{sid}.jsonl"
    ts2 = now_iso()
    with f.open("a", encoding="utf-8") as fh:
        fh.write(_json.dumps(
            {"type": "assistant", "timestamp": ts2,
             "message": {"role": "assistant",
                         "content": [{"type": "text", "text": "又一步"}],
                         "usage": {"input_tokens": 5, "cache_read_input_tokens": 2000,
                                   "cache_creation_input_tokens": 0,
                                   "output_tokens": 7}}}) + "\n")
    assert h.wait_for(
        lambda: len(h.accounts.read(kind="usage", session=sid)) == 2)


def test_usage_harvest_disabled(tmp_path):
    """watch.harvest_usage=False → 不落 usage 行。"""
    from ferryman.accounts import Accounts
    from ferryman.config import ServerCfg, WatchCfg
    from ferryman.ledger import Ledger
    from ferryman.store import Store

    projects = tmp_path / "projects"
    cfg = Config()
    cfg.watch = WatchCfg(poll_interval_s=0.2,
                         cc_projects_dir=str(projects),
                         codex_sessions_dir=str(tmp_path / "no-codex"))
    cfg.watch.harvest_usage = False
    cfg.server = ServerCfg(port=free_port(), data_dir=str(tmp_path / "data"))
    accounts = Accounts(Path(cfg.data_dir))
    watcher = Watcher(cfg, Ledger(), Store(Path(cfg.data_dir)),
                      lambda st: True, now_s(), accounts)
    watcher.start()
    try:
        write_session(projects, "usid-0002", "C:/proj")
        time.sleep(1.0)
        assert accounts.read(kind="usage") == []
    finally:
        watcher.stop()


# ---------- T48 异步等待全链路（停车 / 差异断言 / ack 宽限 / 恢复闭窗） ----------

def _append_jsonl(f: Path, rec: dict) -> None:
    """追加一行会话记录（带换行——残行采集留待下轮的增量语义依赖它）。"""
    with f.open("a", encoding="utf-8") as fh:
        fh.write(json.dumps(rec, ensure_ascii=False) + "\n")


def test_t48_async_wait_flow_end_to_end(h):
    """T48 票04：async 派发 → stop → 窗口停车（不摆渡）→ ack 行（stop 后追加，
    90s 宽限内）不闭窗 → 恢复行（ts=now+120s 越过宽限）→ 守望采集闭窗
    main_resumed → 恢复后摆渡恢复入队（推迟不是丢弃）。

    差异断言防空过（评审#8/#9）：对照会话无停车窗、同样"写完自然闲置"
    → 必被摆渡（正样本，证明守望在跑且会摆）；再等停车会话的台账闲置
    实打实越过总结阈值（守望至少完整评估过一轮）才断言它不在摆渡名单。

    时序纪律（附录#2/#4/#8）：不手改 st.last_write、不把任何文件 mtime 拨到
    过去（ledger.touch 是高水位且 observed_active 要求 mtime≥守护启动时刻，
    手拨会破坏两腿）；ack 确认行在 stop 事件上报之后追加落盘；恢复行时间戳
    写未来（+120s）越过 ACK_GRACE_S。
    """
    sid = "e2e-async"
    ctrl = "e2e-ctrl"
    projects = h.projects / "C--proj"

    # 对照会话：正常收尾会话（既有 write_session 惯例），写完保持新 mtime，
    # 闲置自然超 summarize 线（1s）→ 应被摆渡（差异断言正样本）。
    write_session(h.projects, ctrl, "C:/proj")

    # 停车会话：尾部 async 派发标记（Task input run_in_background=true +
    # tool_result 文本以 "Async agent launched" 开头）。首条 usage 刻意压到
    # MIN_CTX(1000) 之下：装配期（写盘→登记→start/stop 握手）即使意外越过
    # 闲置线也只富化不入队（<min_ctx 标记 handed_off），测试不靠手速；停车
    # 期间若摆渡推迟失效，后续 ack 行的大 usage 会让富化越过 min_ctx 入队
    # ——负样本断言照样能揭穿，不会空过。
    f = projects / f"{sid}.jsonl"
    f.write_text("\n".join(json.dumps(x, ensure_ascii=False) for x in [
        {"type": "user", "timestamp": now_iso(), "cwd": "C:/proj",
         "sessionId": sid, "message": {"role": "user", "content": "派个后台活"}},
        {"type": "assistant", "timestamp": now_iso(),
         "message": {"role": "assistant", "content": [
             {"type": "tool_use", "id": "t1", "name": "Task",
              "input": {"prompt": "x", "run_in_background": True}}],
             "usage": {"input_tokens": 50, "cache_read_input_tokens": 10,
                        "cache_creation_input_tokens": 0, "output_tokens": 5}}},
        {"type": "user", "timestamp": now_iso(),
         "message": {"role": "user", "content": [
             {"type": "tool_result", "tool_use_id": "t1",
              "content": "Async agent launched successfully"}]}},
    ]) + "\n", encoding="utf-8")

    # 守望登记台账（stop 的停车判定 _park_or_close 需要台账里的 transcript_path）
    assert h.wait_for(lambda: h.ledger.get("cc", sid) is not None), "会话未被守望登记"
    assert h.sub({"event": "start", "agent": "cc", "session_id": sid})["ok"]
    assert h.sub({"event": "stop", "agent": "cc", "session_id": sid})["ok"]
    assert h.wait_for(lambda: h.daemon.window_wait("cc", sid)), "停车未成立"
    # 没有秒闭（本次修复的原始 bug：Stop 早到把真实等待记成 0.5min 废账）
    assert h.accounts.read(kind="window", session=sid) == []

    # ack 确认回合：stop 事件之后追加落盘（附录#8）。usage 行 ts=现在，
    # 落在 stop+90s 宽限内——守望采集会把它喂给 note_usage，但不得闭窗。
    _append_jsonl(f, {
        "type": "assistant", "timestamp": now_iso(),
        "message": {"role": "assistant", "content": [
            {"type": "text", "text": "已派出，等它跑完"}],
            "usage": {"input_tokens": 50, "cache_read_input_tokens": 150000,
                       "cache_creation_input_tokens": 0, "output_tokens": 5}}})
    # 采到 ack 行（usage 行 1→2）= note_usage 确实被喂过宽限内的 ts，
    # 此前提下窗口仍未闭才是"宽限生效"的非空证明。
    assert h.wait_for(lambda: len(h.accounts.read(kind="usage", session=sid)) >= 2), \
        "ack 行未被守望采集"
    assert h.accounts.read(kind="window", session=sid) == [], "ack 行误闭窗"
    assert h.daemon.window_wait("cc", sid) is True

    # 差异断言：对照被摆渡（正样本非空）之后，等停车会话的闲置也越过总结
    # 阈值（last_write 变旧 ≥ summarize+0.5s——纯自然流逝，不动任何状态），
    # 再断言它仍不在摆渡名单：同样闲置、唯独差一个停车窗 → 差异归因于 T48。
    assert h.wait_for(lambda: ctrl in h.enqueued_ok), "对照会话未被摆渡——守望链路失效"
    assert h.wait_for(lambda: now_s() - h.ledger.get("cc", sid).last_write
                      >= SUMMARIZE + 0.5), "停车会话闲置未过线，负样本前提不成立"
    assert sid not in h.enqueued_ok, "停车窗未挡住摆渡（T48 摆渡推迟失效）"

    # 主会话恢复调用：追加 usage 行，时间戳 now+120s（越过 ACK_GRACE_S=90s，
    # 附录#2；靠时间戳越线，不拨 mtime、不回拨 stop_ts）。
    resume_ts = (datetime.now(timezone.utc)
                 + timedelta(seconds=120)).isoformat().replace("+00:00", "Z")
    _append_jsonl(f, {
        "type": "assistant", "timestamp": resume_ts,
        "message": {"role": "assistant", "content": [
            {"type": "text", "text": "子代理完成，主会话续跑"}],
            "usage": {"input_tokens": 30, "cache_read_input_tokens": 250000,
                       "cache_creation_input_tokens": 0, "output_tokens": 5}}})
    assert h.wait_for(lambda: any(
        r["close_reason"] == "main_resumed"
        for r in h.accounts.read(kind="window", session=sid))), \
        "恢复行喂入后窗口未以 main_resumed 闭"
    assert h.daemon.window_wait("cc", sid) is False
    rows = h.accounts.read(kind="window", session=sid)
    assert len(rows) == 1 and rows[0]["dur_s"] > 0

    # 恢复后摆渡恢复入队：同一会话最终也被摆渡（推迟语义，不是永久丢弃）
    assert h.wait_for(lambda: sid in h.enqueued_ok), "窗闭后摆渡未恢复入队"
