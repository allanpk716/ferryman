"""T25 · 拦截通知：block 时 Pushover（手机）+ Windows Toast（桌面），文案带交接路径。

原则（DESIGN §6）：通知是尽力而为的旁路——任何故障只吞掉记日志，绝不影响 gate 决策；
异步发送（gate 返回不等通知）。默认 enabled=False（未配置不响，测试不炸 toast）。
"""

import time

import ferryman.notify as notify_mod
from ferryman.config import Config, NotifyCfg


# ---------- 通道：Pushover ----------

def test_pushover_posts_credentials_and_message(monkeypatch):
    calls = {}

    def fake_urlopen(req, timeout=None):
        calls["url"], calls["data"], calls["timeout"] = (
            req.full_url, req.data.decode("utf-8"), timeout)

        class R:
            status = 200

            def __enter__(self):
                return self

            def __exit__(self, *exc):
                return False
        return R()

    monkeypatch.setattr(notify_mod.urllib.request, "urlopen", fake_urlopen)
    assert notify_mod.send_pushover("标题", "消息体", token="tok", user="usr") is True
    assert calls["url"] == notify_mod.PUSHOVER_URL
    assert "tok" in calls["data"] and "usr" in calls["data"]
    import urllib.parse
    decoded = urllib.parse.unquote(calls["data"])            # 表单体是百分号编码
    assert "标题" in decoded and "消息体" in decoded
    assert calls["timeout"] is not None


def test_pushover_swallows_all_failures(monkeypatch):
    def boom(req, timeout=None):
        raise OSError("net down")
    monkeypatch.setattr(notify_mod.urllib.request, "urlopen", boom)
    assert notify_mod.send_pushover("t", "m", token="tok", user="usr") is False


# ---------- 通道：Windows Toast ----------

def test_toast_invokes_powershell_with_escaped_xml(monkeypatch):
    calls = {}

    def fake_run(args, timeout=None, **kw):
        calls["args"], calls["timeout"] = args, timeout

        class R:
            returncode = 0
        return R()

    monkeypatch.setattr(notify_mod.subprocess, "run", fake_run)
    assert notify_mod.send_toast("Ferryman 拦截", "交接: C:/x<&>y.md") is True
    assert calls["args"][0] == "powershell"
    script = calls["args"][-1]
    assert "Ferryman 拦截" in script
    assert "&amp;" in script and "&lt;" in script          # XML 转义（& <）
    assert calls["timeout"] is not None


def test_toast_swallows_failures(monkeypatch):
    import subprocess as sp

    def boom(*a, **k):
        raise sp.TimeoutExpired(cmd="powershell", timeout=1)
    monkeypatch.setattr(notify_mod.subprocess, "run", boom)
    assert notify_mod.send_toast("t", "m") is False


# ---------- 组装：notify_block ----------

def test_notify_block_includes_handoff_path_and_respects_flags(monkeypatch):
    sent = []
    monkeypatch.setattr(notify_mod, "send_pushover",
                        lambda title, message, token, user:
                        sent.append(("push", title, message)) or True)
    monkeypatch.setattr(notify_mod, "send_toast",
                        lambda title, message: sent.append(("toast", title, message)) or True)

    cfg = Config()
    cfg.notify = NotifyCfg(enabled=True, pushover_token="t", pushover_user="u")
    notify_mod.notify_block("C:/handoffs/h1.md", "cc", "s123", cfg)
    assert any(ch == "toast" and "h1.md" in msg for ch, _t, msg in sent)
    assert any(ch == "push" and "h1.md" in msg for ch, _t, msg in sent)

    cfg.notify.enabled = False                              # 总开关关 → 全静默
    sent.clear()
    notify_mod.notify_block("C:/h2.md", "cc", "s", cfg)
    assert sent == []


def test_notify_block_missing_pushover_credentials_skips_push(monkeypatch):
    monkeypatch.delenv("PUSHOVER_TOKEN", raising=False)     # 本机可能真配了凭据
    monkeypatch.delenv("PUSHOVER_USER", raising=False)
    sent = []
    monkeypatch.setattr(notify_mod, "send_pushover",
                        lambda *a, **k: sent.append("push") or True)
    monkeypatch.setattr(notify_mod, "send_toast", lambda *a: sent.append("toast") or True)
    cfg = Config()
    cfg.notify = NotifyCfg(enabled=True, pushover_token="", pushover_user="")
    notify_mod.notify_block("C:/h.md", "cc", "s", cfg)
    assert "toast" in sent and "push" not in sent           # 缺凭据 → 只走 toast


# ---------- 集成：gate block → 异步通知 ----------

def test_gate_block_fires_notification_async(h, monkeypatch):
    h.cfg.notify = NotifyCfg(enabled=True, pushover_token="t", pushover_user="u")
    fired = []
    monkeypatch.setattr(notify_mod, "notify_block",
                        lambda handoff_path, agent, session_id, cfg:
                        fired.append((handoff_path, agent, session_id)))

    from helpers import write_session
    proj = str(h.tmp / "proj")
    sid = "notify-0001"
    f = write_session(h.projects, sid, proj)
    body = {"agent": "cc", "session_id": sid, "transcript_path": str(f),
            "cwd": proj, "prompt": "继续"}
    assert h.wait_for(lambda: h.gate(body)["decision"] == "block")
    assert h.wait_for(lambda: fired)                        # 异步线程已触发
    handoff_path, agent, session_id = fired[0]
    assert "notify-0001" not in handoff_path                # 路径是交接文件
    assert agent == "cc" and session_id == sid
