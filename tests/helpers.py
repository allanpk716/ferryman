"""集成测试共享件：Harness（最小 daemon）、合成会话、端口工具。"""

import json
import socket
import threading
import time
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

import ferryman.daemon as daemon_mod
from ferryman.accounts import Accounts
from ferryman.beat import QWatchStats
from ferryman.config import Config, ServerCfg, ThresholdCfg, WatchCfg
from ferryman.daemon import FerryWorker, Watcher
from ferryman.ledger import Ledger, now_s
from ferryman.server import FerryDaemon, ensure_token, make_server
from ferryman.store import Store

SUMMARIZE, BLOCK, MIN_CTX = 1.0, 2.0, 1000


def free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def write_session(projects: Path, sid: str, cwd: str, *, usage_input=2000) -> Path:
    import os
    d = projects / "C--proj"
    d.mkdir(parents=True, exist_ok=True)
    f = d / f"{sid}.jsonl"
    lines = [
        {"type": "user", "timestamp": now_iso(), "cwd": cwd, "sessionId": sid,
         "message": {"role": "user", "content": "做点活"}},
        {"type": "assistant", "timestamp": now_iso(),
         "message": {"role": "assistant",
                     "content": [{"type": "text", "text": "干完了"}],
                     "usage": {"input_tokens": usage_input, "cache_read_input_tokens": 100,
                                "cache_creation_input_tokens": 0, "output_tokens": 5}}},
        {"type": "ai-title", "aiTitle": "集成测试会话"},
    ]
    f.write_text("\n".join(json.dumps(x, ensure_ascii=False) for x in lines) + "\n",
                 encoding="utf-8")
    os.utime(f, None)
    return f


class Harness:
    """起一套最小 daemon（守望+工人+HTTP）；摆渡模型由 fake 替代（离线）。"""

    def __init__(self, tmp_path: Path, monkeypatch, fake_ferry=None):
        import queue
        self.tmp = tmp_path
        self.projects = tmp_path / "projects"
        self.port = free_port()
        cfg = Config()
        cfg.gate_cc = "enforce"
        cfg.thresholds = ThresholdCfg(summarize_s=SUMMARIZE, block_s=BLOCK,
                                      min_ctx_tokens=MIN_CTX)
        cfg.watch = WatchCfg(poll_interval_s=0.2,
                             cc_projects_dir=str(self.projects),
                             codex_sessions_dir=str(tmp_path / "no-codex"))
        cfg.server = ServerCfg(port=self.port, data_dir=str(tmp_path / "data"))
        cfg.ferry_provider = "fake"            # T39 去内置默认后，测试密闭：注入假 provider
        self.cfg = cfg
        self.token = ensure_token(Path(cfg.data_dir))
        self.ledger = Ledger()
        self.store = Store(Path(cfg.data_dir))
        self.accounts = Accounts(Path(cfg.data_dir))
        self.tasks: queue.Queue = queue.Queue(maxsize=1)
        self.enqueued_ok: list[str] = []
        self.started_at = now_s()

        def enqueue(st) -> bool:
            try:
                self.tasks.put_nowait({"transcript_path": st.transcript_path,
                                       "agent": st.agent, "session_id": st.session_id,
                                       "cwd": st.cwd})
                self.enqueued_ok.append(st.session_id)
                return True
            except queue.Full:
                return False

        self.qwatch_stats = QWatchStats()   # 票04：守望计数器（daemon/watcher 共享）
        self.daemon = FerryDaemon(cfg, self.ledger, self.store, enqueue,
                                  accounts=self.accounts,
                                  qwatch_stats=self.qwatch_stats)

        def default_fake(path, provider, agent="cc"):
            md = ("[Ferryman 交接 · 会话 集成测试会话]\n\n<<<INJECT>>>\n注入层：干完了 fb.py\n"
                  "<<</INJECT>>\n\n# 全文\n干完了：fb.py\n")
            meta = {"title": "集成测试会话", "mode": "L1", "wall_s": 0.01,
                    "covers_until_iso": now_iso(), "mat_tokens_est": 1, "chunks": 1,
                    "usage": {}, "call_walls": [0.01], "source": str(path)}
            return md, meta

        monkeypatch.setattr(daemon_mod, "ferry_session", fake_ferry or default_fake)
        from ferryman.ferry import Provider
        monkeypatch.setattr(daemon_mod, "load_providers",
                            lambda: {"fake": Provider(name="fake",
                                                      base_url="http://127.0.0.1:9/v1",
                                                      model="fake")})
        self.server = make_server(self.daemon, self.port, self.token)
        self.watcher = Watcher(cfg, self.ledger, self.store, enqueue, self.started_at,
                               self.accounts, ferry_daemon=self.daemon,
                               qwatch_stats=self.qwatch_stats)
        self.worker = FerryWorker(cfg, self.store, self.tasks,
                                  accounts=self.accounts)
        threading.Thread(target=self.server.serve_forever,
                         kwargs={"poll_interval": 0.2}, daemon=True).start()
        self.watcher.start()
        self.worker.start()

    def stop(self):
        self.server.shutdown()
        self.watcher.stop()
        self.worker.stop()

    def gate(self, body: dict) -> dict:
        req = urllib.request.Request(
            f"http://127.0.0.1:{self.port}/gate",
            data=json.dumps(body, ensure_ascii=False).encode("utf-8"),
            headers={"Authorization": f"Bearer {self.token}",
                     "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=5) as r:
            return json.loads(r.read().decode("utf-8"))

    def sub(self, body: dict) -> dict:
        req = urllib.request.Request(
            f"http://127.0.0.1:{self.port}/subagent",
            data=json.dumps(body, ensure_ascii=False).encode("utf-8"),
            headers={"Authorization": f"Bearer {self.token}",
                     "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=5) as r:
            return json.loads(r.read().decode("utf-8"))

    def post(self, path: str, body: dict | None = None) -> dict:
        """通用 POST（票04 /qwatch_stop 等无副作用控制端点用）。"""
        req = urllib.request.Request(
            f"http://127.0.0.1:{self.port}{path}",
            data=json.dumps(body or {}, ensure_ascii=False).encode("utf-8"),
            headers={"Authorization": f"Bearer {self.token}",
                     "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=5) as r:
            return json.loads(r.read().decode("utf-8"))

    def get(self, path: str, token: str | None = None):
        req = urllib.request.Request(
            f"http://127.0.0.1:{self.port}{path}",
            headers={"Authorization": f"Bearer {token or self.token}"})
        with urllib.request.urlopen(req, timeout=5) as r:
            return json.loads(r.read().decode("utf-8"))

    def wait_for(self, cond, timeout=15.0, interval=0.2):
        deadline = time.time() + timeout
        while time.time() < deadline:
            if cond():
                return True
            time.sleep(interval)
        return False
