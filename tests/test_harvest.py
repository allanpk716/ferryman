"""用量采集：纯解析器测试（设计 §3.7）。"""

import json

from ferryman.harvest import parse_usage_chunk


def _user_line(ts="2026-09-18T01:00:00Z", cwd="C:/proj"):
    return json.dumps({"type": "user", "timestamp": ts, "cwd": cwd,
                       "sessionId": "s1", "message": {"role": "user",
                                                      "content": "干活"}})


def _asst_line(ts="2026-09-18T01:00:05Z", model="glm-5.3", inp=100, cr=9000, cc=0, out=50):
    return json.dumps({"type": "assistant", "timestamp": ts,
                       "message": {"role": "assistant", "model": model,
                                   "content": [{"type": "text", "text": "好"}],
                                   "usage": {"input_tokens": inp,
                                             "cache_read_input_tokens": cr,
                                             "cache_creation_input_tokens": cc,
                                             "output_tokens": out}}})


def test_parse_extracts_assistant_usage_and_cwd():
    chunk = _user_line() + "\n" + _asst_line() + "\n"
    rows, title, cwd = parse_usage_chunk(chunk)
    assert cwd == "C:/proj"
    assert title == ""
    assert len(rows) == 1
    r = rows[0]
    assert r["input_tokens"] == 100 and r["cache_read_tokens"] == 9000
    assert r["cache_creation_tokens"] == 0 and r["output_tokens"] == 50
    assert r["model"] == "glm-5.3"
    from datetime import datetime
    expected = datetime.fromisoformat("2026-09-18T01:00:05+00:00").timestamp()
    assert r["ts"] == expected
    assert "content" not in r and "message" not in r  # 隐私：无消息内容


def test_parse_tracks_ai_title():
    title_line = json.dumps({"type": "ai-title", "aiTitle": "修登录bug"})
    chunk = title_line + "\n" + _asst_line() + "\n"
    rows, title, cwd = parse_usage_chunk(chunk)
    assert title == "修登录bug"
    assert len(rows) == 1


def test_parse_skips_malformed_and_bare_lines():
    chunk = ("{broken json\n" + _asst_line() + "\n"
             + json.dumps({"type": "user", "message": {"role": "user", "content": "x"}}) + "\n"
             + json.dumps({"type": "assistant", "message": {"role": "assistant",
                                                            "content": "无用量"}}) + "\n")
    rows, _t, _c = parse_usage_chunk(chunk)
    assert len(rows) == 1            # 只有带 usage 的 assistant 出行


def test_parse_skips_nondict_and_null_token_lines():
    """病态行防御：数组行/message 为字符串/token 为 null——跳过不抛且不伤及好行（审查 Nit）。"""
    chunk = ("[1, 2]\n"
             + json.dumps({"type": "assistant", "message": "字符串消息"}) + "\n"
             + json.dumps({"type": "assistant", "message": {"role": "assistant",
                                                            "usage": {"input_tokens": None}}}) + "\n"
             + _asst_line() + "\n")
    rows, _t, _c = parse_usage_chunk(chunk)
    assert len(rows) == 1 and rows[0]["output_tokens"] == 50
