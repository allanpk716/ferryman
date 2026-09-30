#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""真流量重录工具（票04 · 白天 W4 用，夜链不跑、不进 CI）。

把本机 codex→渡口 的真流量录成原始捕获文件（含 SSE 分块边界），再按脱敏
门禁转成候选夹具（对照 internal/dock/fixtures 的四形 schema）；原始捕获
转完即删（默认），绝不入 git。

两条红线：
  1. 原始捕获含真实密钥，只允许落本机 git 忽略目录（默认 .scratch/），
     转完即删；--keep-raw 仅排查时用，用完必须手工删。
  2. 工具只自动清"鉴权类"敏感物（鉴权头 + 真形密钥串）；payload 里的
     session_id/邮箱/代码片段等仍须人工复核后才可并入 internal/dock/fixtures/。

密钥形状规则与 internal/dock/fixtures 的零真实密钥扫描互为镜像（改一处须
同步另一处）；合成占位只认 sk-test-* 前缀。
"""

from __future__ import annotations

import argparse
import base64
import json
import re
import shutil
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib import request as urlrequest

# ---- 密钥形状规则（与 internal/dock/fixtures/fixtures.go keyRules 镜像） ----

REDACTED = "sk-test-redacted"
PLACEHOLDER_AUTH = "codex-placeholder-key"

# (名称, 正则, 占位放行前缀, 动词前缀)
KEY_RULES = [
    ("sk/pk/rk 密钥形", re.compile(r"\b(?:sk|pk|rk)-[A-Za-z0-9][A-Za-z0-9_-]{15,}"),
     ("sk-test-", "codex-placeholder", "fixture-placeholder", "placeholder"), None),
    ("GitHub token 形", re.compile(r"\b(?:ghp|gho|ghu|ghs)_[A-Za-z0-9]{20,}"), (), None),
    ("AWS AccessKey 形", re.compile(r"\bAKIA[0-9A-Z]{16}\b"), (), None),
    ("Google API key 形", re.compile(r"\bAIza[0-9A-Za-z_-]{30,}"), (), None),
    ("Slack token 形", re.compile(r"\bxox[baprs]-[A-Za-z0-9-]{10,}"), (), None),
    ("Bearer 长令牌形", re.compile(r"(?i)\bBearer\s+[A-Za-z0-9+/_=.-]{24,}"),
     ("sk-test-", "codex-placeholder", "fixture-placeholder", "placeholder"), "Bearer"),
]

AUTH_HEADERS = {"authorization", "x-api-key", "x-goog-api-key", "cookie", "set-cookie"}
# 转发时剥的头（逐跳 + 压缩协商——urllib 自动解压，剥掉防双解； host/长度由转发层重算）
HOP_HEADERS = {"connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
               "te", "trailer", "transfer-encoding", "upgrade", "accept-encoding",
               "content-length"}


def redact_text(text: str) -> tuple[str, int]:
    """真形密钥串 → sk-test-redacted 占位；已是合成占位的串原样放行。
    返回（脱敏后文本, 命中数）。"""
    hits = 0

    def make_repl(allowed, verb):
        def repl(match: re.Match) -> str:
            nonlocal hits
            token = match.group(0)
            probe = token[len(verb):].lstrip(" \t") if verb else token
            if any(probe.startswith(p) for p in allowed):
                return token
            hits += 1
            return (verb + " " + REDACTED) if verb else REDACTED
        return repl

    for _, rx, allowed, verb in KEY_RULES:
        text = rx.sub(make_repl(allowed, verb), text)
    return text, hits


def redact_headers(headers: dict) -> tuple[dict, int]:
    """鉴权头整条剥除（夹具 schema 不含头）；其余头过密钥串扫描。
    返回（头, 命中数）。"""
    hits = 0
    out = {}
    for k, v in headers.items():
        if k.lower() in AUTH_HEADERS:
            hits += 1
            continue
        clean, n = redact_text(str(v))
        hits += n
        out[k] = clean
    return out, hits


# ---- record：本机捕获代理（codex → 捕获口 → 渡口） ----

class CaptureState:
    def __init__(self, capture_dir: Path, upstream: str):
        self.capture_dir = capture_dir
        self.upstream = upstream.rstrip("/")
        self.lock = threading.Lock()
        self.seq = 0
        self.exchanges = 0

    def next_file(self, path: str) -> Path:
        with self.lock:
            self.seq += 1
            safe = re.sub(r"[^A-Za-z0-9]+", "_", path).strip("_")[:40] or "root"
            return self.capture_dir / f"exchange-{self.seq:04d}-{safe}.json"


def make_handler(state: CaptureState):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt, *args):  # 安静：不刷屏
            pass

        def do_POST(self):
            self.proxy()

        do_GET = do_POST
        do_PUT = do_POST
        do_DELETE = do_POST

        def proxy(self):
            length = int(self.headers.get("Content-Length") or 0)
            req_body = self.rfile.read(length) if length else b""
            req_headers = {k: v for k, v in self.headers.items()
                           if k.lower() not in HOP_HEADERS}

            req = urlrequest.Request(state.upstream + self.path,
                                     data=req_body if req_body else None,
                                     method=self.command)
            for k, v in req_headers.items():
                req.add_header(k, v)

            started = time.time()
            try:
                resp = urlrequest.urlopen(req, timeout=600)
            except Exception as exc:
                body = json.dumps({"error": {"message": f"capture proxy: {exc}",
                                             "type": "proxy_error"}}).encode()
                self.send_response(502)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                return

            with resp:
                status = resp.status
                resp_headers = {k: v for k, v in resp.headers.items()
                                if k.lower() not in HOP_HEADERS and k.lower() != "content-length"}
                # 流式转发（chunked）：SSE 分块边界按上游 flush 原样过；同时留档。
                self.send_response(status)
                for k, v in resp_headers.items():
                    self.send_header(k, v)
                self.send_header("Transfer-Encoding", "chunked")
                self.end_headers()
                chunks = []
                try:
                    while True:
                        piece = resp.read(65536)
                        if not piece:
                            break
                        chunks.append(piece)
                        self.wfile.write(f"{len(piece):X}\r\n".encode() + piece + b"\r\n")
                        self.wfile.flush()
                    self.wfile.write(b"0\r\n\r\n")
                    self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass  # 客户端先断：捕获照落盘

            with state.lock:
                state.exchanges += 1
                n = state.exchanges
            record = {
                "seq": n,
                "ts_started": started,
                "ts_done": time.time(),
                "method": self.command,
                "path": self.path,
                # 原始头（含真钥）只进原始捕获文件；sanitize 阶段剥除。
                "req_headers": req_headers,
                "req_body_b64": base64.b64encode(req_body).decode(),
                "status": status,
                "resp_headers": resp_headers,
                "resp_chunks_b64": [base64.b64encode(c).decode() for c in chunks],
            }
            out = state.next_file(self.path)
            out.write_text(json.dumps(record, ensure_ascii=False), encoding="utf-8")
            print(f"[record] #{n} {self.command} {self.path} → {status} "
                  f"({len(chunks)} 块) → {out.name}")

    return Handler


def cmd_record(args):
    outdir = Path(args.out)
    outdir.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y%m%d-%H%M%S")
    capture_dir = outdir / f"capture-{stamp}"
    capture_dir.mkdir(parents=True, exist_ok=True)
    state = CaptureState(capture_dir, args.upstream)

    server = ThreadingHTTPServer((args.listen_host, args.listen_port), make_handler(state))
    print(f"[record] 捕获口 http://{args.listen_host}:{args.listen_port} → {args.upstream}")
    print(f"[record] 捕获落盘 {capture_dir}（含真钥，只留本机；脱敏后即删）")
    print("[record] 把 codex 的 base_url 临时指向捕获口，跑若干请求后 Ctrl-C 结束。")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print(f"\n[record] 结束，共 {state.exchanges} 次交换，目录：{capture_dir}")
    finally:
        server.server_close()


# ---- sanitize：脱敏门禁 + 转候选夹具 ----

def parse_sse_frames(text: str) -> list[dict]:
    """SSE 文本 → [{event, data(JSON 文本，单行)}]；与 fixtures schema 对齐。"""
    frames = []
    for block in text.split("\n\n"):
        block = block.strip()
        if not block:
            continue
        event, data_parts = "", []
        for line in block.split("\n"):
            if line.startswith("event:"):
                event = line[len("event:"):].strip()
            elif line.startswith("data:"):
                data_parts.append(line[len("data:"):].strip())
        if not data_parts:
            continue
        data = "\n".join(data_parts)
        try:
            json.loads(data)
        except json.JSONDecodeError:
            continue  # 心跳/注释等非 JSON 帧
        frames.append({"event": event, "data": data})
    return frames


def cmd_sanitize(args):
    capture_dir = Path(args.capture_dir)
    outdir = Path(args.out)
    outdir.mkdir(parents=True, exist_ok=True)
    files = sorted(capture_dir.glob("exchange-*.json"))
    if not files:
        print(f"[sanitize] {capture_dir} 里没有 exchange-*.json", file=sys.stderr)
        sys.exit(2)

    total_hits = 0
    candidates = []
    for f in files:
        rec = json.loads(f.read_text(encoding="utf-8"))
        req_headers, hits = redact_headers(rec.get("req_headers", {}))
        req_body = base64.b64decode(rec.get("req_body_b64", ""))
        req_text, n = redact_text(req_body.decode("utf-8", errors="replace"))
        hits += n
        resp_chunks = [base64.b64decode(c) for c in rec.get("resp_chunks_b64", [])]
        resp_text, n = redact_text(b"".join(resp_chunks).decode("utf-8", errors="replace"))
        hits += n

        fixture = {
            "name": f"candidate_{rec['seq']:04d}",
            "description": (f"真流量重录候选（seq={rec['seq']} {rec['method']} {rec['path']}，"
                            "已过鉴权脱敏门禁；payload 仍须人工复核后方可并入 internal/dock/fixtures/）"),
            "redaction_hits": hits,
            "req_headers_redacted": req_headers,
            "codex_request": None,
            "anthropic_upstream": None,
            "responses_upstream": None,
        }
        try:
            fixture["codex_request"] = json.loads(req_text)
        except json.JSONDecodeError:
            fixture["codex_request_raw"] = req_text

        ctype = rec.get("resp_headers", {}).get("Content-Type", "")
        if "text/event-stream" in ctype:
            frames = parse_sse_frames(resp_text)
            # 同一捕获按事件名归边；两边各留，人工挑形状完整的一边进夹具。
            anthropic = [x for x in frames if x["event"] in
                         ("message_start", "content_block_start", "content_block_delta",
                          "content_block_stop", "message_delta", "message_stop", "error", "ping")]
            responses = [x for x in frames if x["event"].startswith("response.")]
            fixture["anthropic_upstream"] = {"status": rec.get("status", 200),
                                             "content_type": ctype, "sse": anthropic} if anthropic else None
            fixture["responses_upstream"] = {"status": rec.get("status", 200),
                                             "content_type": ctype, "sse": responses} if responses else None
        else:
            fixture["responses_upstream"] = {"status": rec.get("status", 200),
                                             "content_type": ctype or "application/json",
                                             "body": resp_text}

        out = outdir / f"candidate-{rec['seq']:04d}.json"
        out.write_text(json.dumps(fixture, ensure_ascii=False, indent=2), encoding="utf-8")
        candidates.append(out)
        total_hits += hits
        print(f"[sanitize] {f.name} → {out.name}（脱敏命中 {hits} 处）")

    print(f"[sanitize] 完成：{len(candidates)} 个候选，脱敏命中共 {total_hits} 处。")
    print("[sanitize] 下一步：人工复核候选（session_id/邮箱/代码片段等 payload 敏感物"
          "工具不清），挑形状完整的并入 internal/dock/fixtures/（加载器会再跑零真实密钥扫描兜底）。")

    if args.keep_raw:
        print(f"[sanitize] --keep-raw：原始捕获保留在 {capture_dir}（含真钥，排查完务必删除！）")
    else:
        shutil.rmtree(capture_dir)
        print(f"[sanitize] 原始捕获已删除：{capture_dir}（转完即删）")


def main(argv=None):
    ap = argparse.ArgumentParser(description="codex→渡口 真流量重录工具（白天用；夜链不跑）")
    sub = ap.add_subparsers(dest="cmd", required=True)

    p_rec = sub.add_parser("record", help="本机捕获代理：录 codex→渡口 原始流量")
    p_rec.add_argument("--listen", default="127.0.0.1:15799", help="捕获口 host:port")
    p_rec.add_argument("--upstream", default="http://127.0.0.1:15722", help="渡口地址")
    p_rec.add_argument("--out", default="../../.scratch/recorded", help="捕获根目录（须被 git 忽略）")
    p_rec.set_defaults(func=cmd_record)

    p_san = sub.add_parser("sanitize", help="脱敏门禁：剥鉴权/密钥 → 候选夹具 → 删原始捕获")
    p_san.add_argument("--capture-dir", required=True, help="录制输出的 capture-* 子目录")
    p_san.add_argument("--out", default="./candidates", help="候选夹具输出目录")
    p_san.add_argument("--keep-raw", action="store_true", help="保留原始捕获（仅排查用，用完手删）")
    p_san.set_defaults(func=cmd_sanitize)

    args = ap.parse_args(argv)
    if args.cmd == "record":
        host, _, port = args.listen.rpartition(":")
        args.listen_host, args.listen_port = host or "127.0.0.1", int(port)
    args.func(args)


if __name__ == "__main__":
    main()
