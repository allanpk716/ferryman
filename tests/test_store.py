"""T06 · store：valid_handoff 判据、待续 prompt、消费标记、原子写。"""

import time
from datetime import datetime, timezone
from pathlib import Path

from ferryman.store import Store


def _iso(epoch: float) -> str:
    return datetime.fromtimestamp(epoch, tz=timezone.utc).isoformat().replace("+00:00", "Z")


def _mk(tmp_path: Path) -> Store:
    return Store(tmp_path / "data")


def _save(store: Store, *, sid="s1", agent="cc", cwd=None, covers_s=None, status="fresh"):
    return store.save_handoff(
        session_id=sid, agent=agent, cwd=cwd or str(Path.cwd()),
        title="t", covers_until_iso=_iso(covers_s if covers_s is not None else time.time()),
        status=status, handoff_md="x")


def test_valid_handoff_agent_cwd_keys(tmp_path):
    st = _mk(tmp_path)
    proj = tmp_path / "proj"
    _save(st, cwd=str(proj))
    assert st.valid_handoff("cc", str(proj), last_write=0) is not None
    assert st.valid_handoff("codex", str(proj), last_write=0) is None     # agent 双键
    assert st.valid_handoff("cc", str(tmp_path / "other"), last_write=0) is None


def test_valid_handoff_covers_tolerance_and_status(tmp_path):
    st = _mk(tmp_path)
    proj = str(tmp_path / "proj")
    covers = time.time() - 3600
    _save(st, cwd=proj, covers_s=covers, sid="s1")
    assert st.valid_handoff("cc", proj, last_write=covers + 30) is not None   # 60s 容差内
    assert st.valid_handoff("cc", proj, last_write=covers + 120) is None      # 超容差
    _save(st, cwd=proj, covers_s=covers, sid="s2", status="stale")
    assert all(e["status"] != "stale" for e in [st.valid_handoff("cc", proj, last_write=0)]
               if e)                                                          # stale 不算


def test_valid_handoff_freshness_window(tmp_path):
    st = _mk(tmp_path)
    proj = str(tmp_path / "proj")
    _save(st, cwd=proj, covers_s=time.time() - 25 * 3600)                    # 25h 前
    assert st.valid_handoff("cc", proj, last_write=0) is None                # 24h 窗口外


def test_valid_handoff_picks_max_covers(tmp_path):
    st = _mk(tmp_path)
    proj = str(tmp_path / "proj")
    now = time.time()
    _save(st, cwd=proj, covers_s=now - 7200, sid="old")
    _save(st, cwd=proj, covers_s=now - 60, sid="new")
    best = st.valid_handoff("cc", proj, last_write=now - 3600)
    assert best["session_id"] == "new"


def test_pending_prompt_truncate_and_consume(tmp_path):
    st = _mk(tmp_path)
    st.save_pending_prompt("s1", "续" * 600)                 # 600 token > 500 上限
    p = st.pop_pending_prompt("s1")
    assert p is not None and len(p) <= 500 + 10             # 500 截断 + 截断标记后缀
    assert st.pop_pending_prompt("s1") is not None          # 未传 consume → 仍可取
    assert st.pop_pending_prompt("s1", consume_for="newsess") is not None
    assert st.pop_pending_prompt("s1") is None              # 已消费 → 不再给


def test_mark_injected_dedup_and_atomic_write(tmp_path):
    st = _mk(tmp_path)
    e = _save(st)
    st.mark_injected(e["handoff_id"], "sess-A")
    st.mark_injected(e["handoff_id"], "sess-A")
    idx = Path(st.index_path)
    import json
    entry = [h for h in json.loads(idx.read_text(encoding="utf-8"))["handoffs"]
             if h["handoff_id"] == e["handoff_id"]][0]
    assert entry["injected"] == ["sess-A"]
    assert not list(st.dir.parent.glob("*.tmp"))             # 原子写无残留
