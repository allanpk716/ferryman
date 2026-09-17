"""用量采集：增量尾读会话文件，把每次助手记录的 token 用量落账（设计 §3.7）。

- 只取数字与类型，永不落消息内容（隐私不变量）；
- 增量：记住每文件已消费字节偏移，只解析新增的完整行（残行留待下轮）；
- 断点：偏移随 usage 流水入账，daemon 重启后从账本恢复——账本即唯一状态；
  偏移先推进后返回，record() 中途失败时该批尾部行永久丢失（at-most-once，故障隔离语义）；
- 范围 v1：仅 CC 主会话（subagents 转录由守望 glob 层排除；Codex 挂后续）。
"""

from __future__ import annotations

import json
from datetime import datetime
from pathlib import Path


def _ts_of(rec: dict) -> float | None:
    # 无时区的 timestamp 按本地时区解释（CC 转录恒带 Z，现实无影响）。
    ts = rec.get("timestamp")
    if not isinstance(ts, str):
        return None
    try:
        return datetime.fromisoformat(ts.replace("Z", "+00:00")).timestamp()
    except ValueError:
        return None


def _int(v) -> int | None:
    """宽松取整：null/字符串等取不动就 None，由调用方决定整行跳过（审查 Nit）。"""
    try:
        return int(v)
    except (TypeError, ValueError):
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
        msg = rec.get("message")
        if not isinstance(msg, dict):
            continue
        usage = msg.get("usage")
        if not isinstance(usage, dict) or not usage:
            continue
        input_tokens = _int(usage.get("input_tokens", 0))
        cache_read_tokens = _int(usage.get("cache_read_input_tokens", 0))
        cache_creation_tokens = _int(usage.get("cache_creation_input_tokens", 0))
        output_tokens = _int(usage.get("output_tokens", 0))
        if None in (input_tokens, cache_read_tokens,
                    cache_creation_tokens, output_tokens):
            continue            # 数字取不动的行整行跳过，不落数字错误的账
        rows.append({"ts": _ts_of(rec), "model": str(msg.get("model", "")),
                     "input_tokens": input_tokens,
                     "cache_read_tokens": cache_read_tokens,
                     "cache_creation_tokens": cache_creation_tokens,
                     "output_tokens": output_tokens})
    return rows, title, cwd


class HarvestState:
    """每会话文件的采集偏移；daemon 重启后由账本恢复（usage 行自带 offset）。

    病态场景（同路径文件被重写收缩）：偏移清零从头重采，可能与旧行重复——
    append-only 不改写旧账，report 层可按 (lineage_id, ts) 去重
    （重采行的 ts 取自转录原文，重复行 ts 相同）。
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
            off = int(e.get("offset") or 0)
            if off > self._offsets.get(key, -1):
                self._offsets[key] = off
            if e.get("title"):
                self._titles[key] = e["title"]
            if e.get("project"):
                self._cwds[key] = e["project"]

    def maybe_harvest(self, path: Path, size: int, *, agent: str) -> list[dict]:
        """有新增则尾读出 usage 行（带 title/project/offset）；无新增返回空。

        残行（无换行结尾）整段留待下一轮；文件打不开返回空、不推进偏移。
        键按 (agent, 文件名 stem)——假设监视目录树下会话文件名唯一（CC 为 UUID）；
        同名冲突会导致状态串扰。
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
