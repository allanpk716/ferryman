"""T16/T17 · 钩子脚本真跑（PowerShell 子进程）：输出契约、fail-open、还原过滤。"""

import json
import os
import socket
import subprocess
import time
from pathlib import Path

HOOKS = Path(__file__).resolve().parent.parent / "hooks"


def free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _run_ps(script: str, stdin_obj: dict | None, env_extra: dict | None,
            timeout: float = 30.0) -> subprocess.CompletedProcess:
    env = {**os.environ, **(env_extra or {})}
    data = (json.dumps(stdin_obj, ensure_ascii=False) if stdin_obj is not None else b"")
    data = data.encode("utf-8") if isinstance(data, str) else data
    return subprocess.run(
        ["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass",
         "-File", str(HOOKS / script)],
        input=data, capture_output=True, timeout=timeout, env=env)


def _gate_env(h) -> dict:
    return {"FERRYMAN_PORT": str(h.port),
            "FERRYMAN_TOKEN_FILE": str(h.tmp / "data" / "daemon.token")}


# ---------- T16 gate 钩子 ----------

def test_t16_gate_block_json_contract(h):
    from helpers import write_session as _write_session
    proj = str(h.tmp / "proj")
    sid = "hook-0001"
    f = _write_session(h.projects, sid, proj)
    assert h.wait_for(lambda: any(e["session_id"] == sid
                                  for e in h.store._index["handoffs"]))
    body = {"session_id": sid, "transcript_path": str(f), "cwd": proj,
            "prompt": "被拦的原话", "source": "user"}
    deadline = time.time() + 15
    out = ""
    while time.time() < deadline:
        r = _run_ps("ferryman-gate.ps1", body, _gate_env(h))
        out = r.stdout.decode("utf-8", errors="replace").strip()
        if out:
            break
        time.sleep(1)                                    # 未到拦截阈值先放行(空输出)
    assert r.returncode == 0
    payload = json.loads(out)                            # stdout 必须是纯 JSON
    assert payload["decision"] == "block"
    assert payload["suppressOriginalPrompt"] is True
    assert "交接" in payload["reason"]
    assert "hookSpecificOutput" not in payload           # block 不走注入通道


def test_t16_gate_failopen_on_401(h):
    from helpers import write_session as _write_session
    proj = str(h.tmp / "proj")
    sid = "hook-0002"
    f = _write_session(h.projects, sid, proj)
    bad_token = h.tmp / "bad.token"
    bad_token.write_text("wrong-token", encoding="utf-8")
    body = {"session_id": sid, "transcript_path": str(f), "cwd": proj,
            "prompt": "x", "source": "user"}
    r = _run_ps("ferryman-gate.ps1", body,
                {**_gate_env(h), "FERRYMAN_TOKEN_FILE": str(bad_token)})
    assert r.returncode == 0 and not r.stdout.strip()    # 401 → 放行且零输出


def test_t16_gate_failopen_daemon_down():
    body = {"session_id": "s", "transcript_path": "C:/x.jsonl",
            "cwd": "C:/x", "prompt": "hi"}
    r = _run_ps("ferryman-gate.ps1", body,
                {"FERRYMAN_PORT": str(free_port()),
                 "FERRYMAN_TOKEN_FILE": "C:/nonexistent.token"})
    assert r.returncode == 0 and not r.stdout.strip()    # 连接拒绝/文件缺失 → 放行


def test_t16_gate_env_disable_short_circuits():
    t0 = time.time()
    r = _run_ps("ferryman-gate.ps1",
                {"session_id": "s", "prompt": "x"},
                {"FERRYMAN_DISABLE": "1", "FERRYMAN_PORT": str(free_port()),
                 "FERRYMAN_TOKEN_FILE": "C:/nonexistent.token"})
    assert r.returncode == 0 and not r.stdout.strip()
    assert time.time() - t0 < 5                          # 首行短路，不碰网络


# ---------- T17 restore 钩子 ----------

def test_t17_restore_skips_resume(h):
    from helpers import write_session as _write_session
    proj = str(h.tmp / "proj")
    _write_session(h.projects, "hook-0003", proj)
    r = _run_ps("ferryman-restore.ps1",
                {"session_id": "s", "cwd": proj, "source": "resume"},
                _gate_env(h))
    assert r.returncode == 0 and not r.stdout.strip()    # resume/compact 一律不注入


def test_t17_restore_injects_on_clear(h):
    from helpers import write_session as _write_session
    proj = str(h.tmp / "proj")
    sid = "hook-0004"
    _write_session(h.projects, sid, proj)
    assert h.wait_for(lambda: any(e["session_id"] == sid
                                  for e in h.store._index["handoffs"]))
    r = _run_ps("ferryman-restore.ps1",
                {"session_id": "fresh-session", "cwd": proj, "source": "clear"},
                _gate_env(h))
    assert r.returncode == 0
    out = r.stdout.decode("utf-8", errors="replace")
    assert "Ferryman 交接" in out and "不可信" in out    # 注入层 + 消费侧声明


def test_t17_restore_failopen_daemon_down():
    r = _run_ps("ferryman-restore.ps1",
                {"session_id": "s", "cwd": "C:/x", "source": "clear"},
                {"FERRYMAN_PORT": str(free_port()),
                 "FERRYMAN_TOKEN_FILE": "C:/nonexistent.token"})
    assert r.returncode == 0 and not r.stdout.strip()


# ---------- T32 subagent 钩子 ----------

def test_t32_subagent_hook_roundtrip(h):
    r = _run_ps("ferryman-subagent.ps1",
                {"session_id": "hook-sub1", "agent_id": "a123",
                 "agent_type": "general-purpose", "hook_event_name": "SubagentStart"},
                _gate_env(h))
    assert r.returncode == 0 and not r.stdout.strip()     # fire-and-forget：无输出
    assert h.daemon.ledger.subagent_active("cc", "hook-sub1") is True
    r = _run_ps("ferryman-subagent.ps1",
                {"session_id": "hook-sub1", "agent_id": "a123",
                 "hook_event_name": "SubagentStop"},
                _gate_env(h))
    assert r.returncode == 0
    assert h.daemon.ledger.subagent_active("cc", "hook-sub1") is False


def test_t32_subagent_hook_failopen_daemon_down():
    r = _run_ps("ferryman-subagent.ps1",
                {"session_id": "s", "hook_event_name": "SubagentStart"},
                {"FERRYMAN_PORT": str(free_port()),
                 "FERRYMAN_TOKEN_FILE": "C:/nonexistent.token"})
    assert r.returncode == 0 and not r.stdout.strip()     # daemon 死 → 静默放行


def test_t32_subagent_hook_env_disable_short_circuits():
    t0 = time.time()
    r = _run_ps("ferryman-subagent.ps1",
                {"session_id": "s", "hook_event_name": "SubagentStart"},
                {"FERRYMAN_DISABLE": "1", "FERRYMAN_PORT": str(free_port()),
                 "FERRYMAN_TOKEN_FILE": "C:/nonexistent.token"})
    assert r.returncode == 0 and not r.stdout.strip()
    assert time.time() - t0 < 5                          # 首行短路，不碰网络


# ---------- T23 Codex 钩子（准备阶段：脚本契约已真跑验证；TUI /hooks 信任待用户） ----------

def _write_rollout(codex_dir, uuid: str, cwd: str = "C:/proj"):
    """合成 rollout：<dir>/年/月/日/rollout-<ts>-<uuid>.jsonl（session_meta + token_count 轮次）。"""
    import os
    d = codex_dir / "2026" / "09" / "16"
    d.mkdir(parents=True, exist_ok=True)
    f = d / f"rollout-2026-09-16T12-00-00-{uuid}.jsonl"
    lines = [
        {"type": "session_meta", "timestamp": "2026-09-16T12:00:00.000Z",
         "payload": {"session_id": uuid, "cwd": cwd, "cli_version": "0.153.0"}},
        {"type": "event_msg", "timestamp": "2026-09-16T12:00:01.000Z",
         "payload": {"type": "token_count",
                     "info": {"last_token_usage": {"input_tokens": 2000,
                                                    "cached_input_tokens": 100,
                                                    "cached_omitted_tokens": 0,
                                                    "output_tokens": 5}}}},
    ]
    f.write_text("\n".join(json.dumps(x, ensure_ascii=False) for x in lines) + "\n",
                 encoding="utf-8")
    os.utime(f, None)
    return f


def test_t23_codex_gate_hook_blocks_idle(h):
    h.cfg.gate_codex = "enforce"
    proj = str(h.tmp / "proj")
    f = _write_rollout(h.tmp / "no-codex", "codexgat01", cwd=proj)
    # 防御式字段：rollout_path / transcript_path 二选一（真实 schema 晨间信任后校准）
    body = {"session_id": "codexgat01", "rollout_path": str(f),
            "cwd": proj, "prompt": "hi"}
    deadline = time.time() + 15
    out = ""
    while time.time() < deadline:
        r = _run_ps("ferryman-gate-codex.ps1", body, _gate_env(h))
        out = r.stdout.decode("utf-8", errors="replace").strip()
        if out:
            break
        time.sleep(1)                                    # 未达拦截阈值先放行(空输出)
    assert r.returncode == 0
    payload = json.loads(out)                            # stdout 必须是合法 block JSON
    assert payload["decision"] == "block"
    assert payload.get("suppressOriginalPrompt") is True
    assert payload.get("reason")


def test_t23_codex_gate_hook_failopen_daemon_down():
    r = _run_ps("ferryman-gate-codex.ps1",
                {"session_id": "s", "rollout_path": "C:/x.jsonl", "prompt": "hi"},
                {"FERRYMAN_PORT": str(free_port()),
                 "FERRYMAN_TOKEN_FILE": "C:/nonexistent.token"})
    assert r.returncode == 0 and not r.stdout.strip()    # daemon 死 → 放行零输出


def test_t23_codex_restore_hook_filters_source(h):
    r = _run_ps("ferryman-restore-codex.ps1",
                {"session_id": "s", "cwd": "C:/x", "source": "resume"},
                _gate_env(h))
    assert r.returncode == 0 and not r.stdout.strip()    # resume/compact 不注入


def test_t23_codex_restore_hook_failopen_daemon_down():
    r = _run_ps("ferryman-restore-codex.ps1",
                {"session_id": "s", "cwd": "C:/x", "source": "clear"},
                {"FERRYMAN_PORT": str(free_port()),
                 "FERRYMAN_TOKEN_FILE": "C:/nonexistent.token"})
    assert r.returncode == 0 and not r.stdout.strip()
