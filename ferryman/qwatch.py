"""提问潮检测器（question watch detector，T51 票 01）。

给定 CC 会话 jsonl 路径，对末条带文本的 assistant 消息做紧档判定：
是否提问潮、问题单元数、判据构成，以及尾部悬空 tool_use 是否仅由
AskUserQuestion 构成。判据与隐私不变量见 spec 决策 1/2
（docs/superpowers/specs/20260918-question-watch-heartbeat-spec.md）：
  - 剥代码块后按行归桶计数（一行只计一桶）：标记行（❓ / **Qn**）计入；
    问号行计入；编号/列表行仅当行内含问号或疑问词才计入——round-0 实验
    153 条误报的形态就是无疑问信号的纯步骤/清单行，紧档为堵它们而来；
  - 隐私铁律：Verdict 只含计数与布尔，任何字段不携带消息内容。

读取风格与 transcripts.has_dangling_tool_use 同源：只读尾部窗口、逐行
解析 jsonl、坏行/缺字段/读失败一律静默兜底，绝不向调用方抛错。
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from pathlib import Path

from .beat import OUT_OBSERVE

ASK_USER_QUESTION = "AskUserQuestion"   # 该工具悬空＝正在等用户作答，恰是靶场景
DEFAULT_MIN_QUESTIONS = 5               # 提问潮阈值下限（spec 决策 9：min_questions）
TAIL_BYTES = 262_144

# 漏检观测（票06，spec xcheck 附录第 10 条——observe 期粗粒度信号）口径常数：
MISS_IDLE_S = 600.0        # 复活闲置门槛：GLM 实测 TTL 口径（spec D4 ~10 分钟）
MISS_LOOKBACK_S = 1800.0   # 命中回看窗：此前 30 分钟内的末条疑似提问与复活关联

_MARKER_RE = re.compile(r"\*\*Q\s?\d+", re.IGNORECASE)   # **Q1** / **Q 2** 形态
# 编号/列表行：1. 2、 3) 以及 - * • · 起头（**Qn** 粗体行由标记桶先行接住）
_NUMBERED_RE = re.compile(r"^\s*(?:\d{1,3}\s*[.、)]|[-*•·])\s*")
# 成对围栏剥到最近闭合，未闭合围栏剥到文末（都视作代码，不计）
_CODE_FENCE_RE = re.compile(r"```.*?(?:```|\Z)", re.DOTALL)
_QUESTION_WORDS = ("什么", "怎么", "为何", "如何", "哪个", "哪些", "是否",
                   "能不能", "要不要", "还是不是", "还是说",
                   "what", "how", "why", "which", "whether")


@dataclass(frozen=True)
class Breakdown:
    """判据构成（行数，按行归桶、一行只计一桶，不重复计数）。"""

    marker_lines: int                # ❓ / **Qn** 标记行
    qmark_lines: int                 # 含 ？/? 的非编号行
    qualified_numbered_lines: int    # 含问号或疑问词的编号/列表行

    @property
    def total(self) -> int:
        return self.marker_lines + self.qmark_lines + self.qualified_numbered_lines


@dataclass(frozen=True)
class Verdict:
    """提问潮判定结果。隐私铁律：只有计数与布尔，无任何消息内容。"""

    is_surge: bool
    unit_count: int
    breakdown: Breakdown
    askuserquestion_dangling: bool   # 悬空集 ⊆ {AskUserQuestion}；空集真空真


def _questionish(line: str) -> bool:
    """行内含问号或疑问词（英文不区分大小写）。"""
    low = line.lower()
    if "?" in low or "？" in line:
        return True
    return any(w in low for w in _QUESTION_WORDS)


def _classify(text: str) -> Breakdown:
    """剥块后的正文逐行归桶：标记行 > 编号行 > 问号行，一行只进一桶。"""
    marker = qmark = numbered = 0
    for raw in text.split("\n"):
        line = raw.strip()
        if not line:
            continue
        if "❓" in line or _MARKER_RE.search(line):
            marker += 1
        elif _NUMBERED_RE.match(line):
            if _questionish(line):        # 紧档：纯步骤/清单行不计
                numbered += 1
        elif "?" in line or "？" in line:
            qmark += 1                    # 非编号行只认问号，疑问词不算
    return Breakdown(marker, qmark, numbered)


def detect(path: Path, min_questions: int = DEFAULT_MIN_QUESTIONS,
           tail_bytes: int = TAIL_BYTES) -> Verdict:
    """转录尾部 → 提问潮判定（纯函数，除读文件外零副作用）。

    只读尾部 tail_bytes 字节（大 jsonl 不整读）；末条带文本的 assistant
    消息按 message id 聚合文本块（CC 流式分片同 id 多行）；悬空 tool_use
    判定与 has_dangling_tool_use 同法做尾部窗口集合差，另收集 name：
    悬空集 ⊆ {AskUserQuestion} 时 askuserquestion_dangling=True——空集按
    真空真记 True（语义＝「无非该工具悬空」，供命中谓词条件②直接使用）。
    缺文件/空文件/坏行：is_surge=False 零计数兜底，绝不抛错。
    """
    try:
        with open(path, "rb") as f:
            f.seek(0, 2)
            end = f.tell()
            f.seek(max(0, end - tail_bytes))
            data = f.read()
    except OSError:
        return Verdict(False, 0, Breakdown(0, 0, 0), True)
    lines = data.decode("utf-8", errors="replace").split("\n")
    if end > tail_bytes and lines:
        lines = lines[1:]                  # 窗口首行可能是半行，丢弃
    texts: dict[str, list[str]] = {}       # message id → 文本块（分片聚合）
    last_text_key: str | None = None       # 末条带文本的 assistant 消息
    used: dict[str, str] = {}              # tool_use id → name（缺名记 ""，不给豁免）
    served: set[str] = set()
    for i, line in enumerate(lines):
        try:
            d = json.loads(line)
        except ValueError:
            continue
        if not isinstance(d, dict):
            continue
        msg = d.get("message")
        if not isinstance(msg, dict):
            msg = {}
        content = msg.get("content")
        if d.get("type") == "assistant":
            if isinstance(content, str):
                parts = [content]
            elif isinstance(content, list):
                parts = [b.get("text") for b in content
                         if isinstance(b, dict) and b.get("type") == "text"]
            else:
                parts = []
            parts = [p for p in parts if isinstance(p, str) and p.strip()]
            if parts:
                mid = msg.get("id")
                key = mid if isinstance(mid, str) else f"#{i}"
                texts.setdefault(key, []).extend(parts)
                last_text_key = key
        if isinstance(content, list):
            for b in content:
                if not isinstance(b, dict):
                    continue
                if b.get("type") == "tool_use" and isinstance(b.get("id"), str):
                    name = b.get("name")
                    used[b["id"]] = name if isinstance(name, str) else ""
                elif b.get("type") == "tool_result" and isinstance(b.get("tool_use_id"), str):
                    served.add(b["tool_use_id"])
    dangling = set(used) - served
    aq_dangling = all(used[tid] == ASK_USER_QUESTION for tid in dangling)
    if last_text_key is None:
        return Verdict(False, 0, Breakdown(0, 0, 0), aq_dangling)
    # 块边界视作换行边界：不同 text 块不共行，避免前后块首尾粘行漏计
    body = _CODE_FENCE_RE.sub("", "\n".join(texts[last_text_key]))
    bd = _classify(body)
    return Verdict(bd.total >= min_questions, bd.total, bd, aq_dangling)


def correlate_miss_signals(rows: list[dict], *,
                           idle_s: float = MISS_IDLE_S,
                           lookback_s: float = MISS_LOOKBACK_S) -> int:
    """漏检关联计数（票06，observe 期粗粒度信号）：对账本行做纯计数关联。

    一次"全量重付的闲置复活请求"（usage 行：cache_read_tokens == 0，且与该
    会话上一条 usage 行间隔 ≥ idle_s——首条无前驱不算复活）若此前 lookback_s
    内该会话有过"末条疑似提问"命中（qwatch_hit 事件）、且最近一次命中与其
    之间无任何真跳保温（beat 行 outcome ≠ observe——observe 演练未真发、
    不保温），计一次漏检信号：守望看见了提问潮、没能保温、用户最终全量重付。
    其间有真跳出场（hit/miss/error）即检测与执行已尽职——其后重付归 TTL
    漂移/死区取舍（熔断与 D4 遥测管辖），不计漏检。判据真漏检（检测器没
    认出提问潮）无正文级证据可回溯，粗粒度信号不含它——回调判据靠 D8 命中
    清单人工复核。纯函数、只读计数类字段、坏行缺字段一律跳过（隐私不变量：
    计数与 bool，永不接触消息内容）。
    """
    usage: dict[str, list[list[float]]] = {}    # sid → [(ts, cache_read), ...]
    hits: dict[str, list[float]] = {}           # sid → [命中 ts, ...]
    real_beats: dict[str, list[float]] = {}     # sid → [真跳 ts, ...]（observe 演练除外）
    for r in rows:
        if not isinstance(r, dict):
            continue
        sid, ts = r.get("session_id"), r.get("ts")
        if not isinstance(sid, str) or not sid or not isinstance(ts, (int, float)):
            continue
        kind = r.get("kind")
        if kind == "usage":
            cr = r.get("cache_read_tokens")
            if isinstance(cr, (int, float)):
                usage.setdefault(sid, []).append([float(ts), float(cr)])
        elif kind == "qwatch_hit":
            hits.setdefault(sid, []).append(float(ts))
        elif kind == "beat" and r.get("outcome") != OUT_OBSERVE:
            real_beats.setdefault(sid, []).append(float(ts))
    n = 0
    for sid, turns in usage.items():
        turns.sort()
        for (prev_ts, _), (ts, cr) in zip(turns, turns[1:]):
            if cr != 0.0 or ts - prev_ts < idle_s:
                continue    # 非闲置复活 / 非全量重付（首条无前驱天然出局）
            window = [t for t in hits.get(sid, []) if ts - lookback_s <= t < ts]
            if not window:
                continue    # 此前 30 分钟内无末条疑似提问命中可关联
            hit_ts = max(window)             # 取最近一次命中锚定真跳回看
            if any(hit_ts <= b < ts for b in real_beats.get(sid, [])):
                continue    # 其间有真跳保温——非漏检
            n += 1
    return n
