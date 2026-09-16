"""T02/T04 · extract：token 计量、骨架、正文提取、峰值。"""

from pathlib import Path

from ferryman.extract import Turn, chunk_items, extract, material_text, token_estimate
from ferryman.transcripts import assistant_turns


def _write(tmp_path: Path, lines) -> Path:
    import json
    f = tmp_path / "s.jsonl"
    f.write_text("\n".join(json.dumps(x, ensure_ascii=False) if isinstance(x, dict) else x
                           for x in lines) + "\n", encoding="utf-8")
    return f


# ---------- T02 token_estimate ----------

def test_token_estimate_pure_cjk():
    assert token_estimate("中" * 100) == 101   # 100 + 1


def test_token_estimate_ascii():
    est = token_estimate("a" * 35)
    assert 10 <= est <= 12                     # ~35/3.5


def test_token_estimate_mixed_and_empty():
    est = token_estimate("中a" * 10)           # 10 CJK + 10 ASCII
    assert 12 <= est <= 14
    assert token_estimate("") == 1


# ---------- T04 extract ----------

def _fixture(tmp_path: Path) -> Path:
    return _write(tmp_path, [
        {"type": "user", "timestamp": "2026-09-16T10:00:00.000Z",
         "cwd": "C:\\repo", "message": {"role": "user", "content": "做点事"}},
        {"type": "assistant", "timestamp": "2026-09-16T10:00:10.000Z",
         "message": {"role": "assistant", "content": [
             {"type": "tool_use", "name": "Edit",
              "input": {"file_path": "C:\\repo\\a.ts", "old_string": "x", "new_string": "y"}},
             {"type": "tool_use", "name": "Edit",
              "input": {"file_path": "C:\\repo\\a.ts", "old_string": "y", "new_string": "z"}},
             {"type": "tool_use", "name": "Bash",
              "input": {"command": "c" * 200}},
             {"type": "tool_use", "name": "Bash",
              "input": {"command": "c" * 200}},          # 重复命令应去重
             {"type": "tool_use", "name": "Bash", "input": {"command": "echo hi"}},
             {"type": "thinking", "thinking": "内心戏不应进正文"},
             {"type": "text", "text": "改完了 a.ts"},
         ], "usage": {"input_tokens": 1000, "cache_read_input_tokens": 2000,
                       "cache_creation_input_tokens": 500, "output_tokens": 10}}},
        {"type": "user", "timestamp": "2026-09-16T10:00:20.000Z",
         "message": {"role": "user", "content": [         # tool_result 整块丢弃
             {"type": "tool_result", "tool_use_id": "t1",
              "content": "巨大的工具输出不应进入正文"}]}},
        {"type": "user", "timestamp": "2026-09-16T10:00:30.000Z",
         "message": {"role": "user", "content": "长" * 5000}},   # 截断到 4000
        {"type": "assistant", "timestamp": "2026-09-16T10:00:40.000Z",
         "message": {"role": "assistant", "content": [
             {"type": "text", "text": "收尾"}],
             "usage": {"input_tokens": 9000, "cache_read_input_tokens": 0,
                        "cache_creation_input_tokens": 0, "output_tokens": 5}}},
        {"type": "ai-title", "aiTitle": "测试会话标题"},
    ])


def test_extract_skeleton(tmp_path):
    facts, items, _turns = extract(_fixture(tmp_path))
    assert facts.title == "测试会话标题"
    assert facts.cwd == "C:\\repo"
    assert facts.files[0] == ("C:\\repo\\a.ts", 2)        # 按次数降序
    assert len(facts.commands) == 2                        # 去重后
    assert len(facts.commands[0]) == 160                   # 截断
    assert facts.commands[1] == "echo hi"
    assert facts.peak_ctx == 9000                          # 第二轮 9000 > 第一轮 3500


def test_extract_items(tmp_path):
    _facts, items, _turns = extract(_fixture(tmp_path))
    texts = [it.text for it in items]
    assert any("做点事" in t for t in texts)
    assert any("改完了 a.ts" in t for t in texts)
    assert not any("内心戏" in t for t in texts)          # thinking 丢弃
    assert not any("工具输出" in t for t in texts)        # tool_result 丢弃
    long_user = [t for t in texts if t.startswith("长")]
    assert long_user and len(long_user[0]) == 4000         # 单条截断


def test_chunk_items_budget(tmp_path):
    _facts, items, _turns = extract(_fixture(tmp_path))
    chunks = chunk_items(items, budget_tokens=10)          # 极小预算 → 每条一块
    assert len(chunks) == len(items)
    chunks = chunk_items(items, budget_tokens=100000)
    assert len(chunks) == 1


def test_assistant_turns_sorted_and_defensive(tmp_path):
    f = _write(tmp_path, [
        {"type": "assistant", "timestamp": "2026-09-16T10:00:40.000Z",  # 无 Z 也行
         "message": {"role": "assistant", "content": [],
                     "usage": {"input_tokens": 5, "cache_read_input_tokens": 0,
                                "cache_creation_input_tokens": 0}}},
        "{broken json",
        {"type": "assistant", "timestamp": "2026-09-16T10:00:10.000Z",
         "message": {"role": "assistant", "content": [],
                     "usage": {"input_tokens": 7, "cache_read_input_tokens": 0,
                                "cache_creation_input_tokens": 0}}},
        {"type": "assistant", "timestamp": "2026-09-16T10:00:20.000Z",   # 缺 usage
         "message": {"role": "assistant", "content": []}},
        {"type": "user", "timestamp": "2026-09-16T10:00:30.000Z",        # 非 assistant
         "message": {"role": "user", "content": "hi"}},
    ])
    turns = assistant_turns(f)
    assert [t.input_tokens for t in turns] == [7, 5]       # 坏行跳过 + 按时间排序
    assert all(isinstance(t, Turn) for t in turns)


def test_material_text_contains_skeleton_and_items(tmp_path):
    facts, items, _turns = extract(_fixture(tmp_path))
    md = material_text(facts, items)
    assert "确定性骨架" in md and "a.ts" in md and "[user] 做点事" in md
