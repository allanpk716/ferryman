# T41 费用账本与策略计算器 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地六科目 append-only 费用账本 + 价格表模块 + `ferryman account report` CLI + 心跳策略计算器（纯公式）+ 交接 SYSTEM_PROMPT 两招——心跳执行器不在本期（未授权）。

**Architecture:** 新增四个纯模块（prices/accounts/policy/report），daemon 侧只做"接线"：FerryDaemon 与 FerryWorker 增加可选 `accounts` 参数（None=不记账，旧测试零改动），在既有的 block/bypass/inject/subagent/摆渡完成点上各插一行记账。节省额永不入账，report 层按 ADR-0002 用版本化公式现算。

**Tech Stack:** Python ≥3.12 纯标准库（`dependencies = []`），pytest≥8 经 `uv run pytest`，无网络无第三方。

**Spec:** `docs/20260917_1814_费用账本与心跳保活_设计文档.md`（本计划从该 spec 论证而来，执行者须同读）；术语见 `CONTEXT.md` 费用账本/心跳保活两节；ADR：`docs/adr/0002-ledger-facts-only-savings-recomputed.md`。

## Global Constraints

- 纯标准库，零新增依赖；Python ≥3.12（`tomllib` 可用）。
- 测试命令一律 `uv run pytest ...`；全量 `uv run pytest`（当前 120 用例全绿，不许回归）。
- 隐私不变量：账本字段白名单校验，消息内容类字段一律拒绝（设计 §1.2）。
- append-only：账本只增不改；节省额、成本折算永不写入账本（ADR-0002）。
- handoff/beat 流水钉死记账时 `price_ver`（如 `"glm@2026-09-17"`）；改价不重算旧账。
- 测试密闭：不打真网，模型一律 fake（沿用 `tests/helpers.py` Harness 模式）。
- 主开发环境 Windows；路径比较统一走 `ferryman.ledger._norm_path`。
- 提交信息惯例（照抄 git log 风格）：`feat(account): T41 ……（N 用例全绿）`，中文，带任务号；N 填**当次全量套件实际通过数**（当前基线 120，随任务递增）。
- 心跳执行器、beat 科目的生产者：**本期一律不写**（`beat` 仅作为 schema 白名单占坑）。

---

### Task 1: 价格表模块 prices.py

**Files:**
- Create: `ferryman/prices.py`
- Modify: `config.example.toml`（文末追加 `[prices.glm]` 示例）
- Test: `tests/test_prices.py`

**Interfaces:**
- Consumes: 无（独立读 `~/ferryman/config.toml`，与 `ferry.load_config` 同模式）
- Produces（后续任务依赖的精确签名）:
  - `@dataclass(frozen=True) PriceVersion(effective_from: str, p_in: float, p_cache: float | None, p_out: float)`
  - `@dataclass(frozen=True) PriceBook(key: str, unit: str, per: int, versions: tuple[PriceVersion, ...])`，方法 `at(ts: float) -> PriceVersion | None`
  - `load_prices(path: Path | None = None) -> dict[str, PriceBook]`
  - `price_tag(book_key: str, pv: PriceVersion) -> str`（返回 `"{key}@{effective_from}"`）

- [ ] **Step 1: 写失败测试**

```python
# tests/test_prices.py
"""价格表：TOML 解析、版本选择、缺省 p_cache、版本标签。"""
from datetime import datetime, timezone
from pathlib import Path

import pytest

from ferryman.prices import load_prices, price_tag

TOML = """
[prices.glm]
unit = "智谱积分"
per = 10000

[[prices.glm.versions]]
effective_from = "2026-09-01"
p_in = 6.9
p_cache = 1.7
p_out = 24

[[prices.glm.versions]]
effective_from = "2026-09-17"
p_in = 6.9
p_cache = 1.7
p_out = 24

[prices.nocache]
unit = "元"
per = 1000000

[[prices.nocache.versions]]
effective_from = "2026-09-01"
p_in = 1.0
p_out = 2.0
"""

D16 = datetime(2026, 9, 16, 12, 0, tzinfo=timezone.utc).timestamp()
D17 = datetime(2026, 9, 17, 12, 0, tzinfo=timezone.utc).timestamp()


@pytest.fixture
def books(tmp_path: Path) -> dict:
    p = tmp_path / "config.toml"
    p.write_text(TOML, encoding="utf-8")
    return load_prices(path=p)


def test_missing_file_is_empty(tmp_path):
    assert load_prices(path=tmp_path / "nope.toml") == {}


def test_version_selection(books):
    glm = books["glm"]
    assert glm.per == 10_000 and glm.unit == "智谱积分"
    assert glm.at(D16).effective_from == "2026-09-01"   # 生效日前一天 → 旧版
    assert glm.at(D17).effective_from == "2026-09-17"   # 生效日起 → 新版
    assert glm.at(0) is None                            # 早于一切版本


def test_p_cache_optional(books):
    v = books["nocache"].versions[0]
    assert v.p_cache is None                            # 缺省 = 无缓存经济


def test_price_tag(books):
    assert price_tag("glm", books["glm"].at(D17)) == "glm@2026-09-17"
```

- [ ] **Step 2: 跑测试确认失败**

Run: `uv run pytest tests/test_prices.py -v`
Expected: FAIL（`ModuleNotFoundError: ferryman.prices`）

- [ ] **Step 3: 实现**

```python
# ferryman/prices.py
"""价格表：~/ferryman/config.toml [prices.*] → 版本化单价（设计 §1.1，Q1/Q16）。

- 每条流水钉死记账时的 price_tag（"key@effective_from"），改价不重算旧账；
- p_cache 允许缺省（None = 无缓存经济：策略计算器拒算，report 标注不可算）。
"""

from __future__ import annotations

import tomllib
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path


@dataclass(frozen=True)
class PriceVersion:
    effective_from: str            # "YYYY-MM-DD"，生效日 UTC 零点起
    p_in: float
    p_cache: float | None          # None = 未公布/无缓存价
    p_out: float


@dataclass(frozen=True)
class PriceBook:
    key: str                       # 对应 [prices.<key>]
    unit: str                      # 自述单位（如 "智谱积分"）
    per: int                       # 每 N token 一个计价块（万=10000，M=1000000）
    versions: tuple[PriceVersion, ...]   # effective_from 升序

    def at(self, ts: float) -> PriceVersion | None:
        best = None
        for v in self.versions:      # 升序，取最后一个 effective_from ≤ ts
            day = datetime.strptime(v.effective_from, "%Y-%m-%d") \
                .replace(tzinfo=timezone.utc).timestamp()
            if day <= ts:
                best = v
        return best


def price_tag(book_key: str, pv: PriceVersion) -> str:
    return f"{book_key}@{pv.effective_from}"


def load_prices(path: Path | None = None) -> dict[str, PriceBook]:
    """读 [prices.*]；无文件/无节 → 空 dict。"""
    p = path or Path.home() / "ferryman" / "config.toml"
    if not p.exists():
        return {}
    data = tomllib.loads(p.read_text(encoding="utf-8"))
    books: dict[str, PriceBook] = {}
    for key, blk in (data.get("prices") or {}).items():
        vers = []
        for vb in blk.get("versions", []):
            vers.append(PriceVersion(
                effective_from=str(vb["effective_from"]),
                p_in=float(vb["p_in"]),
                p_cache=(float(vb["p_cache"])
                         if vb.get("p_cache") is not None else None),
                p_out=float(vb["p_out"]),
            ))
        vers.sort(key=lambda v: v.effective_from)
        books[key] = PriceBook(key=key, unit=str(blk.get("unit", "")),
                               per=int(blk.get("per", 10_000)),
                               versions=tuple(vers))
    return books
```

`config.example.toml` 文末追加：

```toml
[prices.glm]
# 费用账本（T41）：GLM Coding Plan 积分系数（2026-09-17 实测口径，
# 见 docs/20260917_1630_GLM缓存TTL实测与心跳保温可行性_实验报告.md §4）
unit = "智谱积分"
per = 10000                # 每万 token 计价

[[prices.glm.versions]]
effective_from = "2026-09-17"
p_in = 6.9
p_cache = 1.7              # 不写此键 = 无缓存价：策略计算器拒算、report 标注不可算
p_out = 24
```

- [ ] **Step 4: 跑测试确认通过**

Run: `uv run pytest tests/test_prices.py -v`
Expected: 4 PASS

- [ ] **Step 5: 提交**

```bash
git add ferryman/prices.py tests/test_prices.py config.example.toml
git commit -m "feat(prices): T41 价格表模块——config 版本化三价+缺省 p_cache（4 用例）"
```

---

### Task 2: 账本模块 accounts.py

**Files:**
- Create: `ferryman/accounts.py`
- Test: `tests/test_accounts.py`

**Interfaces:**
- Consumes: 无
- Produces:
  - `SCHEMA_V = 1`，`KINDS = ("handoff", "beat", "block", "inject", "bypass", "window")`
  - `class Accounts(data_dir: Path)`：
    - `record(kind: str, *, ts: float | None = None, **fields) -> dict`（白名单校验，落 `accounts/YYYYMM.jsonl` 一行，返回写入条目）
    - `read(*, since=None, until=None, project=None, session=None, lineage=None, kind=None) -> list[dict]`
  - 通用字段（全部科目可带）：`ts / ts_iso / kind / v / agent / session_id / lineage_id / project`；各科目必填字段见 `_KIND_FIELDS`（window: `opened_ts, closed_ts, dur_s, prefix_tokens, close_reason` 等）

- [ ] **Step 1: 写失败测试**

```python
# tests/test_accounts.py
"""账本：append-only、按月滚动、白名单（隐私不变量）、过滤读取。"""
import time

import pytest

from ferryman.accounts import Accounts

AUG = time.mktime(time.strptime("2026-08-15 12:00:00", "%Y-%m-%d %H:%M:%S"))
SEP = time.mktime(time.strptime("2026-09-16 12:00:00", "%Y-%m-%d %H:%M:%S"))
MID = time.mktime(time.strptime("2026-09-01 12:00:00", "%Y-%m-%d %H:%M:%S"))  # 落在 (AUG, SEP) 内，since/until 断言与运行日期解耦


def rec_handoff(acc, ts=None, sid="s1", outcome="fresh", **kw):
    return acc.record("handoff", ts=ts, agent="cc", session_id=sid,
                      lineage_id=f"L-{sid}", project="C:/proj",
                      provider="glm", model="glm-5.3",
                      price_ver="glm@2026-09-17", prompt_tokens=100,
                      completion_tokens=50, outcome=outcome, wall_s=1.2, **kw)


def test_record_and_read(tmp_path):
    acc = Accounts(tmp_path)
    e = rec_handoff(acc)
    assert e["kind"] == "handoff" and e["v"] == 1 and e["ts"] > 0
    rows = acc.read()
    assert len(rows) == 1 and rows[0]["prompt_tokens"] == 100
    assert rows[0]["ts_iso"]  # 人读时间戳非空


def test_append_only_two_lines(tmp_path):
    acc = Accounts(tmp_path)
    rec_handoff(acc, sid="s1"); rec_handoff(acc, sid="s2")
    f = tmp_path / "accounts" / (time.strftime("%Y%m") + ".jsonl")
    assert len(f.read_text(encoding="utf-8").splitlines()) == 2   # 只增不改


def test_monthly_rollover(tmp_path):
    acc = Accounts(tmp_path)
    rec_handoff(acc, ts=AUG); rec_handoff(acc, ts=SEP)
    files = sorted(p.name for p in (tmp_path / "accounts").glob("*.jsonl"))
    assert files == ["202608.jsonl", "202609.jsonl"]


def test_privacy_whitelist(tmp_path):
    acc = Accounts(tmp_path)
    with pytest.raises(ValueError, match="隐私"):
        acc.record("handoff", agent="cc", session_id="s", lineage_id="L", project="p",
                   provider="x", model="m", price_ver=None,
                   prompt_tokens=1, completion_tokens=1, outcome="fresh",
                   wall_s=0.1, content="用户原话不应入账")
    with pytest.raises(ValueError, match="缺必填"):
        acc.record("block", agent="cc", session_id="s")   # 缺 prefix_tokens/idle_s


def test_unknown_kind(tmp_path):
    acc = Accounts(tmp_path)
    with pytest.raises(ValueError, match="未知科目"):
        acc.record("wage", agent="cc", session_id="s")


def test_filters(tmp_path):
    acc = Accounts(tmp_path)
    rec_handoff(acc, sid="s1", ts=MID)
    acc.record("block", ts=MID, agent="cc", session_id="s2", lineage_id="L2",
               project="C:/q", prefix_tokens=150_000, idle_s=2100)
    assert len(acc.read(kind="block")) == 1
    assert acc.read(kind="block")[0]["prefix_tokens"] == 150_000
    assert len(acc.read(session="s1")) == 1
    assert len(acc.read(project="C:/q")) == 1
    assert len(acc.read(since=SEP + 1)) == 0
    assert len(acc.read(until=AUG + 1)) == 0
    assert len(acc.read(lineage="L2")) == 1


def test_reserved_fields_stamped_not_passable(tmp_path):
    acc = Accounts(tmp_path)
    with pytest.raises(ValueError, match="保留字段"):
        rec_handoff(acc, v=2)
    with pytest.raises(ValueError, match="保留字段"):
        rec_handoff(acc, ts_iso="2020-01-01")


def test_read_skips_corrupt_tail_line(tmp_path):
    acc = Accounts(tmp_path)
    rec_handoff(acc)
    f = tmp_path / "accounts" / (time.strftime("%Y%m") + ".jsonl")
    with open(f, "a", encoding="utf-8") as fh:      # 模拟崩溃撕裂的尾行
        fh.write('{"v": 1, "kind": "handoff", TRUN')
    assert len(acc.read()) == 1                      # 好行仍在，坏行被跳过
```

- [ ] **Step 2: 跑测试确认失败**

Run: `uv run pytest tests/test_accounts.py -v`
Expected: FAIL（`ModuleNotFoundError: ferryman.accounts`）

- [ ] **Step 3: 实现**

```python
# ferryman/accounts.py
"""账本：accounts/YYYYMM.jsonl append-only 流水（ADR-0002，设计 §1.2/§1.3）。

铁律：
- 只记元数据与金额（字段白名单），永不落消息内容——隐私不变量；
- append-only，report 只读不改写；节省在 report 层由版本化公式重算；
- 按月滚动（本地时区）；每行带 schema 版本 v。
"""

from __future__ import annotations

import json
import threading
import time
from datetime import datetime
from pathlib import Path

SCHEMA_V = 1

_KIND_FIELDS: dict[str, set[str]] = {
    # 摆渡的每次模型调用（含分块/重试/失败）
    "handoff": {"provider", "model", "price_ver", "prompt_tokens",
                "completion_tokens", "outcome", "wall_s"},
    # 心跳——T41 仅占坑（执行器未授权，无生产者）
    "beat": {"provider", "model", "price_ver", "prefix_tokens", "cache_read",
             "hit", "cost_pred", "cost_actual"},
    "block": {"prefix_tokens", "idle_s"},
    "inject": {"tokens", "handoff_id"},
    "bypass": {"prefix_tokens"},
    "window": {"opened_ts", "closed_ts", "dur_s", "prefix_tokens", "close_reason"},
}
_COMMON = {"ts", "ts_iso", "kind", "v", "agent", "session_id", "lineage_id", "project"}
KINDS = tuple(_KIND_FIELDS)


class Accounts:
    def __init__(self, data_dir: Path) -> None:
        self.dir = data_dir / "accounts"
        self.dir.mkdir(parents=True, exist_ok=True)
        self._lock = threading.Lock()

    def record(self, kind: str, *, ts: float | None = None, **fields) -> dict:
        if kind not in _KIND_FIELDS:
            raise ValueError(f"未知科目: {kind!r}（可选 {KINDS}）")
        bad = set(fields) - _COMMON - _KIND_FIELDS[kind]
        if bad:
            raise ValueError(f"账本不落这些字段（隐私不变量）: {sorted(bad)}")
        reserved = {"v", "ts_iso"} & set(fields)
        if reserved:
            raise ValueError(f"保留字段由模块盖章，不可传入: {sorted(reserved)}")
        missing = _KIND_FIELDS[kind] - set(fields)
        if missing:
            raise ValueError(f"{kind} 缺必填字段: {sorted(missing)}")
        ts = ts if ts is not None else time.time()
        entry = {"v": SCHEMA_V, "kind": kind, "ts": round(ts, 3),
                 "ts_iso": datetime.fromtimestamp(ts).astimezone()
                 .strftime("%Y-%m-%dT%H:%M:%S%z"),
                 "agent": fields.pop("agent", ""),
                 "session_id": fields.pop("session_id", ""),
                 "lineage_id": fields.pop("lineage_id", ""),
                 "project": fields.pop("project", "")}
        entry.update(fields)
        fname = datetime.fromtimestamp(ts).strftime("%Y%m") + ".jsonl"
        with self._lock:
            with open(self.dir / fname, "a", encoding="utf-8") as f:
                f.write(json.dumps(entry, ensure_ascii=False) + "\n")
        return entry

    def read(self, *, since: float | None = None, until: float | None = None,
             project: str | None = None, session: str | None = None,
             lineage: str | None = None, kind: str | None = None) -> list[dict]:
        out: list[dict] = []
        for f in sorted(self.dir.glob("*.jsonl")):
            for i, line in enumerate(f.read_text(encoding="utf-8").splitlines(), 1):
                if not line.strip():
                    continue
                try:
                    e = json.loads(line)
                except ValueError:      # 崩溃撕裂的尾行：跳过但告警可见（不静默丢账）
                    print(f"[accounts] 跳过损坏行 {f.name}:{i}", flush=True)
                    continue
                if since is not None and e.get("ts", 0) < since:
                    continue
                if until is not None and e.get("ts", 0) > until:
                    continue
                if project and e.get("project") != project:
                    continue
                if session and e.get("session_id") != session:
                    continue
                if lineage and e.get("lineage_id") != lineage:
                    continue
                if kind and e.get("kind") != kind:
                    continue
                out.append(e)
        return out
```

- [ ] **Step 4: 跑测试确认通过**

Run: `uv run pytest tests/test_accounts.py -v`
Expected: 6 PASS

- [ ] **Step 5: 提交**

```bash
git add ferryman/accounts.py tests/test_accounts.py
git commit -m "feat(account): T41 账本模块——六科目 append-only 流水+月滚动+隐私白名单（6 用例）"
```

---

### Task 3: 策略计算器 policy.py

**Files:**
- Create: `ferryman/policy.py`
- Test: `tests/test_policy.py`

**Interfaces:**
- Consumes: `ferryman.prices.PriceBook, PriceVersion`
- Produces:
  - `class NoCachePriceError(ValueError)`
  - `@dataclass(frozen=True) HeartbeatPolicy(ttl_s, tau_s, per_beat_cost, expire_cost, worthwhile_cap_s, grace_s, min_prefix_tokens)`
  - `heartbeat_policy(book: PriceBook, pv: PriceVersion, ttl_s: float, prefix_tokens: int, *, beat_out_tokens: int = 300, safety: float = 0.8, grace_s: float = 300.0, min_prefix_tokens: int = 30_000) -> HeartbeatPolicy`
  - `tier_for(pol: HeartbeatPolicy, wait_s: float) -> str`（`"none" | "beat" | "expire"`）
  - `strategy_costs(book, pv, ttl_s, wait_s, prefix_tokens, *, beat_out_tokens=300, compact_ratio=0.25) -> dict`（键 `none/beat/expire/expire_compact`，单位=价格表自述单位）

- [ ] **Step 1: 写失败测试**（断言数字直接取自实验报告 §4，GLM 积分、S=150k、T=10min）

```python
# tests/test_policy.py
"""策略计算器：闭式公式，断言锚定实验报告 §4 的 GLM 实测数字。"""
import math

import pytest

from ferryman.policy import (
    HeartbeatPolicy, NoCachePriceError, heartbeat_policy, strategy_costs, tier_for,
)
from ferryman.prices import PriceBook, PriceVersion

BOOK = PriceBook(key="glm", unit="智谱积分", per=10_000,
                 versions=(PriceVersion("2026-09-17", 6.9, 1.7, 24),))
PV = BOOK.versions[0]


def test_glm_150k_report_numbers():
    pol = heartbeat_policy(BOOK, PV, ttl_s=600, prefix_tokens=150_000)
    assert pol.tau_s == pytest.approx(480)                      # 0.8×10min
    assert pol.per_beat_cost == pytest.approx(26.2, abs=0.1)    # 25.5 + 0.72
    assert pol.expire_cost == pytest.approx(103.5)              # 15×6.9
    assert pol.worthwhile_cap_s == pytest.approx(1428, rel=0.03)  # ~24min（报告口径 25–28 带）
    assert pol.grace_s == 300 and pol.min_prefix_tokens == 30_000


def test_small_prefix_shrinks_cap():
    big = heartbeat_policy(BOOK, PV, 600, 150_000)
    small = heartbeat_policy(BOOK, PV, 600, 30_000)
    assert small.worthwhile_cap_s < big.worthwhile_cap_s   # 300×P_out 占比变大


def test_no_cache_price_refuses():
    nb = PriceBook("x", "元", 1_000_000, (PriceVersion("2026-09-01", 1.0, None, 2.0),))
    with pytest.raises(NoCachePriceError):
        heartbeat_policy(nb, nb.versions[0], 600, 100_000)


def test_bad_ttl():
    with pytest.raises(ValueError, match="ttl_s"):
        heartbeat_policy(BOOK, PV, 0, 100_000)


def test_tiers():
    pol = heartbeat_policy(BOOK, PV, 600, 150_000)
    assert tier_for(pol, 400) == "none"       # ≤τ：缓存必活
    assert tier_for(pol, 900) == "beat"       # τ..cap
    assert tier_for(pol, 10_000) == "expire"  # >cap


def test_strategy_costs():
    sc = strategy_costs(BOOK, PV, ttl_s=600, wait_s=900, prefix_tokens=150_000)
    assert sc["none"] == pytest.approx(103.5)          # 900s > TTL 600s → 全量重付
    assert sc["beat"] == pytest.approx(2 * 26.2, abs=0.2)   # ⌈900/480⌉ = 2 跳
    assert sc["expire"] == pytest.approx(103.5)
    assert sc["expire_compact"] == pytest.approx(0.25 * 103.5)
    sc2 = strategy_costs(BOOK, PV, 600, 300, 150_000)
    assert sc2["none"] == 0.0                          # 300s ≤ TTL：白等
    assert sc2["beat"] == pytest.approx(1 * 26.2, abs=0.1)
```

- [ ] **Step 2: 跑测试确认失败**

Run: `uv run pytest tests/test_policy.py -v`
Expected: FAIL（`ModuleNotFoundError: ferryman.policy`）

- [ ] **Step 3: 实现**

```python
# ferryman/policy.py
"""策略计算器：价格表 + 实测 TTL → 心跳参数与分档（闭式推导，设计 §2）。

公式出处：docs/20260917_1630 实验报告 §4；金额单位 = 价格表自述单位（按 per 归一）。
p_cache 缺省 → NoCachePriceError（宁可不算，不造数，Q16）。
"""

from __future__ import annotations

import math
from dataclasses import dataclass

from .prices import PriceBook, PriceVersion


class NoCachePriceError(ValueError):
    """无 p_cache：无缓存经济，拒绝推导心跳参数。"""


@dataclass(frozen=True)
class HeartbeatPolicy:
    ttl_s: float
    tau_s: float                 # 0.8 × T
    per_beat_cost: float         # 单次心跳成本
    expire_cost: float           # 放任过期代价
    worthwhile_cap_s: float      # 心跳划算的等待上限
    grace_s: float
    min_prefix_tokens: int


def heartbeat_policy(book: PriceBook, pv: PriceVersion, ttl_s: float,
                     prefix_tokens: int, *, beat_out_tokens: int = 300,
                     safety: float = 0.8, grace_s: float = 300.0,
                     min_prefix_tokens: int = 30_000) -> HeartbeatPolicy:
    if ttl_s <= 0:
        raise ValueError("ttl_s 须 > 0（先跑 experiments/cache-ttl 套件实测）")
    if pv.p_cache is None:
        raise NoCachePriceError(f"{book.key} 无 p_cache，拒绝推导心跳参数")
    pin = prefix_tokens / book.per * pv.p_in
    pcache = prefix_tokens / book.per * pv.p_cache
    per_beat = pcache + beat_out_tokens / book.per * pv.p_out
    tau = safety * ttl_s
    cap = tau * (pin - pcache) / per_beat if per_beat > 0 else math.inf
    return HeartbeatPolicy(ttl_s=ttl_s, tau_s=tau, per_beat_cost=per_beat,
                           expire_cost=pin, worthwhile_cap_s=cap,
                           grace_s=grace_s, min_prefix_tokens=min_prefix_tokens)


def tier_for(pol: HeartbeatPolicy, wait_s: float) -> str:
    if wait_s <= pol.tau_s:
        return "none"
    if wait_s <= pol.worthwhile_cap_s:
        return "beat"
    return "expire"


def strategy_costs(book: PriceBook, pv: PriceVersion, ttl_s: float,
                   wait_s: float, prefix_tokens: int, *,
                   beat_out_tokens: int = 300,
                   compact_ratio: float = 0.25) -> dict:
    """缓存经济学三策略 + compact 变体；handoff 策略由 report 按账本均值另算（§3.4）。

    compact_ratio 为公式内常数（v1 取 0.25，复算附录可见）。
    """
    pol = heartbeat_policy(book, pv, ttl_s, prefix_tokens,
                           beat_out_tokens=beat_out_tokens)
    beats = math.ceil(wait_s / pol.tau_s) if wait_s > 0 else 0
    return {"none": pol.expire_cost if wait_s > ttl_s else 0.0,
            "beat": beats * pol.per_beat_cost,
            "expire": pol.expire_cost,
            "expire_compact": prefix_tokens * compact_ratio / book.per * pv.p_in}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `uv run pytest tests/test_policy.py -v`
Expected: 6 PASS

- [ ] **Step 5: 提交**

```bash
git add ferryman/policy.py tests/test_policy.py
git commit -m "feat(policy): T41 策略计算器——τ/单跳/上限/分档闭式推导，锚定实验数字（6 用例）"
```

---

### Task 4: 接线 handoff 记账（FerryWorker）+ Harness 扩展

**Files:**
- Modify: `ferryman/daemon.py`（FerryWorker 增 `accounts` 参数与记账；serve() 构造 Accounts 并下传）
- Modify: `tests/helpers.py`（Harness 构造 Accounts，注入 FerryDaemon/FerryWorker——为 Task 5/6 铺路）
- Test: `tests/test_accounts.py` 追加集成用例

**Interfaces:**
- Consumes: `Accounts.record`（Task 2）、`load_prices/price_tag`（Task 1）
- Produces: `FerryWorker(cfg, store, tasks, accounts=None)`（第 4 参可选）；`serve()` 内 `accounts = Accounts(cfg.data_dir)` 传入 FerryWorker 与 FerryDaemon（FerryDaemon 的参数在 Task 5 加，本任务先传 store 侧）
- 摆渡完成（fresh/skeleton/failed）各记一条 `handoff` 流水：`outcome ∈ {"fresh","skeleton","failed"}`，token 数取 `meta["usage"]`（失败=0——墙钟超时线程被弃，usage 不可得，注释说明）

- [ ] **Step 1: 写失败测试**（追加到 tests/test_accounts.py；文件头补 `from helpers import write_session`）

```python
def test_ferry_completion_books_handoff(h):
    """端到端：合成会话达总结阈值 → fake 摆渡 → 账本出现 handoff 流水。"""
    write_session(h.projects, "acct1", "C:/proj")
    assert h.wait_for(lambda: any(e["kind"] == "handoff"
                                  for e in h.accounts.read()))
    e = h.accounts.read(kind="handoff")[0]
    assert e["session_id"] == "acct1"
    assert e["lineage_id"].endswith("acct1.jsonl")     # lineage = 归一化 transcript 路径
    assert e["provider"] == "fake"
    assert e["outcome"] in ("fresh", "skeleton", "failed")
    assert "content" not in e and "md" not in e        # 隐私不变量：无正文
```

（`h` fixture 来自 conftest；`write_session` 来自 helpers。）

- [ ] **Step 2: 跑测试确认失败**

Run: `uv run pytest tests/test_accounts.py::test_ferry_completion_books_handoff -v`
Expected: FAIL（`AttributeError: 'Harness' object has no attribute 'accounts'`）

- [ ] **Step 3: 实现**

`tests/helpers.py` Harness.__init__ 内（`self.store = Store(...)` 之后）加：

```python
from ferryman.accounts import Accounts
self.accounts = Accounts(Path(cfg.data_dir))
```

本任务只改 FerryWorker 一侧的构造（第 4 参）：`FerryWorker(cfg, self.store, self.tasks, accounts=self.accounts)`。FerryDaemon 的 accounts 参数 Task 5 才加——Harness 的 FerryDaemon 构造留到 Task 5 一并改，避免本任务中间态 TypeError。

`ferryman/daemon.py`：

```python
# 顶部 import 区
from .accounts import Accounts
from .prices import load_prices, price_tag

class FerryWorker(threading.Thread):
    def __init__(self, cfg, store, tasks, accounts: Accounts | None = None):
        ...
        self.accounts = accounts    # None = 不记账（旧调用/测试零改动）
```

`_do` 成功分支 `self.store.save_handoff(...)` 之后加 `self._book_handoff(item, agent, sid, meta, "fresh")`；except 分支 `self._save_skeleton(...)` 之前加 `self._book_handoff(item, agent, sid, {}, "failed")`，`_save_skeleton` 调用之后加 `self._book_handoff(item, agent, sid, {}, "skeleton")`。新增方法：

```python
    def _book_handoff(self, item: dict, agent: str, sid: str,
                      meta: dict, outcome: str) -> None:
        """摆渡记账：usage 失败记 0（墙钟超时线程被弃，usage 不可得）。
        记账永不弄断摆渡——任何异常吞为警告（骨架兜底不变量优先）。"""
        if self.accounts is None:
            return
        try:
            from .ledger import _norm_path
            books = load_prices()
            provider = self.cfg.ferry_provider
            price_ver = None
            book = books.get(provider)
            if book is not None:
                pv = book.at(time.time())
                price_ver = price_tag(provider, pv) if pv else None
            usage = meta.get("usage") or {}
            self.accounts.record(
                "handoff", agent=agent, session_id=sid,
                lineage_id=_norm_path(item["transcript_path"]),
                project=item.get("cwd", ""), provider=provider,
                model=str(meta.get("model", "")), price_ver=price_ver,
                prompt_tokens=int(usage.get("prompt_tokens", 0)),
                completion_tokens=int(usage.get("completion_tokens", 0)),
                outcome=outcome, wall_s=float(meta.get("wall_s", 0.0)))
        except Exception as e:  # noqa: BLE001 — 坏价格 TOML 等记账故障不得弄断摆渡
            print(f"[account] handoff 记账失败（忽略，摆渡不受影响）: {e}", flush=True)
```

`serve()` 中 `store = Store(cfg.data_dir)` 之后加 `accounts = Accounts(cfg.data_dir)`，`FerryWorker(cfg, store, tasks)` 改 `FerryWorker(cfg, store, tasks, accounts)`。

- [ ] **Step 4: 跑全量确认通过（120+7 用例）**

Run: `uv run pytest`
Expected: 全 PASS（FerryDaemon 第 5 参 Task 5 才加，Harness 提前传会 TypeError——因此本任务 Harness 先只改 FerryWorker 侧，FerryDaemon 侧留到 Task 5 一并改）

- [ ] **Step 5: 提交**

```bash
git add ferryman/daemon.py tests/helpers.py tests/test_accounts.py
git commit -m "feat(account): T41 摆渡记账接线——fresh/skeleton/failed 三态入账（7 用例）"
```

---

### Task 5: 接线 gate/bypass/inject 记账（FerryDaemon）

**Files:**
- Modify: `ferryman/server.py`
- Modify: `tests/helpers.py`（FerryDaemon 构造补 `accounts=self.accounts`）
- Test: `tests/test_accounts.py` 追加

**Interfaces:**
- Consumes: `Accounts.record`；`ferryman.ledger._norm_path`；`ferryman.extract.token_estimate`
- Produces: `FerryDaemon(cfg, ledger, store, enqueue_ferry, accounts=None, started_at=None)`（第 5 参可选，`tests/test_gate.py:26` 与 `test_integration.py` 的既有 3/4 参调用不破）
- 记账点：强续 bypass（分支 0）；block 分支 5 与分支 6（记 `prefix_tokens=st.peak_ctx, idle_s`）；restore 单候选注入（记 `tokens=token_estimate(ctx), handoff_id`；多候选只列清单不注入、不记）

- [ ] **Step 1: 写失败测试**（追加到 tests/test_accounts.py）

```python
def _stale_blocked_session(h):
    """造一个已达拦截阈值、有有效交接的会话（enforce 下必被拦）。"""
    write_session(h.projects, "acct2", "C:/proj")
    assert h.wait_for(lambda: h.store.restore_candidates("cc", "C:/proj"))
    body = {"agent": "cc", "session_id": "acct2",
            "transcript_path": str(h.projects / "C--proj" / "acct2.jsonl"),
            "cwd": "C:/proj", "prompt": "继续干活"}
    return body


def test_block_books_entry(h):
    body = _stale_blocked_session(h)
    r = h.gate(body)
    if r["decision"] == "allow":        # observe/骨架时序兜底：等到 block 为止
        assert h.wait_for(lambda: h.gate(body)["decision"] == "block")
    e = h.accounts.read(kind="block")[-1]
    assert e["session_id"] == "acct2"
    assert e["prefix_tokens"] >= MIN_CTX or e["prefix_tokens"] == 0  # peak_ctx 尽力而为
    assert e["idle_s"] > 0


def test_bypass_books_entry(h):
    body = _stale_blocked_session(h)
    r = h.gate({**body, "prompt": "强续 无论如何继续"})
    assert r["decision"] == "allow"
    e = h.accounts.read(kind="bypass")[-1]
    assert e["session_id"] == "acct2"


def test_restore_books_inject(h):
    body = _stale_blocked_session(h)
    assert h.wait_for(lambda: h.gate(body)["decision"] == "block")
    r = h.get(f"/restore?agent=cc&cwd=C:/proj&session_id=newsid")
    assert r["context"]
    e = h.accounts.read(kind="inject")[-1]
    assert e["session_id"] == "newsid"
    assert e["tokens"] > 0 and e["handoff_id"]
    blk = h.accounts.read(kind="block")[-1]        # R9：inject 与 block 同谱系（Q7 因果链）
    assert e["lineage_id"] == blk["lineage_id"]
```

（文件头补 `from helpers import MIN_CTX, write_session`——`MIN_CTX` 已在 helpers 导出。）

- [ ] **Step 2: 跑测试确认失败**

Run: `uv run pytest tests/test_accounts.py -k "block or bypass or inject" -v`
Expected: FAIL（read 返回空列表）

- [ ] **Step 3: 实现**

`ferryman/server.py`：

```python
# FerryDaemon.__init__ 签名与体
def __init__(self, cfg, ledger, store, enqueue_ferry, accounts=None,
             started_at: float | None = None) -> None:
    ...
    self.accounts = accounts          # None = 不记账（旧测试零改动）
```

新增私有方法：

```python
    def _acct(self, kind: str, st: SessionState | None = None, *,
              agent: str = "", session_id: str = "",
              lineage_id: str | None = None, **fields) -> None:
        """记账薄封装：st 优先（lineage 用归一化 transcript 路径），无 st 用显式参数。
        lineage_id/project 可显式覆盖（inject 需按交接源会话解析谱系，R9）。"""
        if self.accounts is None:
            return
        try:
            from .ledger import _norm_path
            if st is not None:
                agent, session_id = st.agent, st.session_id
                if lineage_id is None:
                    lineage_id = _norm_path(st.transcript_path) if st.transcript_path else session_id
                project = st.cwd or ""
            else:
                if lineage_id is None:
                    lineage_id = session_id      # 无台账线索：lineage 退化为 session 自身
                project = ""
            if "project" in fields:
                project = str(fields.pop("project"))
            self.accounts.record(kind, agent=agent, session_id=session_id,
                                 lineage_id=lineage_id, project=project, **fields)
        except Exception as e:  # noqa: BLE001 — 记账永不弄断闸门
            print(f"[account] {kind} 记账失败（忽略）: {e}", flush=True)
```

gate() 各记账点：
- 魔法前缀分支（`self.stats.bypass += 1` 处）：

```python
        if prompt.startswith("强续") or prompt.startswith("!!"):
            self.stats.bypass += 1
            self._acct("bypass", self.ledger.get(agent, session_id),
                       agent=agent, session_id=session_id,
                       prefix_tokens=(self.ledger.get(agent, session_id).peak_ctx
                                      if self.ledger.get(agent, session_id) else 0))
            return {"decision": "allow", "reason": "bypass"}
```

（写成一次取 st 的局部变量形式，避免三次 get。）

- 分支 5（`self.stats.blocks += 1` 后）与分支 6（`self.stats.blocks += 1` 后）各加：

```python
            self._acct("block", st, prefix_tokens=st.peak_ctx, idle_s=round(idle, 1))
```

restore() 单候选注入路径（`self.store.mark_injected(...)` 之前）加——谱系按交接**源**会话解析（Q7 因果链：inject 与 block 同谱系，R9）：

```python
        from .extract import token_estimate
        from .ledger import _norm_path
        st_src = self.ledger.get(agent, newest["session_id"])
        _lin = (_norm_path(st_src.transcript_path)
                if st_src and st_src.transcript_path else session_id)
        self._acct("inject", None, agent=agent, session_id=session_id,
                   lineage_id=_lin, project=(st_src.cwd or "" if st_src else ""),
                   tokens=token_estimate(ctx), handoff_id=newest["handoff_id"])
```

`ferryman/daemon.py` serve()（Task 4 已建 `accounts` 实例）——FerryDaemon 构造传参（R8，生产记账接线）：

```python
    daemon = FerryDaemon(cfg, ledger, store, enqueue, accounts=accounts,
                         started_at=started_at)
```

`tests/helpers.py`：`FerryDaemon(cfg, self.ledger, self.store, enqueue)` → `FerryDaemon(cfg, self.ledger, self.store, enqueue, accounts=self.accounts)`（Task 4 已建 `self.accounts`）。

- [ ] **Step 4: 跑全量确认通过**

Run: `uv run pytest`
Expected: 全 PASS

- [ ] **Step 5: 提交**

```bash
git add ferryman/server.py tests/helpers.py tests/test_accounts.py
git commit -m "feat(account): T41 闸门记账接线——block/bypass/inject 三科目入账（10 用例）"
```

---

### Task 6: 接线 window 科目（子代理等待窗口）

**Files:**
- Modify: `ferryman/server.py`
- Modify: `tests/helpers.py`（Harness 加 `sub()` 方法）
- Test: `tests/test_accounts.py` 追加

**Interfaces:**
- Consumes: `Accounts.record`；`Ledger.subagent_event`（既有）
- Produces: FerryDaemon 内存窗口表 `_windows: dict[tuple[str, str], dict]`；闭窗记 `window` 流水（`close_reason ∈ {"subagents_done", "prompt"}`）；Harness `sub(body: dict) -> dict`（POST /subagent）
- 语义（设计 §3.1 + 词汇表"等待窗口"）：开窗=首个子代理 start 且主会话无开窗；闭窗=stop 后计数归零（`subagents_done`）或该会话 gate 来讯（`prompt`）。daemon 重启丢内存窗口——与 PendingTable 同级可接受，注释说明。泄漏兜底沿用台账 1h 规则（不闭窗则不记，宁缺毋错）。

- [ ] **Step 1: 写失败测试**（追加到 tests/test_accounts.py）

```python
def test_window_books_on_subagent_cycle(h):
    h.sub({"event": "start", "agent": "cc", "session_id": "acct3"})
    h.sub({"event": "start", "agent": "cc", "session_id": "acct3"})   # 嵌套：计数 2
    h.sub({"event": "stop", "agent": "cc", "session_id": "acct3"})
    h.sub({"event": "stop", "agent": "cc", "session_id": "acct3"})    # 计数归零 → 闭窗
    assert h.wait_for(lambda: h.accounts.read(kind="window"))
    e = h.accounts.read(kind="window")[0]
    assert e["session_id"] == "acct3"
    assert e["dur_s"] >= 0 and e["close_reason"] == "subagents_done"
    assert set(e) >= {"opened_ts", "closed_ts", "dur_s", "prefix_tokens"}


def test_window_closes_on_prompt(h):
    h.sub({"event": "start", "agent": "cc", "session_id": "acct4"})
    r = h.gate({"agent": "cc", "session_id": "acct4", "prompt": "人回来了"})
    assert r["decision"] == "allow"
    e = h.accounts.read(kind="window")[-1]
    assert e["close_reason"] == "prompt"
    # 窗已闭：后续 stop 不再产生第二条
    h.sub({"event": "stop", "agent": "cc", "session_id": "acct4"})
    assert len([x for x in h.accounts.read(kind="window")
                if x["session_id"] == "acct4"]) == 1
```

- [ ] **Step 2: 跑测试确认失败**

Run: `uv run pytest tests/test_accounts.py -k window -v`
Expected: FAIL（`AttributeError: 'Harness' object has no attribute 'sub'`）

- [ ] **Step 3: 实现**

`tests/helpers.py` Harness 加方法：

```python
    def sub(self, body: dict) -> dict:
        req = urllib.request.Request(
            f"http://127.0.0.1:{self.port}/subagent",
            data=json.dumps(body, ensure_ascii=False).encode("utf-8"),
            headers={"Authorization": f"Bearer {self.token}",
                     "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=5) as r:
            return json.loads(r.read().decode("utf-8"))
```

`ferryman/server.py` FerryDaemon：

```python
# __init__ 内
self._windows: dict[tuple[str, str], dict] = {}   # (agent, sid) → {"opened_ts": ...}

# 新增方法
    def _close_window(self, key: tuple[str, str], reason: str) -> None:
        w = self._windows.pop(key, None)
        if w is None or self.accounts is None:
            return
        agent, sid = key
        st = self.ledger.get(agent, sid)
        closed = now_s()
        self._acct("window", st, agent=agent, session_id=sid,
                   opened_ts=round(w["opened_ts"], 3), closed_ts=round(closed, 3),
                   dur_s=round(closed - w["opened_ts"], 1),
                   prefix_tokens=(st.peak_ctx if st else 0), close_reason=reason)
```

subagent() 内（`count = self.ledger.subagent_event(...)` 之后）：

```python
        key = (agent, session_id)
        if count > 0 and key not in self._windows:
            self._windows[key] = {"opened_ts": now_s()}
        if count == 0 and key in self._windows:
            self._close_window(key, "subagents_done")
```

gate() 内（`st = self.ledger.get(...)` 行之后、无台账早退 `if st is None` **之前**——闭窗判定用 body 的 session_id，台账 miss 也要闭）：

```python
        wk = (agent, session_id)     # 主会话来讯 = 等待提前结束（词汇表"等待窗口"）
        if wk in self._windows:
            self._close_window(wk, "prompt")
```

- [ ] **Step 4: 跑全量确认通过**

Run: `uv run pytest`
Expected: 全 PASS

- [ ] **Step 5: 提交**

```bash
git add ferryman/server.py tests/helpers.py tests/test_accounts.py
git commit -m "feat(account): T41 等待窗口入账——子代理计数闭窗+来讯闭窗（12 用例）"
```

---

### Task 7: report CLI + [heartbeat] 配置节

**Files:**
- Create: `ferryman/report.py`
- Modify: `ferryman/config.py`（HeartbeatCfg）
- Modify: `ferryman/__main__.py`（account 子命令）
- Modify: `config.example.toml`（[heartbeat] 预留节）
- Test: `tests/test_report.py`

**Interfaces:**
- Consumes: `Accounts.read`、`prices.load_prices`、`policy.strategy_costs/NoCachePriceError`、`config.load`
- Produces:
  - `SAVINGS_FORMULA = "v1"`
  - `handoff_cost(e: dict, books: dict[str, PriceBook]) -> float | None`（按流水钉死的 `price_ver` 折算：`prompt/per*p_in + completion/per*p_out`；无价格→None）
  - `savings_v1(entries: list[dict], books) -> dict`（键：`lineages`（每族系一行：counts + handoff_cost + gross + inject_cost + net）、`totals`、`unpriced`（provider 列表）、`formula`）
  - `strategy_table(window_entries, books, ttl_s, econ_key: str) -> dict`（每窗口四策略 + `best` + 跳过原因；无 p_cache/无 ttl → `{"skipped": [...]}`）
  - `run(args) -> int`（args 为 argparse namespace：`since/until/project/session/kind/json/provider`，日期串 "YYYY-MM-DD" 本地时区）
- 成效公式 v1（族系级，设计 §1.4）：`毛节省 = Σ_block prefix_tokens/per×(p_in−p_cache)`；`净节省 = 毛 − Σ_inject tokens/per×p_in − Σ_handoff_cost`。block 侧价格在 report 时指定（`--provider`，缺省=ferry_provider 的 book，再缺省=唯一 book；复算附录披露）——daemon 不知道被拦会话走的哪家 provider，此为 v1 已知简化，附录注明。
- bypass：不计节省，总数单列（对照组）。

- [ ] **Step 1: 写失败测试**

```python
# tests/test_report.py
"""report：族系账单、成效公式 v1、策略对比、复算附录。"""
import time

import pytest

from ferryman import report
from ferryman.accounts import Accounts
from ferryman.prices import load_prices

TOML = """
[prices.glm]
unit = "智谱积分"
per = 10000
[[prices.glm.versions]]
effective_from = "2026-09-01"
p_in = 6.9
p_cache = 1.7
p_out = 24
"""


@pytest.fixture
def env(tmp_path, monkeypatch):
    (tmp_path / "config.toml").write_text(TOML, encoding="utf-8")
    monkeypatch.setenv("FERRYMAN_CONFIG", str(tmp_path / "config.toml"))
    acc = Accounts(tmp_path / "data")
    # 族系 L1：一次 block(S=150k) + 一次 inject(2200) + 一次 handoff(50k in/2k out)
    acc.record("block", agent="cc", session_id="s1", lineage_id="L1",
               project="C:/p", prefix_tokens=150_000, idle_s=2100)
    acc.record("inject", agent="cc", session_id="s2", lineage_id="L1",
               project="C:/p", tokens=2200, handoff_id="h1")
    acc.record("handoff", agent="cc", session_id="s1", lineage_id="L1",
               project="C:/p", provider="glm", model="glm-5.3",
               price_ver="glm@2026-09-01", prompt_tokens=50_000,
               completion_tokens=2_000, outcome="fresh", wall_s=9.9)
    acc.record("bypass", agent="cc", session_id="s3", lineage_id="L2",
               project="C:/p", prefix_tokens=150_000)
    return acc, load_prices(tmp_path / "config.toml"), tmp_path


def test_savings_v1_math(env):
    acc, books, _ = env
    s = report.savings_v1(acc.read(), books, books["glm"])
    row = [r for r in s["lineages"] if r["lineage_id"] == "L1"][0]
    assert row["blocks"] == 1 and row["injects"] == 1 and row["handoffs"] == 1
    assert row["gross"] == pytest.approx(78.0)            # 15×(6.9−1.7)
    assert row["inject_cost"] == pytest.approx(1.518)     # 0.22×6.9
    assert row["handoff_cost"] == pytest.approx(39.3)     # 5×6.9 + 0.2×24
    assert row["net"] == pytest.approx(78.0 - 1.518 - 39.3)
    assert s["totals"]["bypass"] == 1                     # 对照组单列
    assert s["formula"] == "v1"


def test_unpriced_provider_listed(env):
    acc, books, tmp_path = env
    acc.record("handoff", agent="cc", session_id="s9", lineage_id="L9",
               project="C:/p", provider="whoever", model="m",
               price_ver=None, prompt_tokens=1, completion_tokens=1,
               outcome="fresh", wall_s=0.1)
    s = report.savings_v1(acc.read(), books, books["glm"])
    assert "whoever" in s["unpriced"]


def test_strategy_table(env):
    acc, books, _ = env
    now = time.time()
    acc.record("window", agent="cc", session_id="s1", lineage_id="L1",
               project="C:/p", opened_ts=now - 900, closed_ts=now,
               dur_s=900, prefix_tokens=150_000, close_reason="subagents_done")
    t = report.strategy_table(acc.read(kind="window"), books, ttl_s=600,
                              econ_key="glm")
    assert len(t["rows"]) == 1
    r = t["rows"][0]
    assert r["none"] == pytest.approx(103.5)      # 900s > TTL
    assert r["beat"] == pytest.approx(2 * 26.2, abs=0.2)
    assert r["expire_compact"] == pytest.approx(25.875)
    assert r["best"] == "expire_compact"


def test_strategy_table_skips_without_ttl_or_cache(env):
    acc, books, _ = env
    t = report.strategy_table(acc.read(kind="window"), books, ttl_s=0,
                              econ_key="glm")
    assert t["rows"] == [] and t["skipped"]


def test_run_json_output(env, tmp_path, monkeypatch):
    acc, books, tmp = env
    monkeypatch.setattr(report, "_accounts_for", lambda cfg_: acc)
    class A:  # 最小 args namespace
        since = until = project = session = kind = None
        json = True; provider = None
    assert report.run(A()) == 0
```

- [ ] **Step 2: 跑测试确认失败**

Run: `uv run pytest tests/test_report.py -v`
Expected: FAIL（`ModuleNotFoundError: ferryman.report`）

- [ ] **Step 3: 实现**

```python
# ferryman/report.py
"""ferryman account report：族系账单 + 成效账 v1 + 策略对比 + 复算附录。

ADR-0002：节省在 report 层由版本化公式现算，原始流水不改写。
block 侧价格在 report 时经 --provider 指定（daemon 不知道被拦会话的 provider，
v1 已知简化，复算附录披露）；handoff 侧用流水钉死的 price_ver。
"""
from __future__ import annotations

import json
import time
from datetime import datetime

from . import config as config_mod
from .accounts import Accounts
from .policy import NoCachePriceError, strategy_costs
from .prices import PriceBook, load_prices

SAVINGS_FORMULA = "v1"
COMPACT_RATIO = 0.25          # 公式内常数（policy.strategy_costs 默认值同源）


def _book_for(books: dict[str, PriceBook], key: str | None) -> PriceBook | None:
    if not books:
        return None
    if key and key in books:
        return books[key]
    if len(books) == 1:
        return next(iter(books.values()))
    return None


def _pv_at(book: PriceBook, ts: float):
    return book.at(ts)


def handoff_cost(e: dict, books: dict[str, PriceBook]) -> float | None:
    """按流水钉死的 price_ver 折算；无价格/无版本 → None（不可算，不造数）。"""
    tag = e.get("price_ver")
    if not tag or "@" not in tag:
        return None
    key, _, ver = tag.partition("@")
    book = books.get(key)
    pv = next((v for v in book.versions if v.effective_from == ver), None) \
        if book else None
    if pv is None:
        return None
    return (e.get("prompt_tokens", 0) / book.per * pv.p_in
            + e.get("completion_tokens", 0) / book.per * pv.p_out)


def savings_v1(entries: list[dict], books: dict[str, PriceBook],
               econ_book: PriceBook | None) -> dict:
    lines: dict[str, dict] = {}
    unpriced: set[str] = set()

    def row(lid: str) -> dict:
        return lines.setdefault(lid, {
            "lineage_id": lid, "project": "", "blocks": 0, "bypass": 0,
            "injects": 0, "handoffs": 0, "windows": 0,
            "handoff_cost": 0.0, "gross": 0.0, "inject_cost": 0.0})

    for e in entries:
        r = row(e.get("lineage_id") or e.get("session_id", "?"))
        r["project"] = r["project"] or e.get("project", "")
        k = e["kind"]
        if k == "block":
            r["blocks"] += 1
            if econ_book is not None:
                pv = _pv_at(econ_book, e.get("ts", 0))
                if pv and pv.p_cache is not None:
                    r["gross"] += e.get("prefix_tokens", 0) / econ_book.per \
                        * (pv.p_in - pv.p_cache)
        elif k == "bypass":
            r["bypass"] += 1
        elif k == "inject":
            r["injects"] += 1
            if econ_book is not None:
                pv = _pv_at(econ_book, e.get("ts", 0))
                if pv:
                    r["inject_cost"] += e.get("tokens", 0) / econ_book.per * pv.p_in
        elif k == "handoff":
            r["handoffs"] += 1
            c = handoff_cost(e, books)
            if c is None:
                unpriced.add(e.get("provider", "?"))
            else:
                r["handoff_cost"] += c
        elif k == "window":
            r["windows"] += 1
    for r in lines.values():                      # 净节省最后一步：毛 − 注入 − handoff
        r["net"] = round(r["gross"] - r["inject_cost"] - r["handoff_cost"], 4)
    totals = {kk: sum(r[kk] for r in lines.values())
              for kk in ("blocks", "bypass", "injects", "handoffs", "windows",
                         "handoff_cost", "gross", "inject_cost")}
    totals["net"] = round(totals["gross"] - totals["inject_cost"]
                          - totals["handoff_cost"], 4)
    return {"formula": SAVINGS_FORMULA, "lineages": sorted(lines.values(),
            key=lambda r: -r["net"]), "totals": totals,
            "unpriced": sorted(unpriced)}


def strategy_table(window_entries: list[dict], books: dict[str, PriceBook],
                   ttl_s: float, econ_key: str | None) -> dict:
    book = _book_for(books, econ_key)
    skipped: list[str] = []
    if ttl_s <= 0:
        skipped.append("ttl 未配置（[heartbeat] ttl_s）")
    if book is None:
        skipped.append("无可用品价格表（--provider / [prices.*]）")
    elif book.versions and book.versions[-1].p_cache is None:
        skipped.append(f"{book.key} 无 p_cache：节省额不可算（Q16）")
        book = None
    rows = []
    if book is not None and ttl_s > 0:
        for e in window_entries:
            try:
                sc = strategy_costs(book, book.at(e.get("ts", 0)) or book.versions[-1],
                                    ttl_s, e.get("dur_s", 0.0),
                                    e.get("prefix_tokens", 0),
                                    compact_ratio=COMPACT_RATIO)
            except NoCachePriceError:
                skipped.append(f"{book.key} 无 p_cache")
                break
            sc["dur_s"] = e.get("dur_s", 0.0)
            sc["prefix_tokens"] = e.get("prefix_tokens", 0)
            sc["best"] = min(("none", "beat", "expire", "expire_compact"),
                             key=lambda kk: sc[kk])
            rows.append(sc)
    return {"rows": rows, "skipped": skipped}


def _accounts_for(cfg) -> Accounts:
    return Accounts(cfg.data_dir)


def _parse_date(s: str | None) -> float | None:
    if not s:
        return None
    return datetime.strptime(s, "%Y-%m-%d").timestamp()


def render_text(s: dict, st: dict, books: dict[str, PriceBook],
                econ_key: str | None, filters: dict) -> str:
    unit = ""
    eb = _book_for(books, econ_key)
    if eb:
        unit = f"（单位：{eb.unit}）"
    L = ["# Ferryman 账本报表", "",
         f"- 成效公式：{SAVINGS_FORMULA} · 策略常数 compact_ratio={COMPACT_RATIO}"
         f" · 经济价格表：{econ_key or '（未指定）'}{unit}",
         f"- 过滤：{filters or '（无）'} · 生成：{time.strftime('%Y-%m-%d %H:%M')}", "",
         "## 按族系（lineage）", "",
         "| lineage | 项目 | block | bypass | inject | handoff | window "
         "| handoff成本 | 毛节省 | 注入成本 | 净节省 |", "|---|---|---:|---:|---:|---:|---:"
         "|---:|---:|---:|---:|"]
    for r in s["lineages"]:
        L.append(f"| {r['lineage_id'][:24]} | {r['project'][:20]} | {r['blocks']} "
                 f"| {r['bypass']} | {r['injects']} | {r['handoffs']} | {r['windows']} "
                 f"| {r['handoff_cost']:.2f} | {r['gross']:.2f} "
                 f"| {r['inject_cost']:.2f} | {r['net']:.2f} |")
    t = s["totals"]
    L += ["", f"**总计**：block {t['blocks']} · bypass {t['bypass']}（对照组，不计节省）"
         f" · 净节省 **{t['net']:.2f}**{unit}", ""]
    if s["unpriced"]:
        L.append(f"- 无价格 provider（token 已记、金额不可算）：{', '.join(s['unpriced'])}")
    if st["rows"]:
        L += ["## 等待窗口策略对比（事后，可复算）", "",
              "| dur_s | 前缀 | 不作为 | 心跳 | 放任 | 放任+compact | 最优 |",
              "|---:|---:|---:|---:|---:|---:|---|"]
        for r in st["rows"]:
            L.append(f"| {r['dur_s']:.0f} | {r['prefix_tokens']} | {r['none']:.2f} "
                     f"| {r['beat']:.2f} | {r['expire']:.2f} "
                     f"| {r['expire_compact']:.2f} | {r['best']} |")
    for skip in st["skipped"]:
        L.append(f"- 策略对比跳过：{skip}")
    L += ["", "## 复算附录", "",
          "- 公式（v1）：净节省 = Σ block S×(P_in−P_cache)/per − Σ inject tok×P_in/per"
          " − Σ handoff (prompt×P_in + completion×P_out)/per",
          "- handoff 侧价格取流水钉死的 price_ver（改价不重算旧账）；"
          "block 侧取本表头经济价格表（daemon 不知被拦会话 provider，v1 简化）",
          "- 复现：ferryman account report --json（同过滤参数）",
          ""]
    return "\n".join(L)


def run(args) -> int:
    cfg = config_mod.load()
    acc = _accounts_for(cfg)
    books = load_prices()
    econ_key = getattr(args, "provider", None) or cfg.ferry_provider or None
    filters = {"since": args.since, "until": args.until, "project": args.project,
               "session": args.session, "kind": args.kind}
    entries = acc.read(since=_parse_date(args.since), until=_parse_date(args.until),
                       project=args.project, session=args.session, kind=args.kind)
    econ_book = _book_for(books, econ_key)
    s = savings_v1(entries, books, econ_book)
    st = strategy_table([e for e in entries if e["kind"] == "window"],
                        books, ttl_s=cfg.heartbeat.ttl_s, econ_key=econ_key)
    if getattr(args, "json", False):
        print(json.dumps({"savings": s, "strategy": st,
                          "econ_provider": econ_key}, ensure_ascii=False, indent=2))
    else:
        print(render_text(s, st, books, econ_key,
                          {kk: vv for kk, vv in filters.items() if vv}))
    return 0
```

`ferryman/config.py`：

```python
@dataclass
class HeartbeatCfg:
    enabled: bool = False        # T41 仅预留：执行器未实装（设计 §0 授权边界）
    ttl_s: float = 0.0           # 0 = 未实测/未配置（report 策略对比跳过）
    ttl_measured_at: str = ""
    ttl_source: str = ""
```

Config 加字段 `heartbeat: HeartbeatCfg = field(default_factory=HeartbeatCfg)`；`load()` 的 `if "notify" in data:` 块后加：

```python
        if "heartbeat" in data:
            hb = data["heartbeat"]
            cfg.heartbeat = HeartbeatCfg(
                enabled=bool(hb.get("enabled", False)),
                ttl_s=float(hb.get("ttl_s", 0.0)),
                ttl_measured_at=str(hb.get("ttl_measured_at", "")),
                ttl_source=str(hb.get("ttl_source", "")))
```

`ferryman/__main__.py`：`sub.add_parser("doctor", ...)` 之后加：

```python
    acc_p = sub.add_parser("account", help="费用账本：流水查询与成效账")
    acc_sub = acc_p.add_subparsers(dest="acct_cmd", required=True)
    rep = acc_sub.add_parser("report", help="族系账单+净节省+策略对比（附复算附录）")
    rep.add_argument("--since", help="起始日 YYYY-MM-DD（本地时区）")
    rep.add_argument("--until", help="截止日 YYYY-MM-DD（本地时区）")
    rep.add_argument("--project")
    rep.add_argument("--session")
    rep.add_argument("--kind", help="handoff|beat|block|inject|bypass|window")
    rep.add_argument("--provider", help="block 侧经济价格表键（缺省=ferry provider）")
    rep.add_argument("--json", action="store_true")
```

命令分发（`if args.cmd == "doctor":` 块后）：

```python
    if args.cmd == "account":
        from . import report

        return report.run(args)
```

`config.example.toml` 追加：

```toml
[heartbeat]
# 心跳保活（T41 仅预留配置节——执行器未实装，默认关闭；设计文档 §3）
enabled = false
# 实测缓存 TTL（跑 experiments/cache-ttl 套件后填；report 策略对比依赖此值）
# ttl_s = 600
# ttl_measured_at = "2026-09-17"
# ttl_source = "docs/20260917_1630_GLM缓存TTL实测与心跳保温可行性_实验报告.md"
```

- [ ] **Step 4: 跑全量确认通过**

Run: `uv run pytest && uv run ferryman account report --help`
Expected: 全 PASS；--help 正常列出参数

- [ ] **Step 5: 提交**

```bash
git add ferryman/report.py ferryman/config.py ferryman/__main__.py config.example.toml tests/test_report.py
git commit -m "feat(account): T41 report CLI——族系账单+成效账v1+策略对比+复算附录（17 用例）"
```

---

### Task 8: SYSTEM_PROMPT 两招 + E1 对比程序

**Files:**
- Modify: `ferryman/ferry.py:52-53`（SYSTEM_PROMPT 要求段）
- Test: 既有全量套件（prompt 为数据，无新单测；防回归靠 test_ferry_providers 的解析用例）

**Interfaces:**
- Consumes: 无
- Produces: SYSTEM_PROMPT 增两条规则（设计 §4，Q12 裁决：断言降级 + 引用不复制）

- [ ] **Step 1: E1 基线（有真实 provider 时执行；无则记录待办跳过）**

若 `eval/out/<provider>/RESULT.md` 无近期基线且 `~/ferryman/config.toml` 已配 provider：
Run: `uv run ferryman eval <provider> --limit 5`
记下 v1 幻觉数与 qa 通过率作基线。无 provider → 在提交信息注明"E1 对比待用户执行"。

- [ ] **Step 2: 修改 SYSTEM_PROMPT 要求段**

`ferry.py` 中把：

```python
要求：文件路径、命令一律从骨架逐字引用，不要凭记忆改写或编造。
『关键文件与改动』一节必须逐字列出骨架"涉及文件"前 10 项与最后 5 条命令，不得省略或概括。"""
```

改为：

```python
要求：文件路径、命令一律从骨架逐字引用，不要凭记忆改写或编造。
『关键文件与改动』一节必须逐字列出骨架"涉及文件"前 10 项与最后 5 条命令，不得省略或概括。
凡无法从骨架或材料逐字核实的状态断言（如「已完成」「已修复」「没问题」），必须加「（推测）」标注，
不得写成确定事实——交接会被下一个会话当作合同使用，错误的确定断言会成为假前提。
已成文的项目资料（spec/ADR/issue/提交记录）只给路径引用，不要整段抄录进叙事。"""
```

- [ ] **Step 3: 跑全量确认无回归**

Run: `uv run pytest`
Expected: 全 PASS

- [ ] **Step 4: E1 对比（有 provider 时）**

Run: `uv run ferryman eval <provider> --limit 5`
对比基线：v1 幻觉数不升、qa 通过率不降、（新增观察项）交接中「（推测）」标注出现且未扩散到骨架逐字清单。若 qa 显著下降，回滚本任务并报告。

- [ ] **Step 5: 提交**

```bash
git add ferryman/ferry.py
git commit -m "feat(ferry): T41 交接prompt两招——断言降级+引用不复制（E1 对比见提交说明）"
```

---

## 收尾（计划外不做，列出供用户裁决）

- 心跳执行器：过 Q13 数据门槛（≥30 窗口/两周）+ Q14 保真度实验后另行授权。
- doctor 集成：检查 accounts 目录可写（小项，可并入 T42）。
- 真机验收：daemon 跑一天后 `ferryman account report` 人工审读复算附录。
