"""E0C · 子代理账目装配器(e0c.py assemble_agents)。

合成会话目录 fixture 单测,覆盖票 02 验收标准:
- 正常配对样本:四列/响应数/行级模型分布正确,meta 字段透传;
- 缺 meta → 未知桶(agentType/depth 未知),不进 direct/total 分桶;
  转录内容可恢复 toolUseId 时父子关系恢复;
- 有 meta 无转录 → spawn 计数 +1、token 记 0、标"无转录";
- direct/total 双口径计数(含 depth=2 样本、未知深度排除);
- 终态四分类:完成(end_turn)/中断/空文件/在跑(mtime 窗口,now 参数注入);
- 防御:无 subagents 目录、坏 meta、意外文件。
"""

import json
import os
import time
from pathlib import Path

from ferryman.e0c import RUNNING_WINDOW_SECONDS, assemble_agents

# ---- 合成行构造 ---------------------------------------------------------------

T0 = "2026-09-18T10:00:00.000Z"
T1 = "2026-09-18T10:00:01.000Z"
T2 = "2026-09-18T10:00:02.000Z"
T3 = "2026-09-18T10:00:03.000Z"

_FOUR = ("input_tokens", "output_tokens", "cache_creation", "cache_read")


def _u(i=100, o=20, cw=300, cr=4000):
    return {"input_tokens": i, "output_tokens": o,
            "cache_creation_input_tokens": cw, "cache_read_input_tokens": cr}


_Z = _u(0, 0, 0, 0)


def _four(acc):
    return tuple(getattr(acc, k) for k in _FOUR)


def _arow(mid, *, ts=T1, model="GLM-5.3", stop="end_turn", usage=None,
          content="text", content_blocks=None):
    """一条 assistant 行;content_blocks 非空时替换 content(塞 tool_use 块用)。"""
    msg = {"role": "assistant", "id": mid, "model": model,
           "usage": _u() if usage is None else usage}
    if stop is not None:
        msg["stop_reason"] = stop
    msg["content"] = (content_blocks if content_blocks is not None
                      else [{"type": content, "text": "x"}])
    return {"type": "assistant", "uuid": f"uuid-{mid}", "timestamp": ts, "message": msg}


def _urow(ts=T0, **extra):
    d = {"type": "user", "timestamp": ts,
         "message": {"role": "user", "content": "做点活"}}
    d.update(extra)
    return d


def _tool_use_block(tid, name="Task"):
    return {"type": "tool_use", "id": tid, "name": name, "input": {"prompt": "x"}}


_OMIT = object()  # sentinel:整个字段不写进 meta


def _wmeta(sub: Path, aid: str, **kw) -> None:
    meta = {"agentType": "general", "description": "干活",
            "toolUseId": f"toolu_{aid}", "spawnDepth": 1, "model": "opus",
            "requestShape": "task"}
    for k, v in kw.items():
        if v is _OMIT:
            meta.pop(k, None)
        else:
            meta[k] = v
    (sub / f"agent-{aid}.meta.json").write_text(
        json.dumps(meta, ensure_ascii=False), encoding="utf-8")


def _wtr(sub: Path, aid: str, rows) -> Path:
    p = sub / f"agent-{aid}.jsonl"
    p.write_text("\n".join(json.dumps(r, ensure_ascii=False) for r in rows) + "\n",
                 encoding="utf-8")
    return p


def _mk_session(tmp_path: Path, name="sess") -> Path:
    s = tmp_path / name
    (s / "subagents").mkdir(parents=True)
    return s


def _row(ledger, aid):
    return next(r for r in ledger.agents if r.agentId == aid)


# ---- 正常配对 ------------------------------------------------------------------


def test_paired_agent_four_columns_and_meta_fields(tmp_path):
    """配对样本:四列经 01 聚合器去重;meta 字段透传;时间戳取首末行(含 user 行)。"""
    s = _mk_session(tmp_path)
    _wtr(s / "subagents", "a1", [
        _urow(T0),
        _arow("m1", ts=T1, stop=None, usage=_Z),          # 占位前置行
        _arow("m1", ts=T2, stop="end_turn"),              # 终行(同 id 只计一次)
        _arow("m2", ts=T3, model="claude-sonnet-5", usage=_u(10, 5, 0, 50)),
    ])
    _wmeta(s / "subagents", "a1", agentType="general", description="探查",
           spawnDepth=1, toolUseId="toolu_a1", model="opus")

    led = assemble_agents(s, now=time.time() + RUNNING_WINDOW_SECONDS * 10)
    r = _row(led, "a1")
    assert _four(r.self_acc) == (110, 25, 300, 4050)
    assert r.self_acc.responses == 2
    assert set(r.self_acc.by_model) == {"GLM-5.3", "claude-sonnet-5"}
    assert r.agentType == "general" and r.description == "探查"
    assert r.spawnDepth == 1 and r.toolUseId == "toolu_a1"
    assert r.requested_model == "opus"  # meta 请求别名,与行级分布无关
    assert r.first_ts == T0 and r.last_ts == T3
    assert r.state == "done" and r.parent_agentId is None
    assert r.no_transcript is False
    assert led.transcript_files == 1 and led.spawn_events == 1
    assert led.direct_spawns == 1 and led.total_spawns == 1
    assert led.longtail == []


# ---- 缺 meta:未知桶 + 父子恢复 ---------------------------------------------------


def test_missing_meta_unknown_bucket_with_parent_recovery(tmp_path):
    """缺 meta → 未知桶(agentType/depth 未知,不进 direct/total);
    转录内容带 toolUseId 时经父转录 tool_use 块恢复父子,token 照入。"""
    s = _mk_session(tmp_path)
    sub = s / "subagents"
    # 父 P(depth 1):转录里发出 tool_use 块 toolu_kid(即 spawn 了 C)
    _wtr(sub, "p", [
        _arow("mp1", ts=T1, stop="end_turn"),
        _arow("mp2", ts=T2, stop="tool_use", content_blocks=[_tool_use_block("toolu_kid")]),
    ])
    _wmeta(sub, "p", spawnDepth=1, toolUseId="toolu_p")
    # 子 C:无 meta,转录首行带 toolUseId 引用 → 可恢复
    _wtr(sub, "c", [
        _urow(T0, toolUseId="toolu_kid"),
        _arow("mc1", ts=T1, stop="end_turn"),
    ])

    led = assemble_agents(s, now=time.time() + RUNNING_WINDOW_SECONDS * 10)
    c = _row(led, "c")
    assert c.agentType is None and c.spawnDepth is None
    assert c.toolUseId == "toolu_kid"          # 从转录内容恢复
    assert c.parent_agentId == "p"             # 父子关系恢复
    assert _four(c.self_acc) == (100, 20, 300, 4000)  # token 照入总账
    assert c.state == "done"
    assert _row(led, "p").parent_agentId is None  # 父在主转录,子代理层看不到
    # 未知深度不进 direct/total 分桶,但占 spawn/转录计数
    assert led.transcript_files == 2 and led.spawn_events == 2
    assert led.direct_spawns == 1 and led.total_spawns == 1
    assert len(led.longtail) == 1 and "c" in led.longtail[0]


def test_missing_meta_without_sniff_stays_orphan(tmp_path):
    """缺 meta 且转录内容无可恢复 toolUseId → 孤儿:未知桶、无父,计数照占。"""
    s = _mk_session(tmp_path)
    _wtr(s / "subagents", "orphan", [_arow("m1", stop="end_turn")])

    led = assemble_agents(s)
    r = _row(led, "orphan")
    assert r.agentType is None and r.spawnDepth is None
    assert r.toolUseId is None and r.parent_agentId is None
    assert led.spawn_events == 1 and led.transcript_files == 1
    assert led.direct_spawns == 0 and led.total_spawns == 0
    assert len(led.longtail) == 1 and "orphan" in led.longtail[0]


# ---- 有 meta 无转录 --------------------------------------------------------------


def test_meta_without_transcript(tmp_path):
    """有 meta 无转录 → 计数+1、token 记 0、标"无转录",state 无从推断。"""
    s = _mk_session(tmp_path)
    _wmeta(s / "subagents", "ghost", spawnDepth=1, toolUseId="toolu_g")

    led = assemble_agents(s)
    r = _row(led, "ghost")
    assert r.no_transcript is True
    assert _four(r.self_acc) == (0, 0, 0, 0) and r.self_acc.responses == 0
    assert r.first_ts is None and r.last_ts is None
    assert r.state is None
    assert r.agentType == "general" and r.spawnDepth == 1
    # 计入 spawn 事件,不计入转录文件数;depth 已知 → 参与 direct/total
    assert led.spawn_events == 1 and led.transcript_files == 0
    assert led.direct_spawns == 1 and led.total_spawns == 1
    assert len(led.longtail) == 1 and "无转录" in led.longtail[0]


# ---- direct/total 双口径 ----------------------------------------------------------


def test_direct_total_dual_counting_with_depth2(tmp_path):
    """双口径:direct=depth1 的 spawn 事件;total=全已知深度;
    depth=2 父子经 toolUseId 恢复;未知深度与 meta 均按规则进出。"""
    s = _mk_session(tmp_path)
    sub = s / "subagents"
    # A(depth1)spawn B;B(depth2)spawn D(但 D 无转录)
    _wtr(sub, "a", [
        _arow("ma1", ts=T1, stop="end_turn"),
        _arow("ma2", ts=T2, stop="tool_use",
              content_blocks=[_tool_use_block("toolu_b")]),
    ])
    _wmeta(sub, "a", spawnDepth=1, toolUseId="toolu_a")
    _wtr(sub, "b", [
        _arow("mb1", ts=T1, stop="end_turn"),
        _arow("mb2", ts=T2, stop="tool_use",
              content_blocks=[_tool_use_block("toolu_d")]),
    ])
    _wmeta(sub, "b", spawnDepth=2, toolUseId="toolu_b")
    # C(depth1):spawn 块在主转录里(本装配器不扫主转录)→ 无父
    _wtr(sub, "c", [_arow("mc1", stop="end_turn")])
    _wmeta(sub, "c", spawnDepth=1, toolUseId="toolu_c")
    # D(depth2)只有 meta
    _wmeta(sub, "d", spawnDepth=2, toolUseId="toolu_d")
    # E:转录无 meta → 未知桶
    _wtr(sub, "e", [_arow("me1", stop="end_turn")])

    led = assemble_agents(s)
    assert [r.agentId for r in led.agents] == ["a", "b", "c", "d", "e"]  # 稳定排序
    assert _row(led, "b").parent_agentId == "a"
    assert _row(led, "d").parent_agentId == "b"  # meta 无转录也能恢复父
    assert _row(led, "a").parent_agentId is None
    assert _row(led, "c").parent_agentId is None
    assert _row(led, "d").no_transcript is True
    # 转录 4 个(a/b/c/e)+ 无转录 meta 1 个(d)= 5 次 spawn
    assert led.transcript_files == 4 and led.spawn_events == 5
    assert led.direct_spawns == 2          # a, c
    assert led.total_spawns == 4           # a, b, c, d(e 未知深度不进桶)


# ---- 终态四分类 ------------------------------------------------------------------


def test_state_interrupted_variants(tmp_path):
    """中断:末响应 stop≠end_turn / 末尾拖 user 行 / 末行截断,均判中断。"""
    s = _mk_session(tmp_path)
    sub = s / "subagents"
    _wtr(sub, "i1", [_arow("m1", stop="tool_use")])               # 末响应是工具调用
    _wtr(sub, "i2", [_arow("m1", stop="end_turn"), _urow(T2)])    # end_turn 后又来 user 行
    _wtr(sub, "i3", [json.dumps(_arow("m1"))[:25]])               # 半行截断
    for aid in ("i1", "i2", "i3"):
        _wmeta(sub, aid)
    led = assemble_agents(s, now=time.time() + RUNNING_WINDOW_SECONDS * 10)
    assert [_row(led, a).state for a in ("i1", "i2", "i3")] == [
        "interrupted", "interrupted", "interrupted"]


def test_state_empty_and_running_window(tmp_path):
    """空文件 → 空;mtime 距扫描 <5 分钟 → 在跑(覆盖终值,now 参数注入)。"""
    s = _mk_session(tmp_path)
    sub = s / "subagents"
    empty = sub / "agent-e.jsonl"
    empty.write_text("", encoding="utf-8")
    done = _wtr(sub, "d", [_arow("m1", stop="end_turn")])
    blankish = sub / "agent-b.jsonl"
    blankish.write_text("\n \n", encoding="utf-8")  # 只有空白行,同空文件
    for p in (empty, done, blankish):
        _wmeta(sub, p.stem[len("agent-"):])
    old = 1_700_000_000.0
    for p in (empty, done, blankish):
        os.utime(p, (old, old))

    far = assemble_agents(s, now=old + RUNNING_WINDOW_SECONDS + 60)
    assert _row(far, "e").state == "empty"
    assert _row(far, "b").state == "empty"
    assert _row(far, "d").state == "done"

    fresh = assemble_agents(s, now=old + 10)
    assert _row(fresh, "d").state == "running"   # 在跑覆盖终值(非终值)
    assert _row(fresh, "e").state == "running"   # 空文件也一样被在跑覆盖
    assert _row(fresh, "b").state == "running"


def test_state_done_requires_end_turn_at_tail(tmp_path):
    """中途 end_turn 但末行是 assistant 新响应(无 stop)→ 中断,不误判完成。"""
    s = _mk_session(tmp_path)
    sub = s / "subagents"
    _wtr(sub, "x", [
        _arow("m1", ts=T1, stop="end_turn"),
        _arow("m2", ts=T2, stop=None, usage=_Z),  # 流式前置行写了、终态没落地
    ])
    _wmeta(sub, "x")
    led = assemble_agents(s, now=time.time() + RUNNING_WINDOW_SECONDS * 10)
    assert _row(led, "x").state == "interrupted"


def test_running_with_default_now(tmp_path):
    """now 缺省走系统时钟:刚写完的转录 → 在跑。"""
    s = _mk_session(tmp_path)
    p = _wtr(s / "subagents", "hot", [_arow("m1", stop="end_turn")])
    _wmeta(s / "subagents", "hot")
    os.utime(p, (time.time(), time.time()))
    led = assemble_agents(s)
    assert _row(led, "hot").state == "running"


# ---- 防御 ------------------------------------------------------------------------


def test_missing_subagents_dir(tmp_path):
    """会话目录没有 subagents/ → 空账本,不抛异常;str 路径也收。"""
    s = tmp_path / "bare"
    s.mkdir()
    led = assemble_agents(str(s))
    assert led.agents == [] and led.spawn_events == 0
    assert led.transcript_files == 0
    assert led.direct_spawns == 0 and led.total_spawns == 0
    assert led.longtail == []


def test_malformed_meta_and_unexpected_files(tmp_path):
    """坏 meta → 按无 meta 处理(不重复记未知桶);意外文件 → 长尾并忽略。"""
    s = _mk_session(tmp_path)
    sub = s / "subagents"
    _wtr(sub, "x", [_arow("m1", stop="end_turn")])
    (sub / "agent-x.meta.json").write_text("not json {", encoding="utf-8")
    (sub / "notes.txt").write_text("杂文件", encoding="utf-8")
    _wmeta(sub, "y")  # 有 meta 无转录

    led = assemble_agents(s, now=time.time() + RUNNING_WINDOW_SECONDS * 10)
    assert _row(led, "x").agentType is None  # 坏 meta 按无 meta → 未知桶
    assert _row(led, "y").no_transcript is True
    assert led.transcript_files == 1 and led.spawn_events == 2
    assert len(led.longtail) == 3
    assert any("meta 解析失败" in m and "x" in m for m in led.longtail)
    assert any("notes.txt" in m for m in led.longtail)
    assert any("无转录" in m and "y" in m for m in led.longtail)


def test_meta_missing_model_field(tmp_path):
    """个别 meta 缺 model → requested_model 为 None,不抛异常。"""
    s = _mk_session(tmp_path)
    _wtr(s / "subagents", "nm", [_arow("m1", stop="end_turn")])
    _wmeta(s / "subagents", "nm", model=_OMIT)
    led = assemble_agents(s, now=time.time() + RUNNING_WINDOW_SECONDS * 10)
    r = _row(led, "nm")
    assert r.requested_model is None
    assert r.state == "done"


def test_meta_with_garbage_fields(tmp_path):
    """meta 字段类型不对(深度为负/非数、字符串字段非字符串)→ 逐字段防御归 None。"""
    s = _mk_session(tmp_path)
    _wtr(s / "subagents", "g", [_arow("m1", stop="end_turn")])
    _wmeta(s / "subagents", "g", agentType=42, description=None,
           toolUseId="", spawnDepth=-3, model=7)
    led = assemble_agents(s, now=time.time() + RUNNING_WINDOW_SECONDS * 10)
    r = _row(led, "g")
    assert r.agentType is None and r.description is None
    assert r.toolUseId is None and r.spawnDepth is None
    assert r.requested_model is None


def test_transcript_path_is_directory(tmp_path):
    """同名目录冒充转录 → 非普通文件不计转录,记长尾,不抛异常。"""
    s = _mk_session(tmp_path)
    (s / "subagents" / "agent-dir.jsonl").mkdir()
    led = assemble_agents(s, now=time.time() + RUNNING_WINDOW_SECONDS * 10)
    assert all(r.agentId != "dir" for r in led.agents)
    assert led.transcript_files == 0 and led.spawn_events == 0
    assert any("agent-dir.jsonl" in m for m in led.longtail)
