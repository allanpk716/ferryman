"""Codex CLI rollout（jsonl）的防御式读取器。

与 transcripts.py 同一防御纪律：坏行/缺字段静默跳过，绝不抛错。

语义（经 2026-06 会话实测校准）：token_count.last_token_usage 里
input_tokens = 完整 prompt（**已包含** cached_input_tokens，OpenAI 惯例；
input 随对话单调增长、cached ≤ input 恒成立），因此：
命中率 = cached ÷ input；上下文规模 ≈ input。
若未来版本语义变化（cached > input 出现即 disjoint/CC 式），调用方需切换公式。

rollout 结构（2026-09-17 真机 2.8MB 样本校准）：
- 每行 {timestamp, type, payload}；type ∈ session_meta / response_item /
  event_msg / turn_context / world_state / token_usage_record；
- 正文在 response_item.payload.type == "message"（role user/assistant，
  content 块 input_text/output_text；developer=技能指令，防注入面丢弃）；
- 工具调用在 response_item.payload.type == "function_call"（name + arguments
  JSON 串）；function_call_output/reasoning 丢弃（工具输出/思考不进材料）。
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

from .extract import _CMD_CHAR_CAP, _ITEM_CHAR_CAP, _MAX_COMMANDS, _MAX_FILES, Facts, Item

_TITLE_CHAR_CAP = 60          # 标题（首条用户消息首行）截断

# function_call.arguments 里视为"命令"的字段（exec_command 的 cmd 等）
_CMD_ARG_KEYS = ("cmd", "command")
# apply_patch 补丁头里的文件操作行（codex 改文件的主通道）
_PATCH_FILE_PREFIXES = ("*** Update File: ", "*** Add File: ", "*** Delete File: ")

# codex 伪装成 user 消息的环境注入前缀（2026-09-17 真机实测：AGENTS.md 指令
# 1238 字节、<turn_aborted> 中断标记）。只枚举已知标记，不泛匹配 "<"——
# 真人粘贴 HTML/XML 片段是常见操作，误杀代价大于漏杀。
_INJECTED_USER_PREFIXES = (
    "# AGENTS.md instructions",
    "<turn_aborted>",
    "<environment_context>",
    "<user_instructions>",
    "<skills_instructions>",
)


def _is_injected_user_text(text: str) -> bool:
    t = text.lstrip()
    return any(t.startswith(p) for p in _INJECTED_USER_PREFIXES)


@dataclass(frozen=True)
class Turn:
    """一条 token_count 轮次（时间已统一为 UTC epoch 秒）。"""

    ts: float
    input_tokens: int  # 完整 prompt（含 cached）
    cached: int
    cache_write: int


def _ts_to_epoch(v: object) -> float | None:
    if not isinstance(v, str) or not v:
        return None
    try:
        dt = datetime.fromisoformat(v.replace("Z", "+00:00"))
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.timestamp()


def token_count_turns(path: Path) -> list[Turn]:
    """会话内全部 token_count 轮次（last_token_usage 口径），按时间升序。"""
    turns: list[Turn] = []
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                if '"token_count"' not in line:
                    continue
                try:
                    d = json.loads(line)
                except ValueError:
                    continue
                if d.get("type") != "event_msg":
                    continue
                payload = d.get("payload") or {}
                if payload.get("type") != "token_count":
                    continue
                u = (payload.get("info") or {}).get("last_token_usage") or {}
                try:
                    inp = int(u.get("input_tokens") or 0)
                    cached = int(u.get("cached_input_tokens") or 0)
                    w = int(u.get("cache_write_input_tokens") or 0)
                except (TypeError, ValueError):
                    continue
                ts = _ts_to_epoch(d.get("timestamp"))
                if ts is None or inp <= 0:
                    continue
                turns.append(Turn(ts, inp, cached, w))
    except OSError:
        return []
    turns.sort(key=lambda t: t.ts)
    return turns


def session_cwd(path: Path) -> str:
    """首条 session_meta 的 payload.cwd（防御式：缺失/坏行/超头部未见过返回空）。

    session_meta 恒为 rollout 首行（2026-06 实测）；只扫头部 10 行防大开文件。
    """
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for i, line in enumerate(f):
                if i >= 10:
                    break
                if '"session_meta"' not in line:
                    continue
                try:
                    d = json.loads(line)
                except ValueError:
                    continue
                if d.get("type") == "session_meta":
                    p = d.get("payload")
                    if isinstance(p, dict):
                        return str(p.get("cwd") or "")
    except OSError:
        return ""
    return ""


def _message_text(payload: dict) -> str:
    """message 的 content 里 input_text/output_text 块的文本（其余类型丢弃）。"""
    parts: list[str] = []
    content = payload.get("content")
    if isinstance(content, list):
        for b in content:
            if not (isinstance(b, dict) and isinstance(b.get("text"), str)):
                continue
            if b.get("type") in ("input_text", "output_text"):
                parts.append(b["text"])
    elif isinstance(content, str):            # 极简容错：无块结构时直接取串
        parts.append(content)
    return "\n".join(parts)


def _skeleton_from_call(name: object, arguments: object,
                        file_counts: dict[str, int],
                        commands: list[str], cmd_seen: set[str]) -> None:
    """一个 function_call → 骨架增量（命令 + 文件）。防御式：坏参数全跳过。"""
    args: dict = {}
    if isinstance(arguments, str) and arguments.strip():
        try:
            a = json.loads(arguments)
            if isinstance(a, dict):
                args = a
        except ValueError:
            args = {}
    elif isinstance(arguments, dict):
        args = arguments

    tool = str(name or "")
    for key in _CMD_ARG_KEYS:
        cmd = args.get(key)
        if isinstance(cmd, str) and cmd.strip():
            k = cmd.strip()
            if k not in cmd_seen and len(commands) < _MAX_COMMANDS:
                cmd_seen.add(k)
                commands.append(k[:_CMD_CHAR_CAP])
            break

    if tool == "apply_patch":
        patch = args.get("input") or args.get("patch") or ""
        if isinstance(patch, str):
            for line in patch.splitlines():
                line = line.strip()
                for prefix in _PATCH_FILE_PREFIXES:
                    if line.startswith(prefix):
                        fp = line[len(prefix):].strip()
                        if fp:
                            file_counts[fp] = file_counts.get(fp, 0) + 1
    fp = args.get("file_path") or args.get("notebook_path")
    if isinstance(fp, str) and fp:
        file_counts[fp] = file_counts.get(fp, 0) + 1


def extract_codex(path: Path) -> tuple[Facts, list[Item]]:
    """读取一个 codex rollout：返回 (骨架, 正文条目)。单遍扫描，防御式。

    2026-09-17 11:17 事故的修复主体：此前 ferry_session 只会 CC 格式，
    codex 会话 0 正文进模型 → "无实际开发活动"垃圾交接。
    """
    facts = Facts(source=str(path))
    items: list[Item] = []
    file_counts: dict[str, int] = {}
    commands: list[str] = []
    cmd_seen: set[str] = set()
    first_iso = last_iso = None

    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                if '"type"' not in line:
                    continue
                try:
                    d = json.loads(line)
                except ValueError:
                    continue
                ts = d.get("timestamp")
                if isinstance(ts, str):
                    if first_iso is None:
                        first_iso = ts
                    last_iso = ts
                t = d.get("type")
                if t == "session_meta":
                    p = d.get("payload")
                    if isinstance(p, dict) and not facts.cwd:
                        cwd = p.get("cwd")
                        if isinstance(cwd, str) and cwd:
                            facts.cwd = cwd
                    continue
                if t != "response_item":
                    continue
                p = d.get("payload")
                if not isinstance(p, dict):
                    continue
                pt = p.get("type")
                if pt == "message":
                    role = p.get("role")
                    if role not in ("user", "assistant"):
                        continue          # developer/system = 指令注入面，不进材料
                    text = _message_text(p).strip()
                    if role == "user" and _is_injected_user_text(text):
                        continue          # codex 环境注入伪装的 user 消息
                    if text:
                        items.append(Item(role, text[:_ITEM_CHAR_CAP]))
                elif pt == "function_call":
                    _skeleton_from_call(p.get("name"), p.get("arguments"),
                                        file_counts, commands, cmd_seen)
                # reasoning / function_call_output：不进骨架也不进正文
    except OSError:
        return facts, items

    turns = token_count_turns(path)
    facts.first_ts, facts.last_ts = first_iso, last_iso
    facts.n_turns = len(turns)
    facts.peak_ctx = max((t2.input_tokens for t2 in turns), default=0)
    facts.total_input_tokens = sum(t2.input_tokens for t2 in turns)
    facts.files = sorted(file_counts.items(), key=lambda kv: (-kv[1], kv[0]))[:_MAX_FILES]
    facts.commands = commands
    for it in items:                      # 标题：首条用户消息首行（rollout 无 ai-title）
        if it.role == "user":
            first_line = it.text.splitlines()[0].strip() if it.text else ""
            if first_line:
                facts.title = first_line[:_TITLE_CHAR_CAP]
            break
    return facts, items
