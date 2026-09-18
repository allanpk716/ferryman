"""E0C · usage 聚合器 + 子代理账目装配器 + 会话聚合器/族系/快照一致性。

聚合器(解析层纯函数):对一段 Claude Code 转录 jsonl(主会话或子代理转录通用)
产出该文件的确定性账目:四列加总(input/output/缓存写/缓存读)、响应数、行级
model 分布、长尾信号。
聚合规则(spec 20260918-subagent-token-e0c · Implementation Decisions 1):

- assistant 行按 message.id 分组,一组合并只计一次;优先取带 stop_reason 的行;
- 组内非零 usage 互不一致 → 整组入长尾、不计入加总;
- 占位行(usage 全零 / null / 字段缺失)不贡献账目;组内全是占位行 → 全零占位组入长尾;
- 无 message.id 的 assistant 行跳过并计长尾;坏/半行 JSON 跳过并计长尾。

装配器(编排层,Implementation Decisions 2):扫描会话目录 <会话id>/subagents/,
把每个子代理的转录与 meta 配对,装配账目行(agentId/类型/描述/深度/self 账目/
首末时间戳/终态/父链),并给出 spawn 计数的 direct/total 双口径与装配层长尾。

会话聚合器(编排层,Implementation Decisions 3/4):主转录 + 全部子代理装配成
会话总账(主 + Σ 子 self,含嵌套不双计),产出按 toolUseId 父链的 subtree 聚合、
direct/total 两口径交叉校验(差值进长尾)、逐文件快照(扫描时刻/字节/sha256)、
扫描窗口与末尾重枚举(新增文件标"扫描窗口外")、按主转录+会话目录全体文件
max mtime 的在跑标记(子代理运行期间主文件无中间写入,只看主 mtime 会漏);
族系合计行复用台账 lineage 规则(同 transcript_path 换 session_id → 同链),
恒标"识别未验证"。

报告层(编排 + 渲染,Implementation Decisions 5/6/7):scan_projects 扫描
projects 根下全部主转录(跳过 subagents/,--limit 限制项目目录数);load_recon
读对账手记 jsonl({ts, agentId, agentType, self_reported_tokens, source});
render_report 为纯函数,把会话账 + 族系行 + 手记折成中文报告(Q1 逐会话明细 /
Q2 占比与分布[含回传耦合固定注记] / Q3 对账 / Q4 长尾清单 + 定型建议判据);
run 为 CLI 入口(ferryman e0c,注册见 __main__.py)。

行内格式属 CC 内部实现、版本间会变:缺字段/坏值一律防御处理,绝不向调用方抛错。
"""

from __future__ import annotations

import hashlib
import io
import json
import os
import time
from collections import Counter
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path, PureWindowsPath
from typing import Literal, TextIO


@dataclass(frozen=True)
class UsageColumns:
    """四列口径(任意聚合层级共用)。"""

    input_tokens: int = 0
    output_tokens: int = 0
    cache_creation: int = 0
    cache_read: int = 0

    def __iter__(self):
        """按字段声明序可迭代,方便解包与逐列运算。"""
        return iter((self.input_tokens, self.output_tokens,
                     self.cache_creation, self.cache_read))


@dataclass(frozen=True)
class UsageAccount:
    """单个转录文件的确定性账目。

    responses = 计入账目的响应数(即有干净非零 usage 的 message.id 组数);
    非零冲突组与全零占位组只入 longtail、不占 responses,
    保证"四列 ÷ responses"的每响应均摊口径自洽。
    by_model 为行级 message.model 分组(与 meta.model 无关);model 缺失归 "unknown"。
    longtail 为人类可读的异常信号清单(坏行/无 id 行/冲突组/占位组)。
    """

    input_tokens: int = 0
    output_tokens: int = 0
    cache_creation: int = 0
    cache_read: int = 0
    responses: int = 0
    by_model: dict[str, UsageColumns] = field(default_factory=dict)
    longtail: list[str] = field(default_factory=list)


def aggregate_usage(source: Path | str | TextIO) -> UsageAccount:
    """把一段转录 jsonl 折成 UsageAccount。

    source 为文件路径(Path 或 str)或已打开的文本文件对象(或任何按行迭代的文本流)。
    路径读不到/打不开时按空账处理(防御式,与 transcripts.py 同风格)。
    """
    if isinstance(source, (str, Path)):
        try:
            f = open(source, "r", encoding="utf-8", errors="replace")
        except OSError:
            return UsageAccount()
        with f:
            return _aggregate_lines(f)
    return _aggregate_lines(source)


def _usage_4col(raw: object) -> tuple[int, int, int, int] | None:
    """从 usage 字段提取四列;字段缺失/null/值不可解析一律视为占位(返回 None)。"""
    if not isinstance(raw, dict):
        return None
    try:
        return (int(raw.get("input_tokens") or 0),
                int(raw.get("output_tokens") or 0),
                int(raw.get("cache_creation_input_tokens") or 0),
                int(raw.get("cache_read_input_tokens") or 0))
    except (TypeError, ValueError):
        return None


def _aggregate_lines(lines: TextIO) -> UsageAccount:
    # 组结构:message.id → [(行级 model, stop_reason, 四列或 None), ...](按文件顺序)
    groups: dict[str, list[tuple[str, object, tuple[int, int, int, int] | None]]] = {}
    longtail: list[str] = []
    for lineno, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            d = json.loads(line)
        except ValueError:
            longtail.append(f"第 {lineno} 行:JSON 解析失败(坏/半行),跳过")
            continue
        if not isinstance(d, dict):
            longtail.append(f"第 {lineno} 行:JSON 行不是对象,跳过")
            continue
        if d.get("type") != "assistant":
            continue  # user/progress 等行是正常内容,静默忽略
        msg = d.get("message")
        if not isinstance(msg, dict):
            longtail.append(f"第 {lineno} 行:assistant 行缺 message.id,跳过")
            continue
        mid = msg.get("id")
        if not isinstance(mid, str) or not mid:
            longtail.append(f"第 {lineno} 行:assistant 行缺 message.id,跳过")
            continue
        model_raw = msg.get("model")
        model = model_raw if isinstance(model_raw, str) and model_raw else "unknown"
        groups.setdefault(mid, []).append((model, msg.get("stop_reason"),
                                           _usage_4col(msg.get("usage"))))

    totals = [0, 0, 0, 0]
    by_model_acc: dict[str, list[int]] = {}
    responses = 0
    for mid, rows in groups.items():
        nonzeros = {u for (_, _, u) in rows if u is not None and any(u)}
        if len(nonzeros) > 1:
            longtail.append(f"message.id={mid}:组内 {len(nonzeros)} 种非零 usage 互不一致"
                            f"({len(rows)} 行),整组不计入")
            continue
        if not nonzeros:
            longtail.append(f"message.id={mid}:全零占位组({len(rows)} 行),无实际 usage,不计入")
            continue
        usage = nonzeros.pop()
        # 优先取带 stop_reason 的行(同 usage 拆多行时它才代表终态),其次首行
        carriers = [r for r in rows if r[2] == usage]
        stopped = [r for r in carriers if r[1] is not None]
        model = (stopped or carriers)[0][0]
        responses += 1
        for i in range(4):
            totals[i] += usage[i]
        acc = by_model_acc.setdefault(model, [0, 0, 0, 0])
        for i in range(4):
            acc[i] += usage[i]

    return UsageAccount(input_tokens=totals[0], output_tokens=totals[1],
                        cache_creation=totals[2], cache_read=totals[3],
                        responses=responses,
                        by_model={m: UsageColumns(*v) for m, v in by_model_acc.items()},
                        longtail=longtail)


# ============================================================================
# 子代理账目装配器(spec Implementation Decisions 2)
# ============================================================================

AgentState = Literal["done", "interrupted", "empty", "running"]

# mtime 距扫描时点小于该秒数 → 在跑(非终值,覆盖终态推断);经验值,可 monkeypatch
RUNNING_WINDOW_SECONDS = 300


@dataclass(frozen=True)
class AgentRow:
    """单个子代理的账目行。

    agentType/description/spawnDepth/toolUseId/requested_model 来自 meta;
    缺 meta(或 meta 坏)时全部 None → 未知桶,不参与 direct/total 分桶与 subtree。
    self_acc 为该子代理自身账目(经 aggregate_usage;"self"是 Python 关键字,故加后缀)。
    state 仅对有转录者推断;有 meta 无转录时为 None,以 no_transcript=True 标记。
    parent_agentId 仅当父也是本目录下某个子代理且经 toolUseId 恢复成功时非 None
    (depth=1 的父是主会话,不在本装配器视野,故为 None)。
    """

    agentId: str
    agentType: str | None
    description: str | None
    spawnDepth: int | None
    toolUseId: str | None
    requested_model: str | None
    self_acc: UsageAccount
    first_ts: str | None
    last_ts: str | None
    state: AgentState | None
    parent_agentId: str | None
    no_transcript: bool = False


@dataclass(frozen=True)
class AgentLedger:
    """subagents/ 目录的装配账本。

    spawn_events = 转录文件数 + 有 meta 无转录数(计数基准);
    direct_spawns / total_spawns 只数已知 depth 的 spawn 事件(depth=1 / 全深度);
    缺 meta 的转录占 spawn_events 与 transcript_files,但不进双口径桶;
    longtail 为装配层信号(不成对/坏 meta/意外文件);逐转录的解析层信号在各 self_acc.longtail。
    agents 按 agentId 字典序稳定排序。
    """

    agents: list[AgentRow]
    spawn_events: int
    transcript_files: int
    direct_spawns: int
    total_spawns: int
    longtail: list[str]


@dataclass(frozen=True)
class _Scan:
    """单个转录文件的一次性扫描结果(内部)。"""

    account: UsageAccount
    state: AgentState  # 尚未叠加"在跑"覆盖的终态推断
    first_ts: str | None
    last_ts: str | None
    tool_use_ids: tuple[str, ...]  # 本转录发出的 tool_use 块 id(本转录是这些子代理的"父")
    read_failed: bool = False


def _opt_str(v: object) -> str | None:
    """meta 字符串字段防御:非字符串/空串一律 None。"""
    return v if isinstance(v, str) and v else None


def _opt_depth(v: object) -> int | None:
    """meta 深度字段防御:bool/不可解析/负值一律 None(未知)。"""
    if isinstance(v, bool):
        return None
    try:
        d = int(v)
    except (TypeError, ValueError):
        return None
    return d if d >= 0 else None


def _mtime(path: Path) -> float | None:
    try:
        return path.stat().st_mtime
    except OSError:
        return None


def _scan_transcript(path: Path) -> _Scan:
    """读一个子代理转录:账目(复用聚合器)+ 终态推断 + 首末时间戳 + tool_use 块 id。

    全程防御:读不了按空账;坏行按"非 assistant"参与终态推断(末行坏 → 中断)。
    终态(spec):无任何非空内容 → 空;最后一条非 attachment 的非空行是 assistant
    且末响应组 stop=end_turn → 完成;其余 → 中断。attachment 行(SubagentStop
    等钩子收尾)只是钩子产物,不作终态判据,但其时间戳照记。"在跑"由调用方按 mtime 叠加。
    """
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return _Scan(UsageAccount(), "empty", None, None, (), read_failed=True)
    account = aggregate_usage(io.StringIO(text))
    first_ts: str | None = None
    last_ts: str | None = None
    tool_ids: list[str] = []
    stops_by_mid: dict[str, list[object]] = {}
    last_mid: str | None = None
    last_is_assistant = False
    saw_content = False
    for line in text.splitlines():
        if not line.strip():
            continue
        saw_content = True
        d = None
        try:
            d = json.loads(line)
        except ValueError:
            pass
        if isinstance(d, dict):
            ts = d.get("timestamp")
            if isinstance(ts, str) and ts:
                if first_ts is None:
                    first_ts = ts
                last_ts = ts
        row_type = d.get("type") if isinstance(d, dict) else None
        if row_type == "assistant":
            msg = d.get("message")
            mid = msg.get("id") if isinstance(msg, dict) else None
            if isinstance(mid, str) and mid:
                stop = msg.get("stop_reason") if isinstance(msg, dict) else None
                stops_by_mid.setdefault(mid, []).append(stop)
                last_mid = mid
            content = msg.get("content") if isinstance(msg, dict) else None
            if isinstance(content, list):
                for block in content:
                    if isinstance(block, dict) and block.get("type") == "tool_use":
                        bid = block.get("id")
                        if isinstance(bid, str) and bid:
                            tool_ids.append(bid)
            last_is_assistant = True
        elif row_type != "attachment":  # 钩子行跳过,不改变终态判据
            last_is_assistant = False

    if not saw_content:
        state: AgentState = "empty"
    elif last_is_assistant and last_mid is not None:
        stops = [s for s in stops_by_mid[last_mid] if s is not None]
        state = "done" if stops and stops[0] == "end_turn" else "interrupted"
    else:
        state = "interrupted"
    return _Scan(account, state, first_ts, last_ts, tuple(tool_ids))


def assemble_agents(session_dir: Path | str, *, now: float | None = None) -> AgentLedger:
    """扫描 <会话id>/subagents/,装配每个子代理的账目行。

    now 为扫描时点(epoch 秒),缺省取当前时间;测试注入固定值控制"在跑"窗口。
    目录不存在/条目读不了 → 尽力装配并记长尾,绝不向调用方抛错。
    """
    scan_ts = time.time() if now is None else float(now)
    sub = Path(session_dir) / "subagents"
    longtail: list[str] = []
    transcripts: dict[str, Path] = {}
    metas: dict[str, Path] = {}

    if sub.is_dir():
        try:
            entries = sorted(sub.iterdir())
        except OSError:
            entries = []
        for p in entries:
            name = p.name
            if not p.is_file():
                longtail.append(f"subagents/ 下非普通文件 {name},忽略")
                continue
            if name.startswith("agent-") and name.endswith(".jsonl"):
                aid = name[len("agent-"):-len(".jsonl")]
                if aid:
                    transcripts[aid] = p
                    continue
            elif name.startswith("agent-") and name.endswith(".meta.json"):
                aid = name[len("agent-"):-len(".meta.json")]
                if aid:
                    metas[aid] = p
                    continue
            longtail.append(f"subagents/ 下意外文件 {name},忽略")

    meta_data: dict[str, dict] = {}
    meta_failed: set[str] = set()
    for aid, p in metas.items():
        try:
            d = json.loads(p.read_text(encoding="utf-8", errors="replace"))
        except (OSError, ValueError):
            d = None
        if isinstance(d, dict):
            meta_data[aid] = d
        else:
            meta_failed.add(aid)
            longtail.append(f"agent {aid}:meta 解析失败,按无 meta 处理")

    scans = {aid: _scan_transcript(p) for aid, p in transcripts.items()}
    for aid, sc in scans.items():
        if sc.read_failed:
            longtail.append(f"agent {aid}:转录读取失败,按空账处理")

    # tool_use 块 id → 发出者(持有该块的转录就是这次 spawn 的"父");
    # 一 id 多父属异常,取首见并记长尾。主会话转录不在本装配器视野,depth=1 无父可恢复。
    owner: dict[str, str] = {}
    for aid in sorted(scans):
        for tid in scans[aid].tool_use_ids:
            prev = owner.setdefault(tid, aid)
            if prev != aid:
                longtail.append(f"tool_use id {tid} 同时见于 {prev} 与 {aid} 的转录,"
                                f"父判定取 {prev}")

    rows: list[AgentRow] = []
    for aid in sorted(set(transcripts) | set(metas)):
        md = meta_data.get(aid)
        if md is not None:
            agent_type = _opt_str(md.get("agentType"))
            description = _opt_str(md.get("description"))
            meta_tid = _opt_str(md.get("toolUseId"))
            depth = _opt_depth(md.get("spawnDepth"))
            requested_model = _opt_str(md.get("model"))
        else:
            agent_type = description = meta_tid = depth = requested_model = None
        sc = scans.get(aid)
        if sc is not None:  # 有转录(配对或缺 meta)
            acc, first_ts, last_ts = sc.account, sc.first_ts, sc.last_ts
            tid = meta_tid
            no_transcript = False
            if md is None and aid not in meta_failed:
                longtail.append(f"agent {aid}:有转录无 meta,入未知桶"
                                f"(类型/深度未知,父子不可恢复)")
            mtime = _mtime(transcripts[aid])
            if mtime is not None and (scan_ts - mtime) < RUNNING_WINDOW_SECONDS:
                state: AgentState | None = "running"  # 在跑(非终值),覆盖终态推断
            else:
                state = sc.state
        else:  # 有 meta 无转录
            acc, first_ts, last_ts = UsageAccount(), None, None
            tid = meta_tid
            state = None
            no_transcript = True
            longtail.append(f"agent {aid}:有 meta 无转录,token 记 0")
        parent: str | None = None
        if tid:
            cand = owner.get(tid)
            if cand is not None and cand != aid:
                parent = cand
        rows.append(AgentRow(agentId=aid, agentType=agent_type, description=description,
                             spawnDepth=depth, toolUseId=tid, requested_model=requested_model,
                             self_acc=acc, first_ts=first_ts, last_ts=last_ts,
                             state=state, parent_agentId=parent, no_transcript=no_transcript))

    meta_only = sum(1 for aid in metas if aid not in transcripts)
    return AgentLedger(
        agents=rows,
        spawn_events=len(transcripts) + meta_only,
        transcript_files=len(transcripts),
        direct_spawns=sum(1 for r in rows if r.spawnDepth == 1),
        total_spawns=sum(1 for r in rows if r.spawnDepth is not None),
        longtail=longtail,
    )


# ============================================================================
# 会话聚合器 + 族系 + 快照一致性(spec Implementation Decisions 3/4)
# ============================================================================

# spawn 工具名:现行版本叫 "Agent",旧版叫 "Task";必须精确匹配——
# TaskCreate/TaskUpdate/TaskStop/TaskOutput 是任务清单工具,不是 spawn(实测核实)
AGENT_TASK_TOOLS = frozenset({"Agent", "Task"})


@dataclass(frozen=True)
class SessionAccount:
    """单个会话的总账(主转录 + 全部子代理)。

    total = 主文件四列 + Σ 全部子代理 self(含嵌套;子代理账目各来自独立转录文件,
    天然不双计)。subtree 只收有可用 meta 的子代理(spec:未知桶不参与 subtree),
    值为该子代理 self + 其全部可恢复后代的四列合计。
    cross_checks 为两口径交叉校验结果(人类可读,含"一致/不一致"结论);
    校验差值同时进 longtail。file_digests 覆盖主转录与会话目录下全部枚举文件,
    键为相对主转录所在目录(项目目录)的 posix 相对路径,值为(扫描时刻,字节,sha256)。
    scan_window 为(起点, 末尾重枚举后终点);now 注入时两点相同(测试确定性)。
    running = 主转录与会话目录全部枚举文件的最大 mtime 距扫描时点 <
    RUNNING_WINDOW_SECONDS → 整场在跑(非终值),调用方(报告层)据此不进终值
    汇总、单独成节。看 max mtime 而非仅主转录:子代理运行期间主文件没有中间
    写入(tool_result 等子代理结束才落盘),只看主 mtime 会漏长跑子代理。
    path 为主转录绝对路径(归一化),供族系识别按 transcript_path 分组。
    """

    session_id: str
    path: str
    main: UsageAccount
    agents: AgentLedger
    total: UsageColumns
    subtree: dict[str, UsageColumns]
    cross_checks: list[str]
    file_digests: dict[str, tuple[float, int, str]]
    scan_window: tuple[float, float]
    running: bool
    longtail: list[str]


@dataclass(frozen=True)
class LineageRow:
    """族系合计行:一条 lineage 链(同 transcript_path 换 session_id)的合计。

    session_ids 按扫描窗口起点排序(非验证过的会话时间序;实测每链单会话,
    排序只为输出稳定);total 为链上各会话 total 四列合计。
    识别规则复用台账(ledger.py:同 transcript_path 出现新 session_id → 同链),
    但台账只保证"运行期观察到同路径复用"这一弱信号,未验证磁盘上的真实延续关系,
    故整行恒标"识别未验证"(unverified=True,basis 注明依据)。
    """

    session_ids: list[str]
    total: UsageColumns
    unverified: bool = True
    basis: str = "台账规则:同 transcript_path 出现新 session_id(识别未验证)"


def _sha256_file(path: Path) -> tuple[int, str] | None:
    """流式读文件算 (字节大小, sha256);读不了返回 None(防御式)。"""
    try:
        h = hashlib.sha256()
        n = 0
        with open(path, "rb") as f:
            for chunk in iter(lambda: f.read(1 << 20), b""):
                h.update(chunk)
                n += len(chunk)
        return n, h.hexdigest()
    except OSError:
        return None


def _list_files_recursive(root: Path) -> list[Path]:
    """枚举目录下全部普通文件(递归、稳定排序);目录不存在/读不了返回空表。"""
    if not root.is_dir():
        return []
    out: list[Path] = []
    for dirpath, _dirnames, filenames in os.walk(root):
        for name in filenames:
            out.append(Path(dirpath) / name)
    return sorted(out)


def _count_agent_task_calls(path: Path) -> int:
    """统计一段转录里 Agent/Task 的 tool_use 块数(精确名匹配,防御式)。"""
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return 0
    n = 0
    for line in text.splitlines():
        if '"tool_use"' not in line:
            continue
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if not isinstance(d, dict) or d.get("type") != "assistant":
            continue
        msg = d.get("message")
        content = msg.get("content") if isinstance(msg, dict) else None
        if isinstance(content, list):
            for b in content:
                if (isinstance(b, dict) and b.get("type") == "tool_use"
                        and b.get("name") in AGENT_TASK_TOOLS):
                    n += 1
    return n


def _add_cols(a: UsageColumns, b: UsageColumns) -> UsageColumns:
    return UsageColumns(a.input_tokens + b.input_tokens,
                        a.output_tokens + b.output_tokens,
                        a.cache_creation + b.cache_creation,
                        a.cache_read + b.cache_read)


def _cols_of(acc: UsageAccount) -> UsageColumns:
    return UsageColumns(acc.input_tokens, acc.output_tokens,
                        acc.cache_creation, acc.cache_read)


def _has_usable_meta(r: AgentRow) -> bool:
    """未知桶判据:meta 缺失/坏到四个字段全 None(与装配器"入未知桶"口径一致)。"""
    return any((r.agentType, r.description, r.toolUseId, r.requested_model))


def _subtree_columns(agents: list[AgentRow]) -> dict[str, UsageColumns]:
    """按 toolUseId 父链聚合 subtree:self + 全部可恢复后代;未知桶不参与。

    父链来自装配器恢复的 parent_agentId,正常形态是森林(每个子代理至多一个父、
    父必先于子存在,无环);折叠带 memo,visiting 集合纯防御兜底,正常数据不触达。
    """
    usable = [r for r in agents if _has_usable_meta(r)]
    selfcols = {r.agentId: _cols_of(r.self_acc) for r in usable}
    children: dict[str, list[str]] = {}
    for r in usable:
        if r.parent_agentId in selfcols:
            children.setdefault(r.parent_agentId, []).append(r.agentId)
    memo: dict[str, UsageColumns] = {}

    def fold(aid: str, visiting: set[str]) -> UsageColumns:
        if aid in memo:
            return memo[aid]
        if aid in visiting:  # 防御兜底:正常森林无环,此分支正常数据不触达
            return selfcols[aid]
        visiting.add(aid)
        tot = selfcols[aid]
        for ch in children.get(aid, ()):
            tot = _add_cols(tot, fold(ch, visiting))
        visiting.discard(aid)
        memo[aid] = tot
        return tot

    return {aid: fold(aid, set()) for aid in selfcols}


def _cross_checks(main_calls: int, ledger: AgentLedger,
                  parent_calls: int) -> tuple[list[str], list[str]]:
    """两口径交叉校验(spec Decision 3):返回(校验结果行, 差值长尾行)。

    direct:主转录 Agent/Task 调用数 vs depth=1 meta 数 vs depth=1 转录数;
    total:各父转录(主 + 全部子代理转录)直接子调用合计 vs total spawn 事件数。
    """
    checks: list[str] = []
    deltas: list[str] = []
    d_meta = ledger.direct_spawns
    d_trans = sum(1 for r in ledger.agents
                  if r.spawnDepth == 1 and not r.no_transcript)
    if main_calls == d_meta == d_trans:
        checks.append(f"direct 口径:主转录 Agent/Task 调用 {main_calls} 次 / "
                      f"depth=1 meta {d_meta} 条 / depth=1 转录 {d_trans} 份 — 一致")
    else:
        checks.append(f"direct 口径:主转录 Agent/Task 调用 {main_calls} 次 / "
                      f"depth=1 meta {d_meta} 条 / depth=1 转录 {d_trans} 份 — 不一致")
        deltas.append(f"direct 口径差值:主转录调用 − depth=1 meta = {main_calls - d_meta},"
                      f"depth=1 meta − depth=1 转录 = {d_meta - d_trans}")
    unknown = sum(1 for r in ledger.agents if r.spawnDepth is None)
    if parent_calls == ledger.total_spawns:
        checks.append(f"total 口径:各父转录直接子调用合计 {parent_calls} 次 / "
                      f"total spawn 事件 {ledger.total_spawns} 次"
                      f"(未知深度 {unknown} 条)— 一致")
    else:
        checks.append(f"total 口径:各父转录直接子调用合计 {parent_calls} 次 / "
                      f"total spawn 事件 {ledger.total_spawns} 次"
                      f"(未知深度 {unknown} 条)— 不一致")
        deltas.append(f"total 口径差值:父转录直接子调用合计 − total spawn 事件 = "
                      f"{parent_calls - ledger.total_spawns}")
    return checks, deltas


def aggregate_session(session_file: Path | str, *, now: float | None = None) -> SessionAccount:
    """把主转录 + 全部子代理装配成会话总账(spec Implementation Decisions 3/4)。

    session_file 为主转录路径(<项目目录>/<会话id>.jsonl);子代理取
    <项目目录>/<会话id>/subagents/(经 assemble_agents)。now 为扫描时点
    (epoch 秒),缺省取当前时间;注入固定值可控制"在跑"窗口并使快照可复现。
    全程防御:主转录缺/坏按空账记长尾,目录缺失按空装配,绝不向调用方抛错。
    """
    scan_ts = time.time() if now is None else float(now)
    clock = time.time if now is None else (lambda: scan_ts)
    session_file = Path(session_file)
    session_id = session_file.stem
    sess_dir = session_file.parent / session_id
    longtail: list[str] = []

    # -- 首枚举 + 快照:主转录 + 会话目录下全部文件(扫描时刻, 字节, sha256) --------
    enum1 = _list_files_recursive(sess_dir)
    file_digests: dict[str, tuple[float, int, str]] = {}
    if session_file.is_file():
        snap = _sha256_file(session_file)
        if snap is not None:
            file_digests[session_file.name] = (clock(), snap[0], snap[1])
        else:
            longtail.append(f"主转录 {session_file.name} 读取失败,主账按空处理")
    else:
        longtail.append(f"主转录 {session_file.name} 不存在,主账按空处理")
    for p in enum1:
        snap = _sha256_file(p)
        if snap is not None:
            file_digests[p.relative_to(session_file.parent).as_posix()] = (
                clock(), snap[0], snap[1])

    # -- 账目:主 + 子代理 --------------------------------------------------------
    main = aggregate_usage(session_file)
    agents = assemble_agents(sess_dir, now=scan_ts)
    total = _cols_of(main)
    for r in agents.agents:
        total = _add_cols(total, _cols_of(r.self_acc))
    subtree = _subtree_columns(agents.agents)

    # -- 交叉校验:direct / total 两口径,差值进长尾 ---------------------------------
    main_calls = _count_agent_task_calls(session_file)
    parent_calls = main_calls
    for r in agents.agents:
        if not r.no_transcript:
            parent_calls += _count_agent_task_calls(
                sess_dir / "subagents" / f"agent-{r.agentId}.jsonl")
    checks, deltas = _cross_checks(main_calls, agents, parent_calls)
    longtail.extend(deltas)

    # -- 末尾重枚举:窗口内新增文件标"扫描窗口外",不进账目 ---------------------------
    enum2 = _list_files_recursive(sess_dir)
    seen = {p.relative_to(session_file.parent).as_posix()
            for p in enum1}
    for p in enum2:
        rel = p.relative_to(session_file.parent).as_posix()
        if rel not in seen:
            longtail.append(f"扫描窗口外新增文件 {rel},未计入本账")

    # -- 在跑判定:主转录 + 会话目录全部文件的 max mtime 距扫描时点 <5min → 整场在跑 --
    # 只看主 mtime 会漏:子代理运行期间主文件没有任何中间写入(Agent 的 tool_result
    # 要等子代理结束才落盘),长跑子代理会让主 mtime 脱离窗口;首轮枚举(enum1)
    # 已覆盖会话目录,取全体最大 mtime 作活跃信号。窗口外新增文件不计(未在扫描视野)。
    mtimes = [_mtime(session_file)]
    mtimes.extend(_mtime(p) for p in enum1)
    mtimes = [m for m in mtimes if m is not None]
    running = bool(mtimes) and (scan_ts - max(mtimes)) < RUNNING_WINDOW_SECONDS

    return SessionAccount(
        session_id=session_id,
        path=os.path.abspath(str(session_file)),
        main=main,
        agents=agents,
        total=total,
        subtree=subtree,
        cross_checks=checks,
        file_digests=file_digests,
        scan_window=(scan_ts, clock()),
        running=running,
        longtail=longtail,
    )


def _lineage_key(path: str, projects_dir: Path) -> str:
    """lineage 分组键:优先相对 projects_dir,再按台账语义归一(正斜杠 + 小写)。

    归一化与 ledger._norm_path 同语义(分隔符统一 + 大小写不敏感);先相对化
    使键在项目根挪动时仍稳定。
    """
    p = str(path)
    try:
        p = str(Path(p).relative_to(projects_dir))
    except (ValueError, OSError):
        pass
    return str(PureWindowsPath(p)).replace("\\", "/").lower()


def lineage_rows(sessions: list[SessionAccount],
                 projects_dir: Path) -> list[LineageRow]:
    """族系合计行:按台账 lineage 规则(同 transcript_path 换 session_id → 同链)分组。

    规则复用台账(ledger.py),实现重写为等价纯函数:Ledger 是运行期有锁内存结构、
    由 daemon 事件喂,不适用于纯文件扫描;规则本体(归一化 transcript_path 分组)保留。
    实测 ~/.claude/projects(289 个会话文件):文件名与 sessionId 严格一一对应、
    无跨文件 uuid/message.id 复用、compact_boundary 的 logicalParentUuid 全部
    指向同文件——真实数据上每条链都退化为"单会话自成一行",故合计行照常输出
    (退化行也有价值:整链合计 = 单会话账),恒标"识别未验证"。
    在跑会话(非终值)不进族系合计,由调用方单独成节。
    """
    groups: dict[str, list[SessionAccount]] = {}
    for s in sessions:
        if s.running:
            continue
        groups.setdefault(_lineage_key(s.path, Path(projects_dir)), []).append(s)
    rows: list[LineageRow] = []
    for grp in groups.values():
        grp.sort(key=lambda s: (s.scan_window[0], s.session_id))
        total = UsageColumns()
        for s in grp:
            total = _add_cols(total, s.total)
        rows.append(LineageRow(session_ids=[s.session_id for s in grp], total=total))
    rows.sort(key=lambda r: r.session_ids[0])
    return rows


# ============================================================================
# 报告层:扫描编排 + 手记解析 + 渲染纯函数 + CLI(spec Implementation Decisions 5/6/7)
# ============================================================================

# 固定文案(spec Decision 7,评审定为必现注记):头部 + Q2 各出现一次
FEEDBACK_COUPLE_NOTE = (
    "**回传耦合注记(固定)**:子代理结果回传主会话后,以 input/cache_read 形式再计入"
    "主会话后续请求——“子代理占比”结构性偏低、主会话偏高,占比只作方向参考。")

_STATE_CN = {"done": "完成", "interrupted": "中断",
             "empty": "空文件", "running": "在跑(非终值)"}

# 长尾明细单会话最多列出的原始行数(真实数据长尾可能上百,报告要能读)
_LONGTAIL_DETAIL_CAP = 40


def fmt_int(x: int) -> str:
    """千分位整数(报告统一口径)。"""
    return f"{x:,}"


def _pct(num: float, den: float) -> str:
    return "n/a" if den == 0 else f"{num / den * 100:.1f}%"


def _four_cells(c: UsageColumns) -> list[str]:
    return [fmt_int(c.input_tokens), fmt_int(c.output_tokens),
            fmt_int(c.cache_creation), fmt_int(c.cache_read)]


def scan_projects(projects_dir: Path | str, *,
                  now: float | None = None, limit: int | None = None) -> list[SessionAccount]:
    """扫描 projects 根下全部主转录,逐个 aggregate_session。

    主转录判据:*.jsonl 且路径里没有 subagents/ 分量(真实布局
    <projects>/<项目slug>/<会话id>.jsonl,子代理在 <会话id>/subagents/ 下)。
    limit 限制**项目目录数**(=主转录所在目录,冒烟用),按路径排序取前 N。
    目录不存在 → 空表(防御式);now 透传 aggregate_session 保证可复现。
    """
    root = Path(projects_dir)
    if not root.is_dir():
        return []
    files = [p for p in sorted(root.rglob("*.jsonl"))
             if "subagents" not in p.parts]
    if limit is not None:
        proj_dirs = sorted({p.parent for p in files}, key=str)[:max(0, limit)]
        allowed = set(proj_dirs)
        files = [p for p in files if p.parent in allowed]
    return [aggregate_session(p, now=now) for p in files]


def load_recon(path: Path | str) -> dict | None:
    """读对账手记 jsonl(spec Decision 5 通道)。

    返回 {path, entries, bad_lines}:entries 只收 agentId 为非空字符串的行;
    坏 JSON 行 / 缺 agentId 行计入 bad_lines 如实上报。文件读不到 → None
    (报告层写"无样本")。
    """
    try:
        text = Path(path).read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None
    entries: list[dict] = []
    bad = 0
    for line in text.splitlines():
        if not line.strip():
            continue
        try:
            d = json.loads(line)
        except ValueError:
            bad += 1
            continue
        if isinstance(d, dict) and isinstance(d.get("agentId"), str) and d["agentId"]:
            entries.append(d)
        else:
            bad += 1
    return {"path": str(path), "entries": entries, "bad_lines": bad}


def _render_session_rows(L: list[str], acc: SessionAccount) -> None:
    """一个会话的明细表:主会话行 + 各子代理行(self/subtree、类型、深度、模型、终态)。"""
    L.append(f"### 会话 `{acc.session_id}`")
    L.append("")
    L.append(f"- 主转录:`{acc.path}`")
    L.append("")
    L.append("| 行 | 类型 | 深度 | 模型 | 终态 | 响应 | input | output | 缓存写 | 缓存读 "
             "| subtree(input/output/缓存写/缓存读) |")
    L.append("|---|---|---|---|---|---:|---:|---:|---:|---:|---|")
    m = acc.main
    served = [k for k in m.by_model if k != "unknown"]
    main_model = served[0] if len(served) == 1 else ("混合" if served else "—")
    cells = ["主会话", "—", "—", main_model,
             "在跑(非终值)" if acc.running else "终值",
             fmt_int(m.responses), *_four_cells(_cols_of(m)), "—"]
    L.append("| " + " | ".join(cells) + " |")
    for r in acc.agents.agents:
        state = "无转录" if r.no_transcript else _STATE_CN.get(r.state, "—")
        st = acc.subtree.get(r.agentId)
        st_s = "—" if st is None else " / ".join(fmt_int(v) for v in st)
        cells = [f"`{r.agentId}`", r.agentType or "未知",
                 "未知" if r.spawnDepth is None else str(r.spawnDepth),
                 r.requested_model or "未知", state,
                 fmt_int(r.self_acc.responses), *_four_cells(_cols_of(r.self_acc)), st_s]
        L.append("| " + " | ".join(cells) + " |")
    L.append("")
    t = acc.total
    L.append(f"- 会话总账(主 + Σ 子 self,含嵌套不双计):input {fmt_int(t.input_tokens)} / "
             f"output {fmt_int(t.output_tokens)} / 缓存写 {fmt_int(t.cache_creation)} / "
             f"缓存读 {fmt_int(t.cache_read)}")
    for c in acc.cross_checks:
        L.append(f"- 交叉校验:{c}")
    L.append("")


def _q2_sums(finals: list[SessionAccount]):
    """终值会话的 Q2 汇总:主/子合计、按类型/深度/模型分布(模型用行级 by_model)。"""
    main_sum = UsageColumns()
    agents_sum = UsageColumns()
    by_type: dict[str, UsageColumns] = {}
    n_type: Counter = Counter()
    by_depth: dict[str, UsageColumns] = {}
    n_depth: Counter = Counter()
    by_model: dict[str, UsageColumns] = {}
    n_rows = 0
    for acc in finals:
        for mdl, c in acc.main.by_model.items():
            by_model[mdl] = _add_cols(by_model.get(mdl, UsageColumns()), c)
        main_sum = _add_cols(main_sum, _cols_of(acc.main))
        for r in acc.agents.agents:
            c = _cols_of(r.self_acc)
            agents_sum = _add_cols(agents_sum, c)
            n_rows += 1
            tk = r.agentType or "未知"
            by_type[tk] = _add_cols(by_type.get(tk, UsageColumns()), c)
            n_type[tk] += 1
            dk = "未知" if r.spawnDepth is None else str(r.spawnDepth)
            by_depth[dk] = _add_cols(by_depth.get(dk, UsageColumns()), c)
            n_depth[dk] += 1
            for mdl, mc in r.self_acc.by_model.items():
                by_model[mdl] = _add_cols(by_model.get(mdl, UsageColumns()), mc)
    return (main_sum, agents_sum, by_type, n_type, by_depth, n_depth,
            by_model, n_rows)


def _dist_table(rows: list[tuple[str, int, UsageColumns]]) -> list[str]:
    """分布小表:| 分组 | 行数 | 四列 |。"""
    L = ["| 分组 | 行数 | input | output | 缓存写 | 缓存读 |",
         "|---|---:|---:|---:|---:|---:|"]
    L += [f"| {name} | {n} | {' | '.join(_four_cells(c))} |" for name, n, c in rows]
    return L


def _depth_sort_key(name: str):
    return (1, 0) if name == "未知" else (0, int(name))


def _q3_pairs(accounts: list[SessionAccount], recon: dict | None):
    """手记条目按 agentId 与扫描到的子代理配对。

    返回 (matched, unmatched):matched = (entry, AgentRow, 自报量, 文件四列合计,
    文件 in+out, 残差);unmatched = entry(扫描未见该 agentId)。
    残差口径 = in+out(评审 R2 裁定:CLI 自报数语义 ≈ 去重 in+out;cache_read
    占体量 90%+,四列口径会得出 -363%~-3590% 的假残差,故只并列展示作参考)。
    自报量缺/坏按 0(残差如实为 −in+out,残差率记 n/a)。
    """
    if recon is None:
        return [], []
    agent_map: dict[str, AgentRow] = {}
    for acc in accounts:
        for r in acc.agents.agents:
            agent_map.setdefault(r.agentId, r)
    matched: list[tuple[dict, AgentRow, int, int, int, int]] = []
    unmatched: list[dict] = []
    for e in recon["entries"]:
        r = agent_map.get(e.get("agentId"))
        if r is None:
            unmatched.append(e)
            continue
        try:
            reported = int(e.get("self_reported_tokens") or 0)
        except (TypeError, ValueError):
            reported = 0
        cols = _cols_of(r.self_acc)
        four_sum = sum(cols)
        inout_sum = cols.input_tokens + cols.output_tokens
        matched.append((e, r, reported, four_sum, inout_sum, reported - inout_sum))
    return matched, unmatched


def render_report(accounts: list[SessionAccount],
                  lineages: list[LineageRow],
                  recon: dict | None,
                  meta: dict) -> str:
    """把会话账 + 族系行 + 对账手记渲染成中文报告(纯函数,不碰 IO)。

    accounts:scan_projects 的输出;lineages:lineage_rows 的输出;
    recon:load_recon 的输出({path, entries, bad_lines})或 None(无手记 → Q3"无样本");
    meta:{generated_at, projects_dir, limit}。
    """
    finals = [a for a in accounts if not a.running]
    runnings = [a for a in accounts if a.running]
    L: list[str] = []

    # -- 头部 + 方法注记 ---------------------------------------------------------
    L.append("# E0c · Claude Code 子代理 token 记账实验报告")
    L.append("")
    L.append(f"- 生成:{meta.get('generated_at', '')}(工具:`ferryman e0c`)")
    L.append(f"- 数据:{len(accounts)} 个会话(终值 {len(finals)},在跑 {len(runnings)}),"
             f"扫描根:{meta.get('projects_dir', '~/.claude/projects')}")
    if meta.get("limit") is not None:
        L.append(f"- 冒烟限制:只扫前 {meta['limit']} 个项目目录(样本有限,数字不作全量结论)")
    L.append("- 口径:四列 = input / output / cache_creation / cache_read(官方 usage 字段);"
             "**四列按 message.id 去重**(同一 id 拆多行只计一次,优先取带 stop_reason 的行);"
             "**responses = 计入账目的干净组数**(组内冲突组 / 全零占位组只进长尾,不占 responses)")
    L.append(f"- {FEEDBACK_COUPLE_NOTE}")
    L.append("")

    # -- Q1 逐会话明细账 ----------------------------------------------------------
    L.append("## Q1 · 逐会话明细账")
    L.append("")
    if not finals:
        L.append("(无终值会话)")
        L.append("")
    for acc in finals:
        _render_session_rows(L, acc)
    if lineages:
        L.append("### 族系合计(识别未验证)")
        L.append("")
        L.append("| 链上会话 | input | output | 缓存写 | 缓存读 | 识别依据 |")
        L.append("|---|---:|---:|---:|---:|---|")
        for row in lineages:
            ids = ", ".join(f"`{s}`" for s in row.session_ids)
            L.append(f"| {ids} | {' | '.join(_four_cells(row.total))} | {row.basis} |")
        L.append("")

    # -- 在跑会话(非终值,单独成节) ---------------------------------------------
    if runnings:
        L.append("## 在跑会话(非终值,不进终值汇总)")
        L.append("")
        L.append("以下会话扫描时仍在活跃写入(mtime 距扫描 <5 分钟),账目不进 Q2 占比与"
                 "族系合计;行内数字是扫描瞬间的快照,只会偏小。")
        L.append("")
        for acc in runnings:
            _render_session_rows(L, acc)

    # -- Q2 占比与分布(仅终值会话) ----------------------------------------------
    L.append("## Q2 · 子代理占比与分布(仅终值会话)")
    L.append("")
    if not finals:
        L.append("(无终值会话,无占比数据)")
        L.append("")
    else:
        (main_sum, agents_sum, by_type, n_type, by_depth, n_depth,
         by_model, _n_rows) = _q2_sums(finals)
        total_sum = _add_cols(main_sum, agents_sum)
        L.append("| 口径 | input | output | 缓存写 | 缓存读 |")
        L.append("|---|---:|---:|---:|---:|")
        L.append(f"| 主会话合计 | {' | '.join(_four_cells(main_sum))} |")
        L.append(f"| 子代理合计(self) | {' | '.join(_four_cells(agents_sum))} |")
        L.append(f"| 总账 | {' | '.join(_four_cells(total_sum))} |")
        shares = [_pct(a, t) for a, t in zip(agents_sum, total_sum,
                                             strict=True)]
        L.append(f"| 子代理占比 | {' | '.join(shares)} |")
        L.append("")
        L.append(FEEDBACK_COUPLE_NOTE)
        L.append("")
        L.append("### 按类型(agentType,未知桶单列)")
        L.append("")
        L += _dist_table([(k, n_type[k], by_type[k]) for k in sorted(by_type)])
        L.append("")
        L.append("### 按深度(spawnDepth,未知桶单列)")
        L.append("")
        L += _dist_table([(k, n_depth[k], by_depth[k])
                          for k in sorted(by_depth, key=_depth_sort_key)])
        L.append("")
        L.append("### 按模型(行级 message.model,与 meta 请求别名无关)")
        L.append("")
        L.append("| 模型 | input | output | 缓存写 | 缓存读 |")
        L.append("|---|---:|---:|---:|---:|")
        L += [f"| {m} | {' | '.join(_four_cells(by_model[m]))} |"
              for m in sorted(by_model)]
        L.append("")

    # -- Q3 对账 ------------------------------------------------------------------
    L.append("## Q3 · 与 CLI 自报数对账")
    L.append("")
    if recon is None:
        L.append("无样本(未提供对账手记 jsonl)。")
        L.append("")
    else:
        entries = recon["entries"]
        L.append(f"- 手记:`{recon['path']}`(有效 {len(entries)} 条,"
                 f"坏行 {recon['bad_lines']} 条)")
        if not entries:
            L.append("- 无样本(手记文件为空)。")
        else:
            if len(entries) < 10:
                L.append(f"- 注:样本 {len(entries)} 条,不足 10,以下仅供参考。")
            L.append("- 残差口径=in+out(与 CLI 自报语义对齐;四列口径另列仅作参考)。")
            L.append("")
            L.append("| agentId | 类型 | 自报总量 | 文件四列合计(参考) | 文件 in+out "
                     "| 残差(in+out) | 残差率 | 备注 |")
            L.append("|---|---|---:|---:|---:|---:|---:|---|")
            matched, unmatched = _q3_pairs(accounts, recon)
            for e, r, reported, four_sum, inout_sum, residual in matched:
                atype = (e.get("agentType") if isinstance(e.get("agentType"), str)
                         else None) or r.agentType or "未知"
                rate = ("n/a" if reported <= 0
                        else f"{residual / reported * 100:+.1f}%")
                L.append(f"| `{r.agentId}` | {atype} | {fmt_int(reported)} | "
                         f"{fmt_int(four_sum)} | {fmt_int(inout_sum)} | "
                         f"{fmt_int(residual)} | {rate} | |")
            for e in unmatched:
                atype = e.get("agentType") if isinstance(e.get("agentType"), str) else "未知"
                reported = e.get("self_reported_tokens")
                rep_s = fmt_int(int(reported)) if isinstance(reported, int) else "—"
                L.append(f"| `{e.get('agentId')}` | {atype} | {rep_s} | — | — | — | — |"
                         f" 扫描未见该 agentId(在跑/窗口外/他机) |")
        L.append("")

    # -- Q4 长尾清单 ---------------------------------------------------------------
    L += _render_q4(accounts, finals, runnings)

    # -- 定型建议(附判据,不附决定) ----------------------------------------------
    L += _render_suggestion(accounts, finals, recon)

    return "\n".join(L) + "\n"


def _render_q4(accounts: list[SessionAccount], finals: list[SessionAccount],
               runnings: list[SessionAccount]) -> list[str]:
    """Q4 长尾清单:类别分布表 + 原始明细(逐会话封顶)。"""
    n_agents = sum(len(a.agents.agents) for a in accounts)
    n_sessions = len(accounts)
    n_transcripts = sum(1 + a.agents.transcript_files for a in accounts)
    states = Counter(r.state for a in accounts for r in a.agents.agents)
    no_transcript = sum(1 for a in accounts for r in a.agents.agents
                        if r.no_transcript)
    unknown_bucket = sum(1 for a in accounts for r in a.agents.agents
                         if r.agentType is None and not r.no_transcript)
    parse_lines = [ln for a in accounts for ln in
                   [*a.main.longtail,
                    *(x for r in a.agents.agents for x in r.self_acc.longtail)]]
    c_no_id = sum(1 for x in parse_lines if "缺 message.id" in x)
    c_bad = sum(1 for x in parse_lines
                if "JSON 解析失败" in x or "JSON 行不是对象" in x)
    c_conflict = sum(1 for x in parse_lines if "互不一致" in x)
    c_zero = sum(1 for x in parse_lines if "全零占位组" in x)
    c_oow = sum(1 for a in accounts for x in a.longtail if "扫描窗口外" in x)

    L = ["## Q4 · 长尾清单(分布与占比)", "",
         "异常与边界样本的分布;占比分母按行类别标注(子代理行 / 会话 / 转录文件)。", "",
         "| 类别 | 数量 | 分母 | 占比 |", "|---|---:|---:|---:|"]
    rows = [
        ("中断(子代理终态)", states.get("interrupted", 0), n_agents, "子代理行"),
        ("空文件(子代理终态)", states.get("empty", 0), n_agents, "子代理行"),
        ("在跑(子代理,非终值)", states.get("running", 0), n_agents, "子代理行"),
        ("在跑会话(整场非终值)", len(runnings), n_sessions, "会话"),
        ("有 meta 无转录(不成对)", no_transcript, n_agents, "子代理行"),
        ("有转录无 meta(未知桶,不成对)", unknown_bucket, n_agents, "子代理行"),
        ("无 message.id 行", c_no_id, n_transcripts, "转录文件"),
        ("坏行/半行 JSON", c_bad, n_transcripts, "转录文件"),
        ("组内非零 usage 冲突", c_conflict, n_transcripts, "转录文件"),
        ("全零占位组", c_zero, n_transcripts, "转录文件"),
        ("扫描窗口外新增文件", c_oow, n_sessions, "会话"),
    ]
    L += [f"| {name} | {n} | {den}({what}) | {_pct(n, den)} |"
          for name, n, den, what in rows]
    L.append("")
    L.append(f"### 长尾明细(原始行,每会话最多 {_LONGTAIL_DETAIL_CAP} 条)")
    L.append("")
    any_detail = False
    for acc in accounts:
        lines = [*acc.longtail, *acc.agents.longtail, *acc.main.longtail]
        lines += [f"agent {r.agentId}:{x}" for r in acc.agents.agents
                  for x in r.self_acc.longtail]
        if not lines:
            continue
        any_detail = True
        L.append(f"- `{acc.session_id}`:")
        for x in lines[:_LONGTAIL_DETAIL_CAP]:
            L.append(f"  - {x}")
        if len(lines) > _LONGTAIL_DETAIL_CAP:
            L.append(f"  - …(另 {len(lines) - _LONGTAIL_DETAIL_CAP} 条略)")
    if not any_detail:
        L.append("(无长尾明细)")
    L.append("")
    return L


def _render_suggestion(accounts: list[SessionAccount], finals: list[SessionAccount],
                       recon: dict | None) -> list[str]:
    """定型建议:只给判据与本机实测锚点,不替维护者做决定。"""
    matched, _unmatched = _q3_pairs(accounts, recon)
    rates = []
    for _e, _r, reported, _four, _inout, residual in matched:
        if reported > 0:
            rates.append(abs(residual) / reported)
    if rates:
        r_max = max(rates)
        rate_line = (f"本机实测:残差率最大 {r_max * 100:.1f}%"
                     f"(n={len(rates)}{' ,样本不足 10' if len(rates) < 10 else ''})。")
    else:
        rate_line = "本机实测:无对账样本,判据一暂无法评估。"
    spawns = sum(a.agents.spawn_events for a in finals)
    avg_line = (f"{spawns / len(finals):.1f}" if finals else "n/a")
    return [
        "## 定型建议(附判据,不附决定)",
        "",
        "是否把记账做成正式功能由维护者决定,本报告只给判据与本机锚点:",
        "",
        f"- 判据一(口径可信):Q3 残差率(in+out 口径)多数 ≤5% 且无 >10% 离群 → "
        f"文件侧 in+out 口径可作对账权威;出现 >10% 离群先解释再议"
        f"(spec 已知残差未解释,见 Q3)。{rate_line}",
        f"- 判据二(功能价值):若子代理占比可观(参考:output 列 ≥20%)且会话平均 spawn "
        f"数不小(本机实测:终值会话平均 {avg_line} 次/会话,计 {spawns:,} 次),"
        f"记账入正式功能才有信息收益;两值都低则留实验记录即可。",
        "- 判据三(防御边界):Q4 中不成对 / 在跑 / 扫描窗口外的实际占比,决定记账功能"
        "至少要带的三类防御(缺 meta 未知桶、在跑排除、末尾重枚举);占比越高,防御越不能省。",
        "",
    ]


def run(projects: str | None = None, out: str | None = None,
        recon: str | None = None, limit: int | None = None) -> int:
    """`ferryman e0c` 入口:扫描 → 族系 → 手记 → 渲染 → 落盘(UTF-8)。

    projects 缺省 ~/.claude/projects;out 缺省 reports/e0c-cc-subagent-tokens.md
    (相对仓库根,与 e0/e0b 同惯例);recon 为对账手记 jsonl 路径(可选,缺则
    Q3 记"无样本");limit 限制项目目录数(真实数据冒烟用)。
    """
    projects_dir = Path(projects) if projects else Path.home() / ".claude" / "projects"
    out_path = (Path(out) if out else
                Path(__file__).resolve().parent.parent / "reports"
                / "e0c-cc-subagent-tokens.md")
    accounts = scan_projects(projects_dir, limit=limit)
    lineages = lineage_rows(accounts, projects_dir)
    recon_info = load_recon(recon) if recon else None
    meta = {"generated_at": datetime.now().strftime("%Y-%m-%d %H:%M"),
            "projects_dir": str(projects_dir), "limit": limit}
    text = render_report(accounts, lineages, recon_info, meta)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(text, encoding="utf-8")
    n_running = sum(1 for a in accounts if a.running)
    print(f"e0c done sessions={len(accounts)} running={n_running} out={out_path}")
    return 0

