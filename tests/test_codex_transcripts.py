"""T03(codex 部分) · codex_transcripts 防御解析。"""

import json
from pathlib import Path

from ferryman.codex_transcripts import token_count_turns


def _token_count_line(ts, inp, cached, write=0):
    return json.dumps({
        "timestamp": ts, "type": "event_msg",
        "payload": {"type": "token_count",
                    "info": {"last_token_usage": {
                        "input_tokens": inp, "cached_input_tokens": cached,
                        "cache_write_input_tokens": write, "output_tokens": 10}}}})


def test_token_count_turns_parses_and_skips(tmp_path):
    f = tmp_path / "rollout-x.jsonl"
    f.write_text("\n".join([
        _token_count_line("2026-09-16T06:00:01Z", 1000, 800),
        "{broken",
        json.dumps({"timestamp": "2026-09-16T06:00:02Z", "type": "event_msg",
                    "payload": {"type": "something_else"}}),           # 非 token_count
        json.dumps({"timestamp": "2026-09-16T06:00:03Z", "type": "response_item"}),  # 非 event_msg
        _token_count_line("2026-09-16T06:00:04Z", 0, 0),               # input=0 跳过
        _token_count_line("2026-09-16T06:00:00Z", 3000, 2500),         # 时间更早，排序后应在前
    ]) + "\n", encoding="utf-8")
    turns = token_count_turns(f)
    assert [(t.input_tokens, t.cached) for t in turns] == [(3000, 2500), (1000, 800)]
    assert turns[0].ts <= turns[1].ts                     # UTC 排序


def test_missing_file_returns_empty(tmp_path):
    assert token_count_turns(tmp_path / "nope.jsonl") == []
