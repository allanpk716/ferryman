"""T51 票01 · 提问潮检测器（qwatch.detect）。

紧档判据（spec 决策 1）：剥代码块后——标记行（❓ / **Qn**）计入；问号行计入；
编号/列表行仅当行内含问号或疑问词才计入（round-0 实验 153 条误报的形态缩影
＝纯步骤/清单行，一律不计）。unit_count ≥ min_questions（默认 5）为提问潮。
隐私铁律：Verdict 只有计数与布尔，任何字段不携带消息内容。
"""

from __future__ import annotations

import json
from pathlib import Path

from ferryman.qwatch import Breakdown, detect

_TS = "2026-09-18T12:00:00.000Z"

# 正例语料：❓ **Qn** 家族格式、8 个问题单元（spec 决策 1 的靶消息形态）
_SURGE_8 = "\n".join([
    "好，先把口径一次对齐，逐条答我：",
    "❓ **Q1** - **TTL 口径**：保温间隔按实测 600s 还是配置值？",
    "❓ **Q2** - **预算封顶**：单会话每日上限多少，超了先停谁？",
    "❓ **Q3** - **熔断语义**：连续 MISS 两次降级后，要不要人工拨回？",
    "❓ **Q4** - **窗口互斥**：与停车窗同时命中，先开者赢还是检测器优先？",
    "❓ **Q5** - **摆渡死线**：强制入队提前量 480s 够不够，要不要留骨架？",
    "❓ **Q6** - **费用科目**：心跳花费记独立科目还是并入摆渡账？",
    "❓ **Q7** - **observe 时长**：跑满一周还是攒够 10 次命中就复核？",
    "❓ **Q8** - **开关归属**：一键停改 mode 配置，还是另设 kill 开关？",
])


def _line(d: dict) -> str:
    return json.dumps(d, ensure_ascii=False)


def _assistant(text: str | None = None,
               tools: tuple[tuple[str, str], ...] = (),
               mid: str = "msg_1") -> dict:
    blocks = []
    if text is not None:
        blocks.append({"type": "text", "text": text})
    blocks += [{"type": "tool_use", "id": tid, "name": name, "input": {}}
               for tid, name in tools]
    return {"type": "assistant", "timestamp": _TS,
            "message": {"id": mid, "role": "assistant", "content": blocks,
                        "usage": {"input_tokens": 10, "cache_read_input_tokens": 100,
                                   "cache_creation_input_tokens": 0, "output_tokens": 5}}}


def _result(*tool_ids: str) -> dict:
    return {"type": "user", "timestamp": _TS,
            "message": {"role": "user", "content": [
                {"type": "tool_result", "tool_use_id": tid, "content": "ok"}
                for tid in tool_ids]}}


def _write(tmp_path: Path, lines: list[str], name: str = "s.jsonl") -> Path:
    f = tmp_path / name
    f.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return f


def test_t51_surge_positive_fixture(tmp_path):
    """已知正例：❓ **Qn** 家族 8 单元 → is_surge=True，判据构成如实。"""
    f = _write(tmp_path, [_line(_assistant(_SURGE_8))])
    v = detect(f)
    assert v.is_surge is True
    assert v.unit_count == 8
    assert v.breakdown == Breakdown(marker_lines=8, qmark_lines=0,
                                    qualified_numbered_lines=0)
    assert v.askuserquestion_dangling is True   # 无 tool_use：空集 ⊆ {AQ}（真空真口径）


def test_t51_negative_status_report_not_surge(tmp_path):
    """反例：状态汇报/步骤清单/票台账（编号行无疑问词无疑问号）＋散落 2 问 → 不触发。"""
    report = "\n".join([
        "任务完成，汇报如下：",
        "1. 读取配置文件，确认阈值生效。",
        "2. 重跑测试套件，204 用例全绿。",
        "3. 更新台账，写入费用科目。",
        "4. 生成报告，输出对比表格。",
        "- 检测器票：绿。",
        "- 调度器票：绿。",
        "下一步要不要我继续优化？",
        "另外，文档是否同步？",
        "缓存策略沿用既有口径，无变更。",
    ])
    f = _write(tmp_path, [_line(_assistant(report))])
    v = detect(f)
    assert v.is_surge is False
    assert v.unit_count == 2
    assert v.breakdown.qualified_numbered_lines == 0
    assert v.breakdown.qmark_lines == 2
    assert v.breakdown.marker_lines == 0


def test_t51_code_block_stripped(tmp_path):
    """代码块内的问号/编号不计数（先剥块再判定）。"""
    msg = "\n".join([
        "两个口径问题：",
        "❓ **Q1** - **费率**：按哪个档位算？",
        "❓ **Q2** - **窗口**：多长合适？",
        "示例查询：",
        "```sql",
        "SELECT * FROM t WHERE a = ? AND b = ?;",
        "-- 1. 这行是问句吗？算不算编号？",
        "```",
        "以上。",
    ])
    f = _write(tmp_path, [_line(_assistant(msg))])
    v = detect(f)
    assert v.unit_count == 2
    assert v.breakdown == Breakdown(marker_lines=2, qmark_lines=0,
                                    qualified_numbered_lines=0)
    assert v.is_surge is False


def test_t51_single_question_not_surge(tmp_path):
    """一次一问（1 单元）不触发；min_questions 是参数。"""
    f = _write(tmp_path, [_line(_assistant("这个配置要不要保留？"))])
    v = detect(f)
    assert v.unit_count == 1
    assert v.is_surge is False
    assert detect(f, min_questions=1).is_surge is True


def test_t51_numbered_line_needs_question_signal(tmp_path):
    """紧档核心：编号/列表行含问号或疑问词才计入，纯步骤行不计。"""
    msg = "\n".join([
        "1. 你想要什么口径？",
        "2、哪个先做？",
        "3) Tell me HOW it works",          # 疑问词（英文，大小写不敏感）无问号也计
        "4. 这条是纯步骤，直接执行。",
        "5. 这条也是纯步骤。",
    ])
    f = _write(tmp_path, [_line(_assistant(msg))])
    v = detect(f)
    assert v.unit_count == 3
    assert v.breakdown == Breakdown(marker_lines=0, qmark_lines=0,
                                    qualified_numbered_lines=3)
    assert v.is_surge is False


def test_t51_askuserquestion_dangling_true_surge_unaffected(tmp_path):
    """悬空 tool_use 仅 AskUserQuestion：aq=True 且不影响 is_surge（靶场景豁免）。"""
    f = _write(tmp_path, [_line(_assistant(_SURGE_8,
                                           tools=(("t1", "AskUserQuestion"),)))])
    v = detect(f)
    assert v.askuserquestion_dangling is True
    assert v.is_surge is True
    assert v.unit_count == 8


def test_t51_dangling_other_tool_reported_false(tmp_path):
    """悬空含其他工具 → 如实 False；{AskUserQuestion, 其他} 混合也 False。"""
    f1 = _write(tmp_path, [_line(_assistant(_SURGE_8, tools=(("t1", "Bash"),)))])
    v1 = detect(f1)
    assert v1.askuserquestion_dangling is False
    assert v1.is_surge is True                  # 悬空与否不参与 is_surge
    f2 = _write(tmp_path, [_line(_assistant(_SURGE_8, tools=(
        ("t1", "AskUserQuestion"), ("t2", "Task"))))], name="s2.jsonl")
    assert detect(f2).askuserquestion_dangling is False


def test_t51_dangling_empty_set_is_vacuous_true(tmp_path):
    """无悬空（tool_use 已全部回包 / 纯文本无工具）→ 空集 ⊆ {AQ} 记 True。

    字段真实语义＝「悬空集不含 AskUserQuestion 之外的工具」，谓词②可直接用；
    True 不代表真有 AskUserQuestion 悬空（spec 决策 2 的集合表述）。
    """
    f1 = _write(tmp_path, [_line(_assistant("就一个问题。", tools=(("t1", "AskUserQuestion"),))),
                           _line(_result("t1"))])
    assert detect(f1).askuserquestion_dangling is True
    f2 = _write(tmp_path, [_line(_assistant("纯文本，无工具调用。"))], name="s2.jsonl")
    assert detect(f2).askuserquestion_dangling is True


def test_t51_uses_last_text_assistant_message(tmp_path):
    """只判末条带文本的 assistant 消息：前一条提问潮被后一条普通答复覆盖。"""
    f = _write(tmp_path, [_line(_assistant(_SURGE_8, mid="msg_1")),
                          _line(_assistant("干完了，无问题。", mid="msg_2"))])
    v = detect(f)
    assert v.is_surge is False
    assert v.unit_count == 0


def test_t51_streaming_chunks_of_same_message_merged(tmp_path):
    """同 message id 的流式分片（多行各带部分文本块）合并后再判定。"""
    half1 = "\n".join(f"❓ **Q{i}** - **标题{i}**：要哪个？" for i in range(1, 5))
    half2 = "\n".join(f"❓ **Q{i}** - **标题{i}**：要哪个？" for i in range(5, 9))
    f = _write(tmp_path, [_line(_assistant(half1, mid="msg_1")),
                          _line(_assistant(half2, mid="msg_1"))])
    v = detect(f)
    assert v.is_surge is True
    assert v.unit_count == 8


def test_t51_bad_lines_and_empty_file_safe(tmp_path):
    """坏行/空文件/缺文件安全跳过：不抛错，零计数兜底（沿 transcripts 风格）。"""
    f = _write(tmp_path, ["这不是json", "{broken", "", _line(_assistant(_SURGE_8))])
    v = detect(f)
    assert v.is_surge is True
    assert v.unit_count == 8

    empty = tmp_path / "empty.jsonl"
    empty.write_text("", encoding="utf-8")
    ve = detect(empty)
    assert ve.is_surge is False
    assert ve.unit_count == 0
    assert ve.breakdown == Breakdown(marker_lines=0, qmark_lines=0,
                                     qualified_numbered_lines=0)
    assert ve.askuserquestion_dangling is True   # 空集口径，同上

    missing = detect(tmp_path / "nope.jsonl")
    assert missing.is_surge is False
    assert missing.unit_count == 0
