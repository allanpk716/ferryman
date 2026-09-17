"""P0 修复 · Codex 摆渡提取：rollout → (骨架, 正文)，ferry/骨架两路分派。

2026-09-17 11:17 事故：daemon 门槛用 token_count_turns（codex 感知）放行了
峰值 191k 的会话，但 ferry_session 只会解析 CC 格式 → 0 正文 0 轮 0 峰值，
LLM 无米下锅产出"无实际开发活动"的垃圾交接。本组测试钉住 codex 提取语义
与两处分派（ferry_session 总结路 + daemon 骨架降级路）。
"""

import json
import os
import time
from pathlib import Path

import pytest

from ferryman.codex_transcripts import extract_codex


def _line(ts, typ, payload):
    return json.dumps({"timestamp": ts, "type": typ, "payload": payload},
                      ensure_ascii=False)


def _msg(ts, role, text):
    block_type = "output_text" if role == "assistant" else "input_text"
    return _line(ts, "response_item", {"type": "message", "role": role,
                                       "content": [{"type": block_type, "text": text}]})


def _tok(ts, inp, cached=0):
    return _line(ts, "event_msg", {"type": "token_count",
                                   "info": {"last_token_usage": {
                                       "input_tokens": inp,
                                       "cached_input_tokens": cached,
                                       "cache_write_input_tokens": 0}}})


def _call(ts, name, **args):
    return _line(ts, "response_item", {"type": "function_call", "name": name,
                                       "arguments": json.dumps(args, ensure_ascii=False)})


def _write_rollout(tmp_path, lines, *, old_mtime=True) -> Path:
    f = tmp_path / "rollout-2026-09-17T10-51-25-01a0ad46-ac17-7d73-a203-072c58812fd0.jsonl"
    f.write_text("\n".join(lines) + "\n", encoding="utf-8")
    if old_mtime:
        old = time.time() - 60
        os.utime(f, (old, old))
    return f


def _sample_lines():
    return [
        _line("2026-09-17T02:52:19.862Z", "session_meta",
              {"session_id": "01a0ad46", "cwd": "C:\\proj", "originator": "codex-tui"}),
        _msg("2026-09-17T02:52:20.000Z", "developer", "<skills_instructions>系统提示</skills>"),
        _msg("2026-09-17T02:52:21.000Z", "user", "修一下登录页的 bug"),
        _msg("2026-09-17T02:52:30.000Z", "assistant", "好的，我先看下 auth.py 的登录分支"),
        _line("2026-09-17T02:52:31.000Z", "response_item",
              {"type": "reasoning", "summary": [{"type": "summary_text", "text": "思考中"}]}),
        _call("2026-09-17T02:52:32.000Z", "exec_command", cmd="rg def login C:\\proj"),
        _call("2026-09-17T02:52:40.000Z", "apply_patch",
              input="*** Begin Patch\n*** Update File: C:\\proj\\auth.py\n@@\n-old\n+new\n*** End Patch"),
        _line("2026-09-17T02:52:41.000Z", "response_item",
              {"type": "function_call_output", "call_id": "x",
               "output": "Chunk ID: f07351\nOutput: 1385 tokens"}),
        "{broken",
        _tok("2026-09-17T02:52:45.000Z", 20000, 18000),
        _tok("2026-09-17T03:16:44.435Z", 25000, 22000),
    ]


# ---------- extract_codex：骨架 + 正文 ----------

def test_extract_codex_full_semantics(tmp_path):
    f = _write_rollout(tmp_path, _sample_lines())
    facts, items = extract_codex(f)

    assert facts.cwd == "C:\\proj"
    assert facts.title == "修一下登录页的 bug"          # 首条用户消息作标题（rollout 无 ai-title）
    assert facts.first_ts == "2026-09-17T02:52:19.862Z"
    assert facts.last_ts == "2026-09-17T03:16:44.435Z"
    assert facts.n_turns == 2 and facts.peak_ctx == 25000   # token_count 口径
    assert facts.commands == ["rg def login C:\\proj"]
    assert facts.files and facts.files[0][0] == "C:\\proj\\auth.py"

    # 正文只要 user/assistant 文本：developer/reasoning/工具输出全丢
    assert [it.role for it in items] == ["user", "assistant"]
    assert "修一下登录页" in items[0].text
    assert "auth.py" in items[1].text


def test_extract_codex_defensive(tmp_path):
    # 文件不存在 / 空文件 / 全坏行 → 空骨架不抛错
    facts, items = extract_codex(tmp_path / "nope.jsonl")
    assert facts.n_turns == 0 and facts.peak_ctx == 0 and facts.cwd is None
    assert items == []

    f = _write_rollout(tmp_path, ["{broken", "", "not json"])
    facts, items = extract_codex(f)
    assert items == [] and facts.title is None


def test_extract_codex_title_cap_and_fallback(tmp_path):
    long_msg = "这是一条特别长的用户消息" * 30            # 330 字 → 截到 60
    lines = [_msg("2026-09-17T02:52:21.000Z", "user", long_msg)]
    f = _write_rollout(tmp_path, lines)
    facts, _ = extract_codex(f)
    assert facts.title and len(facts.title) <= 60

    # 无用户消息 → 无标题（不用 assistant 兜底）
    f2 = _write_rollout(tmp_path, [_msg("2026-09-17T02:52:21.000Z", "assistant", "回复")])
    facts2, _ = extract_codex(f2)
    assert facts2.title is None


def test_extract_codex_skips_injected_user_messages(tmp_path):
    """codex 把环境注入伪装成 user 消息（2026-09-17 真机实测）：
    AGENTS.md 指令、<turn_aborted> 中断标记——不进正文、不作标题（防注入面）。
    真人消息不受影响（哪怕以 < 开头粘贴 HTML，未命中已知标记即保留）。"""
    lines = [
        _msg("2026-09-17T02:52:19.895Z", "user", "# AGENTS.md instructions\n\n<INSTRUCTIONS>"),
        _msg("2026-09-17T02:52:27.126Z", "user", "我去查一下流人第六季什么时候上线"),
        _msg("2026-09-17T03:21:14.546Z", "user",
             "<turn_aborted>\nThe user interrupted the previous turn on purpose."),
        _msg("2026-09-17T03:21:20.000Z", "user", "<div>粘贴的 HTML 片段</div>"),
    ]
    f = _write_rollout(tmp_path, lines)
    facts, items = extract_codex(f)
    assert [it.text for it in items] == ["我去查一下流人第六季什么时候上线",
                                         "<div>粘贴的 HTML 片段</div>"]
    assert facts.title == "我去查一下流人第六季什么时候上线"


# ---------- ferry_session 分派 ----------

def test_ferry_session_dispatches_codex(tmp_path, monkeypatch):
    """agent='codex' → 材料用 codex 提取（此前 0 正文导致垃圾交接）。"""
    import ferryman.ferry as ferry_mod
    from ferryman.ferry import Provider

    captured = {}

    def fake_chat(provider, system, user, **kw):
        captured["material"] = user
        return ("<<<INJECT>>>\n注入层\n<<</INJECT>>>\n\n# 全文", {})

    monkeypatch.setattr(ferry_mod, "chat", fake_chat)
    f = _write_rollout(tmp_path, _sample_lines())

    provider = Provider(name="fake", base_url="http://127.0.0.1:9/v1", model="fake")
    md, meta = ferry_mod.ferry_session(f, provider, agent="codex")

    assert "修一下登录页" in captured["material"]        # 用户正文进了材料
    assert "auth.py" in captured["material"]             # 骨架文件清单进了材料
    assert "0 轮" not in captured["material"]
    assert meta["title"] == "修一下登录页的 bug"
    assert "修一下登录页" in md


# ---------- daemon 骨架降级路（codex 分派） ----------

def test_skeleton_fallback_uses_codex_extraction(tmp_path, monkeypatch):
    """codex 会话摆渡失败降级骨架时，骨架也要有 codex 骨架（非 CC 空骨架）。"""
    def exploding(path, provider, agent="cc"):
        raise RuntimeError("provider down")

    from helpers import Harness
    harness = Harness(tmp_path, monkeypatch, fake_ferry=exploding)
    try:
        codex_dir = tmp_path / "no-codex"
        d = codex_dir / "2026" / "09" / "17"
        d.mkdir(parents=True, exist_ok=True)
        f = d / "rollout-2026-09-17T10-51-25-01a0ad46-ac17-7d73-a203-072c58812fd0.jsonl"
        f.write_text("\n".join(_sample_lines()) + "\n", encoding="utf-8")
        os.utime(f, None)          # mtime=now：≥ daemon 启动才 observed_active（lookback=0）

        assert harness.wait_for(lambda: any(
            e["agent"] == "codex" and e["status"] == "skeleton"
            for e in harness.store._index["handoffs"])), "codex 骨架降级未发生"
        md = Path([e for e in harness.store._index["handoffs"]
                   if e["agent"] == "codex"][0]["path"]).read_text(encoding="utf-8")
        assert "auth.py" in md                          # 文件清单在
        assert "rg def login" in md                     # 命令清单在
        assert "25000" in md                            # 峰值上下文在（非 0）
    finally:
        harness.stop()
