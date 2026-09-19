#!/usr/bin/env python3
"""router-fidelity/replay —— 差分重放器（L1）。

取一份入站捕获（tap req 件，CC 原始请求），体原样重放，分别发往两条被测路径
（-a/-b），各臂落出站捕获（tap 同款 JSON 形状），供 diff.py 直接对比：

    <out>/a/<ts>_reqNNN.json  实发头（占位 auth）与体
    <out>/a/<ts>_respNNN.json 状态码 + SSE usage 四列（正文零落盘）

auth 只重建占位令牌——真钥只活在各被测路径内部，这正是差分重放的观测前提：
    Authorization: Bearer PROXY_MANAGED
    X-Api-Key: PROXY_MANAGED

头部处理复用 q14s3 经验：跳过 accept-encoding/content-length/host/connection/
authorization/x-api-key；非 ASCII 头值（转发器脱敏残留，含 U+2026）一律不重放；
Accept-Encoding 固定 identity（SSE 必须可解析）。

用法:
    python -X utf8 replay.py <capture.json|捕获目录> -a http://... -b http://... [--out DIR] [--timeout 120]
    python -X utf8 replay.py --selftest
"""
import argparse
import datetime as dt
import json
import os
import sys
import threading
import tempfile
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

for _stream in (sys.stdout, sys.stderr):
    if hasattr(_stream, "reconfigure"):
        try:
            _stream.reconfigure(encoding="utf-8", errors="replace")
        except Exception:
            pass

AUTH_LITERAL = "Bearer PROXY_MANAGED"
XAPIK_LITERAL = "PROXY_MANAGED"
# q14s3 同款跳过集合：逐跳头与 auth 类（auth 由占位令牌重建）
SKIP_HEADERS = {"accept-encoding", "content-length", "host", "connection",
                "authorization", "x-api-key"}

USAGE_COLS = ("input_tokens", "cache_read_input_tokens",
              "cache_creation_input_tokens", "output_tokens")


def _now_iso():
    return dt.datetime.now(dt.timezone.utc).astimezone().isoformat(timespec="microseconds")


def _ascii_ok(v):
    """非 ASCII 头值防御：转发器脱敏残留（含 …）一律不重放。"""
    return all(ord(c) < 128 for c in str(v))


class SSEUsage:
    """SSE usage 提取器，tap.go sseParser / q14s3 同语义：

    message_start 先入账，message_delta 逐字段覆盖（GLM 实测：start 恒全 0，
    真值在 delta——必须覆盖而非 setdefault）；delta 未带的列保留 start 值。
    """

    def __init__(self):
        self.usage = {}
        self.saw_start = False
        self.saw_delta = False
        self._data = []

    def feed(self, text):
        """喂一块（或多行）响应文本，按 SSE 帧解析（空行=事件边界）。"""
        for raw in text.split("\n"):
            line = raw.rstrip("\r")
            if line == "":
                self._flush_event()
            elif line.startswith("data:"):
                v = line[len("data:"):]
                if v.startswith(" "):
                    v = v[1:]  # 单个前导空格是字段分隔符，不是内容
                self._data.append(v)
            # 其余行（event:/注释/keep-alive）不参与——事件类型以 data JSON 的 type 为准

    def _flush_event(self):
        if not self._data:
            return
        payload = "\n".join(self._data)  # SSE 多行 data 并接（规范行为）
        self._data = []
        try:
            ev = json.loads(payload)
        except Exception:
            return  # 非 JSON 行忽略
        t = ev.get("type")
        if t == "message_start":
            u = (ev.get("message") or {}).get("usage") or {}
            self.usage.update(u)  # start 先入账
            self.saw_start = True
        elif t == "message_delta":
            self.usage.update(ev.get("usage") or {})  # delta 逐字段覆盖（GLM 真值在 delta）
            self.saw_delta = True

    def source(self):
        if self.saw_delta:
            return "message_delta"
        if self.saw_start:
            return "message_start"
        return "none"

    def value(self):
        """四列 usage；"没拿到"返回 None——绝不给全 0 假值（Q14 雷）。"""
        if not (self.saw_start or self.saw_delta):
            return None
        return {k: int(self.usage.get(k) or 0) for k in USAGE_COLS}


def build_request(entry):
    """入站捕获 → (实发头, 体字节, path, query)。体原样，仅重建占位 auth。"""
    headers = {}
    for k, v in (entry.get("headers") or {}).items():
        if k.lower() in SKIP_HEADERS:
            continue
        if not _ascii_ok(v):
            continue
        headers[k] = str(v)
    headers["Authorization"] = AUTH_LITERAL
    headers["X-Api-Key"] = XAPIK_LITERAL
    if not any(k.lower() == "content-type" for k in headers):
        headers["Content-Type"] = "application/json"
    headers["Accept-Encoding"] = "identity"  # SSE 必须可解析，压缩流 usage 无从谈起
    if entry.get("body") is not None:
        data = json.dumps(entry["body"], ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    else:
        data = str(entry.get("body_raw") or "").encode("utf-8")
    path = entry.get("path") or "/v1/messages"
    query = entry.get("query") or ""
    return headers, data, path, query


def _write_capture(adir, kind, seq, entry):
    os.makedirs(adir, exist_ok=True)
    name = "%s_%s%03d.json" % (dt.datetime.now().strftime("%Y%m%d_%H%M%S"), kind, seq)
    path = os.path.join(adir, name)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(entry, f, ensure_ascii=False, indent=2)
    return path


def replay_arm(arm, base_url, entry, out_dir, timeout):
    """单臂重放：发请求、流式解析 SSE usage、落 tap 形状出站捕获（req+resp 两件）。"""
    headers, data, path, query = build_request(entry)
    url = base_url.rstrip("/") + path + (("?%s" % query) if query else "")
    parser = SSEUsage()
    status, ctype, err = None, "", ""
    try:
        req = urllib.request.Request(url, data=data, headers=headers, method="POST")
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            status = resp.status
            ctype = resp.headers.get("Content-Type", "")
            is_sse = "text/event-stream" in ctype
            for raw in resp:  # 流式逐行：正文增量消化，不留存
                if is_sse:
                    parser.feed(raw.decode("utf-8", "replace"))
    except urllib.error.HTTPError as e:
        status = e.code
        try:
            body = e.read()[:200].decode("utf-8", "replace")
        except Exception:
            body = ""
        err = "HTTP %s: %s" % (e.code, body)
    except Exception as e:
        err = "%s: %s" % (type(e).__name__, str(e)[:200])

    seq = entry.get("seq") if isinstance(entry.get("seq"), int) else 1
    adir = os.path.join(out_dir, arm)
    req_entry = {
        "seq": seq, "ts": _now_iso(), "method": "POST",
        "path": path, "query": query, "headers": headers,
    }
    if entry.get("body") is not None:
        req_entry["body"] = entry["body"]
    else:
        req_entry["body_raw"] = str(entry.get("body_raw") or "")
    resp_entry = {
        "seq": seq, "ts": _now_iso(), "status": status,
        "content_type": ctype, "usage_source": parser.source(),
    }
    usage = parser.value()
    if usage is not None:
        resp_entry["usage"] = usage
    if err:
        resp_entry["error"] = err
    _write_capture(adir, "req", seq, req_entry)
    _write_capture(adir, "resp", seq, resp_entry)
    return {"arm": arm, "status": status, "usage": usage,
            "usage_source": parser.source(), "error": err, "dir": adir}


def load_capture(path):
    """入站捕获：单文件直读；目录取最新 *_req*.json。"""
    if os.path.isdir(path):
        cands = [f for f in os.listdir(path) if f.endswith(".json") and "_req" in f]
        if not cands:
            raise FileNotFoundError("目录无 *_req*.json 捕获: %s" % path)
        path = os.path.join(path, max(cands, key=lambda f: os.path.getmtime(os.path.join(path, f))))
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def run(capture_path, url_a, url_b, out_dir, timeout):
    entry = load_capture(capture_path)
    results = [
        replay_arm("a", url_a, entry, out_dir, timeout),
        replay_arm("b", url_b, entry, out_dir, timeout),
    ]
    for r in results:
        u = r["usage"] or {}
        print("arm %s: status=%s usage in=%s cr=%s cc=%s out=%s source=%s → %s" % (
            r["arm"], r["status"], u.get("input_tokens"), u.get("cache_read_input_tokens"),
            u.get("cache_creation_input_tokens"), u.get("output_tokens"),
            r["usage_source"], r["dir"]), flush=True)
        if r["error"]:
            print("arm %s error: %s" % (r["arm"], r["error"]), file=sys.stderr)
    print("出站捕获已落 <out>/{a,b}（tap 同款形状，可直接 diff.py <out>/a <out>/b）")
    return 0 if all(not r["error"] for r in results) else 1


# ---- 自测（内置 http.server 本地回环双上游，零外呼） ----

def selftest():
    def sse_body(delta_usage):
        start = '{"type":"message_start","message":{"usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":0}}}'
        delta = json.dumps({"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": delta_usage},
                           ensure_ascii=False)
        filler = '{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}'
        stop = '{"type":"message_stop"}'
        return "".join("data: %s\n\n" % x for x in (start, filler, delta, stop)).encode("utf-8")

    captured = {}  # 每臂记录 mock 收到的体与 auth（断言原样重放与占位令牌）

    def make_handler(tag, body):
        class H(BaseHTTPRequestHandler):
            def do_POST(self):
                n = int(self.headers.get("Content-Length") or 0)
                captured[tag] = {"body": self.rfile.read(n),
                                 "auth": self.headers.get("Authorization"),
                                 "xapik": self.headers.get("X-Api-Key")}
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *a):
                pass
        return H

    servers, urls = [], []
    sse_a = sse_body({"output_tokens": 7, "cache_read_input_tokens": 4321})
    sse_b = sse_body({"output_tokens": 9, "cache_read_input_tokens": 8787, "input_tokens": 12})
    for tag, sse in (("a", sse_a), ("b", sse_b)):
        srv = ThreadingHTTPServer(("127.0.0.1", 0), make_handler(tag, sse))
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        servers.append(srv)
        urls.append("http://127.0.0.1:%d" % srv.server_address[1])
    try:
        with tempfile.TemporaryDirectory() as td:
            cap = {
                "seq": 3, "ts": "2026-09-19T21:00:00+08:00", "method": "POST",
                "path": "/v1/messages", "query": "beta=true",
                "headers": {
                    "Content-Type": "application/json",
                    "Authorization": "Bearer SECRET-DO-NOT-SEND",
                    "X-Api-Key": "SECRET-KEY-ALSO-NOT",
                    "Accept-Encoding": "gzip,br",
                    "X-Custom-Probe": "keepme",
                    "X-Bad-Non-Ascii": "café…redacted",
                },
                "body": {"model": "claude-sonnet-5", "max_tokens": 16,
                         "messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}]},
            }
            capf = os.path.join(td, "req_in.json")
            with open(capf, "w", encoding="utf-8") as f:
                json.dump(cap, f, ensure_ascii=False, indent=2)
            outd = os.path.join(td, "out")
            rc = run(capf, urls[0], urls[1], outd, timeout=10)
            assert rc == 0, "自测失败：run 应返回 0，实得 %s" % rc

            # 两臂各落 req+resp 两件（tap 同款命名）
            for arm in ("a", "b"):
                files = sorted(os.listdir(os.path.join(outd, arm)))
                assert sum(1 for f in files if "_req" in f) == 1, "自测失败：%s 臂应落 1 份 req 件，实得 %r" % (arm, files)
                assert sum(1 for f in files if "_resp" in f) == 1, "自测失败：%s 臂应落 1 份 resp 件，实得 %r" % (arm, files)

            def read1(arm, kind):
                d = os.path.join(outd, arm)
                fn = [f for f in os.listdir(d) if ("_%s" % kind) in f][0]
                with open(os.path.join(d, fn), encoding="utf-8") as f:
                    return json.load(f)

            req_a = read1("a", "req")
            resp_a = read1("a", "resp")
            resp_b = read1("b", "resp")

            # 占位令牌上线路 + 真钥不外泄 + 跳过集合/非 ASCII 防御生效
            assert req_a["headers"]["Authorization"] == "Bearer PROXY_MANAGED", req_a["headers"]
            assert req_a["headers"]["X-Api-Key"] == "PROXY_MANAGED", req_a["headers"]
            assert req_a["headers"]["Accept-Encoding"] == "identity", req_a["headers"]
            assert req_a["headers"]["X-Custom-Probe"] == "keepme", req_a["headers"]
            assert "X-Bad-Non-Ascii" not in req_a["headers"], req_a["headers"]
            with open(os.path.join(outd, "a", sorted(os.listdir(os.path.join(outd, "a")))[0]), encoding="utf-8") as f:
                assert "SECRET" not in f.read(), "自测失败：真钥值不得出现在出站捕获"

            # 体原样重放（mock 侧记录对账）
            assert json.loads(captured["a"]["body"]) == cap["body"], "自测失败：重放体应与捕获体逐字节等义"
            assert captured["a"]["auth"] == "Bearer PROXY_MANAGED", captured["a"]
            assert captured["b"]["auth"] == "Bearer PROXY_MANAGED", captured["b"]

            # usage 提取：message_start 全 0 → message_delta 真值逐字段覆盖，
            # start 独有列保留（input/cache_creation 必须是 0 而非丢失）
            assert resp_a["usage"] == {"input_tokens": 0, "cache_read_input_tokens": 4321,
                                       "cache_creation_input_tokens": 0, "output_tokens": 7}, resp_a["usage"]
            assert resp_a["usage_source"] == "message_delta", resp_a
            assert resp_b["usage"] == {"input_tokens": 12, "cache_read_input_tokens": 8787,
                                       "cache_creation_input_tokens": 0, "output_tokens": 9}, resp_b["usage"]
    finally:
        for s in servers:
            s.shutdown()
            s.server_close()
    print("replay.py selftest: 全过（内置 http.server 本地回环双上游，零外呼）")
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description="差分重放器：同一入站捕获喂两条被测路径（L1）")
    ap.add_argument("capture", nargs="?", help="入站捕获：tap req 件 JSON，或捕获目录（取最新 req 件）")
    ap.add_argument("-a", dest="url_a", help="被测路径 A 的 base URL")
    ap.add_argument("-b", dest="url_b", help="被测路径 B 的 base URL")
    ap.add_argument("--out", default="", help="出站捕获目录（默认 ~/ferryman/router-fidelity-replay/<ts>）")
    ap.add_argument("--timeout", type=int, default=120, help="单臂请求超时秒数")
    ap.add_argument("--selftest", action="store_true", help="内置自测（本地回环，零外呼）")
    args = ap.parse_args(argv)

    if args.selftest:
        return selftest()
    if not args.capture or not args.url_a or not args.url_b:
        ap.error("需要 <capture> -a <url> -b <url>，或 --selftest")

    out_dir = args.out
    if not out_dir:
        home = os.path.expanduser("~")
        out_dir = os.path.join(home, "ferryman", "router-fidelity-replay",
                               dt.datetime.now().strftime("%Y%m%d_%H%M%S"))
    os.makedirs(out_dir, exist_ok=True)
    return run(args.capture, args.url_a, args.url_b, out_dir, args.timeout)


if __name__ == "__main__":
    sys.exit(main())
