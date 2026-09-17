"""T35 · 守护进程自举与唯一化。

- 自举：任意钩子（CC/Codex）POST 前探测 daemon，不在则拉起 ~/ferryman/start-daemon.cmd
  （install 时生成，含绝对 venv python 路径）——"agent 启动会话即点火"，无需开机自启。
- 唯一化：
  * Windows 陷阱回归：socketserver 默认 allow_reuse_address=1，Windows 的 SO_REUSEADDR
    语义允许两个进程绑同一端口（连接归属未定义）→ Windows 上必须禁用；
  * serve() 绑定失败 → 探测现有实例 /stats（带 token）：健康 → "已在运行"退出 0；
  * 绑定成功写 daemon.pid，退出清理。
"""

import socket
import threading
import time

import pytest

from ferryman.server import already_running, make_server


def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class _StopDaemon:
    """最小的 FerryDaemon 替身（满足 DaemonLike；只要 /stats 能回 200）。"""

    def gate(self, body: dict) -> dict:
        return {"decision": "allow"}

    def subagent(self, body: dict) -> dict:
        return {"ok": True}

    def restore(self, agent: str, cwd: str, session_id: str) -> dict:
        return {"context": None}

    def health(self) -> dict:
        return {"gate_calls_total": 0, "health_alert": False}


def _serve_bg(port: int, token: str):
    srv = make_server(_StopDaemon(), port, token)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    time.sleep(0.2)
    return srv


def test_windows_double_bind_is_rejected():
    """同端口第二个 make_server 必须抛 OSError（Windows 下 allow_reuse_address=1
    会静默双绑定——连接归属未定义，唯一化的地基）。"""
    port = _free_port()
    first = make_server(_StopDaemon(), port, token="t1")
    try:
        with pytest.raises(OSError):
            make_server(_StopDaemon(), port, token="t2")
    finally:
        first.server_close()


def test_already_running_true_for_healthy_instance():
    port = _free_port()
    srv = _serve_bg(port, token="tok-abc")
    try:
        assert already_running(port, "tok-abc") is True
    finally:
        srv.shutdown()
        srv.server_close()


def test_already_running_false_for_wrong_token_or_dead_port():
    port = _free_port()
    srv = _serve_bg(port, token="tok-abc")
    try:
        assert already_running(port, "WRONG") is False     # 401 → 不是我们的实例
    finally:
        srv.shutdown()
        srv.server_close()
    assert already_running(port, "tok-abc") is False       # 端口已无人监听


# ---------------- ensure_launcher（start-daemon.cmd 生成） ----------------

def test_ensure_launcher_venv_python(tmp_path):
    from ferryman.install import ensure_launcher
    fake_repo = tmp_path / "repo"
    py = fake_repo / ".venv" / "Scripts" / "python.exe"
    py.parent.mkdir(parents=True)
    py.write_text("", encoding="utf-8")
    data = tmp_path / "ferryman"
    launcher = ensure_launcher(data_dir=data, repo=fake_repo)
    assert launcher == data / "start-daemon.cmd"
    body = launcher.read_text(encoding="utf-8")
    assert str(py) in body                       # 绝对 venv python
    assert "-m ferryman serve" in body
    assert str(fake_repo) in body                # cd /d 到仓库
    assert "serve.out.log" in body               # 输出重定向（脱离启动器的关键）


def test_ensure_launcher_falls_back_to_uv(tmp_path):
    from ferryman.install import ensure_launcher
    launcher = ensure_launcher(data_dir=tmp_path, repo=tmp_path)   # 无 .venv
    body = launcher.read_text(encoding="utf-8")
    assert "uv" in body and "ferryman serve" in body


def test_install_cc_also_writes_launcher(tmp_path):
    from ferryman.install import install_cc
    p = tmp_path / "settings.json"
    p.write_text("{}", encoding="utf-8")
    install_cc(settings_path=p, ccswitch_db=tmp_path / "no.db",
               data_dir=tmp_path)
    assert (tmp_path / "start-daemon.cmd").exists()
