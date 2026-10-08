#!/usr/bin/env python3
"""同模型摆渡·追加重放实跳臂编排器（ADR-0016 启用硬门槛的实验臂）。

用法:
    python arm_append.py [--deadline-s 1200] [--max-tokens 8192] [--aged-offset-s 300]

流程:
    1. 起抓包转发器 15723 → 渡口 15722（复用 experiments/capture；捕获到 ~/ferryman/captures-sm-arm）
    2. 造臂: %TEMP%\\sm-arm-<ts>\\ 项目级 settings 把 ANTHROPIC_BASE_URL 指到 15723；
       两轮 claude -p（第二轮 --continue）造带真实工具调用的研究型前缀；
       T0 = 末次请求被转发器捕获的时刻
    3. 基线心跳: 捕获 body 原样重放（仅 max_tokens=1）直打渡口 → ratio_base；
       ratio_base < 0.85 = 前缀已冷，中止（无法测追加）
    4. 追加重放 v1: 前缀原样 + 末尾追加一条 user 摆渡指令，max_tokens 放开 → 四判据:
       ① cr_v1 ≥ cr_base（缓存读不低于同前缀基线）
       ② v1 去掉追加消息后与原 body 逐字节一致（JSON 规范化比对）
       ③ stop_reason ≠ tool_use
       ④ 输出含 <<<INJECT>>> 与六节标题（可解析为交接 MD）
    5. v1 被端点拒绝（连续 user 角色等怪癖）→ v2 兜底: 追加为末条 user 消息内的
       额外 text block（不新增消息条目），同四判据
    6. 可选 aged 臂: T0+aged-offset 再跑一次 v1（略老缓存下的追加行为）
    7. 产物: captures-sm-arm/sm_arm_results.json + sm_arm_report.md（四判据逐条落纸）

纪律: 硬墙钟 deadline（到点即止，不再发任何请求）；miss 绝不重试；
      单请求超时 120s；claude -p 超时 300s。
"""
import argparse
import datetime as dt
import glob
import hashlib
import json
import os
import shutil
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
HOME = os.path.expanduser("~")
CAPDIR = os.path.join(HOME, "ferryman", "captures-sm-arm")
RESULTS = os.path.join(CAPDIR, "sm_arm_results.json")
REPORT = os.path.join(CAPDIR, "sm_arm_report.md")
CLAUDE = shutil.which("claude") or "claude"
DOCK = "http://127.0.0.1:15722"              # 渡口入站口（生产真路径）
FWD_LISTEN = "127.0.0.1:15723"               # 抓包转发器（架在 CC 与渡口之间）
AUTH_LITERAL = "Bearer PROXY_MANAGED"        # 渡口入站不鉴权，占位照抄（server.go:7）
RATIO_HIT = 0.85

RESEARCH_PROMPT = (
    "读 C:/WorkSpace/agent/Ferryman/experiments/beat-fidelity/q14s3.py，"
    "总结它的三个关键设计（造臂/心跳重放/判据），每个设计给出文件里的函数名。"
    "然后用两句话回答：如果把它的重放从 max_tokens=1 改成放开输出，需要动哪几处。"
)
CONTINUE_PROMPT = "把上面的总结压缩成三行。"

# 追加摆渡指令（ADR-0016 决定三口径：只输出交接 MD、禁止调用工具；
# 六节结构要求内联——捕获前缀的 system 是 CC 的，不是摆渡人的）。
FERRY_APPEND = (
    "【同模型摆渡指令】现在请你为以上这段会话产出一份\"交接 MD\"，"
    "供一个全新会话不读原始记录就能接着干。只输出交接 MD 结构，禁止调用任何工具，"
    "禁止输出与交接无关的任何内容。直接以标记行开始、不要开场白：\n"
    "<<<INJECT>>>\n"
    "（注入层：≤2200 token 的浓缩版——目标/最新状态/下一步/关键文件/续接第一句话。必须自包含。）\n"
    "<<</INJECT>>\n"
    "（全文：以下六节，总量 ≤8000 token）\n"
    "# 目标\n# 已完成与关键结论\n# 未完成与下一步\n# 关键文件与改动\n# 踩过的坑与决策\n# 续接第一句话\n"
    "要求：文件路径、命令一律逐字引用，不要凭记忆改写或编造；"
    "凡无法从上文逐字核实的状态断言必须加「（推测）」标注，不得写成确定事实。"
)

SECTIONS = ["# 目标", "# 已完成与关键结论", "# 未完成与下一步",
            "# 关键文件与改动", "# 踩过的坑与决策", "# 续接第一句话"]

_lock = threading.RLock()  # 可重入：step() 持锁调 save_state()（首次跑因非重入锁自死锁，py-spy 定位）
_state = {"meta": {}, "steps": [], "tally": []}


def log(msg):
    print("[%s] %s" % (dt.datetime.now().strftime("%H:%M:%S"), msg), flush=True)


def save_state():
    with _lock:
        with open(RESULTS, "w", encoding="utf-8") as f:
            json.dump(_state, f, ensure_ascii=False, indent=1)


def step(name, **kw):
    with _lock:
        rec = {"step": name, "ts": dt.datetime.now().isoformat(timespec="seconds")}
        rec.update(kw)
        _state["steps"].append(rec)
        save_state()
    return rec


def iso_to_epoch(ts):
    import re
    m = re.match(r"^(.*T\d\d:\d\d:\d\d)\.(\d+)(.*)$", ts)
    if m:
        frac = (m.group(2) + "000000")[:6]
        ts = "%s.%s%s" % (m.group(1), frac, m.group(3))
    ts = ts.replace("Z", "+00:00")
    return dt.datetime.fromisoformat(ts).timestamp()


# ---------- 转发器 ----------

def start_forwarder():
    for _ in range(3):
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
         "-upstream", DOCK, "-out", CAPDIR],
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


def run_claude(cwd, args, tag, timeout=300):
    t0 = time.time()
    try:
        r = subprocess.run([CLAUDE] + args, cwd=cwd, capture_output=True, timeout=timeout)
        out = r.stdout.decode("utf-8", "replace")[:200]
        err = r.stderr.decode("utf-8", "replace")[:200]
        log("%s claude rc=%s %.1fs out=%r err=%r" % (tag, r.returncode, time.time() - t0, out[:100], err[:100]))
        return r.returncode
    except subprocess.TimeoutExpired:
        log("%s claude 超时(%ds)" % (tag, timeout))
        return -1


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
        if not body.get("messages"):
            continue
        caps.append({"path": p, "epoch": ep, "entry": d})
    caps.sort(key=lambda c: c["epoch"])
    return caps


# ---------- 重放 ----------

def canonical(body):
    return json.dumps(body, ensure_ascii=False, separators=(",", ":"), sort_keys=True)


def post_messages(entry, body):
    """POST 到渡口，SSE 解析 usage/stop_reason/文本；返回 dict。"""
    data = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    headers = {"Authorization": AUTH_LITERAL, "X-Api-Key": "PROXY_MANAGED",
               "Content-Type": "application/json"}
    skip = {"accept-encoding", "content-length", "host", "connection",
            "authorization", "x-api-key"}
    for k, v in (entry.get("headers") or {}).items():
        if k.lower() in skip:
            continue
        if any(ord(c) > 127 for c in v):  # 转发器脱敏残留（含 …）不重放
            continue
        headers[k] = v
    q = ("?" + entry["query"]) if entry.get("query") else ""
    url = DOCK + entry.get("path", "/v1/messages") + q
    sha = hashlib.sha256(data).hexdigest()[:12]
    t0 = time.time()
    usage, texts, stop_reason, err = {}, [], "", ""
    http_status = 0
    try:
        req = urllib.request.Request(url, data=data, headers=headers, method="POST")
        with urllib.request.urlopen(req, timeout=120) as resp:
            http_status = resp.status
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
                    for k, v in (ev.get("usage") or {}).items():
                        usage[k] = v
                    if ev.get("delta", {}).get("stop_reason"):
                        stop_reason = ev["delta"]["stop_reason"]
                elif t == "content_block_delta":
                    d = ev.get("delta") or {}
                    if d.get("type") == "text_delta":
                        texts.append(d.get("text", ""))
                elif t == "error":
                    err = str(ev)[:300]
                elif t == "message_stop":
                    break
    except urllib.error.HTTPError as e:
        return {"outcome": "http_error", "http": e.code,
                "note": "HTTP %s: %s" % (e.code, e.read()[:300].decode("utf-8", "replace"))}
    except Exception as e:
        return {"outcome": "error", "note": "%s: %s" % (type(e).__name__, str(e)[:300])}
    ms = int((time.time() - t0) * 1000)
    cr = usage.get("cache_read_input_tokens") or 0
    cc = usage.get("cache_creation_input_tokens") or 0
    inp = usage.get("input_tokens") or 0
    denom = cr + cc + inp
    ratio = (cr / denom) if denom else 0.0
    return {"outcome": "ok", "http": http_status, "ms": ms,
            "cr": cr, "cc": cc, "in": inp, "ratio": round(ratio, 4),
            "stop_reason": stop_reason, "sha": sha, "err": err,
            "text": "".join(texts)}


def append_v1(body, instruction, max_tokens):
    """末尾追加一条独立 user 消息。返回 (新body, 追加前body的深拷贝)。"""
    nb = json.loads(json.dumps(body))
    orig = json.loads(json.dumps(body))
    nb["messages"].append({"role": "user", "content": [
        {"type": "text", "text": instruction}]})
    nb["max_tokens"] = max_tokens
    return nb, orig


def append_v2(body, instruction, max_tokens):
    """末条 user 消息内追加一个 text block（不新增消息条目）。"""
    nb = json.loads(json.dumps(body))
    orig = json.loads(json.dumps(body))
    for msg in reversed(nb["messages"]):
        if msg.get("role") == "user":
            if isinstance(msg.get("content"), list):
                msg["content"].append({"type": "text", "text": instruction})
            else:
                msg["content"] = [{"type": "text", "text": msg["content"]},
                                  {"type": "text", "text": instruction}]
            break
    else:
        return None, orig
    nb["max_tokens"] = max_tokens
    return nb, orig


def check_criteria(res, base, orig, nb):
    """四判据逐条（ADR-0016 决定一）。返回 dict。"""
    c1 = res["cr"] >= base["cr"]
    # 判据②: 去掉追加后与原 body 一致——由构造保证，但仍验证消息数与逐键一致
    c2 = len(nb["messages"]) == len(orig["messages"]) + 1 and \
        all(canonical(a) == canonical(b) for a, b in zip(nb["messages"][:-1], orig["messages"])) and \
        canonical({k: v for k, v in nb.items() if k not in ("messages", "max_tokens")}) == \
        canonical({k: v for k, v in orig.items() if k not in ("messages", "max_tokens")})
    c3 = res.get("stop_reason", "") not in ("tool_use", "")
    txt = res.get("text", "")
    c4 = "<<<INJECT>>>" in txt and all(s in txt for s in SECTIONS)
    return {"c1_cr_ge_base": bool(c1), "c2_prefix_intact": bool(c2),
            "c3_stop_ok": bool(c3), "c4_handoff_md": bool(c4),
            "stop_reason": res.get("stop_reason"), "output_chars": len(txt)}


# ---------- 主流程 ----------

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--deadline-s", type=int, default=1200)
    ap.add_argument("--max-tokens", type=int, default=8192)
    ap.add_argument("--aged-offset-s", type=int, default=300)
    ap.add_argument("--prompt", default=None, help="覆盖造臂轮1 prompt（B2 大前缀臂用）")
    ap.add_argument("--continue-prompt", default=None)
    ap.add_argument("--claude-timeout", type=int, default=300)
    ap.add_argument("--aged-off", action="store_true", help="跳过 aged 臂（B2 只验即时）")
    args = ap.parse_args()
    if args.prompt:
        globals()["RESEARCH_PROMPT"] = args.prompt
    if args.continue_prompt:
        globals()["CONTINUE_PROMPT"] = args.continue_prompt
    t_end = time.time() + args.deadline_s

    # 硬死线看门狗：到点 os._exit——业务逻辑里的 deadline 检查只覆盖常规分支，
    # 任何意外挂死由这里兜底（长跑测试必须带终结机制）。
    def _watchdog():
        time.sleep(args.deadline_s + 60)
        log("硬死线到点，强制退出（部分结果已落盘）")
        os._exit(3)
    threading.Thread(target=_watchdog, daemon=True).start()

    os.makedirs(CAPDIR, exist_ok=True)
    _state["meta"] = {"started": dt.datetime.now().isoformat(timespec="seconds"),
                      "deadline_s": args.deadline_s, "max_tokens": args.max_tokens}
    arm_name = "sm-arm-" + dt.datetime.now().strftime("%H%M%S")
    arm = setup_arm(arm_name)
    _state["meta"]["arm_dir"] = arm
    save_state()

    fwd = start_forwarder()
    try:
        t_mark = time.time()
        rc1 = run_claude(arm, ["-p", RESEARCH_PROMPT, "--allowedTools", "Read,Grep"], "造臂轮1", timeout=args.claude_timeout)
        rc2 = run_claude(arm, ["-p", CONTINUE_PROMPT, "--continue",
                               "--allowedTools", "Read,Grep"], "造臂轮2(--continue)", timeout=args.claude_timeout)
        caps = load_captures(t_mark - 5)
        step("arm_built", rc1=rc1, rc2=rc2, captures=len(caps))
        if not caps:
            log("无捕获，中止")
            return 1
        base_cap = caps[-1]
        T0 = float(base_cap["epoch"])
        body = base_cap["entry"]["body"]
        log("重放基线=%s（messages=%d, T0=%.0f, 距今 %.0fs）" %
            (os.path.basename(base_cap["path"]), len(body.get("messages", [])),
             T0, time.time() - T0))

        # 基线心跳（max_tokens=1 原样重放）
        bb = json.loads(json.dumps(body)); bb["max_tokens"] = 1
        base = post_messages(base_cap["entry"], bb)
        step("baseline_beat", **{k: v for k, v in base.items() if k != "text"})
        log("基线: cr=%s in=%s ratio=%s" % (base.get("cr"), base.get("in"), base.get("ratio")))
        # 中止判据只看"缓存完全没工作"（cr=0）或请求失败——大前缀末段在断点之后
        # 属结构性部分命中（B2 实测 0.6056 仍在正常工作），不能按 ratio<0.85 判冷。
        if base["outcome"] != "ok" or base.get("cr", 0) == 0:
            log("基线异常（outcome=%s cr=%s）——缓存链路不通，中止" % (base.get("outcome"), base.get("cr")))
            return 2

        # v1: 末尾追加独立 user 消息
        nb, orig = append_v1(body, FERRY_APPEND, args.max_tokens)
        v1 = post_messages(base_cap["entry"], nb)
        crit1 = check_criteria(v1, base, orig, nb) if v1["outcome"] == "ok" else None
        step("append_v1", crit=crit1, **{k: v for k, v in v1.items() if k != "text"})
        if v1["outcome"] == "ok":
            log("v1: cr=%d ratio=%.3f stop=%s 判据=%s" %
                (v1["cr"], v1["ratio"], v1.get("stop_reason"), crit1))
        else:
            log("v1 失败: %s" % v1.get("note"))

        # v2 兜底（仅当 v1 被拒）
        crit2 = None
        v2 = None
        if v1["outcome"] != "ok":
            nb2, orig2 = append_v2(body, FERRY_APPEND, args.max_tokens)
            if nb2:
                v2 = post_messages(base_cap["entry"], nb2)
                crit2 = check_criteria(v2, base, orig2, nb2) if v2["outcome"] == "ok" else None
                step("append_v2", crit=crit2, **{k: v for k, v in v2.items() if k != "text"})
                log("v2: %s" % (v2.get("note") or
                                "cr=%d ratio=%.3f stop=%s 判据=%s" %
                                (v2["cr"], v2["ratio"], v2.get("stop_reason"), crit2)))

        # aged 臂：T0+aged-offset 再一发 v1（默认 5 分钟）
        aged = None
        critA = None
        wait = T0 + args.aged_offset_s - time.time()
        if not args.aged_off and wait > 0 and time.time() + wait + 150 < t_end:
            log("aged 臂等待 %.0fs（T0+%ds）……" % (wait, args.aged_offset_s))
            time.sleep(wait)
            aged = post_messages(base_cap["entry"], nb)
            critA = check_criteria(aged, base, orig, nb) if aged["outcome"] == "ok" else None
            step("append_aged", crit=critA, **{k: v for k, v in aged.items() if k != "text"})
            log("aged: cr=%d ratio=%.3f 判据=%s" % (aged.get("cr", 0), aged.get("ratio", 0), critA))

        write_report(base, v1, crit1, v2, crit2, aged, critA, body, args)
        log("完成: %s" % REPORT)
        return 0
    finally:
        if fwd:
            fwd.terminate()


def write_report(base, v1, crit1, v2, crit2, aged, critA, body, args):
    def fmt(r):
        if not r:
            return "—"
        if r["outcome"] != "ok":
            return "失败: %s" % r.get("note")
        return "cr=%d cc=%d in=%d ratio=%.3f stop=%s" % (r["cr"], r["cc"], r["in"], r["ratio"], r["stop_reason"])

    def verd(c):
        return "全过" if c and all(v for k, v in c.items() if k.startswith("c")) else \
               ("未全过" if c else "未执行")

    lines = [
        "# 同模型摆渡·追加重放实跳臂报告",
        "",
        "- 时间: %s" % _state["meta"]["started"],
        "- 路径: CC→转发器(15723)→渡口(15722)→智谱；重放直打渡口（与真流量同改写路径）",
        "- 前缀: messages=%d, max_tokens 放开至 %d" % (len(body.get("messages", [])), args.max_tokens),
        "",
        "## 四判据（ADR-0016 决定一）",
        "",
        "| 臂 | 结果 | ①cr≥基线 | ②前缀完好 | ③stop_reason | ④交接MD | 判定 |",
        "|---|---|---|---|---|---|---|",
        "| 基线心跳(max_tokens=1) | %s | — | — | — | — | — |" % fmt(base),
        "| v1 追加独立user消息 | %s | %s | %s | %s(%s) | %s | %s |" % (
            fmt(v1),
            crit1 and crit1["c1_cr_ge_base"], crit1 and crit1["c2_prefix_intact"],
            crit1 and crit1["c3_stop_ok"], crit1 and crit1["stop_reason"] if crit1 else "",
            crit1 and crit1["c4_handoff_md"], verd(crit1)),
    ]
    if v2:
        lines.append("| v2 追加text block | %s | %s | %s | %s(%s) | %s | %s |" % (
            fmt(v2), crit2 and crit2["c1_cr_ge_base"], crit2 and crit2["c2_prefix_intact"],
            crit2 and crit2["c3_stop_ok"], crit2 and crit2["stop_reason"] if crit2 else "",
            crit2 and crit2["c4_handoff_md"], verd(crit2)))
    if aged:
        lines.append("| aged(+%ds) | %s | %s | %s | %s(%s) | %s | %s |" % (
            args.aged_offset_s, fmt(aged), critA and critA["c1_cr_ge_base"],
            critA and critA["c2_prefix_intact"], critA and critA["c3_stop_ok"],
            critA and critA["stop_reason"] if critA else "",
            critA and critA["c4_handoff_md"], verd(critA)))
    lines += ["", "## 结论（人工复核后回写 arm_verdict.jsonl）", "",
              "- 待填：四判据是否全过、是否建议启用智谱同模型档。", ""]
    with open(REPORT, "w", encoding="utf-8") as f:
        f.write("\n".join(lines))


if __name__ == "__main__":
    sys.exit(main())
