# T42 用量落库（usage 科目）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 守望线程增量尾读 CC 会话文件，把每次助手记录的 token 用量落进账本新科目 `usage`——会话文件 30 天清理后的审计地基 + 心跳成效判定地面真值的持久化（设计文档 §3.7"用量落库"）。

**Architecture:** 新模块 `ferryman/harvest.py` 承担纯解析与增量偏移状态；`Watcher` 在轮询里对每个 CC 会话文件尾读新增字节，解析出 usage 行经 `Accounts.record` 落账；偏移随流水入账，daemon 重启后从账本恢复（账本即唯一状态，无第二状态文件）。

**Tech Stack:** 纯标准库（json/datetime/pathlib），无新依赖。

**Spec:** `docs/20260917_1814_费用账本与心跳保活_设计文档.md` §3.7（用量落库小节）。

## Global Constraints

- **隐私不变量**：usage 行只含数字与类型（token 数、模型名、标题、偏移），**永不落消息内容**；字段必须过 `_KIND_FIELDS` 白名单。
- **故障隔离不变量**：采集/记账的任何异常不得弄断守望循环与摆渡（try/except 吞为警告，模式同 `daemon.py _book_handoff`）。
- **append-only**：只追加不改写；重复风险仅存在于"文件被重写收缩"的病态场景，接受并在注释中注明 report 层可按 (session_id, offset) 去重。
- **范围 v1**：仅 CC 主会话（subagents 转录已被 `_poll_cc` glob 层排除，天然继承）；Codex 挂后续（Ruling：CC 先行，等查看器显示真实需要再议）。
- 现有 164 个测试全绿不得回归；每任务收尾跑全量 `python -m pytest tests/ -q`。
- 代码风格贴合现有：中文 docstring、`flush=True` 的警告 print、类型标注、局部 import 模式。

---

### Task 1: 纯解析器 + usage 科目

**Files:**
- Create: `ferryman/harvest.py`
- Modify: `ferryman/accounts.py:20-31`（`_KIND_FIELDS` 加 usage）
- Test: `tests/test_harvest.py`（新建）
- Test: `tests/test_accounts.py`（追加 1 个用例）

**Interfaces:**
- Consumes: `accounts._KIND_FIELDS` 白名单机制（现成）。
- Produces: `parse_usage_chunk(text: str, *, title: str = "", cwd: str = "") -> tuple[list[dict], str, str]`——输入一段**完整行**的 JSONL 文本，输出 (usage 行列表, 最新标题, 最新 cwd)。行 dict 键：`ts`(float|None)、`model`、`input_tokens`、`cache_read_tokens`、`cache_creation_tokens`、`output_tokens`（int）。Task 2/3 依赖此签名。

- [ ] **Step 1: 写失败测试** `tests/test_harvest.py`：

```python
"""用量采集：纯解析器测试（设计 §3.7）。"""

import json

from ferryman.harvest import parse_usage_chunk


def _user_line(ts="2026-09-18T01:00:00Z", cwd="C:/proj"):
    return json.dumps({"type": "user", "timestamp": ts, "cwd": cwd,
                       "sessionId": "s1", "message": {"role": "user",
                                                      "content": "干活"}})


def _asst_line(ts="2026-09-18T01:00:05Z", model="glm-5.3", inp=100, cr=9000, cc=0, out=50):
    return json.dumps({"type": "assistant", "timestamp": ts,
                       "message": {"role": "assistant", "model": model,
                                   "content": [{"type": "text", "text": "好"}],
                                   "usage": {"input_tokens": inp,
                                             "cache_read_input_tokens": cr,
                                             "cache_creation_input_tokens": cc,
                                             "output_tokens": out}}})


def test_parse_extracts_assistant_usage_and_cwd():
    chunk = _user_line() + "\n" + _asst_line() + "\n"
    rows, title, cwd = parse_usage_chunk(chunk)
    assert cwd == "C:/proj"
    assert title == ""
    assert len(rows) == 1
    r = rows[0]
    assert r["input_tokens"] == 100 and r["cache_read_tokens"] == 9000
    assert r["cache_creation_tokens"] == 0 and r["output_tokens"] == 50
    assert r["model"] == "glm-5.3"
    assert abs(r["ts"] - 1758150005.0) < 1  # 2026-09-18T01:00:05Z 的 epoch（允许时区库差）
    assert "content" not in r and "message" not in r  # 隐私：无消息内容


def test_parse_tracks_ai_title():
    title_line = json.dumps({"type": "ai-title", "aiTitle": "修登录bug"})
    chunk = title_line + "\n" + _asst_line() + "\n"
    rows, title, cwd = parse_usage_chunk(chunk)
    assert title == "修登录bug"
    assert len(rows) == 1


def test_parse_skips_malformed_and_bare_lines():
    chunk = ("{broken json\n" + _asst_line() + "\n"
             + json.dumps({"type": "user", "message": {"role": "user", "content": "x"}}) + "\n"
             + json.dumps({"type": "assistant", "message": {"role": "assistant",
                                                            "content": "无用量"}}) + "\n")
    rows, _t, _c = parse_usage_chunk(chunk)
    assert len(rows) == 1            # 只有带 usage 的 assistant 出行
```

注意 `ts` 断言：用 `datetime.fromisoformat("2026-09-18T01:00:05+00:00").timestamp()` 的期望值在测试里现场算，不要硬编码数字（实现若用 replace("Z","+00:00") 则两值相等；写成 `from datetime import datetime; expected = datetime.fromisoformat("2026-09-18T01:00:05+00:00").timestamp(); assert r["ts"] == expected`）。

- [ ] **Step 2: 跑测试确认失败**：`python -m pytest tests/test_harvest.py -q` → FAIL（ModuleNotFoundError）。

- [ ] **Step 3: 实现** `ferryman/harvest.py`：

```python
"""用量采集：增量尾读会话文件，把每次助手记录的 token 用量落账（设计 §3.7）。

- 只取数字与类型，永不落消息内容（隐私不变量）；
- 增量：记住每文件已消费字节偏移，只解析新增的完整行（残行留待下轮）；
- 断点：偏移随 usage 流水入账，daemon 重启后从账本恢复——账本即唯一状态；
- 范围 v1：仅 CC 主会话（subagents 转录由守望 glob 层排除；Codex 挂后续）。
"""

from __future__ import annotations

import json
from datetime import datetime


def _ts_of(rec: dict) -> float | None:
    ts = rec.get("timestamp")
    if not isinstance(ts, str):
        return None
    try:
        return datetime.fromisoformat(ts.replace("Z", "+00:00")).timestamp()
    except ValueError:
        return None


def parse_usage_chunk(text: str, *, title: str = "",
                      cwd: str = "") -> tuple[list[dict], str, str]:
    """解析一段完整 JSONL 行 → (usage 行列表, 最新标题, 最新 cwd)。

    只认两类记录出行/更新：ai-title（更新标题）、assistant 且 message.usage
    非空（出行）；其余记录只可能补 cwd（首个带 cwd 的记录）。损坏行跳过不抛。
    """
    rows: list[dict] = []
    for line in text.splitlines():
        if not line.strip():
            continue
        try:
            rec = json.loads(line)
        except ValueError:
            continue
        if not isinstance(rec, dict):
            continue
        if rec.get("type") == "ai-title":
            if rec.get("aiTitle"):
                title = str(rec["aiTitle"])
            continue
        if not cwd and rec.get("cwd"):
            cwd = str(rec["cwd"])
        if rec.get("type") != "assistant":
            continue
        msg = rec.get("message") or {}
        usage = msg.get("usage") or {}
        if not usage:
            continue
        rows.append({"ts": _ts_of(rec), "model": str(msg.get("model", "")),
                     "input_tokens": int(usage.get("input_tokens", 0)),
                     "cache_read_tokens": int(usage.get("cache_read_input_tokens", 0)),
                     "cache_creation_tokens":
                         int(usage.get("cache_creation_input_tokens", 0)),
                     "output_tokens": int(usage.get("output_tokens", 0))})
    return rows, title, cwd
```

- [ ] **Step 4: 账本加科目** `ferryman/accounts.py` 的 `_KIND_FIELDS`（window 行后追加）：

```python
    # 逐次请求的用量遥测（设计 §3.7；会话文件 30 天清理后的审计地基）
    "usage": {"model", "title", "input_tokens", "cache_read_tokens",
              "cache_creation_tokens", "output_tokens", "offset"},
```

`tests/test_accounts.py` 追加：

```python
def test_usage_kind_roundtrip(tmp_path):
    acc = Accounts(tmp_path)
    e = acc.record("usage", ts=1758150005.0, agent="cc", session_id="s1",
                   lineage_id="L1", project="C:/proj", model="glm-5.3",
                   title="修登录bug", input_tokens=100, cache_read_tokens=9000,
                   cache_creation_tokens=0, output_tokens=50, offset=2048)
    assert e["kind"] == "usage" and e["offset"] == 2048
    with pytest.raises(ValueError, match="不落这些字段"):
        acc.record("usage", agent="cc", session_id="s1", model="m", title="",
                   input_tokens=1, cache_read_tokens=0, cache_creation_tokens=0,
                   output_tokens=0, offset=1, message_content="泄漏")
```

（文件头部如缺 `import pytest`/`from ferryman.accounts import Accounts` 则补；复用文件里已有的 fixture 与风格。）

- [ ] **Step 5: 跑测试确认通过**：`python -m pytest tests/test_harvest.py tests/test_accounts.py -q` → 全 PASS。

- [ ] **Step 6: 跑全量**：`python -m pytest tests/ -q` → 164+新增 全绿。

- [ ] **Step 7: 提交**：`git add -A && git commit -m "feat(harvest): T42 纯解析器+usage科目——ai-title/assistant用量提取，隐私白名单"`

---

### Task 2: 增量状态 HarvestState

**Files:**
- Modify: `ferryman/harvest.py`（追加类）
- Test: `tests/test_harvest.py`（追加）

**Interfaces:**
- Consumes: Task 1 的 `parse_usage_chunk`。
- Produces: `HarvestState(accounts)`；方法 `maybe_harvest(path: Path, size: int, *, agent: str) -> list[dict]`——返回的行 dict 在 Task 1 行键之上加 `title`、`project`（cwd）、`offset`（本批结束的字节偏移），`ts` 同前。Task 3 依赖。

- [ ] **Step 1: 写失败测试**（追加到 `tests/test_harvest.py`）：

```python
import threading  # noqa: F401 —— Accounts 需要（如已有则不重复）
from pathlib import Path

from ferryman.accounts import Accounts
from ferryman.harvest import HarvestState, parse_usage_chunk  # noqa: F401


def _mk_session(p: Path):
    p.write_text(_user_line() + "\n" + _asst_line() + "\n", encoding="utf-8")


def test_incremental_and_offsets(tmp_path):
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    _mk_session(f)
    hs = HarvestState(acc)
    rows = hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    assert len(rows) == 1
    assert rows[0]["offset"] == f.stat().st_size      # 本批结束偏移
    assert rows[0]["title"] == "" and rows[0]["project"] == "C:/proj"
    # 无新增 → 空
    assert hs.maybe_harvest(f, f.stat().st_size, agent="cc") == []
    # 追加一条 assistant → 只有新增量
    with open(f, "a", encoding="utf-8") as fh:
        fh.write(_asst_line(ts="2026-09-18T01:01:00Z") + "\n")
    rows2 = hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    assert len(rows2) == 1 and rows2[0]["offset"] == f.stat().st_size


def test_partial_line_held_back(tmp_path):
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    f.write_text(_user_line() + "\n", encoding="utf-8")
    hs = HarvestState(acc)
    hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    half = _asst_line()[:30]                          # 无换行的半行
    with open(f, "a", encoding="utf-8") as fh:
        fh.write(half)
    assert hs.maybe_harvest(f, f.stat().st_size, agent="cc") == []
    with open(f, "a", encoding="utf-8") as fh:        # 补完
        fh.write(_asst_line()[30:] + "\n")
    rows = hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    assert len(rows) == 1


def test_title_carried_across_batches(tmp_path):
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    title_line = json.dumps({"type": "ai-title", "aiTitle": "起名了"})
    f.write_text(title_line + "\n", encoding="utf-8")
    hs = HarvestState(acc)
    hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    with open(f, "a", encoding="utf-8") as fh:
        fh.write(_asst_line() + "\n")
    rows = hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    assert rows and rows[0]["title"] == "起名了"       # 标题跨批次携带


def test_resume_from_accounts(tmp_path):
    """重启恢复：新 HarvestState 从账本行恢复偏移与标题，不重复采集。"""
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    f.write_text(json.dumps({"type": "ai-title", "aiTitle": "旧名"}) + "\n"
                 + _asst_line() + "\n", encoding="utf-8")
    hs = HarvestState(acc)
    for r in hs.maybe_harvest(f, f.stat().st_size, agent="cc"):
        acc.record("usage", ts=r["ts"], agent="cc", session_id="s1",
                   lineage_id="L", project=r["project"], model=r["model"],
                   title=r["title"], input_tokens=r["input_tokens"],
                   cache_read_tokens=r["cache_read_tokens"],
                   cache_creation_tokens=r["cache_creation_tokens"],
                   output_tokens=r["output_tokens"], offset=r["offset"])
    with open(f, "a", encoding="utf-8") as fh:        # 停机期间新增
        fh.write(_asst_line(ts="2026-09-18T02:00:00Z") + "\n")
    hs2 = HarvestState(acc)                            # "重启"
    rows = hs2.maybe_harvest(f, f.stat().st_size, agent="cc")
    assert len(rows) == 1                              # 只采新增
    assert rows[0]["title"] == "旧名"                  # 标题也已恢复


def test_shrink_rereads(tmp_path):
    acc = Accounts(tmp_path)
    f = tmp_path / "s1.jsonl"
    _mk_session(f)
    hs = HarvestState(acc)
    hs.maybe_harvest(f, f.stat().st_size, agent="cc")
    f.write_text(_asst_line(ts="2026-09-18T03:00:00Z") + "\n", encoding="utf-8")
    rows = hs.maybe_harvest(f, f.stat().st_size, agent="cc")   # 收缩→从头重采
    assert len(rows) == 1
```

- [ ] **Step 2: 跑测试确认失败**：`python -m pytest tests/test_harvest.py -q` → 新用例 FAIL（ImportError: HarvestState）。

- [ ] **Step 3: 实现**（追加到 `ferryman/harvest.py`，并补 `from pathlib import Path` import）：

```python
class HarvestState:
    """每会话文件的采集偏移；daemon 重启后由账本恢复（usage 行自带 offset）。

    病态场景（同路径文件被重写收缩）：偏移清零从头重采，可能与旧行重复——
    append-only 不改写旧账，report 层可按 (session_id, offset) 去重。
    """

    def __init__(self, accounts) -> None:
        self._accounts = accounts
        self._offsets: dict[tuple[str, str], int] = {}
        self._titles: dict[tuple[str, str], str] = {}
        self._cwds: dict[tuple[str, str], str] = {}
        try:
            entries = accounts.read(kind="usage")
        except Exception:                    # 账本读失败 → 从零采（重复风险接受）
            entries = []
        for e in entries:
            key = (e.get("agent", ""), e.get("session_id", ""))
            off = int(e.get("offset", 0))
            if off > self._offsets.get(key, -1):
                self._offsets[key] = off
            if e.get("title"):
                self._titles[key] = e["title"]
            if e.get("project"):
                self._cwds[key] = e["project"]

    def maybe_harvest(self, path: Path, size: int, *, agent: str) -> list[dict]:
        """有新增则尾读出 usage 行（带 title/project/offset）；无新增返回空。

        残行（无换行结尾）整段留待下一轮；文件打不开返回空、不推进偏移。
        """
        key = (agent, path.stem)
        offset = self._offsets.get(key, 0)
        if size < offset:                    # 重写/收缩 → 从头重采
            offset = 0
        if size == offset:
            return []
        try:
            with open(path, "rb") as f:
                f.seek(offset)
                raw = f.read(size - offset)
        except OSError:
            return []
        if not raw:
            return []
        end = len(raw)
        if not raw.endswith(b"\n"):
            nl = raw.rfind(b"\n")
            if nl < 0:
                return []                    # 一整段没有完整行
            end = nl + 1
        chunk = raw[:end].decode("utf-8", errors="replace")
        rows, title, cwd = parse_usage_chunk(
            chunk, title=self._titles.get(key, ""), cwd=self._cwds.get(key, ""))
        new_offset = offset + end
        for r in rows:
            r["title"] = title
            r["project"] = cwd
            r["offset"] = new_offset
        if title:
            self._titles[key] = title
        if cwd:
            self._cwds[key] = cwd
        self._offsets[key] = new_offset
        return rows
```

- [ ] **Step 4: 跑测试确认通过**：`python -m pytest tests/test_harvest.py -q` → 全 PASS。

- [ ] **Step 5: 跑全量** → 全绿。

- [ ] **Step 6: 提交**：`git add -A && git commit -m "feat(harvest): T42 增量尾读状态——字节偏移/残行留待/账本断点恢复/收缩重采"`

---

### Task 3: 守望接线 + 配置开关 + 集成测试

**Files:**
- Modify: `ferryman/daemon.py`（Watcher 加 accounts 参数与 `_harvest`；`_poll_cc` 调用；`serve()` 传参）
- Modify: `ferryman/config.py`（WatchCfg.harvest_usage + load 解析）
- Modify: `config.example.toml`（[watch] 段加一行）
- Modify: `tests/helpers.py`（Harness 构造 Watcher 传 accounts）
- Test: `tests/test_integration.py`（追加 2 个用例）

**Interfaces:**
- Consumes: Task 2 的 `HarvestState.maybe_harvest`；`ledger._norm_path`。
- Produces: `Watcher(cfg, ledger, store, enqueue, started_at, accounts=None)`——第 6 个可选参数（旧调用零改动）；`WatchCfg.harvest_usage: bool = True`。

- [ ] **Step 1: 写失败集成测试**（追加到 `tests/test_integration.py`，复用文件里已有的 Harness 用法与 fixture 风格）：

```python
def test_usage_harvested_to_accounts(harness, projects):
    """T42：守望把会话用量落账为 usage 行（含标题/cwd），追加只采增量。"""
    import json as _json
    sid = "usid-0001"
    write_session(projects, sid, "C:/proj", usage_input=1234)
    assert harness.wait_for(
        lambda: harness.accounts.read(kind="usage", session=sid))
    rows = harness.accounts.read(kind="usage", session=sid)
    assert len(rows) == 1
    r = rows[0]
    assert r["agent"] == "cc" and r["session_id"] == sid
    assert r["input_tokens"] == 1234 and r["cache_read_tokens"] == 100
    assert r["model"] == "" and r["title"] == "集成测试会话"
    assert r["lineage_id"] and r["offset"] > 0
    # 追加一条 assistant → 只 +1 行
    f = projects / "C--proj" / f"{sid}.jsonl"
    ts2 = now_iso()
    f.open("a", encoding="utf-8").write(_json.dumps(
        {"type": "assistant", "timestamp": ts2,
         "message": {"role": "assistant",
                     "content": [{"type": "text", "text": "又一步"}],
                     "usage": {"input_tokens": 5, "cache_read_input_tokens": 2000,
                               "cache_creation_input_tokens": 0,
                               "output_tokens": 7}}}) + "\n")
    assert harness.wait_for(
        lambda: len(harness.accounts.read(kind="usage", session=sid)) == 2)
```

（fixture 名以文件内现有为准——若 Harness 用法是 `h = Harness(tmp_path, monkeypatch)` 形式则照抄该模式；`write_session`/`now_iso` 从 helpers 导入。注意：write_session 的 assistant 行模型名留空、cache_read=100、ai-title 行自带标题"集成测试会话"。）

```python
def test_usage_harvest_disabled(harness_disabled, projects):
    """watch.harvest_usage=False → 不落 usage 行。"""
    sid = "usid-0002"
    write_session(projects, sid, "C:/proj")
    import time
    time.sleep(1.0)                    # 足够越过 2 个轮询周期
    assert harness_disabled.accounts.read(kind="usage") == []
```

（`harness_disabled` 若无现成 fixture，就在用例内自建：`cfg.watch.harvest_usage = False` 后构造 Harness——照抄本文件里现有 Harness 构造的样板。）

- [ ] **Step 2: 跑测试确认失败**：`python -m pytest tests/test_integration.py -q -k usage` → FAIL（Watcher 无 accounts / 无 harvest）。

- [ ] **Step 3: 实现**

`ferryman/config.py`：`WatchCfg` 加字段 `harvest_usage: bool = True`（注释：用量采集开关，隐私敏感可关）；`load()` 的 `"watch"` 分支加 `harvest_usage=bool(w.get("harvest_usage", True))`。

`config.example.toml` 的 `[watch]` 段加：`# harvest_usage = true   # 用量采集（usage 科目）：会话文件 30 天清理后的审计地基`。

`ferryman/daemon.py` Watcher：

```python
    def __init__(self, cfg: Config, ledger: Ledger, store: Store,
                 enqueue, started_at: float, accounts=None):
        super().__init__(daemon=True, name="ferryman-watch")
        self.cfg, self.ledger, self.store = cfg, ledger, store
        self.enqueue = enqueue
        self.started_at = started_at
        self.accounts = accounts          # None = 不采集（旧调用/测试零改动）
        self.harvest = None
        if accounts is not None and cfg.watch.harvest_usage:
            from .harvest import HarvestState
            self.harvest = HarvestState(accounts)
        self._stop = threading.Event()
        ...（其余不变）
```

`_poll_cc` 的循环体内、`st = self.ledger.touch(...)` 之后、`self._maybe_enqueue(st)` 之前插一行 `self._harvest_usage(p, size, st)`。

新方法（放在 `_maybe_enqueue` 之后）：

```python
    def _harvest_usage(self, path: Path, size: int, st) -> None:
        """T42 用量采集：账本故障不得弄断守望（故障隔离不变量，模式同 _book_handoff）。"""
        if self.harvest is None:
            return
        try:
            rows = self.harvest.maybe_harvest(path, size, agent="cc")
            if not rows:
                return
            from .ledger import _norm_path
            lineage = _norm_path(str(path))
            for r in rows:
                self.accounts.record(
                    "usage", ts=r["ts"], agent="cc", session_id=st.session_id,
                    lineage_id=lineage, project=r["project"] or st.cwd,
                    model=r["model"], title=r["title"],
                    input_tokens=r["input_tokens"],
                    cache_read_tokens=r["cache_read_tokens"],
                    cache_creation_tokens=r["cache_creation_tokens"],
                    output_tokens=r["output_tokens"], offset=r["offset"])
        except Exception as e:  # noqa: BLE001 — 采集故障只警告
            print(f"[harvest] 用量采集失败（忽略继续）: {path.name}: {e}",
                  flush=True)
```

`serve()`：`watcher = Watcher(cfg, ledger, store, enqueue, started_at, accounts)`。

`tests/helpers.py` Harness：`self.watcher = Watcher(cfg, self.ledger, self.store, enqueue, self.started_at, self.accounts)`。

- [ ] **Step 4: 跑测试确认通过**：`python -m pytest tests/test_integration.py -q -k usage` → PASS。

- [ ] **Step 5: 跑全量**：`python -m pytest tests/ -q` → 全绿（164+8 左右）。

- [ ] **Step 6: 提交**：`git add -A && git commit -m "feat(harvest): T42 守望接线——增量采集usage入账+harvest_usage开关（故障隔离）"`

---

## Self-Review

1. **Spec 覆盖**：§3.7"用量落库"的增量尾读/字段/标题携带/隐私——Task 1-3 全落；"地面真值持久化"由 cache_read_tokens 字段承接。✓
2. **占位符扫描**：无 TBD/待补；全部代码完整给出。✓
3. **类型一致性**：`parse_usage_chunk` 行键（ts/model/input/cache_read/cache_creation/output）→ `HarvestState` 追加 title/project/offset → `Watcher` 全量传 `Accounts.record`——键名逐一对齐 `usage` 白名单（model/title/input_tokens/cache_read_tokens/cache_creation_tokens/output_tokens/offset）+ 公共键（agent/session_id/lineage_id/project）。✓
