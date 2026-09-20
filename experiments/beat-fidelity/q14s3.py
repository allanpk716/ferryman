#!/usr/bin/env python3
"""Q14 段三实跳编排器（路线 B：捕获重放）。

用法:
    python q14s3.py [--arms 3] [--offsets 480,960,1440] [--truth 1500] [--prompt "只回复两个字：收到"]

流程（对每臂并行）:
    1. 起抓包转发器 15722→15721（复用 experiments/capture，捕获到 ~/ferryman/captures-q14s3）
    2. 造臂: %TEMP%\\q14-armN\\ 下项目级 settings.json 把 ANTHROPIC_BASE_URL 指到 15722，
       跑一次 claude -p 造 41k 量级真实前缀；T0 = 转发器收到该请求的时刻
    3. 心跳: T0+offsets 逐跳——捕获 body 原样重放（仅改 max_tokens=1），直打 15721；
       SSE 解析 usage；miss(ratio<0.85) 即停该臂剩余跳（miss 绝不重试）
    4. 地面真值: T0+truth 时在同目录 claude -p --continue 续跑一发，读会话 jsonl 的
       assistant usage cache_read —— 对账矩阵按设计文档 §3.5 判定
    5. 记账: 全部请求 token 汇总 + 按方案价格折算积分（P_in=0.00068/P_cache=0.00017/千tok级）

产物: ~/ferryman/captures-q14s3/q14s3_results.json（每个事件落盘，崩溃保部分数据）
"""
import argparse
import datetime as dt
import glob
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
HOME = os.path.expanduser("~")
CAPDIR = os.path.join(HOME, "ferryman", "captures-q14s3")
RESULTS = os.path.join(CAPDIR, "q14s3_results.json")
CLAUDE = shutil.which("claude") or "claude"
PROXY_BEAT = "http://127.0.0.1:15721"          # 心跳直打代理（生产同构）
FWD_LISTEN = "127.0.0.1:15722"
AUTH_LITERAL = "Bearer PROXY_MANAGED"           # 令牌字面量（真钥只在代理侧）
RATIO_HIT = 0.85                                # 首跳判据（方案 §0-2）
P_IN = 0.00068                                  # 积分/token（方案价格折算，标注用）
P_CACHE = 0.00017

_lock = threading.Lock()
_state = {"arms": {}, "tally": {"creation": [], "beats": [], "truth": []}}


def log(msg):
    print("[%s] %s" % (dt.datetime.now().strftime("%H:%M:%S"), msg), flush=True)


def save_state():
    with _lock:
        with open(RESULTS, "w", encoding="utf-8") as f:
            json.dump(_state, f, ensure_ascii=False, indent=1)


def iso_to_epoch(ts):
    """RFC3339Nano(7-9位小数) → epoch 秒；Z 后缀兼容。"""
    m = re.match(r"^(.*T\d\d:\d\d:\d\d)\.(\d+)(.*)$", ts)
    if m:
        frac = (m.group(2) + "000000")[:6]
        ts = "%s.%s%s" % (m.group(1), frac, m.group(3))
    ts = ts.replace("Z", "+00:00")
    return dt.datetime.fromisoformat(ts).timestamp()


def project_munge(path):
    return path.replace(":", "-").replace("\\", "-").replace("/", "-")


# ---------- 转发器 ----------

def start_forwarder():
    for _ in range(10):  # 端口占用则直接复用（上次未清）
        try:
            with urllib.request.urlopen("http://%s/__capture/ping" % FWD_LISTEN, timeout=2) as r:
                if r.status == 200:
                    log("转发器已在运行，复用")
                    return None
        except Exception:
            break
    logf = open(os.path.join(CAPDIR, "forwarder.log"), "ab")
    p = subprocess.Popen(
        ["go", "run", "./experiments/capture", "-listen", FWD_LISTEN,
         "-out", CAPDIR],
        cwd=REPO, stdout=logf, stderr=subprocess.STDOUT)
    for _ in range(60):
        try:
            with urllib.request.urlopen("http://%s/__capture/ping" % FWD_LISTEN, timeout=2) as r:
                if r.status == 200:
                    log("转发器就绪 (pid %s)" % p.pid)
                    return p
        except Exception:
            time.sleep(1)
    raise RuntimeError("转发器 60s 未就绪，见 forwarder.log")


# ---------- 造臂 ----------

def setup_arm(name):
    d = os.path.join(os.environ.get("TEMP", os.path.join(HOME, "AppData", "Local", "Temp")), name)
    os.makedirs(os.path.join(d, ".claude"), exist_ok=True)
    with open(os.path.join(d, ".claude", "settings.json"), "w", encoding="utf-8") as f:
        json.dump({"env": {"ANTHROPIC_BASE_URL": "http://%s" % FWD_LISTEN}}, f)
    return d


def run_claude(cwd, args, tag):
    t0 = time.time()
    try:
        r = subprocess.run([CLAUDE] + args, cwd=cwd, capture_output=True, timeout=300)
        out = r.stdout.decode("utf-8", "replace")[:400]
        err = r.stderr.decode("utf-8", "replace")[:400]
        log("%s claude rc=%s %.1fs out=%r err=%r" % (tag, r.returncode, time.time() - t0, out[:120], err[:120]))
        return r.returncode
    except subprocess.TimeoutExpired:
        log("%s claude 超时" % tag)
        return -1


def read_session_jsonls(arm_dir):
    proj = os.path.join(HOME, ".claude", "projects", project_munge(arm_dir))
    files = glob.glob(os.path.join(proj, "*.jsonl"))
    if not files:  # 兜底：模糊匹配
        files = glob.glob(os.path.join(HOME, ".claude", "projects", "*" + os.path.basename(arm_dir) + "*", "*.jsonl"))
    return files


def assistant_usages(path, since_epoch=0):
    out = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            try:
                e = json.loads(line)
            except Exception:
                continue
            if e.get("type") != "assistant":
                continue
            ts = e.get("timestamp") or ""
            try:
                ep = iso_to_epoch(ts) if ts else 0
            except Exception:
                ep = 0
            if ep < since_epoch:
                continue
            u = (e.get("message") or {}).get("usage") or {}
            out.append({"ts": ts, "usage": {k: u.get(k) for k in
                        ("input_tokens", "cache_read_input_tokens",
                         "cache_creation_input_tokens", "output_tokens") if k in u}})
    return out


def load_captures(since_epoch):
    caps = []
    for p in glob.glob(os.path.join(CAPDIR, "20*_req*.json")):
        try:
            d = json.load(open(p, encoding="utf-8"))
        except Exception:
            continue
        try:
            ep = iso_to_epoch(d["ts"])
        except Exception:
            continue
        if ep < since_epoch:
            continue
        body = d.get("body") or {}
        sid = (d.get("headers") or {}).get("X-Claude-Code-Session-Id")
        if not sid:
            try:
                sid = json.loads(body.get("metadata", {}).get("user_id", "{}")).get("session_id")
            except Exception:
                sid = None
        if not sid or not body.get("messages"):
            continue
        caps.append({"path": p, "epoch": ep, "sid": sid, "entry": d})
    caps.sort(key=lambda c: c["epoch"])
    return caps


# ---------- 心跳 ----------

def send_beat(cap):
    """原样重放捕获请求（仅改 max_tokens=1），直打 15721；返回 (outcome, usage, note)。"""
    entry = cap["entry"]
    body = json.loads(json.dumps(entry["body"]))  # 深拷贝
    body["max_tokens"] = 1
    data = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    headers = {"Authorization": AUTH_LITERAL, "X-Api-Key": "PROXY_MANAGED",
               "Content-Type": "application/json"}
    skip = {"accept-encoding", "content-length", "host", "connection",
            "authorization", "x-api-key"}
    for k, v in (entry.get("headers") or {}).items():
        if k.lower() in skip:
            continue
        if any(ord(c) > 127 for c in v):  # 转发器脱敏残留（含 …）一律不重放
            continue
        headers[k] = v
    sha = hashlib.sha256(data).hexdigest()[:12]
    t0 = time.time()
    usage, err = {}, ""
    try:
        req = urllib.request.Request(PROXY_BEAT + "/v1/messages?beta=true", data=data,
                                     headers=headers, method="POST")
        with urllib.request.urlopen(req, timeout=120) as resp:
            status = resp.status
            for raw in resp:
                line = raw.decode("utf-8", "replace").strip()
                if not line.startswith("data:"):
                    continue
                payload = line[5:].strip()
                if not payload or payload == "[DONE]":
                    continue
                try:
                    ev = json.loads(payload)
                except Exception:
                    continue
                t = ev.get("type")
                if t == "message_start":
                    usage.update(ev.get("message", {}).get("usage") or {})
                elif t == "message_delta":
                    # GLM 实测：message_start 的 usage 全 0，真值在 message_delta——必须覆盖而非 setdefault
                    for k, v in (ev.get("usage") or {}).items():
                        usage[k] = v
                elif t == "error":
                    err = str(ev)[:200]
                elif t == "message_stop":
                    break
    except urllib.error.HTTPError as e:
        return "error", usage, "HTTP %s: %s" % (e.code, e.read()[:200].decode("utf-8", "replace"))
    except Exception as e:
        return "error", usage, "%s: %s" % (type(e).__name__, str(e)[:200])
    ms = int((time.time() - t0) * 1000)
    cr = usage.get("cache_read_input_tokens") or 0
    cc = usage.get("cache_creation_input_tokens") or 0
    inp = usage.get("input_tokens") or 0
    denom = cr + cc + inp
    ratio = (cr / denom) if denom else 0.0
    outcome = "hit" if ratio >= RATIO_HIT else "miss"
    note = "status=%s %dms cr=%d cc=%d in=%d ratio=%.3f sha=%s%s" % (
        resp_status(usage, err), ms, cr, cc, inp, ratio, sha, (" err=" + err) if err else "")
    usage.update({"ratio": round(ratio, 4)})
    return outcome, usage, note


def resp_status(usage, err):
    return "error" if err else "ok"


# ---------- 主流程 ----------

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--arms", type=int, default=3)
    ap.add_argument("--offsets", default="480,960,1440")
    ap.add_argument("--truth", type=int, default=1500)
    ap.add_argument("--prompt", default="只回复两个字：收到")
    ap.add_argument("--resume-prompt", default="再回复两个字：好的")
    args = ap.parse_args()
    offsets = [int(x) for x in args.offsets.split(",")]

    os.makedirs(CAPDIR, exist_ok=True)
    fwd = start_forwarder()
    since = time.time()
    arms = []
    try:
        for i in range(args.arms):
            name = "q14-arm%d" % (i + 1)
            d = setup_arm(name)
            arms.append({"name": name, "dir": d, "sid": None, "cap": None, "dead": False})
        for a in arms:  # 造臂（串行+间隔，捕获文件按时间自然分开）
            run_claude(a["dir"], ["-p", args.prompt], a["name"])
            time.sleep(15)
        # 会话 id 反查（从各臂项目目录的 jsonl）
        for a in arms:
            files = read_session_jsonls(a["dir"])
            if files:
                newest = max(files, key=os.path.getmtime)
                with open(newest, encoding="utf-8") as f:
                    for line in f:
                        try:
                            e = json.loads(line)
                        except Exception:
                            continue
                        if e.get("sessionId"):
                            a["sid"] = e["sessionId"]
                            break
                a["usages_create"] = assistant_usages(newest)
                log("%s sid=%s jsonl=%s create_usage=%s" % (a["name"], a["sid"], os.path.basename(newest), a["usages_create"][-1:] if a.get("usages_create") else []))
        # 捕获匹配
        caps = load_captures(since - 30)
        by_sid = {}
        for c in caps:
            by_sid.setdefault(c["sid"], []).append(c)
        for a in arms:
            lst = by_sid.get(a["sid"]) or []
            if lst:
                a["cap"] = lst[-1]  # 取该会话最后一个请求（多轮时即最大前缀）
            log("%s captures=%d 用 %s" % (a["name"], len(lst), a["cap"]["path"] if a["cap"] else "无!"))
        missing = [a["name"] for a in arms if not a["cap"]]
        if missing:
            log("缺捕获的臂: %s —— 中止（不花钱）" % missing)
            return 2
        with _lock:
            for a in arms:
                _state["arms"][a["name"]] = {"sid": a["sid"], "capture": os.path.basename(a["cap"]["path"]),
                                              "t0": a["cap"]["epoch"], "beats": [], "truth": None,
                                              "create_usage": a.get("usages_create", [])}
                _state["tally"]["creation"] += a.get("usages_create", [])
        save_state()

        # 心跳调度（事件表：每臂 offsets 各一跳 + 真值一发，按 deadline 升序睡到点触发）
        events = []
        for a in arms:
            for bi, off in enumerate(offsets, 1):
                events.append((a["cap"]["epoch"] + off, "beat", a, bi))
            events.append((a["cap"]["epoch"] + args.truth, "truth", a, None))
        events.sort(key=lambda e: e[0])

        def fire_beat(a, bi, deadline):
            late = time.time() - deadline
            if a["dead"]:
                log("%s beat%d 跳过（臂已停）" % (a["name"], bi))
                return
            outcome, usage, note = send_beat(a["cap"])
            log("%s beat%d → %s | %s | 晚点%.1fs" % (a["name"], bi, outcome, note, late))
            with _lock:
                _state["arms"][a["name"]]["beats"].append(
                    {"index": bi, "plan_epoch": deadline, "outcome": outcome,
                     "usage": usage, "note": note})
                _state["tally"]["beats"].append({"arm": a["name"], "index": bi,
                                                 "outcome": outcome, "usage": usage})
            if outcome == "miss":
                a["dead"] = True
                log("%s miss 即停：剩余跳取消" % a["name"])
            save_state()

        def fire_truth(a, deadline):
            t_start = time.time()
            rc = run_claude(a["dir"], ["-p", "--continue", args.resume_prompt], a["name"] + ":truth")
            time.sleep(3)
            usages = []
            for f in read_session_jsonls(a["dir"]):
                usages += assistant_usages(f, since_epoch=t_start - 5)
            verdict = None
            if usages:
                u = usages[-1]["usage"]
                cr = u.get("cache_read_input_tokens") or 0
                denom = cr + (u.get("cache_creation_input_tokens") or 0) + (u.get("input_tokens") or 0)
                ratio = (cr / denom) if denom else 0.0
                verdict = "hit" if ratio >= RATIO_HIT else "miss"
                log("%s 真值 → %s cr=%d ratio=%.3f usage=%s" % (a["name"], verdict, cr, ratio, u))
            else:
                log("%s 真值未读到 usage（rc=%s）" % (a["name"], rc))
            with _lock:
                _state["arms"][a["name"]]["truth"] = {"rc": rc, "usages": usages, "verdict": verdict}
                _state["tally"]["truth"] += usages
            save_state()

        for deadline, kind, a, bi in events:
            wait = deadline - time.time()
            if wait > 0:
                log("等待 %.0fs → 下一事件" % wait)
                time.sleep(wait)
            if kind == "beat":
                threading.Thread(target=fire_beat, args=(a, bi, deadline), daemon=True).start()
            else:
                threading.Thread(target=fire_truth, args=(a, deadline), daemon=True).start()
        time.sleep(30)  # 等尾线程收尾
        report(offsets)
        return 0
    finally:
        if fwd:
            fwd.terminate()
            log("转发器已停")


def report(offsets):
    log("=" * 60)
    log("对账矩阵（心跳×真值，设计文档 §3.5）")
    total_in = total_cr = total_cc = total_out = 0
    for name, a in _state["arms"].items():
        beats = a.get("beats") or []
        bsum = "".join(b["outcome"][0].upper() for b in beats) or "-"
        truth = (a.get("truth") or {}).get("verdict") or "?"
        cell = {"hit-hit": "真保温 ✅", "hit-miss": "保真度失败（自我续命实锤）❌",
                "miss-hit": "TTL/时序问题 → 复测", "miss-miss": "保温中途被驱逐 → 复测"}.get(
            "%s-%s" % (("hit" if bsum.startswith("H") and "M" not in bsum and "E" not in bsum else ("miss" if bsum != "-" else "?")), truth), "?")
        log("  %s: 跳=%s 真值=%s → %s (t0=%s)" % (name, bsum, truth, cell,
            dt.datetime.fromtimestamp(a["t0"]).strftime("%H:%M:%S")))
        for grp in ([u for u in _state["tally"]["creation"]] + [b["usage"] for b in _state["tally"]["beats"]]
                    + [u for u in _state["tally"]["truth"]]):
            total_in += grp.get("input_tokens") or 0
            total_cr += grp.get("cache_read_input_tokens") or 0
            total_cc += grp.get("cache_creation_input_tokens") or 0
            total_out += grp.get("output_tokens") or 0
    est = total_in * P_IN + (total_cr + total_cc) * P_CACHE
    log("记账汇总: input=%d cache_read=%d cache_creation=%d output=%d" % (total_in, total_cr, total_cc, total_out))
    log("  折算积分（方案价格估计）: ≈%.1f" % est)
    save_state()


if __name__ == "__main__":
    sys.exit(main())
