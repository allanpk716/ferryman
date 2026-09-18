# T48 异步等待信号修复（附 T49/T50 协议）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Ferryman 认得出"主会话在等异步子代理"的真实等待（25–35 分钟），修好窗口台账、缺口 A 豁免、摆渡判闲三处失真；T49/T50 为协议性后续，不在本计划内实施。

**Architecture:** 停表停车（park）状态机——SubagentStop 到达时若判定刚完成的派发是异步启动（转录尾部标记），窗口不闭，挂起等"主会话恢复调用"（新 usage 行）或 1 小时过期；停着的窗口同时作为缺口 A 豁免与摆渡推迟的第三道信号。同步子代理路径语义不变（stop 即闭）。

**Tech Stack:** Python 3（ferryman 包，pytest），无新依赖。

**Spec:** 本文档自含；证据链=2026-09-18 AntFeedingLog 会话（800fd007）账本分析与转录核对，背景见会话记录与 `docs/20260917_1814_费用账本与心跳保活_设计文档.md` §3.1。

## 评审重点（给用户的四个判断点）

1. **异步判据**用转录尾部标记：Task/Agent 的 `input.run_in_background`（或 `background`）为真，**或** tool_result 文本含 `"Async agent launched"`（实机观测文案，CC 改版可能失效）。判不中一律退回旧语义（stop 即闭窗）——宁可少记真窗，不误停假窗。
2. **停车过期上界 1 小时**（复用 `SUBAGENT_EVENT_LEAK_S`，与计数道同界）：async 代理跑超 1 小时后豁免失效。今天实测最长等待 34.8 分钟，1 小时够用；更长等待接受失明（如实记 `expired`）。
3. **同步路径语义不变**：非异步派发 stop 即闭（`subagents_done`），所有既有测试零改动通过是硬性要求。
4. **prompt 与停车窗的交互**：停着的窗口遇用户输入**不闭**（async 真身还在跑，缺口 A 应放行）；只有未停车的窗口照旧以 `"prompt"` 闭。强续照常 bypass。

## Global Constraints

- 心跳执行器未授权，本计划**不实现任何心跳逻辑**（用户铁律）。
- 隐私不变量：账本只落元数据与金额，永不落消息内容（`has_async_launch` 只返回 bool，不落任何内容）。
- Go 查看器只读账本，本计划不改 viewer。
- 记账永不弄断闸门/守望主路径（异常吞掉+打印，模式同 `_acct`）。
- 全量测试绿（当前 204 用例）为每个 commit 的前置条件。
- 代码注释密度与风格随现有文件（中文注释、Ruling 编号留痕）。

## 背景：今天实测的失真证据（2026-09-18，会话 800fd007）

- 转录实况：主会话 11:54:23 以 async 方式派 Agent（tool_result 秒回 "Async agent launched successfully"），真身 30 分钟后完成，主会话 12:24:51 恢复并全量重付 254k input。
- 账本实况：8 个等待窗口全记成 dur 0.5–0.6 分钟（Stop 事件 ~35 秒早到）；摆渡在机器等待中途误触发 2 次（12:20:34、14:15:00，本地模型零成本但语义错误）。
- 连带风险：缺口 A 的"子代理在飞"计数道在 async 等待期间恒为 0，悬空 tool_use 道也不成立（tool_result 已回）——enforce 模式下用户中途输消息会被当凉会话拦截。

---

## Task 1: `has_async_launch()` —— 转录尾部异步派发判定

**Files:**
- Modify: `ferryman/transcripts.py`（文件末尾追加函数）
- Test: `tests/test_transcripts.py`（追加）

**Interfaces:**
- Produces: `has_async_launch(path: Path, tail_bytes: int = 262_144) -> bool`——后续 Task 2 的 `server.py` 消费。

- [ ] **Step 1: 写失败测试**

```python
# tests/test_transcripts.py 追加
from ferryman.transcripts import has_async_launch


def _write_lines(p, blocks):
    p.write_text("\n".join(json.dumps(b) for b in blocks) + "\n", encoding="utf-8")


def test_async_launch_by_input_flag(tmp_path):
    p = tmp_path / "a.jsonl"
    _write_lines(p, [
        {"type": "assistant", "message": {"role": "assistant", "content": [
            {"type": "tool_use", "id": "t1", "name": "Task",
             "input": {"prompt": "干活", "run_in_background": True}}]}},
        {"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": "t1", "content": "ok"}]}},
    ])
    assert has_async_launch(p) is True


def test_async_launch_by_result_marker(tmp_path):
    """实机兜底：input 无标志位，但 tool_result 文本含实机文案。"""
    p = tmp_path / "b.jsonl"
    _write_lines(p, [
        {"type": "assistant", "message": {"role": "assistant", "content": [
            {"type": "tool_use", "id": "t1", "name": "Agent",
             "input": {"prompt": "干活"}}]}},
        {"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": "t1",
             "content": "Async agent launched successfully (agent-abc)"}]}},
    ])
    assert has_async_launch(p) is True


def test_sync_task_not_async(tmp_path):
    p = tmp_path / "c.jsonl"
    _write_lines(p, [
        {"type": "assistant", "message": {"role": "assistant", "content": [
            {"type": "tool_use", "id": "t1", "name": "Task",
             "input": {"prompt": "干活"}}]}},
        {"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": "t1", "content": "done"}]}},
    ])
    assert has_async_launch(p) is False


def test_async_launch_last_dispatch_wins(tmp_path):
    """尾部最后一个 Task/Agent 派发说了算：旧的 async 标记不算数。"""
    p = tmp_path / "d.jsonl"
    _write_lines(p, [
        {"type": "assistant", "message": {"role": "assistant", "content": [
            {"type": "tool_use", "id": "t1", "name": "Task",
             "input": {"run_in_background": True}}]}},
        {"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": "t1", "content": "ok"}]}},
        {"type": "assistant", "message": {"role": "assistant", "content": [
            {"type": "tool_use", "id": "t2", "name": "Task",
             "input": {"prompt": "同步活"}}]}},
        {"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": "t2", "content": "done"}]}},
    ])
    assert has_async_launch(p) is False


def test_async_launch_missing_file(tmp_path):
    assert has_async_launch(tmp_path / "none.jsonl") is False
```

- [ ] **Step 2: 跑测试确认失败**

Run: `python -m pytest tests/test_transcripts.py -k async_launch -v`
Expected: FAIL（ImportError: cannot import name 'has_async_launch'）

- [ ] **Step 3: 实现**

```python
# ferryman/transcripts.py 追加（风格随 has_dangling_tool_use）
def has_async_launch(path: Path, tail_bytes: int = 262_144) -> bool:
    """尾部异步派发判定（T48）：最后一个 Task/Agent 派发是否为后台/异步启动。

    True = 该会话刚派出 async 子代理（工具调用秒回、真身仍在跑）——等待窗口
    应停表停车（Stop 到达不闭窗），直到主会话恢复调用。判据（对最后一个
    Task/Agent tool_use 取 OR）：input 的 run_in_background/background 为真；
    或其 tool_result 文本含 "Async agent launched"（2026-09-18 实机观测文案，
    CC 改版可能失效——判不中一律 False，退回旧语义=Stop 即闭窗）。
    只读尾部 tail_bytes；坏行/缺字段/OSError 一律 False。
    """
    try:
        with open(path, "rb") as f:
            f.seek(0, 2)
            end = f.tell()
            f.seek(max(0, end - tail_bytes))
            data = f.read()
    except OSError:
        return False
    lines = data.decode("utf-8", errors="replace").split("\n")
    if end > tail_bytes and lines:
        lines = lines[1:]                  # 窗口首行可能是半行，丢弃
    last_dispatch: str | None = None       # 尾窗内最后一个 Task/Agent tool_use id
    is_async: dict[str, bool] = {}
    for line in lines:
        if '"tool_use"' not in line and '"tool_result"' not in line:
            continue
        try:
            d = json.loads(line)
        except ValueError:
            continue
        content = (d.get("message") or {}).get("content")
        if not isinstance(content, list):
            continue
        for b in content:
            if not isinstance(b, dict):
                continue
            if b.get("type") == "tool_use" and b.get("name") in ("Task", "Agent") \
                    and isinstance(b.get("id"), str):
                last_dispatch = b["id"]
                inp = b.get("input")
                is_async[b["id"]] = bool(
                    isinstance(inp, dict)
                    and (inp.get("run_in_background") or inp.get("background")))
            elif b.get("type") == "tool_result" \
                    and isinstance(b.get("tool_use_id"), str):
                c = b.get("content")
                if isinstance(c, str):
                    text = c
                elif isinstance(c, list):
                    text = "".join(x.get("text", "") for x in c
                                   if isinstance(x, dict))
                else:
                    text = ""
                if "Async agent launched" in text:
                    is_async[b["tool_use_id"]] = True
    return bool(last_dispatch and is_async.get(last_dispatch))
```

- [ ] **Step 4: 跑测试确认通过**

Run: `python -m pytest tests/test_transcripts.py -v`
Expected: 全 PASS（含既有用例）

- [ ] **Step 5: 提交**

```bash
git add ferryman/transcripts.py tests/test_transcripts.py
git commit -m "feat(transcripts): has_async_launch 尾部异步派发判定（T48 步1）"
```

---

## Task 2: 停表停车状态机（park / main_resumed / expired）

**Files:**
- Modify: `ferryman/server.py:31`（import）、`server.py:103-108`（窗口表+锁）、`server.py:121-125`（gate prompt 闭窗钩子）、`server.py:377-398`（subagent 开闭窗）、`server.py:225-244`（_machine_waiting）、`server.py:303-315`（_close_window 拆分）
- Test: `tests/test_gate.py`（追加窗口段用例）

**Interfaces:**
- Consumes: Task 1 的 `has_async_launch`。
- Produces:
  - `FerryDaemon.note_usage(agent: str, session_id: str, ts: float) -> None`——Task 3 的 Watcher 消费。
  - `FerryDaemon.window_wait(agent: str, session_id: str) -> bool`——Task 3 与 `_machine_waiting` 消费。
  - window 流水 `close_reason` 新增枚举值 `"main_resumed"`、`"expired"`（accounts.py 只校验字段名不校验值，无 schema 改动）。

- [ ] **Step 1: 写失败测试**

```python
# tests/test_gate.py 窗口段追加
@pytest.fixture
def wenv(tmp_path):
    led = Ledger()
    store = Store(tmp_path / "data")
    acc = Accounts(tmp_path / "data")
    cfg = Config()
    cfg.gate_cc = "enforce"
    cfg.thresholds = ThresholdCfg(summarize_s=10, block_s=30, min_ctx_tokens=100)
    d = FerryDaemon(cfg, led, store, lambda st: True, accounts=acc)
    return d, led, acc, tmp_path


def _async_session(led, tmp_path, sid, idle_s=0):
    """带 async 派发标记的会话转录 + 台账登记。"""
    p = tmp_path / f"{sid}.jsonl"
    blocks = [
        {"type": "assistant", "message": {"role": "assistant", "content": [
            {"type": "tool_use", "id": "t1", "name": "Task",
             "input": {"prompt": "干活", "run_in_background": True}}]}},
        {"type": "user", "message": {"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": "t1",
             "content": "Async agent launched successfully"}]}},
    ]
    p.write_text("\n".join(json.dumps(b) for b in blocks) + "\n", encoding="utf-8")
    led.touch("cc", sid, str(p), mtime=time.time() - idle_s, size=10,
              cwd="C:/proj", peak_ctx=150000, daemon_started_at=0)
    return p


def _sync_session(led, tmp_path, sid):
    p = tmp_path / f"{sid}.jsonl"
    p.write_text("", encoding="utf-8")
    led.touch("cc", sid, str(p), mtime=time.time(), size=0, cwd="C:/proj",
              peak_ctx=150000, daemon_started_at=0)
    return p


def _events(d, sid):
    assert d.subagent({"event": "start", "agent": "cc", "session_id": sid})["ok"]
    assert d.subagent({"event": "stop", "agent": "cc", "session_id": sid})["ok"]


def test_async_stop_parks_until_main_resumes(wenv):
    """核心案：async 派发 stop 后不闭窗；主会话恢复调用才闭（main_resumed）。"""
    d, led, acc, tmp = wenv
    sid = "as1"
    _async_session(led, tmp, sid)
    _events(d, sid)
    assert _window_rows(acc, sid) == []          # 不再秒闭（今天记 0.6min 的 bug）
    assert d.window_wait("cc", sid) is True      # 机器等待在停
    d.note_usage("cc", sid, ts=time.time() + 60)  # 模拟恢复调用（晚于停表）
    rows = _window_rows(acc, sid)
    assert len(rows) == 1
    assert rows[0]["close_reason"] == "main_resumed"
    assert rows[0]["dur_s"] > 0
    assert d.window_wait("cc", sid) is False


def test_sync_stop_closes_immediately(wenv):
    """同步派发语义不变：stop 即闭（既有行为回归锁）。"""
    d, led, acc, tmp = wenv
    sid = "sy1"
    _sync_session(led, tmp, sid)
    _events(d, sid)
    rows = _window_rows(acc, sid)
    assert len(rows) == 1 and rows[0]["close_reason"] == "subagents_done"
    assert d.window_wait("cc", sid) is False


def test_note_usage_before_stop_ignored(wenv):
    """停车前完成的调用被本轮才采集（ts <= stop_ts）→ 不闭窗。"""
    d, led, acc, tmp = wenv
    sid = "as3"
    _async_session(led, tmp, sid)
    _events(d, sid)
    d.note_usage("cc", sid, ts=time.time() - 300)   # 早于停表
    assert _window_rows(acc, sid) == []
    assert d.window_wait("cc", sid) is True


def test_parked_window_exempts_gate(wenv):
    """停车窗期间用户输消息 → 缺口 A 放行（今天会误拦的场景）。"""
    d, led, acc, tmp = wenv
    sid = "as4"
    _async_session(led, tmp, sid, idle_s=9999)      # 闲置远超 block 线
    _events(d, sid)
    r = d.gate(_body(sid, str(tmp_path_join(tmp, sid)), "C:/proj"))
    assert r["decision"] == "allow" and r["reason"] == "machine-waiting"
    # 停车窗不因 prompt 闭——真身还在跑，等待没结束
    assert d.window_wait("cc", sid) is True


def test_parked_window_expires(wenv):
    """停表超 1h（SUBAGENT_EVENT_LEAK_S）→ 懒过期闭窗记 expired，豁免失效。"""
    d, led, acc, tmp = wenv
    sid = "as5"
    _async_session(led, tmp, sid)
    _events(d, sid)
    d._windows[("cc", sid)]["stop_ts"] = time.time() - 4000   # 直接拨表
    assert d.window_wait("cc", sid) is False
    rows = _window_rows(acc, sid)
    assert rows and rows[-1]["close_reason"] == "expired"


def test_prompt_still_closes_open_window(wenv):
    """未停车的窗口遇 prompt 照旧闭（R1 既有语义回归锁）。"""
    d, led, acc, tmp = wenv
    sid = "sy6"
    p = _sync_session(led, tmp, sid)
    assert d.subagent({"event": "start", "agent": "cc", "session_id": sid})["ok"]
    d.gate(_body(sid, str(p), "C:/proj"))
    rows = _window_rows(acc, sid)
    assert len(rows) == 1 and rows[0]["close_reason"] == "prompt"


def tmp_path_join(tmp, sid):
    return tmp / f"{sid}.jsonl"
```

（`tmp_path_join` 若与现有 helper 冲突则内联；`_body`/`_window_rows`/`_open_close` 复用文件内已有 helper。）

- [ ] **Step 2: 跑测试确认失败**

Run: `python -m pytest tests/test_gate.py -k "parked or async_stop or sync_stop or note_usage or prompt_still" -v`
Expected: FAIL（`note_usage`/`window_wait` 属性不存在；async 案秒闭）

- [ ] **Step 3: 实现**

3a. import（`server.py:31`）：

```python
from .transcripts import has_async_launch, has_dangling_tool_use
```

3b. `FerryDaemon.__init__`（`server.py:108` 附近）加锁；窗口结构多一个 `stop_ts`：

```python
        self._wlock = threading.RLock()   # T48：窗口表跨 HTTP/守望双线程，加锁
        self._windows: dict[tuple[str, str], dict] = {}
```

3c. gate 的 prompt 闭窗钩子（`server.py:121-125`）改为只闭未停车的窗：

```python
        # 0. 主会话来讯 = 等待提前结束（词汇表"等待窗口"）——强续/bypass prompt 亦算
        #    恢复写入，故闭窗钩子置于 bypass 判定之前（R1；且台账 miss 也要闭）。
        #    T48 修正：停车窗（async 真身仍在跑）不因 prompt 闭——等待没结束，
        #    缺口 A 照常放行（放行文案已声明"不承诺生效时机"）。
        with self._wlock:
            wk = (agent, session_id)
            w0 = self._windows.get(wk)
            if w0 is not None and w0.get("stop_ts") is None:
                self._close_window(wk, "prompt")
```

3d. `subagent()`（`server.py:389-397`）开闭窗改写：

```python
        # T41/T48 等待窗口：首个子代理 start 开窗（嵌套不重复开）；计数归零时
        # 同步派发即闭（旧语义），异步派发停表停车等主会话恢复（T48）。
        key = (agent, session_id)
        with self._wlock:
            if count > 0:
                w = self._windows.get(key)
                if w is None or now_s() - w["opened_ts"] > SUBAGENT_EVENT_LEAK_S:
                    # 首开；或重锚（R10：Stop 丢失防 dur 虚高）。旧窗若为停车窗，
                    # 按过期语义如实闭账（closed=stop+LEAK），不静默丢弃。
                    old = self._windows.pop(key, None) if w is not None else None
                    if old is not None and old.get("stop_ts") is not None:
                        self._record_window(
                            key, old, "expired",
                            closed_ts=old["stop_ts"] + SUBAGENT_EVENT_LEAK_S)
                    self._windows[key] = {"opened_ts": now_s(), "stop_ts": None}
                elif w.get("stop_ts") is not None:
                    w["stop_ts"] = None         # 停车窗又来 start：续窗（再派/嵌套）
            elif count == 0 and key in self._windows:
                self._park_or_close(key, agent, session_id)
```

3e. 新增三个方法（放 `_close_window` 旁）：

```python
    def _park_or_close(self, key, agent: str, session_id: str) -> None:
        """计数归零：同步派发→即闭窗（旧语义）；异步派发→停表停车（T48）。

        异步判据读主会话转录尾部（has_async_launch）；判不中（CC 改文案/
        钩子早于文件落盘的竞态）一律退回旧语义=即闭——宁可少记一个真窗，
        不误停一个假窗。须持 _wlock 调用。"""
        w = self._windows.get(key)
        if w is None or w.get("stop_ts") is not None:
            return
        st = self.ledger.get(agent, session_id)
        path = st.transcript_path if st is not None else ""
        if agent == "cc" and path and has_async_launch(Path(path)):
            w["stop_ts"] = now_s()          # 停表停车：async 真身仍在跑
        else:
            self._close_window(key, "subagents_done")

    def note_usage(self, agent: str, session_id: str, ts: float) -> None:
        """T48 闭窗道：主会话恢复调用（新 usage 行 ts 晚于停表）→ 等待结束。

        由 Watcher._harvest_usage 每轮喂新行最大 ts；ts <= stop_ts 不算
        （那是停车前已完成的调用被本轮才采集）。"""
        key = (agent, session_id)
        with self._wlock:
            w = self._windows.get(key)
            if w and w.get("stop_ts") is not None and ts > w["stop_ts"]:
                self._close_window(key, "main_resumed")

    def window_wait(self, agent: str, session_id: str) -> bool:
        """缺口A 第三道：异步等待窗在停（stop 已到、主会话未恢复）。

        懒过期：停表超 SUBAGENT_EVENT_LEAK_S（1h，与计数道同界）→ 闭窗记
        expired（closed=stop+LEAK，如实反映"只观察到这"）并返回 False。"""
        key = (agent, session_id)
        with self._wlock:
            w = self._windows.get(key)
            if w is None or w.get("stop_ts") is None:
                return False
            if now_s() - w["stop_ts"] > SUBAGENT_EVENT_LEAK_S:
                self._close_window(key, "expired",
                                   closed_ts=w["stop_ts"] + SUBAGENT_EVENT_LEAK_S)
                return False
            return True
```

3f. `_close_window` 拆出 `_record_window`（支持指定 closed_ts；均为"须持锁或自取锁"安全）：

```python
    def _close_window(self, key: tuple[str, str], reason: str,
                      closed_ts: float | None = None) -> None:
        """闭等待窗口并入账 window 流水（pop 先行 → 天然幂等，绝不双记）。"""
        w = self._windows.pop(key, None)
        if w is None:
            return
        self._record_window(key, w, reason, closed_ts)

    def _record_window(self, key: tuple[str, str], w: dict, reason: str,
                       closed_ts: float | None = None) -> None:
        if self.accounts is None:
            return
        agent, sid = key
        st = self.ledger.get(agent, sid)
        closed = closed_ts if closed_ts is not None else now_s()
        self._acct("window", st, agent=agent, session_id=sid,
                   opened_ts=round(w["opened_ts"], 3), closed_ts=round(closed, 3),
                   dur_s=round(closed - w["opened_ts"], 1),
                   prefix_tokens=self._window_prefix(st, sid, w["opened_ts"]),
                   close_reason=reason)
```

3g. `_machine_waiting`（`server.py:225-244`）加第三道：

```python
        try:
            if self.ledger.subagent_active(agent, session_id):
                return True
        except Exception:  # noqa: BLE001 — 豁免判定异常不影响闸门主路径
            pass
        if self.window_wait(agent, session_id):
            return True                    # T48 第三道：异步停车窗
```

- [ ] **Step 4: 跑测试确认通过（含全量回归）**

Run: `python -m pytest tests/test_gate.py -v && python -m pytest tests/ -q`
Expected: 新用例全 PASS；全量绿（既有窗口用例走同步路径语义不变，零改动通过）

- [ ] **Step 5: 提交**

```bash
git add ferryman/server.py tests/test_gate.py
git commit -m "feat(server): 等待窗口停表停车状态机——async 派发 stop 不闭窗，主会话恢复或 1h 过期才闭（T48 步2）"
```

---

## Task 3: Watcher 接线——摆渡推迟 + usage 闭窗喂入

**Files:**
- Modify: `ferryman/daemon.py:53-62`（Watcher 构造器）、`daemon.py:115-132`（_maybe_enqueue）、`daemon.py:154-176`（_harvest_usage 尾部）、`daemon.py:329`（serve 接线）
- Modify: `tests/helpers.py:109-110`（Harness 接线）
- Test: `tests/test_gate.py`（追加 Watcher 级用例）

**Interfaces:**
- Consumes: Task 2 的 `window_wait` / `note_usage`。
- Produces: `Watcher(..., ferry_daemon=None)` 可选参数（默认 None，旧测试零改动）。

- [ ] **Step 1: 写失败测试**

```python
# tests/test_gate.py 追加
def test_parked_window_defers_ferry(wenv):
    """回归锁（20260918 12:20/14:15 误摆渡案）：停车窗期间不摆渡，恢复后才摆。"""
    from ferryman.daemon import Watcher
    from ferryman.config import Config as Cfg, ThresholdCfg as T
    d, led, acc, tmp = wenv
    sid = "wf1"
    _async_session(led, tmp, sid)
    cfg = Cfg()
    cfg.thresholds = T(summarize_s=10, block_s=30, min_ctx_tokens=100)
    calls: list[str] = []
    w = Watcher(cfg, led, None, lambda st: (calls.append(st.session_id), True)[1],
                started_at=0, accounts=None, ferry_daemon=d)
    st = led.get("cc", sid)
    st.observed_active = True
    st.last_write = time.time() - 999          # 闲置远超 summarize 线
    st.peak_ctx = 150000
    _events(d, sid)                            # 开窗+停车
    w._maybe_enqueue(st)
    assert calls == []                         # 停车窗 → 推迟摆渡
    d.note_usage("cc", sid, ts=time.time() + 1)
    w._maybe_enqueue(st)
    assert calls == [sid]                      # 窗闭后照常摆渡
```

（`Watcher` 构造器若还有必填项以现状为准；`store` 传 None 时 `_maybe_enqueue` 不得触碰它——当前实现不触碰，仅 enqueue 路径用。）

- [ ] **Step 2: 跑测试确认失败**

Run: `python -m pytest tests/test_gate.py -k defers_ferry -v`
Expected: FAIL（`Watcher.__init__` 不接受 `ferry_daemon`）

- [ ] **Step 3: 实现**

3a. `Watcher.__init__`（`daemon.py:54-62`）：

```python
    def __init__(self, cfg: Config, ledger: Ledger, store: Store,
                 enqueue, started_at: float, accounts=None,
                 ferry_daemon: "FerryDaemon | None" = None):
        # ……既有赋值不动……
        self.ferry_daemon = ferry_daemon      # T48：停车窗判定+usage 闭窗喂入
```

3b. `_maybe_enqueue`（`daemon.py:123-126` 之后加一道）：

```python
        if self.ledger.subagent_active(st.agent, st.session_id):
            return                      # T32：子代理运行中（钩子计数，内存判定）→ 推迟，不置 handed_off
        if self.ferry_daemon is not None \
                and self.ferry_daemon.window_wait(st.agent, st.session_id):
            return                      # T48：异步子代理等待中（停表未复写）→ 推迟
                                          # （20260918 12:20/14:15 误摆渡案）
```

3c. `_harvest_usage`（记录循环之后、返回之前）：

```python
            if self.ferry_daemon is not None and rows:
                self.ferry_daemon.note_usage(
                    "cc", st.session_id, max(r["ts"] for r in rows))
```

3d. `serve()`（`daemon.py:329`）：

```python
    watcher = Watcher(cfg, ledger, store, enqueue, started_at, accounts,
                      ferry_daemon=daemon)
```

3e. `tests/helpers.py:109-110`（Harness 同款接线）：

```python
        self.watcher = Watcher(cfg, self.ledger, self.store, enqueue,
                               self.started_at, self.accounts,
                               ferry_daemon=self.daemon)
```

- [ ] **Step 4: 跑全量测试**

Run: `python -m pytest tests/ -q`
Expected: 全绿（含 Harness 集成用例——它们顺带验证接线不破坏现有端到端路径）

- [ ] **Step 5: 提交**

```bash
git add ferryman/daemon.py tests/helpers.py tests/test_gate.py
git commit -m "feat(daemon): 摆渡推迟吃停车窗信号 + usage 行喂入闭窗（T48 步3）"
```

---

## Task 4: 端到端集成测试（Harness 全链路）

**Files:**
- Test: `tests/test_integration.py`（追加）

**Interfaces:**
- Consumes: Task 1–3 全部。

- [ ] **Step 1: 写失败→通过的端到端用例**

```python
# tests/test_integration.py 追加
def test_async_wait_flow_end_to_end(harness, tmp_path, monkeypatch):
    """真链路：async 派发 → stop → 窗口停车（不摆渡）→ 主会话写新 usage 行
    → 守望采集 → 窗口闭（main_resumed）。"""
    import json as J
    h = harness
    sid = "e2e-async"
    projects = h.projects / "C--proj"
    projects.mkdir(parents=True, exist_ok=True)
    f = projects / f"{sid}.jsonl"
    blocks = [
        {"type": "user", "timestamp": now_iso(), "cwd": "C:/proj",
         "sessionId": sid, "message": {"role": "user", "content": "干"}},
        {"type": "assistant", "timestamp": now_iso(),
         "message": {"role": "assistant", "content": [
             {"type": "tool_use", "id": "t1", "name": "Task",
              "input": {"prompt": "x", "run_in_background": True}}],
             "usage": {"input_tokens": 2000, "cache_read_input_tokens": 100,
                        "cache_creation_input_tokens": 0, "output_tokens": 5}}},
        {"type": "user", "timestamp": now_iso(),
         "message": {"role": "user", "content": [
             {"type": "tool_result", "tool_use_id": "t1",
              "content": "Async agent launched successfully"}]}},
    ]
    f.write_text("\n".join(J.dumps(b) for b in blocks) + "\n", encoding="utf-8")
    import os
    os.utime(f, None)
    assert h.wait_for(lambda: h.ledger.get("cc", sid) is not None)
    assert h.sub({"event": "start", "agent": "cc", "session_id": sid})["ok"]
    assert h.sub({"event": "stop", "agent": "cc", "session_id": sid})["ok"]
    assert h.wait_for(lambda: h.daemon.window_wait("cc", sid))   # 停车成立

    # 闲置远超 summarize 线也不摆渡（误摆渡回归锁）
    st = h.ledger.get("cc", sid)
    st.observed_active = True
    st.last_write = time.time() - 999
    st.peak_ctx = 150000
    time.sleep(1.0)                                              # 让守望跑几轮
    assert sid not in h.enqueued_ok

    # 主会话恢复：追加新 assistant usage 行（ts=now，晚于停表）
    blocks.append({"type": "assistant", "timestamp": now_iso(),
                   "message": {"role": "assistant", "content": [
                       {"type": "text", "text": "回来了"}],
                       "usage": {"input_tokens": 30, "cache_read_input_tokens": 250000,
                                  "cache_creation_input_tokens": 0, "output_tokens": 5}}})
    f.write_text("\n".join(J.dumps(b) for b in blocks) + "\n", encoding="utf-8")
    os.utime(f, None)
    assert h.wait_for(lambda: any(
        r["close_reason"] == "main_resumed"
        for r in h.accounts.read(kind="window") if r["session_id"] == sid))
```

（`harness` fixture 与 `now_iso` 按 `tests/test_integration.py` 现有写法对齐；若 fixture 名不同，随现有文件。）

- [ ] **Step 2: 跑测试**

Run: `python -m pytest tests/test_integration.py -k async_wait -v`
Expected: PASS（若守望轮询时序抖动，放宽 `wait_for` 超时至 15s 默认值再判）

- [ ] **Step 3: 全量回归 + 提交**

```bash
python -m pytest tests/ -q
git add tests/test_integration.py
git commit -m "test(integration): async 等待全链路——停车/不摆渡/恢复闭窗（T48 步4）"
```

---

## Task 5: 文档收口

**Files:**
- Modify: `CONTEXT.md`（词汇表"等待窗口"条目）
- Modify: `docs/20260918_A6人工验收_缺口A上线后演练单.md`（追加 async 用例）

- [ ] **Step 1: CONTEXT.md"等待窗口"条目补停车语义与 close_reason 枚举**

在条目内追加（措辞随现有条目风格）：

> 停车（park）：SubagentStop 到达且判定刚完成派发为异步启动（尾部标记）时，窗口不闭、停表挂起；主会话恢复调用（新 usage 行）闭为 `main_resumed`，停表超 1h 闭为 `expired`（closed=stop+LEAK）。close_reason 全集：`subagents_done`（同步即闭）/ `prompt`（主会话来讯，未停车窗）/ `main_resumed`（停车窗+恢复调用）/ `expired`（停车窗超界/重锚旧窗）。停车窗同时是缺口 A 豁免与摆渡推迟的第三道信号。

- [ ] **Step 2: A6 演练单追加 async 用例**

追加一行演练项（沿用单内表格/步骤格式）：

> **async 变体**：派一个后台子代理（Task/Agent 异步启动，跑 ≥3 分钟）→ 等待期间向主会话输消息 → 期望：豁免放行提示（机器等待中），不生成摆渡；子代理完成后主会话自动续跑；账本 window 行 close_reason=`main_resumed` 且 dur 覆盖真实等待（时序页可见）。

- [ ] **Step 3: 提交**

```bash
git add CONTEXT.md docs/20260918_A6人工验收_缺口A上线后演练单.md
git commit -m "docs: 等待窗口停车语义入词汇表 + A6 演练单补 async 用例（T48 步5）"
```

---

## Task 6: 真机验收（用户参与，合入后）

- [ ] 用户跑 A6 演练单（含新 async 用例）：派 async 代理 → 中途输消息看豁免 → 等完成看续跑。
- [ ] 次日核查账本：`python -c` 读 window 行（或时序页），确认 AntFeedingLog 后续会话的窗口 dur 覆盖真实等待（对照本次 0.5–0.6 分钟的废数据）。
- [ ] 观察一周：无停车窗误停（同步会话秒闭如旧）、无误摆渡复发。

---

# Part B：T49 · 压缩赛道 spike + 试点（协议，等用户批）

> 目标：长编排会话前缀从 ~26 万压到 ≤15 万，砍 70% 的缓存读取通行费。今天 800fd007 会话即基线样本（4972 积分，其中通行费 3475、断缓存重付 838）。

**B1. 可配性侦察（≤半天，产出结论文档）**

1. 查 cc-switch 供应商配置有无 context window 字段：`cc-switch` 库/界面（路由调研已备底：`docs/research/` CC 路由篇）；目标口径 ~150k–200k。
2. 查 CC 侧开关：settings.json / 环境变量是否可覆盖 auto-compact 触发点（T47 调研已知坑：1M 窗口永不触发）。
3. 小实验（若找到旋钮）：设 ~150k → 跑真会话过 150k → 核对 jsonl compact 事件 + usage 行 `input+cache_read` 骤降。
4. 产出 `docs/research/20260918_auto-compact可配性结论.md`：可配 → 直接 ADR 进试点；不可配 → bili 试点 vs Ferryman 提示钩子两案对比表（引用 `docs/research/20260918_billion-context持续压缩调研.md` 坑清单），交用户裁决。

**B2. 试点（2 周，只动用户自己会话）**

- 应用配置；每周 `python -m ferryman report --since <周一>` 出三指标：单会话总积分、前缀 P95（usage 行 input+cache_read）、断缓存重付合计；对比 202609 基线。
- 验收线（T47 现实档口径）：长会话单会话成本降 ≥30%；四坑无回潮。
- 试点期间不动其它轨道；每周数字随手可查（时序页/参数页已具备）。

**备注（本计划不做，留档）**：等待期 compact 是甜点位（等待 25 分钟里压缩零延迟成本，回来只重付小前缀）——若 B2 走"Ferryman 提示钩子"路线，此为首选触发时机。

---

# Part C：T50 · 心跳复盘点（协议，无代码）

1. **前置**：T48 合入且真实窗口（close_reason ∈ {main_resumed, expired}）攒满 ≥30 个，或满两周。
2. **动作**：`python -m ferryman report` 策略对比节（注意 v1 三口径缺陷已披露，结论只作方向参考）。
3. **判据**：
   - 等待时长中位数 >25 分钟（心跳 cap≈24.5min，20260918 实测 5/5 超线）→ **心跳正式关案**：不实施、Q14 实验不做，设计文档记一笔（省 100–120 积分实验费）。
   - 出现 10–24 分钟簇 → 再谈 Q14 授权。
4. **顺带 5 分钟**：TTL 实验报告（`docs/20260917_1630_…实验报告.md`）附录补两个野外样本：27.6 min 全前缀命中、24.4 min 未命中（20260918 会话 800fd007）——灰色区比实验室宽，`ttl_s=600` 安全线不动。

---

## Self-Review 记录

- 覆盖检查：三处失真（窗口台账/缺口A豁免/摆渡判闲）分别由 Task 2、Task 2g+Task 3b、Task 3b 覆盖；接线由 Task 3/4 覆盖；文档与真机验收 Task 5/6。T49/T50 为协议，无代码任务。
- 占位符扫描：无 TBD/TODO；所有代码步骤给出完整代码。
- 类型一致性：`has_async_launch(Path)->bool`、`note_usage(str,str,float)->None`、`window_wait(str,str)->bool`、`_park_or_close(key,str,str)->None`、`_close_window(key,str,float|None)->None`、`_record_window(key,dict,str,float|None)->None`、`Watcher(...,ferry_daemon="FerryDaemon|None")` 各任务间签名一致。
- 已知残留风险（如实声明）：① 异步判据依赖 CC 文案/标志位，改版即失效（退回旧语义，A6 演练可发现）；② Stop 钩子早于转录落盘的竞态（同上退路）；③ 停车窗 1h 上界外的长 async 等待失明（记 expired）；④ 集成测试时序抖动风险（wait_for 兜底）。
