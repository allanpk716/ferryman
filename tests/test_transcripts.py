"""T31 · 悬空 tool_use 判定（transcripts.py 尾部读取器）。

守望入队前的运行中判定：最后的 tool_use 未收到 tool_result = 工具/子代理仍在跑，
按 mtime 看似闲置实则运行中，摆渡应推迟。covers_until 兜正确性，本判定只防浪费。
"""

import json
from pathlib import Path

from ferryman.transcripts import has_async_launch, has_dangling_tool_use


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


# ---- T48 票01 · has_async_launch 尾部异步派发判定 ----

def _task_use(tid: str, name: str = "Task", inp: dict | None = None) -> dict:
    """一条 assistant 派发行（Task/Agent tool_use）。"""
    return {"type": "assistant", "timestamp": "2026-09-16T12:00:00.000Z",
            "message": {"role": "assistant", "content": [
                {"type": "tool_use", "id": tid, "name": name,
                 "input": inp if inp is not None else {"prompt": "干活"}}]}}


def _task_result(tid: str, content) -> dict:
    """一条 user tool_result 行（content 可为字符串或块列表）。"""
    return {"type": "user", "timestamp": "2026-09-16T12:00:01.000Z",
            "message": {"role": "user", "content": [
                {"type": "tool_result", "tool_use_id": tid, "content": content}]}}


def _write_blocks(tmp_path: Path, blocks: list[dict], name: str = "s.jsonl") -> Path:
    f = tmp_path / name
    f.write_text("\n".join(_line(b) for b in blocks) + "\n", encoding="utf-8")
    return f


def test_t48_async_launch_by_input_flag(tmp_path):
    """input.run_in_background 为真 → async 派发。"""
    f = _write_blocks(tmp_path, [
        _task_use("t1", inp={"prompt": "干活", "run_in_background": True}),
        _task_result("t1", "ok"),
    ])
    assert has_async_launch(f) is True


def test_t48_async_launch_by_background_alias(tmp_path):
    """background 别名标志位同样命中（CC 字段名两形态）。"""
    f = _write_blocks(tmp_path, [
        _task_use("t1", inp={"prompt": "干活", "background": True}),
        _task_result("t1", "ok"),
    ])
    assert has_async_launch(f) is True


def test_t48_async_launch_by_result_marker(tmp_path):
    """input 无标志位，但 tool_result 文本以实机文案为前缀 → async（实机兜底）。"""
    f = _write_blocks(tmp_path, [
        _task_use("t1", name="Agent"),
        _task_result("t1", "Async agent launched successfully (agent-abc)"),
    ])
    assert has_async_launch(f) is True


def test_t48_async_launch_result_first_text_block_prefix(tmp_path):
    """result 为块列表时取首个 text 块做前缀匹配（实机 result 形态）。"""
    f = _write_blocks(tmp_path, [
        _task_use("t1"),
        _task_result("t1", [{"type": "text",
                             "text": "Async agent launched successfully (agent-abc)"}]),
    ])
    assert has_async_launch(f) is True


def test_t48_sync_task_not_async(tmp_path):
    """同步派发（无标志位、result 无文案）→ False。"""
    f = _write_blocks(tmp_path, [
        _task_use("t1"),
        _task_result("t1", "done"),
    ])
    assert has_async_launch(f) is False


def test_t48_async_launch_last_dispatch_wins(tmp_path):
    """尾部最后一个 Task/Agent 派发说了算：旧的 async 标记不算数。

    交错派发（async A 在飞 + 再派 sync B）在本函数层面就是 False——
    async 等待的保留由 server 侧 saw_async latch 兜住（round 0 e2 实验）。"""
    f = _write_blocks(tmp_path, [
        _task_use("t1", inp={"run_in_background": True}),
        _task_result("t1", "ok"),
        _task_use("t2", inp={"prompt": "同步活"}),
        _task_result("t2", "done"),
    ])
    assert has_async_launch(f) is False


def test_t48_sync_result_midtext_repeat_is_false(tmp_path):
    """附录#6 负例：同步 result 中段复读实机文案（非前缀）→ 不误判 async。"""
    f = _write_blocks(tmp_path, [
        _task_use("t1"),
        _task_result("t1", "子代理报告：之前的派发 Async agent launched successfully，已收尾"),
    ])
    assert has_async_launch(f) is False


def test_t48_result_marker_only_in_second_text_block_is_false(tmp_path):
    """附录#6 严格化：只认首个 text 块——文案出现在第二块不算。"""
    f = _write_blocks(tmp_path, [
        _task_use("t1"),
        _task_result("t1", [
            {"type": "text", "text": "同步任务完成"},
            {"type": "text", "text": "Async agent launched successfully"},
        ]),
    ])
    assert has_async_launch(f) is False


def test_t48_async_launch_ignores_non_agent_tools(tmp_path):
    """非 Task/Agent 工具（如 Bash 的 run_in_background）不算子代理派发。"""
    f = _write_blocks(tmp_path, [
        _task_use("t1", name="Bash",
                  inp={"command": "ls", "run_in_background": True}),
        _task_result("t1", "ok"),
    ])
    assert has_async_launch(f) is False


def test_t48_async_launch_missing_or_empty_file_is_false(tmp_path):
    """文件缺失/空文件 → False（判不中一律退回旧语义）。"""
    assert has_async_launch(tmp_path / "none.jsonl") is False
    f = tmp_path / "empty.jsonl"
    f.write_text("", encoding="utf-8")
    assert has_async_launch(f) is False


def test_t48_async_launch_bad_lines_skipped_not_raised(tmp_path):
    """附录#11：message 非 dict（字符串/列表）与顶层非 dict 的坏行安全跳过。

    坏行均含 "tool_use"/"tool_result" 字样以穿过预过滤、真正命中守卫；
    只含坏行 → False；坏行夹在好行间 → 跳过后照常判定，绝不抛 AttributeError。"""
    bad_msg_str = {"type": "assistant", "message": "字符串不是dict",
                   "tool_use": {"id": "t9"}}          # 行内含 "tool_use" → 穿过预过滤
    bad_msg_list = {"type": "user", "message": ["列表也不是dict"],
                    "tool_result": {"tool_use_id": "t9"}}
    non_dict_line = json.dumps(["tool_use", "顶层是列表"])   # json.loads 出来不是 dict
    assert has_async_launch(_write_blocks(
        tmp_path, [bad_msg_str, bad_msg_list], name="bad_only.jsonl")) is False
    assert has_async_launch(_write_blocks(tmp_path, [
        bad_msg_str,
        _task_use("t1", inp={"run_in_background": True}),
        bad_msg_list,
        _task_result("t1", "ok"),
        non_dict_line,
    ])) is True
