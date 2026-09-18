"""E0C · usage 聚合器(e0c.py 解析层纯函数)。

合成 fixture 单测,覆盖 spec 验收标准:
- 同一 message.id 拆多行(thinking/tool_use 各一行)只计一次;
- 占位行(usage 全零/null/缺失)不贡献;stop=None 行不干扰;
- 组内非零 usage 冲突 → 整组入长尾、不入账;
- 无 id 行、坏/半行 JSON → 跳过并计长尾;
- 模型分布按行级 message.model 分组(与 meta.model 无关);
- 空文件/无 assistant 行 → 全零账目 + 无长尾,不抛异常。
"""

import io
import json
from pathlib import Path

from ferryman.e0c import aggregate_usage


# ---- 合成行构造 ---------------------------------------------------------------

def _u(i=100, o=20, cw=300, cr=4000):
    """标准完整 usage;带默认值方便断言四元组。"""
    return {"input_tokens": i, "output_tokens": o,
            "cache_creation_input_tokens": cw, "cache_read_input_tokens": cr}


_Z = _u(0, 0, 0, 0)


def _assistant(mid, *, model="GLM-5.3", stop="end_turn", usage=None, content="text"):
    """一条 assistant 行。usage=None → 标准完整 usage;"omit" → 整个字段不放。"""
    msg: dict = {"role": "assistant", "content": [{"type": content, "text": "x"}]}
    if usage == "omit":
        pass
    else:
        msg["usage"] = _u() if usage is None else usage
    if mid is not None:
        msg["id"] = mid
    if model is not None:
        msg["model"] = model
    if stop is not None:
        msg["stop_reason"] = stop
    return {"type": "assistant", "uuid": f"uuid-{mid}",
            "timestamp": "2026-09-18T10:00:00.000Z", "message": msg}


def _user_row():
    return {"type": "user", "timestamp": "2026-09-18T10:00:00.000Z",
            "message": {"role": "user", "content": "做点活"}}


def _write(tmp_path: Path, items: list) -> Path:
    """items 里 dict → json 行;str → 原样写入(构造坏/半行)。"""
    f = tmp_path / "s.jsonl"
    f.write_text("\n".join(json.dumps(x, ensure_ascii=False) if isinstance(x, dict) else x
                           for x in items) + "\n", encoding="utf-8")
    return f


FOUR = ("input_tokens", "output_tokens", "cache_creation", "cache_read")


def _four(acc):
    return tuple(getattr(acc, k) for k in FOUR)


# ---- 去重与占位 ---------------------------------------------------------------


def test_same_id_split_rows_counted_once(tmp_path):
    """同一 message.id 拆多行(thinking 前置占位行 + tool_use 终行)只计一次。"""
    lines = [
        _user_row(),
        _assistant("msg_1", stop=None, usage=_Z, content="thinking"),
        _assistant("msg_1", stop="tool_use", content="tool_use"),
        _assistant("msg_2", stop="end_turn"),
    ]
    acc = aggregate_usage(_write(tmp_path, lines))
    assert _four(acc) == (200, 40, 600, 8000)
    assert acc.responses == 2
    assert acc.longtail == []


def test_same_usage_duplicated_rows_counted_once(tmp_path):
    """两行同 id 携带同一份非零 usage(验收原文场景)→ 只计一次。"""
    lines = [
        _assistant("msg_1", content="thinking"),
        _assistant("msg_1", stop="tool_use", content="tool_use"),
    ]
    acc = aggregate_usage(_write(tmp_path, lines))
    assert _four(acc) == (100, 20, 300, 4000)
    assert acc.responses == 1
    assert acc.longtail == []


def test_placeholder_rows_do_not_contribute(tmp_path):
    """占位行(全零/字段缺失)不贡献账目,也不把所在组拖进长尾。"""
    lines = [
        _assistant("msg_1", stop=None, usage=_Z),
        _assistant("msg_1", stop=None, usage="omit"),
        _assistant("msg_1", stop="end_turn"),
        _assistant("msg_2", stop="end_turn", usage=_u(7, 1, 2, 3)),
    ]
    acc = aggregate_usage(_write(tmp_path, lines))
    assert _four(acc) == (107, 21, 302, 4003)
    assert acc.responses == 2
    assert acc.longtail == []


def test_all_zero_group_goes_longtail(tmp_path):
    """全零占位组(流式前置行写了、响应从未落地)→ 长尾,不入账不占 responses。"""
    lines = [
        _assistant("msg_ok", stop="end_turn"),
        _assistant("msg_dead", stop=None, usage=_Z),
        _assistant("msg_dead", stop=None, usage="omit"),
    ]
    acc = aggregate_usage(_write(tmp_path, lines))
    assert _four(acc) == (100, 20, 300, 4000)
    assert acc.responses == 1
    assert len(acc.longtail) == 1 and "msg_dead" in acc.longtail[0]


# ---- 冲突与坏行 ----------------------------------------------------------------


def test_nonzero_conflict_group_goes_longtail(tmp_path):
    """组内非零 usage 互不一致 → 整组入长尾、不计入加总(即便其中有 stop 行)。"""
    lines = [
        _assistant("msg_1", stop=None, usage=_u(100, 20, 300, 4000)),
        _assistant("msg_1", stop="tool_use", usage=_u(200, 20, 300, 4000)),
        _assistant("msg_2", stop="end_turn"),
    ]
    acc = aggregate_usage(_write(tmp_path, lines))
    assert _four(acc) == (100, 20, 300, 4000)  # 只有 msg_2 入账
    assert acc.responses == 1
    assert len(acc.longtail) == 1 and "msg_1" in acc.longtail[0]


def test_three_way_conflict_longtail(tmp_path):
    """三种非零 usage 同组 → 长尾,且互不一致的行谁带 stop 都救不回来。"""
    lines = [
        _assistant("msg_x", stop=None, usage=_u(1, 0, 0, 0)),
        _assistant("msg_x", stop=None, usage=_u(2, 0, 0, 0)),
        _assistant("msg_x", stop="end_turn", usage=_u(3, 0, 0, 0)),
    ]
    acc = aggregate_usage(_write(tmp_path, lines))
    assert _four(acc) == (0, 0, 0, 0)
    assert acc.responses == 0
    assert len(acc.longtail) == 1 and "3 种" in acc.longtail[0]


def test_missing_id_row_skipped_and_longtailed(tmp_path):
    """无 message.id 的 assistant 行 → 跳过并计长尾,不影响其余账目。"""
    lines = [
        _assistant(None, stop="end_turn", usage=_u(999, 0, 0, 0)),
        _assistant("msg_1", stop="end_turn"),
    ]
    acc = aggregate_usage(_write(tmp_path, lines))
    assert _four(acc) == (100, 20, 300, 4000)
    assert acc.responses == 1
    assert len(acc.longtail) == 1 and "message.id" in acc.longtail[0]


def test_bad_and_half_json_lines_skipped_and_longtailed(tmp_path):
    """坏 JSON 行与截断半行 → 跳过并计长尾;合法行照常入账。"""
    good = json.dumps(_assistant("msg_1"), ensure_ascii=False)
    half = json.dumps(_assistant("msg_1", usage=_u(500, 0, 0, 0)), ensure_ascii=False)[:40]
    f = tmp_path / "s.jsonl"
    f.write_text(good + "\n" + half + "\n" + "not json at all {\n", encoding="utf-8")
    acc = aggregate_usage(f)
    assert _four(acc) == (100, 20, 300, 4000)
    assert acc.responses == 1
    assert len(acc.longtail) == 2


def test_non_object_json_line_longtailed(tmp_path):
    """合法 JSON 但不是对象(数组行)→ 同坏行处理。"""
    lines = ['[1, 2, 3]', _assistant("msg_1")]
    acc = aggregate_usage(_write(tmp_path, lines))
    assert acc.responses == 1 and len(acc.longtail) == 1


def test_blank_lines_silent(tmp_path):
    """空白行(写入残留)静默跳过,不算坏行。"""
    f = tmp_path / "s.jsonl"
    f.write_text("\n" + json.dumps(_assistant("msg_1")) + "\n\n", encoding="utf-8")
    acc = aggregate_usage(f)
    assert acc.responses == 1 and acc.longtail == []


def test_non_assistant_rows_ignored_silently(tmp_path):
    """user/progress 等非 assistant 行是正常内容,静默忽略不计长尾。"""
    other = {"type": "progress", "data": "x"}
    acc = aggregate_usage(_write(tmp_path, [_user_row(), other, _assistant("msg_1")]))
    assert acc.responses == 1 and acc.longtail == []


# ---- 模型分布 ------------------------------------------------------------------


def test_by_model_row_level_distribution(tmp_path):
    """模型分布按行级 message.model 分组,各 model 四列独立累计。"""
    lines = [
        _assistant("msg_1", model="GLM-5.3"),
        _assistant("msg_2", model="claude-sonnet-5", usage=_u(10, 5, 0, 50)),
        _assistant("msg_3", model="GLM-5.3", usage=_u(10, 5, 0, 50)),
    ]
    acc = aggregate_usage(_write(tmp_path, lines))
    assert set(acc.by_model) == {"GLM-5.3", "claude-sonnet-5"}
    assert tuple(acc.by_model["GLM-5.3"]) == (110, 25, 300, 4050)
    assert tuple(acc.by_model["claude-sonnet-5"]) == (10, 5, 0, 50)
    assert acc.responses == 3


def test_stop_reason_row_model_preferred(tmp_path):
    """同 id 同 usage 拆两行、model 不同 → 取带 stop_reason 行的 model。"""
    lines = [
        _assistant("msg_1", model="M-early", stop=None),
        _assistant("msg_1", model="M-final", stop="end_turn"),
    ]
    acc = aggregate_usage(_write(tmp_path, lines))
    assert acc.responses == 1
    assert set(acc.by_model) == {"M-final"}


def test_missing_model_becomes_unknown(tmp_path):
    """行级 model 缺失 → 归入 unknown 桶,不抛异常。"""
    lines = [_assistant("msg_1", model=None)]
    acc = aggregate_usage(_write(tmp_path, lines))
    assert set(acc.by_model) == {"unknown"}
    assert tuple(acc.by_model["unknown"]) == (100, 20, 300, 4000)


# ---- 防御与输入形态 -------------------------------------------------------------


def test_empty_file_and_no_assistant_rows(tmp_path):
    """空文件 / 只有非 assistant 行 → 全零账目 + 无长尾,不抛异常。"""
    empty = tmp_path / "empty.jsonl"
    empty.write_text("", encoding="utf-8")
    for f in (empty, _write(tmp_path, [_user_row()])):
        acc = aggregate_usage(f)
        assert _four(acc) == (0, 0, 0, 0)
        assert acc.responses == 0
        assert acc.by_model == {}
        assert acc.longtail == []


def test_unreadable_path_returns_empty_account(tmp_path):
    """路径不存在 → 空账目,不抛异常(防御式,与 transcripts.py 同风格)。"""
    acc = aggregate_usage(tmp_path / "nope" / "missing.jsonl")
    assert _four(acc) == (0, 0, 0, 0) and acc.longtail == []


def test_file_object_input_matches_path(tmp_path):
    """打开的文件对象与路径两种输入形态结果一致。"""
    p = _write(tmp_path, [_assistant("msg_1"), _assistant("msg_2", usage=_u(1, 2, 3, 4))])
    via_path = aggregate_usage(p)
    via_handle = aggregate_usage(open(p, "r", encoding="utf-8"))
    assert via_path == via_handle


def test_mixed_file_end_to_end(tmp_path):
    """大杂烩:真实形态(占位前置行+终行、用户行、无 id 行、截断尾行)一次算平。"""
    user = json.dumps(_user_row(), ensure_ascii=False)
    rows = [
        user,
        json.dumps(_assistant("msg_a", stop=None, usage=_Z), ensure_ascii=False),
        json.dumps(_assistant("msg_a", stop="tool_use", content="tool_use"), ensure_ascii=False),
        json.dumps(_assistant(None, usage=_u(50, 0, 0, 0)), ensure_ascii=False),  # 无 id
        json.dumps(_assistant("msg_b", stop="end_turn", usage=_u(10, 10, 10, 10)), ensure_ascii=False),
        json.dumps(_assistant("msg_c", stop=None, usage=_Z), ensure_ascii=False),  # 响应中断
    ]
    truncated = json.dumps(_assistant("msg_d"), ensure_ascii=False)[:25]  # 半行
    f = tmp_path / "mix.jsonl"
    f.write_text("\n".join(rows + [truncated]) + "\n", encoding="utf-8")

    acc = aggregate_usage(f)
    assert _four(acc) == (110, 30, 310, 4010)  # msg_a + msg_b
    assert acc.responses == 2
    assert len(acc.longtail) == 3  # 无 id 行 + 全零占位组 + 半行
    assert any("message.id" in s for s in acc.longtail)
    assert any("msg_c" in s for s in acc.longtail)
    assert any("JSON" in s for s in acc.longtail)
