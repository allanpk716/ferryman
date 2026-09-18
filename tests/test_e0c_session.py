"""E0C · 会话聚合器 + 族系 + 快照一致性(e0c.py aggregate_session / lineage_rows)。

合成会话树 fixture 单测,覆盖票 03 验收标准:
- 会话总账 = 主文件四列 + Σ 子代理 self(含嵌套孙子),不双计;
- subtree 按 toolUseId 父链聚合;缺 meta 的未知桶不参与 subtree;
- 交叉校验两口径:direct(主转录 Agent/Task 调用 vs depth=1 meta vs depth=1 转录)、
  total(各父转录直接子调用合计 vs total spawn 事件数);差值进长尾;
- 快照一致性:每文件(扫描时刻,字节,sha256);扫描窗口起止;
  末尾重枚举,新增文件标"扫描窗口外"进长尾、不进账目;
- 在跑会话(主 mtime<5min)整场标在跑;
- 族系合计行:复用台账"同 transcript_path 换 session_id"规则,链上会话 id 列表,
  恒标"识别未验证";在跑会话不进族系合计。
"""

import hashlib
import json
import os
import time
from pathlib import Path

import ferryman.e0c as e0c
from ferryman.e0c import (RUNNING_WINDOW_SECONDS, AgentLedger, SessionAccount,
                          UsageAccount, UsageColumns, aggregate_session,
                          lineage_rows)

# ---- 合成行构造(与 test_e0c_agents 同风格) -------------------------------------

T0 = "2026-09-18T10:00:00.000Z"
T1 = "2026-09-18T10:00:01.000Z"
T2 = "2026-09-18T10:00:02.000Z"

_FOUR = ("input_tokens", "output_tokens", "cache_creation", "cache_read")

SID = "11111111-2222-3333-4444-555555555555"


def _u(i=100, o=20, cw=300, cr=4000):
    return {"input_tokens": i, "output_tokens": o,
            "cache_creation_input_tokens": cw, "cache_read_input_tokens": cr}


def _four(x):
    return tuple(getattr(x, k) for k in _FOUR)


def _arow(mid, *, ts=T1, stop="end_turn", usage=None, blocks=None):
    msg = {"role": "assistant", "id": mid, "model": "GLM-5.3",
           "usage": _u() if usage is None else usage,
           "content": blocks if blocks is not None else [{"type": "text", "text": "x"}]}
    if stop is not None:
        msg["stop_reason"] = stop
    return {"type": "assistant", "uuid": f"uuid-{mid}", "timestamp": ts, "message": msg}


def _urow(ts=T0):
    return {"type": "user", "timestamp": ts,
            "message": {"role": "user", "content": "做点活"}}


def _block(tid, name="Task"):
    return {"type": "tool_use", "id": tid, "name": name, "input": {"prompt": "x"}}


def _wjsonl(p: Path, rows) -> Path:
    p.write_text("\n".join(json.dumps(r, ensure_ascii=False) for r in rows) + "\n",
                 encoding="utf-8")
    return p


def _wmeta(sub: Path, aid: str, **kw) -> None:
    meta = {"agentType": "general", "description": "干活",
            "toolUseId": f"toolu_{aid}", "spawnDepth": 1, "model": "opus"}
    meta.update(kw)
    (sub / f"agent-{aid}.meta.json").write_text(
        json.dumps(meta, ensure_ascii=False), encoding="utf-8")


# 基准会话树:主文件 2 次 Agent/Task 调用(toolu_a/toolu_c);
# a(depth1,转录内再 spawn toolu_b)→ b(depth2 嵌套孙子);c(depth1);u(缺 meta 未知桶)。
# 两口径天然一致:direct 2/2/2,total 父调用 3(main2+a1) vs total_spawns 3。
def _mk_tree(tmp_path: Path, *, sid=SID) -> tuple[Path, Path]:
    proj = tmp_path / "proj"
    sub = proj / sid / "subagents"
    sub.mkdir(parents=True)
    mainp = _wjsonl(proj / f"{sid}.jsonl", [
        _urow(T0),
        _arow("mm1", ts=T1),
        _arow("mm2", ts=T2, stop="tool_use",
              blocks=[_block("toolu_a", "Agent"), _block("toolu_c", "Task")]),
    ])
    _wjsonl(sub / "agent-a.jsonl", [
        _arow("ma1"),
        _arow("ma2", stop="tool_use", blocks=[_block("toolu_b", "Task")]),
    ])
    _wmeta(sub, "a", spawnDepth=1)
    _wjsonl(sub / "agent-b.jsonl", [_arow("mb1")])
    _wmeta(sub, "b", spawnDepth=2)
    _wjsonl(sub / "agent-c.jsonl", [_arow("mc1")])
    _wmeta(sub, "c", spawnDepth=1)
    _wjsonl(sub / "agent-u.jsonl", [_arow("mu1")])  # 缺 meta
    return proj, mainp


OLD = 1_700_000_000.0
FAR_NOW = OLD + RUNNING_WINDOW_SECONDS + 60


def _aged(tree: tuple[Path, Path]) -> None:
    """把全部产物 mtime 拨老,脱离在跑窗口。"""
    proj, _ = tree
    for p in sorted(proj.rglob("*")):
        if p.is_file():
            os.utime(p, (OLD, OLD))


# ---- 会话总账:主 + Σ子,不双计 ----------------------------------------------------


def test_total_is_main_plus_all_agent_selfs_no_double_count(tmp_path):
    tree = _mk_tree(tmp_path)
    _aged(tree)
    acc = aggregate_session(tree[1], now=FAR_NOW)
    # 主 2 响应 + a(2)+b(1)+c(1)+u(1) = 7 份默认 usage;嵌套 b 只经自己转录计一次
    assert _four(acc.total) == (700, 140, 2100, 28000)
    assert isinstance(acc.total, UsageColumns)
    assert isinstance(acc.main, UsageAccount)
    assert _four(acc.main) == (200, 40, 600, 8000)
    assert acc.session_id == SID
    assert acc.running is False
    assert acc.agents.total_spawns == 3 and acc.agents.direct_spawns == 2


def test_subtree_rolls_up_parent_chain(tmp_path):
    tree = _mk_tree(tmp_path)
    _aged(tree)
    acc = aggregate_session(tree[1], now=FAR_NOW)
    assert set(acc.subtree) == {"a", "b", "c"}      # 缺 meta 的 u 不参与 subtree
    assert _four(acc.subtree["a"]) == (300, 60, 900, 12000)  # a(2 响应) + 嵌套 b(1)
    assert _four(acc.subtree["b"]) == (100, 20, 300, 4000)   # b 自身
    assert _four(acc.subtree["c"]) == (100, 20, 300, 4000)


# ---- 交叉校验 --------------------------------------------------------------------


def test_cross_checks_consistent_no_longtail(tmp_path):
    tree = _mk_tree(tmp_path)
    _aged(tree)
    acc = aggregate_session(tree[1], now=FAR_NOW)
    direct = next(s for s in acc.cross_checks if "direct" in s)
    total = next(s for s in acc.cross_checks if "total" in s)
    assert "2" in direct and "一致" in direct      # 主调用 2 / meta 2 / 转录 2
    assert "3" in total and "一致" in total        # 父调用 3 vs total spawn 3
    assert not any("不一致" in m for m in acc.longtail)


def test_direct_mismatch_delta_goes_to_longtail(tmp_path):
    proj, mainp = _mk_tree(tmp_path)
    # 主转录多发一次 Agent 调用但无对应子代理 → 3/2/2
    rows = mainp.read_text(encoding="utf-8").rstrip("\n").split("\n")
    rows.append(json.dumps(_arow("mm3", ts=T2, stop="tool_use",
                                 blocks=[_block("toolu_x", "Agent")]),
                           ensure_ascii=False))
    mainp.write_text("\n".join(rows) + "\n", encoding="utf-8")
    _aged((proj, mainp))
    acc = aggregate_session(mainp, now=FAR_NOW)
    assert any("direct" in s and "不一致" in s for s in acc.cross_checks)
    assert any("差值" in m and "direct" in m for m in acc.longtail)


def test_total_mismatch_delta_goes_to_longtail(tmp_path):
    proj, mainp = _mk_tree(tmp_path)
    # b 的转录多发一次 Task 调用但无对应 meta → 父调用 4 vs total spawn 3
    bp = proj / SID / "subagents" / "agent-b.jsonl"
    rows = bp.read_text(encoding="utf-8").rstrip("\n").split("\n")
    rows.append(json.dumps(_arow("mb2", ts=T2, stop="tool_use",
                                 blocks=[_block("toolu_y", "Task")]),
                           ensure_ascii=False))
    bp.write_text("\n".join(rows) + "\n", encoding="utf-8")
    _aged((proj, mainp))
    acc = aggregate_session(mainp, now=FAR_NOW)
    assert any("total" in s and "不一致" in s for s in acc.cross_checks)
    assert any("差值" in m and "total" in m for m in acc.longtail)


def test_meta_only_agent_zero_tokens_and_direct_mismatch(tmp_path):
    tree = _mk_tree(tmp_path)
    proj, mainp = tree
    (proj / SID / "subagents" / "agent-c.jsonl").unlink()   # c 变成有 meta 无转录
    _aged(tree)
    acc = aggregate_session(mainp, now=FAR_NOW)
    assert _four(acc.total) == (600, 120, 1800, 24000)      # c 记 0,仍占总账行
    assert _four(acc.subtree["c"]) == (0, 0, 0, 0)
    assert any("direct" in s and "不一致" in s for s in acc.cross_checks)  # 2/2/1
    assert any("差值" in m and "direct" in m for m in acc.longtail)


# ---- 快照一致性 ------------------------------------------------------------------


def test_file_digests_snapshot(tmp_path):
    tree = _mk_tree(tmp_path)
    proj, mainp = tree
    _aged(tree)
    acc = aggregate_session(mainp, now=OLD)
    assert acc.scan_window == (OLD, OLD)
    raw = (proj / f"{SID}.jsonl").read_bytes()
    key_main = f"{SID}.jsonl"
    assert key_main in acc.file_digests
    ts, size, sha = acc.file_digests[key_main]
    assert ts == OLD and size == len(raw)
    assert sha == hashlib.sha256(raw).hexdigest()
    # 子代理转录与 meta 都在快照里,posix 相对路径,相对主转录所在目录
    for rel in (f"{SID}/subagents/agent-a.jsonl", f"{SID}/subagents/agent-a.meta.json",
                f"{SID}/subagents/agent-u.jsonl"):
        assert rel in acc.file_digests, rel
        p = proj / rel
        raw = p.read_bytes()
        assert acc.file_digests[rel][1] == len(raw)
        assert acc.file_digests[rel][2] == hashlib.sha256(raw).hexdigest()


def test_rescan_marks_new_file_out_of_window(tmp_path, monkeypatch):
    tree = _mk_tree(tmp_path)
    _aged(tree)
    orig = e0c._list_files_recursive
    calls = {"n": 0}

    def wrapped(root):
        calls["n"] += 1
        if calls["n"] == 2:  # 末尾重枚举前,会话目录里冒出新文件
            (Path(root) / "subagents" / "agent-late.jsonl").write_text(
                json.dumps(_arow("ml1")), encoding="utf-8")
        return orig(root)

    monkeypatch.setattr(e0c, "_list_files_recursive", wrapped)
    acc = aggregate_session(tree[1], now=FAR_NOW)
    assert calls["n"] == 2
    assert any("扫描窗口外" in m and "agent-late.jsonl" in m for m in acc.longtail)
    # 窗口外文件不进账目、不进快照
    assert all(r.agentId != "late" for r in acc.agents.agents)
    assert acc.agents.transcript_files == 4
    assert f"{SID}/subagents/agent-late.jsonl" not in acc.file_digests


# ---- 在跑会话 --------------------------------------------------------------------


def test_running_session_flag_by_main_mtime(tmp_path):
    tree = _mk_tree(tmp_path)
    _aged(tree)
    assert aggregate_session(tree[1], now=FAR_NOW).running is False
    assert aggregate_session(tree[1], now=OLD + 10).running is True


def test_running_flag_uses_fresh_subagent_files(tmp_path):
    """子代理文件新鲜、主文件很旧 → 整场在跑:子代理运行期间主文件无中间写入
    (Agent 的 tool_result 等子代理结束才落盘),只看主 mtime 会漏长跑子代理。"""
    tree = _mk_tree(tmp_path)
    proj, mainp = tree
    _aged(tree)
    fresh = OLD + 100   # 距 now(OLD+370)270s < 300s 在窗口内;距主 mtime 100s 本身在窗口外
    sub = proj / SID / "subagents"
    os.utime(sub / "agent-a.jsonl", (fresh, fresh))
    os.utime(sub / "agent-a.meta.json", (fresh, fresh))
    acc = aggregate_session(mainp, now=FAR_NOW)   # now 远超主 mtime 的在跑窗口
    assert acc.running is True


def test_missing_main_file_is_defensive(tmp_path):
    proj, mainp = _mk_tree(tmp_path)
    mainp.unlink()
    _aged((proj, mainp))
    acc = aggregate_session(mainp, now=FAR_NOW)
    assert _four(acc.main) == (0, 0, 0, 0)
    assert acc.running is False
    assert _four(acc.total) == (500, 100, 1500, 20000)   # 主空账 + a(2)+b+c+u 各 1
    assert any("主转录" in m for m in acc.longtail)


def test_str_path_and_scan_window(tmp_path):
    tree = _mk_tree(tmp_path)
    _aged(tree)
    acc = aggregate_session(str(tree[1]), now=OLD)
    assert acc.scan_window == (OLD, OLD)
    assert os.path.isabs(acc.path) and acc.path.replace("\\", "/").endswith(f"{SID}.jsonl")


# ---- 族系合计行 --------------------------------------------------------------------


def _mk_acc(sid, path, *, total=(100, 20, 300, 4000), running=False,
            window=(0.0, 0.0)):
    return SessionAccount(session_id=sid, path=path, main=UsageAccount(),
                         agents=AgentLedger([], 0, 0, 0, 0, []),
                         total=UsageColumns(*total), subtree={}, cross_checks=[],
                         file_digests={}, scan_window=window, running=running,
                         longtail=[])


def test_lineage_singleton_per_session_unverified(tmp_path):
    pdir = tmp_path / "projects"
    pdir.mkdir()
    accs = [_mk_acc("bbb", str(pdir / "bbb.jsonl"), total=(7, 0, 0, 0)),
            _mk_acc("aaa", str(pdir / "aaa.jsonl"))]
    rows = lineage_rows(accs, pdir)
    assert [r.session_ids for r in rows] == [["aaa"], ["bbb"]]   # 稳定排序
    assert rows[1].total == UsageColumns(7, 0, 0, 0)
    assert all(r.unverified is True for r in rows)
    assert all("识别未验证" in r.basis for r in rows)


def test_lineage_same_path_forms_chain_combined_total(tmp_path):
    pdir = tmp_path / "projects"
    pdir.mkdir()
    shared = str(pdir / "x.jsonl")   # 台账规则:同一路径出现新 session_id → 同链
    accs = [_mk_acc("s2", shared, total=(10, 0, 0, 0), window=(2.0, 2.0)),
            _mk_acc("s1", shared, total=(1, 2, 0, 0), window=(1.0, 1.0)),
            _mk_acc("other", str(pdir / "other.jsonl"))]
    rows = lineage_rows(accs, pdir)
    assert len(rows) == 2
    chain = next(r for r in rows if len(r.session_ids) == 2)
    assert chain.session_ids == ["s1", "s2"]              # 链内按扫描窗口排序
    assert chain.total == UsageColumns(11, 2, 0, 0)
    assert chain.unverified is True


def test_lineage_excludes_running_sessions(tmp_path):
    pdir = tmp_path / "projects"
    pdir.mkdir()
    accs = [_mk_acc("hot", str(pdir / "hot.jsonl"), running=True),
            _mk_acc("cold", str(pdir / "cold.jsonl"))]
    rows = lineage_rows(accs, pdir)
    assert [r.session_ids for r in rows] == [["cold"]]


def test_lineage_empty_input(tmp_path):
    assert lineage_rows([], tmp_path) == []
