"""E0C · usage 聚合器 + 子代理账目装配器。

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

行内格式属 CC 内部实现、版本间会变:缺字段/坏值一律防御处理,绝不向调用方抛错。
"""

from __future__ import annotations

import io
import json
import re
import time
from dataclasses import dataclass, field
from pathlib import Path
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

# 缺 meta 时从转录内容嗅探 spawn toolUseId 的兜底手段:找 "toolUseId":"<id>" 字样。
# 真实磁盘是否在子代理转录里落这个引用待实测验证;嗅不到就留在未知桶,绝不硬猜。
_TOOLUSE_SNIFF = re.compile(r'"toolUseId"\s*:\s*"([^"\s]{6,})"')


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
    sniffed_tool_use_id: str | None  # 转录内容嗅探到的 spawn toolUseId(缺 meta 时的恢复手段)
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
    """读一个子代理转录:账目(复用聚合器)+ 终态推断 + 首末时间戳 + tool_use 块 id + 嗅探。

    全程防御:读不了按空账;坏行按"非 assistant"参与终态推断(末行坏 → 中断)。
    终态(spec):无任何非空内容 → 空;末条非空行是 assistant 且末响应组 stop=end_turn
    → 完成;其余 → 中断。"在跑"由调用方按 mtime 叠加覆盖。
    """
    try:
        text = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return _Scan(UsageAccount(), "empty", None, None, (), None, read_failed=True)
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
        is_assistant = isinstance(d, dict) and d.get("type") == "assistant"
        if is_assistant:
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
        last_is_assistant = is_assistant

    if not saw_content:
        state: AgentState = "empty"
    elif last_is_assistant and last_mid is not None:
        stops = [s for s in stops_by_mid[last_mid] if s is not None]
        state = "done" if stops and stops[0] == "end_turn" else "interrupted"
    else:
        state = "interrupted"
    sniffed = _TOOLUSE_SNIFF.search(text)
    return _Scan(account, state, first_ts, last_ts, tuple(tool_ids),
                 sniffed.group(1) if sniffed else None)


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
            tid = meta_tid or sc.sniffed_tool_use_id
            no_transcript = False
            if md is None and aid not in meta_failed:
                longtail.append(f"agent {aid}:有转录无 meta,入未知桶(类型/深度未知)")
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
