"""L0 提取器：CC 会话 jsonl → 确定性骨架 + 对话正文（摆渡执行器与评测共用的前半段）。

设计依据 docs/DESIGN.md §6.4（三层漏斗之 L0 + 确定性骨架/模型叙事分工）：
- 骨架：文件改动、命令、时间线、标题、峰值上下文——程序化抽取，零经过模型；
- 正文：user/assistant 的 text（thinking、tool_result 输出一律丢弃，防注入面最小化）；
- token 估算：CJK 1 token/字、其余 chars/3.5（DESIGN §6.8 计量口径）。

防御纪律同 transcripts.py：坏行/缺字段静默跳过。
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass, field
from pathlib import Path

from .transcripts import Turn, ai_title, assistant_turns

_ITEM_CHAR_CAP = 4000       # 单条正文截断（防超长粘贴撑爆材料）
_CMD_CHAR_CAP = 160         # 骨架里单条命令截断
_MAX_COMMANDS = 2000        # 收集上限（防病态内存；正常会话远不及，渲染时取尾部 20）
_MAX_FILES = 200

_FREEZE_USER_CAP = 500      # 末段定格：用户末条截断（设计文档 §4.1）
_FREEZE_ASST_CAP = 1500     # 末段定格：助手末条截断
_TRUNC_MARK = "（已截断，全文见会话文件）"

_CJK = re.compile(r"[　-鿿＀-￯]")


def token_estimate(text: str) -> int:
    """DESIGN §6.8 计量口径：CJK 1 token/字，其余按 3.5 字符/token（保守近似）。"""
    cjk = len(_CJK.findall(text))
    other = len(text) - cjk
    return cjk + int(other / 3.5) + 1


@dataclass
class Facts:
    """确定性骨架（零经过模型）。"""

    source: str = ""
    title: str | None = None
    cwd: str | None = None
    first_ts: str | None = None
    last_ts: str | None = None
    n_turns: int = 0
    peak_ctx: int = 0
    files: list[tuple[str, int]] = field(default_factory=list)   # (路径, 次数) 按次数降序
    commands: list[str] = field(default_factory=list)            # 按首次出现序
    total_input_tokens: int = 0
    # 末段定格（§4.1）：最后一轮对话的程序化保留，INJECT 层第一段
    freeze_user: str = ""                                   # 末条用户文本（≤500 字）
    freeze_asst_text: str = ""                              # 末条助手文本（≤1500 字）
    freeze_tools: list[str] = field(default_factory=list)   # 末条助手的工具名
    freeze_choice: tuple[str, list[str]] | None = None      # (问题一句话, [选项标签])

    def skeleton_text(self) -> str:
        # 末段定格三档互斥，优先级：choice > 工具摘要 > 逐字文本（choice 时文本也带上）
        lines = ["## 末段定格（最后一轮对话，程序化保留）"]
        lines.append(f"[user] {self.freeze_user or '（无）'}")
        if self.freeze_choice is not None:
            q, labels = self.freeze_choice
            lines.append(f"[assistant] {self.freeze_asst_text or '（无）'}")
            lines.append(f"【上次停在选择】问题：{q}")
            lines += [f"  选项：{lb}" for lb in labels]
            lines.append("（选择原文见会话文件，请在新会话中重述该选择）")
        elif not self.freeze_asst_text and self.freeze_tools:
            lines.append(f"[assistant] （末条为工具调用：{'，'.join(self.freeze_tools)}，无文字回复）")
        else:
            lines.append(f"[assistant] {self.freeze_asst_text or '（无）'}")
        lines += [
            "## 确定性骨架（程序化抽取，未经模型）",
            f"- 标题: {self.title or '(无)'}",
            f"- 工作目录: {self.cwd or '(未知)'}",
            f"- 起止: {self.first_ts or '?'} ~ {self.last_ts or '?'}（{self.n_turns} 轮，峰值上下文 {self.peak_ctx} tokens）",
        ]
        if self.files:
            top = ", ".join(f"{p}(×{c})" for p, c in self.files[:20])
            lines.append(f"- 涉及文件（次数降序，前 20）: {top}")
        if self.commands:
            recent = self.commands[-20:]
            lines.append(f"- 执行过的命令（去重后最近 {len(recent)} 条，序）:")
            lines += [f"  - {c}" for c in recent]
        return "\n".join(lines)


@dataclass
class Item:
    role: str  # "user" | "assistant"
    text: str


def _content_text(content) -> str:
    """取 message.content 里的 text 部分；其余类型（thinking/tool_use/tool_result）由调用方分流。"""
    if isinstance(content, str):
        return content
    parts: list[str] = []
    if isinstance(content, list):
        for b in content:
            if isinstance(b, dict) and b.get("type") == "text":
                t = b.get("text")
                if isinstance(t, str):
                    parts.append(t)
    return "\n".join(parts)


def _freeze_finalize(facts: Facts, items: list[Item],
                     asst_text: str, asst_tools: list[str],
                     asst_choice: tuple[str, list[str]] | None) -> None:
    """末段定格定稿：末条用户/助手文本（超长截断加尾标）+ 末条助手的选择与工具名。"""
    facts.freeze_user = next((it.text for it in reversed(items) if it.role == "user"), "")
    if len(facts.freeze_user) > _FREEZE_USER_CAP:
        facts.freeze_user = facts.freeze_user[:_FREEZE_USER_CAP] + _TRUNC_MARK
    at = asst_text.strip()
    facts.freeze_asst_text = at[:_FREEZE_ASST_CAP] + _TRUNC_MARK if len(at) > _FREEZE_ASST_CAP else at
    facts.freeze_tools = asst_tools
    facts.freeze_choice = asst_choice


def extract(path: Path) -> tuple[Facts, list[Item], list[Turn]]:
    """读取一个 CC 会话：返回 (骨架, 正文条目, usage 轮次)。单遍扫描。"""
    facts = Facts(source=str(path))
    items: list[Item] = []
    file_counts: dict[str, int] = {}
    commands: list[str] = []
    cmd_seen: set[str] = set()
    first_iso = last_iso = None
    last_asst_text = ""                                   # 末段定格跟踪（每条 assistant 覆盖）
    last_asst_tools: list[str] = []
    last_asst_choice: tuple[str, list[str]] | None = None

    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                if '"type"' not in line:
                    continue
                try:
                    d = json.loads(line)
                except ValueError:
                    continue
                t = d.get("type")

                ts = d.get("timestamp")
                if isinstance(ts, str):
                    if first_iso is None:
                        first_iso = ts
                    last_iso = ts
                if t == "ai-title":
                    title = d.get("aiTitle")
                    if isinstance(title, str) and title.strip():
                        facts.title = title.strip()
                    continue
                if t == "user" and facts.cwd is None:
                    cwd = d.get("cwd")
                    if isinstance(cwd, str) and cwd:
                        facts.cwd = cwd

                if t not in ("user", "assistant"):
                    continue
                msg = d.get("message") or {}
                content = msg.get("content")

                if t == "assistant":
                    # 末段定格跟踪：每条 assistant 先清空再记录，循环自然只留最后一条
                    last_asst_text = _content_text(content)
                    last_asst_tools = []
                    last_asst_choice = None
                    # tool_use → 骨架（file_path / command）
                    if isinstance(content, list):
                        for b in content:
                            if not (isinstance(b, dict) and b.get("type") == "tool_use"):
                                continue
                            name = b.get("name")
                            if isinstance(name, str) and name:
                                last_asst_tools.append(name)
                            if name == "AskUserQuestion":
                                qs = (b.get("input") or {}).get("questions")
                                # 多问只存第一问（v1 简化，其余问丢弃——原文见会话文件）
                                if (isinstance(qs, list) and qs and isinstance(qs[0], dict)
                                        and isinstance(qs[0].get("question"), str) and qs[0]["question"]):
                                    labels = [o["label"] for o in (qs[0].get("options") or [])
                                              if isinstance(o, dict) and isinstance(o.get("label"), str)]
                                    last_asst_choice = (qs[0]["question"], labels)
                            inp = b.get("input") or {}
                            fp = inp.get("file_path") or inp.get("notebook_path")
                            if isinstance(fp, str) and fp:
                                file_counts[fp] = file_counts.get(fp, 0) + 1
                            cmd = inp.get("command")
                            if isinstance(cmd, str) and cmd.strip():
                                key = cmd.strip()
                                if key not in cmd_seen and len(commands) < _MAX_COMMANDS:
                                    cmd_seen.add(key)
                                    commands.append(key[:_CMD_CHAR_CAP])
                    if last_asst_text.strip():
                        items.append(Item("assistant", last_asst_text.strip()[:_ITEM_CHAR_CAP]))
                else:  # user：只要 text，tool_result（工具输出）整块丢弃
                    if isinstance(content, list) and any(
                        isinstance(b, dict) and b.get("type") == "tool_result" for b in content
                    ):
                        continue
                    text = _content_text(content)
                    if text.strip():
                        items.append(Item("user", text.strip()[:_ITEM_CHAR_CAP]))
    except OSError:
        _freeze_finalize(facts, items, last_asst_text, last_asst_tools, last_asst_choice)
        return facts, items, []

    turns = assistant_turns(path)
    facts.first_ts, facts.last_ts = first_iso, last_iso
    facts.n_turns = len(turns)
    facts.peak_ctx = max((t.ctx_tokens for t in turns), default=0)
    facts.total_input_tokens = sum(t.input_tokens + t.cache_read + t.cache_creation for t in turns)
    facts.files = sorted(file_counts.items(), key=lambda kv: (-kv[1], kv[0]))[:_MAX_FILES]
    facts.commands = commands
    _freeze_finalize(facts, items, last_asst_text, last_asst_tools, last_asst_choice)
    if facts.title is None:
        facts.title = ai_title(path)
    return facts, items, turns


def material_text(facts: Facts, items: list[Item]) -> str:
    """L1 输入材料 = 骨架 + 正文流。"""
    parts = [facts.skeleton_text(), "", "## 会话正文（user/assistant 文本，工具输出已省略）"]
    parts += [f"[{it.role}] {it.text}" for it in items]
    return "\n".join(parts)


def chunk_items(items: list[Item], budget_tokens: int) -> list[list[Item]]:
    """L2 分块：按 token 预算切正文条目（骨架不参与分块，进 reduce 阶段）。"""
    chunks: list[list[Item]] = []
    cur: list[Item] = []
    cur_tokens = 0
    for it in items:
        n = token_estimate(it.text) + 8
        if cur and cur_tokens + n > budget_tokens:
            chunks.append(cur)
            cur, cur_tokens = [], 0
        cur.append(it)
        cur_tokens += n
    if cur:
        chunks.append(cur)
    return chunks
