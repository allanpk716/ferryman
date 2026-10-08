#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""票03 · mock 服务:随机路径 + 到期自杀 + 本机验证

用途:把票01/02 产出的用量统计 mock 页(widget/ui/mock/stats.html + stats/
stats.data.js)用带随机段与截止时间的短命/限时服务暴露到局域网,供手机在同
Wi-Fi / 虚拟局域网(Tailscale)上打开评审。仅用 Python 标准库,零第三方依赖。

URL 契约(验收逐条对应):
- GET /                          → 404(根路径一律不给)
- GET /<token>/stats.html        → 200(页面含「kimi 产出」标记)
- GET /<token>/stats.data.js     → 200(别名:实际文件在 mock/stats/ 子目录,
                                    见 _ALIASES)
- GET /<token>/<root 内相对路径>  → 200(允许多级子路径——页面自身引用的
                                    stats/stats.data.js 即走此路;逐段过白名单,
                                    只服务普通文件,目录不给)
- 其余一切:token 不对、段数不对、含 . / .. / 反斜杠 / 非法字符 → 一律 404
  (穿越拒绝;再叠加 resolve 后必须仍在 root 内的双保险)

自杀:--deadline 支持两种形态——
- 绝对:"YYYY-MM-DD HH:MM"(本地时区;也收 "YYYY-MM-DD HH:MM:SS" 与
  "YYYY-MM-DD",后者取当日 08:00)
- 相对:"+N seconds|minutes|hours"(如 "+2 minutes"、"+5 seconds")
缺省=明晨 08:00(本地)。检查点:启动时一次、每个请求一次、另加看门狗线程
到点关停(空闲无请求也能准时自杀),退出码 0。

用法:
  python -B .scratch/usage-stats-view/tools/serve.py \
      --token <随机段> [--port 8080] [--deadline "YYYY-MM-DD HH:MM"] \
      [--root widget/ui/mock] [--lan-ip 192.168.100.102] [--vpn-ip 100.121.249.122]

启动即打印三行 URL:同 Wi-Fi / 虚拟局域网(手机可达)与本机 127.0.0.1。
零闪窗:本脚本只被自带隐藏控制台的宿主(Git Bash / Claude Code Bash 工具)
拉起,自身不再创建任何控制台;禁 wscript/计划任务形态。
"""

from __future__ import annotations

import argparse
import re
import sys
import threading
import time
from datetime import datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import unquote

# token 与文件路径段的严格白名单:字母/数字/点/横线/下划线;".",".." 显式再拒
_SEG_RE = re.compile(r"^[A-Za-z0-9._-]+$")

# 数据文件别名:票02 页面 src=stats/stats.data.js(数据在 mock/stats/ 子目录),
# 票03 验收契约要求 /<token>/stats.data.js 亦可达——映射到根内同一文件。
# 仅此一条,目标仍在 root 内,穿越检查照常生效。
_ALIASES = {
    "stats.data.js": "stats/stats.data.js",
}

_MIME = {
    ".html": "text/html; charset=utf-8",
    ".js": "text/javascript; charset=utf-8",
    ".css": "text/css; charset=utf-8",
    ".json": "application/json; charset=utf-8",
}

_REL_RE = re.compile(
    r"^\+(\d+)\s*(seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h)$",
    re.IGNORECASE,
)


def parse_deadline(spec: str | None) -> datetime:
    """解析 --deadline:相对 "+N unit" 或绝对本地时区时间;缺省明晨 08:00。"""
    now = datetime.now()
    if spec is None or spec.strip() == "":
        nxt = (now + timedelta(days=1)).replace(hour=8, minute=0, second=0, microsecond=0)
        return nxt
    spec = spec.strip()
    m = _REL_RE.match(spec)
    if m:
        n = int(m.group(1))
        unit = m.group(2).lower()
        if unit.startswith("s"):
            delta = timedelta(seconds=n)
        elif unit.startswith("m"):
            delta = timedelta(minutes=n)
        else:
            delta = timedelta(hours=n)
        return now + delta
    for fmt in ("%Y-%m-%d %H:%M", "%Y-%m-%d %H:%M:%S"):
        try:
            return datetime.strptime(spec, fmt)
        except ValueError:
            continue
    try:
        return datetime.strptime(spec, "%Y-%m-%d").replace(hour=8, minute=0)
    except ValueError:
        pass
    raise ValueError(
        "--deadline 无法解析:{!r}(支持 \"+N seconds/minutes/hours\"、"
        "\"YYYY-MM-DD HH:MM\" 或 \"YYYY-MM-DD\")".format(spec)
    )


def validate_token(token: str) -> str:
    if not token or token in (".", "..") or not _SEG_RE.match(token):
        raise ValueError(
            "--token 只允许字母/数字/横线/下划线/点(且不为 . 或 ..):{!r}".format(token)
        )
    return token


class MockHandler(BaseHTTPRequestHandler):
    server_version = "MockStats/1.0"
    protocol_version = "HTTP/1.1"
    # 类属性,main() 注入
    token: str = ""
    root: Path = Path(".")
    deadline: datetime = datetime.max

    def do_GET(self) -> None:  # noqa: N802(BaseHTTPRequestHandler 命名)
        self._handle(head_only=False)

    def do_HEAD(self) -> None:  # noqa: N802
        self._handle(head_only=True)

    # ---- 内部 ----

    def _expired(self) -> bool:
        """请求循环内的截止检查(第 2 检查点);到点顺手触发关停。"""
        if datetime.now() >= self.deadline:
            threading.Thread(target=self.server.shutdown, daemon=True).start()
            return True
        return False

    def _handle(self, head_only: bool) -> None:
        if self._expired():
            self.send_error(503, "deadline expired")
            return
        raw = self.path.split("?", 1)[0].split("#", 1)[0]
        decoded = unquote(raw)
        parts = [p for p in decoded.split("/") if p != ""]
        if len(parts) < 2:  # 根路径与无 token 路径一律 404
            self.send_error(404)
            return
        token, name = parts[0], "/".join(parts[1:])
        if token != self.token:
            self.send_error(404)
            return
        name = _ALIASES.get(name, name)
        segs = name.split("/")
        for s in segs:
            if s in (".", "..") or not _SEG_RE.match(s):
                self.send_error(404)  # 穿越/非法段:与"不存在"同形,不给探针信息
                return
        target = self.root.joinpath(*segs)
        try:
            resolved = target.resolve()
            root = self.root.resolve()
            if not resolved.is_relative_to(root) or not resolved.is_file():
                self.send_error(404)
                return
            body = resolved.read_bytes()
        except OSError:
            self.send_error(404)
            return
        ctype = _MIME.get(resolved.suffix.lower(), "application/octet-stream")
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        if not head_only:
            self.wfile.write(body)

    def log_message(self, fmt: str, *args) -> None:
        sys.stdout.write("[mock] %s %s\n" % (self.address_string(), fmt % args))
        sys.stdout.flush()


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="票03 · mock 服务:随机路径+到期自杀")
    ap.add_argument("--token", required=True, help="URL 随机段(字母数字-_)")
    ap.add_argument("--port", type=int, default=8080, help="监听端口(缺省 8080)")
    ap.add_argument("--deadline", default=None,
                    help="截止时间:\"YYYY-MM-DD HH:MM\" 本地时区或 \"+N seconds/minutes/hours\";缺省明晨 08:00")
    ap.add_argument("--root", default=None,
                    help="静态根目录(缺省=仓库内 widget/ui/mock)")
    ap.add_argument("--lan-ip", default="192.168.100.102", help="同 Wi-Fi 网卡 IP(打印用)")
    ap.add_argument("--vpn-ip", default="100.121.249.122", help="虚拟局域网 IP(打印用)")
    args = ap.parse_args(argv)

    try:  # 管道输出统一 UTF-8,防 GBK 控制台编码错
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    except Exception:
        pass

    try:
        token = validate_token(args.token)
        deadline = parse_deadline(args.deadline)
    except ValueError as e:
        print("[mock] 参数错误:{}".format(e), file=sys.stderr)
        return 2
    if not (1 <= args.port <= 65535):
        print("[mock] 参数错误:--port 越界:{}".format(args.port), file=sys.stderr)
        return 2

    if args.root:
        root = Path(args.root)
        if not root.is_absolute():
            root = Path.cwd() / root
    else:
        root = Path(__file__).resolve().parents[3] / "widget" / "ui" / "mock"
    root = root.resolve()
    if not root.is_dir():
        print("[mock] root 不是目录:{}".format(root), file=sys.stderr)
        return 2

    # 启动时查 deadline(第 1 检查点):已过点直接退出,不占端口
    if datetime.now() >= deadline:
        print("[mock] 启动时已过截止时间 {},直接退出(0)".format(
            deadline.strftime("%Y-%m-%d %H:%M:%S")), flush=True)
        return 0

    try:
        httpd = ThreadingHTTPServer(("0.0.0.0", args.port), MockHandler)
    except OSError as e:
        print("[mock] 端口 {} 监听失败:{}".format(args.port, e), file=sys.stderr)
        return 1
    httpd.daemon_threads = True
    MockHandler.token = token
    MockHandler.root = root
    MockHandler.deadline = deadline

    # 看门狗:到点关停(空闲无人请求也能准时自杀;exit 0)
    def _watchdog() -> None:
        remain = (deadline - datetime.now()).total_seconds()
        if remain > 0:
            time.sleep(remain)
        print("[mock] 到达截止时间 {},自杀退出".format(
            deadline.strftime("%Y-%m-%d %H:%M:%S")), flush=True)
        httpd.shutdown()

    threading.Thread(target=_watchdog, daemon=True).start()

    url = "http://{}:{}/{}/stats.html"
    print("[mock] root={} token={} 截止={} (本地时区)".format(
        root, token, deadline.strftime("%Y-%m-%d %H:%M:%S")))
    print(url.format(args.lan_ip, args.port, token) + "   (手机·同 Wi-Fi)")
    print(url.format(args.vpn_ip, args.port, token) + "   (手机·虚拟局域网)")
    print(url.format("127.0.0.1", args.port, token) + "   (本机)")
    print("[mock] 监听 0.0.0.0:{},Ctrl-C 可提前停".format(args.port), flush=True)

    try:
        httpd.serve_forever(poll_interval=0.5)
    except KeyboardInterrupt:
        pass
    finally:
        httpd.server_close()
    print("[mock] 已退出(0)", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
