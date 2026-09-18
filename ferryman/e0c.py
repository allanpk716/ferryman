"""E0C · usage 聚合器(解析层纯函数)。

对一段 Claude Code 转录 jsonl(主会话或子代理转录通用)产出该文件的确定性账目:
四列加总(input/output/缓存写/缓存读)、响应数、行级 model 分布、长尾信号。
聚合规则(spec 20260918-subagent-token-e0c · Implementation Decisions 1):

- assistant 行按 message.id 分组,一组合并只计一次;优先取带 stop_reason 的行;
- 组内非零 usage 互不一致 → 整组入长尾、不计入加总;
- 占位行(usage 全零 / null / 字段缺失)不贡献账目;组内全是占位行 → 全零占位组入长尾;
- 无 message.id 的 assistant 行跳过并计长尾;坏/半行 JSON 跳过并计长尾。

行内格式属 CC 内部实现、版本间会变:缺字段/坏值一律防御处理,绝不向调用方抛错。
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import TextIO


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
