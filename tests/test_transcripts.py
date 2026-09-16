"""T31 · 悬空 tool_use 判定（transcripts.py 尾部读取器）。

守望入队前的运行中判定：最后的 tool_use 未收到 tool_result = 工具/子代理仍在跑，
按 mtime 看似闲置实则运行中，摆渡应推迟。covers_until 兜正确性，本判定只防浪费。
"""

import json
from pathlib import Path

from ferryman.transcripts import has_dangling_tool_use


def _line(d: dict) -> str:
    return json.dumps(d, ensure_ascii=False)


def _assistant(*tool_ids: str, text: str = "好") -> dict:
    blocks = [{"type": "text", "text": text}]
    blocks += [{"type": "tool_use", "id": tid, "name": "Bash", "input": {}} for tid in tool_ids]
    return {"type": "assistant", "timestamp": "2026-09-16T12:00:00.000Z",
            "message": {"role": "assistant", "content": blocks,
                        "usage": {"input_tokens": 10, "cache_read_input_tokens": 100,
                                   "cache_creation_input_tokens": 0, "output_tokens": 5}}}


def _result(*tool_ids: str) -> dict:
    return {"type": "user", "timestamp": "2026-09-16T12:00:01.000Z",
            "message": {"role": "user", "content": [
                {"type": "tool_result", "tool_use_id": tid, "content": "ok"} for tid in tool_ids]}}


def _write(tmp_path: Path, lines: list[str]) -> Path:
    f = tmp_path / "s.jsonl"
    f.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return f


def test_t31_dangling_use_is_true(tmp_path):
    """最后 assistant 消息带 tool_use 且无后续 tool_result → 运行中。"""
    f = _write(tmp_path, [_line(_assistant("t1"))])
    assert has_dangling_tool_use(f) is True


def test_t31_matched_use_is_false(tmp_path):
    """tool_use 已有同 id tool_result → 静止。"""
    f = _write(tmp_path, [_line(_assistant("t1")), _line(_result("t1"))])
    assert has_dangling_tool_use(f) is False


def test_t31_text_only_tail_is_false(tmp_path):
    """尾部无 tool_use（纯文本收尾）→ 静止。"""
    f = _write(tmp_path, [_line(_assistant()), _line(_assistant())])
    assert has_dangling_tool_use(f) is False


def test_t31_empty_file_is_false(tmp_path):
    """空文件/无 assistant 行 → 静止（宁可多摆渡）。"""
    f = tmp_path / "empty.jsonl"
    f.write_text("", encoding="utf-8")
    assert has_dangling_tool_use(f) is False
    assert has_dangling_tool_use(tmp_path / "nope.jsonl") is False   # 缺文件也静止


def test_t31_earlier_pair_plus_new_dangling_is_true(tmp_path):
    """历史配对完整、仅最新的 tool_use 悬空 → 运行中。"""
    f = _write(tmp_path, [_line(_assistant("t1")), _line(_result("t1")),
                          _line(_assistant("t2"))])
    assert has_dangling_tool_use(f) is True


def test_t31_multi_block_needs_all_matched(tmp_path):
    """一条 assistant 多个 tool_use：全部收到 result 才算静止。"""
    f = _write(tmp_path, [_line(_assistant("t1", "t2")), _line(_result("t1"))])
    assert has_dangling_tool_use(f) is True


def test_t31_window_cut_tolerates_miss(tmp_path):
    """悬空 tool_use 被挤出尾部窗口 → 允许漏判（best-effort，covers_until 兜底）。"""
    filler = _line(_assistant("old")) + "\n" + _line(_result("old")) + "\n" + "x" * 400 + "\n"
    f = _write(tmp_path, [filler[:-1], _line(_assistant("t1"))])
    assert has_dangling_tool_use(f, tail_bytes=1024) is True   # 窗口装得下整条 t1 行 → 检出
    assert has_dangling_tool_use(f, tail_bytes=64) is False    # t1 行被切成半行丢弃 → 漏判
