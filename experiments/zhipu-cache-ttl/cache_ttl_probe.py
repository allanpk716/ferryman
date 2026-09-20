#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""智谱 GLM Coding Plan 上下文缓存 TTL 探针（Track A：直打 OpenAI 兼容编码端点）

测量两个问题：
  Q-A 闲置 TTL  —— 一段固定前缀写入缓存后，闲置多久仍能命中？
  Q-B 刷新语义  —— 命中一次是否重置计时（决定"心跳保温"是否可行）？

原理：发一段固定长前缀 -> 等 X 分钟 -> 再发几乎相同的请求 ->
      看 usage.prompt_tokens_details.cached_tokens（满值=活着，0=已失效）。
      每个等待档位用一段全新前缀（写入 -> 干等 -> 只探一次），避免探测本身给缓存续命。

用法：
  python cache_ttl_probe.py preflight --api-key $ZHIPU_API_KEY
  python cache_ttl_probe.py ladder    --api-key $ZHIPU_API_KEY
  python cache_ttl_probe.py refresh   --api-key $ZHIPU_API_KEY --interval 8 --count 6
  python cache_ttl_probe.py report    --results results/

仅依赖标准库。详见同目录 README.md。
"""
from __future__ import annotations

import argparse
import glob
import json
import random
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime
from pathlib import Path

DEFAULT_BASE_URL = "https://open.bigmodel.cn/api/coding/paas/v4"  # 官方 FAQ：Claude Code/Cherry Studio 之外工具的编码端点
DEFAULT_MODEL = "glm-5.3"
# Coding Plan 声明"仅限官方支持的指定工具与产品环境"，端点可能按 UA 门禁。
# Cherry Studio 是官方文档点名的受支持工具，默认伪装之；可用 --user-agent 覆盖。
DEFAULT_UA = "CherryStudio/1.5.0"
# 探针不需要深度思考；若端点对该字段报 400，把 EXTRA_BODY 改成 {} 再跑。
EXTRA_BODY = {"thinking": {"type": "disabled"}}
WRITE_USER = "请记住以上资料备用。请只回复：READY"
PROBE_USER = "请只回复：OK"
HIT_RATIO = 0.85  # cached_tokens / prompt_tokens >= 该值记为命中（容忍尾部新增 token）
MAX_TOKENS = 16
TIMEOUT_S = 240


# ---------------------------------------------------------------- 基础工具

def now_iso() -> str:
    return datetime.now().strftime("%Y-%m-%d %H:%M:%S")


def build_prefix(arm_id: str, chars: int) -> str:
    """按 arm_id 确定性生成一段 ~chars 字符的中文填充文本（同一 arm_id 永远字节级相同）。"""
    rng = random.Random(arm_id)
    topics = ["缓存一致性", "任务调度", "日志聚合", "索引压缩", "会话恢复",
              "令牌计量", "快照校验", "增量同步", "队列回压", "分片路由"]
    units = ["ms", "qps", "%", "MB", "条/秒"]
    lines = [f"（内部技术参考资料汇编 编号 {arm_id}，仅供链路测控使用，无业务含义。）"]
    total = len(lines[0])
    i = 0
    while total < chars:
        i += 1
        line = (f"{i}. 在{rng.choice(topics)}场景下，样本组 {rng.randint(1000, 9999)} "
                f"测得指标 {rng.uniform(0, 100):.2f}{rng.choice(units)}，"
                f"与基线 {rng.randint(10, 99)} 相差 {rng.uniform(0, 5):.3f} 个标准差；"
                f"该结论适用于批次 {rng.randint(100000, 999999)}，复核人 {rng.randint(1, 99)} 号。")
        lines.append(line)
        total += len(line)
    return "\n".join(lines)


def post_chat(base_url: str, api_key: str, ua: str, model: str, messages: list,
              max_tokens: int = MAX_TOKENS) -> dict:
    """发一次 chat/completions，返回 {ok, http, latency_ms, prompt_tokens, cached_tokens, error...}。"""
    url = base_url.rstrip("/") + "/chat/completions"
    payload = {"model": model, "messages": messages,
               "max_tokens": max_tokens, "temperature": 0.1, **EXTRA_BODY}
    last: dict = {"ok": False, "http": None, "error": "no attempt"}
    for attempt in range(1, 4):  # 网络错误/429 重试，4xx/5xx 业务错误不重试
        req = urllib.request.Request(
            url, data=json.dumps(payload).encode("utf-8"),
            headers={"Authorization": f"Bearer {api_key}",
                     "Content-Type": "application/json",
                     "Accept": "application/json",
                     "User-Agent": ua},
            method="POST")
        t0 = time.monotonic()
        try:
            with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
                body = json.loads(resp.read().decode("utf-8"))
            usage = body.get("usage") or {}
            ptd = usage.get("prompt_tokens_details") or {}
            return {"ok": True, "http": resp.status,
                    "latency_ms": round((time.monotonic() - t0) * 1000),
                    "prompt_tokens": usage.get("prompt_tokens"),
                    "completion_tokens": usage.get("completion_tokens"),
                    "cached_tokens": ptd.get("cached_tokens"),
                    "has_cache_field": "cached_tokens" in ptd,
                    "attempt": attempt}
        except urllib.error.HTTPError as e:
            detail = ""
            try:
                detail = e.read().decode("utf-8", "replace")[:300]
            except Exception:
                pass
            last = {"ok": False, "http": e.code, "error": detail, "attempt": attempt}
            if e.code == 429:
                time.sleep(30)
                continue
            return last
        except Exception as e:  # 连接层错误
            last = {"ok": False, "http": None, "error": repr(e), "attempt": attempt}
            time.sleep(5)
    return last


def record(results_path: Path, **kw) -> None:
    line = json.dumps({"ts": now_iso(), **kw}, ensure_ascii=False)
    with results_path.open("a", encoding="utf-8") as f:
        f.write(line + "\n")


def load_state(path: Path) -> dict:
    if path.exists():
        return json.loads(path.read_text(encoding="utf-8"))
    return {"arms": {}}


def save_state(path: Path, state: dict) -> None:
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(state, ensure_ascii=False), encoding="utf-8")
    tmp.replace(path)


def sleep_until(deadline: float) -> None:
    """分片睡眠（<=30s/片），保证 Ctrl-C 及时响应；到点返回。"""
    while True:
        remain = deadline - time.time()
        if remain <= 0:
            return
        time.sleep(min(30, max(0.2, remain)))


def make_messages(prefix: str, user: str) -> list:
    return [{"role": "system", "content": prefix}, {"role": "user", "content": user}]


# ---------------------------------------------------------------- 子命令

def cmd_preflight(a) -> None:
    """两发小请求：验证 key/端点/UA 门禁 + 提醒去费用明细确认扣的是编码套餐。"""
    out_dir = Path(a.out_dir); out_dir.mkdir(parents=True, exist_ok=True)
    results = out_dir / f"preflight_{datetime.now():%Y%m%d_%H%M%S}.jsonl"
    tag = f"preflight-{int(time.time())}"
    prefix = build_prefix(tag, a.chars)
    for label in ("write", "probe"):
        r = post_chat(a.base_url, a.api_key, a.user_agent, a.model,
                      make_messages(prefix, PROBE_USER))
        record(results, phase="preflight", arm_id=tag, event=label,
               model=a.model, ua=a.user_agent, base_url=a.base_url, **r)
        print(f"[{label}] ok={r['ok']} http={r.get('http')} "
              f"prompt={r.get('prompt_tokens')} cached={r.get('cached_tokens')} "
              f"latency={r.get('latency_ms')}ms error={r.get('error')}")
    print(f"\n结果已写入 {results}")
    print("下一步（人工）：开放平台 -> 费用明细，确认上面两发抵扣的是【编码套餐】而非账户余额。")
    print("若报 1113/401/403：多为 UA 门禁或 Base URL 不对，见 README「门禁与计费验证」。")


def cmd_ladder(a) -> None:
    """TTL 阶梯：每档间隔 x 重复数 = 一条臂（独立前缀）。写入 -> 干等 -> 只探一次。"""
    out_dir = Path(a.out_dir); out_dir.mkdir(parents=True, exist_ok=True)
    results = out_dir / f"ladder_{datetime.now():%Y%m%d_%H%M%S}.jsonl"
    state_path = out_dir / "state_ladder.json"
    state = load_state(state_path)
    gaps = [float(g) for g in a.gaps.split(",")]
    arms = state["arms"]
    todo = []
    for g in gaps:
        for rep in range(a.reps):
            arm_id = f"{g:g}min-r{rep}"
            if arm_id not in arms:
                todo.append(arm_id)
                arms[arm_id] = {"gap_min": g, "rep": rep, "chars": a.chars,
                                "prefix": build_prefix(arm_id, a.chars),
                                "written_at": None, "write": None, "probe": None}
    save_state(state_path, state)
    est_tokens = int(a.chars / 1.5)
    print(f"前缀 ~{a.chars} 字符（实测 token 以响应 usage 为准，约 {est_tokens}）。"
          f"臂数={len(gaps) * a.reps}（新写入 {len(todo)}），总墙钟≈最大间隔 {max(gaps):g} 分钟。")
    print(f"预算粗估：每臂 2 发 × ~{est_tokens} token 输入，全 miss 计。结果 -> {results}\n")

    # 1) 写入所有未写臂（顺序发，慢但稳）
    for arm_id in todo:
        arm = arms[arm_id]
        r = post_chat(a.base_url, a.api_key, a.user_agent, a.model,
                      make_messages(arm["prefix"], WRITE_USER))
        arm["write"] = r
        if r["ok"]:
            arm["written_at"] = time.time()
        record(results, phase="ladder", arm_id=arm_id, gap_min=arm["gap_min"],
               event="write", model=a.model, ua=a.user_agent, **r)
        print(f"[write] {arm_id}: ok={r['ok']} prompt={r.get('prompt_tokens')} "
              f"latency={r.get('latency_ms')}ms err={r.get('error')}")
        save_state(state_path, state)

    # 2) 按到点时间逐臂探测（单线程调度）
    try:
        while True:
            pending = [x for x in arms.values()
                       if x["write"] and x["write"]["ok"] and not x["probe"]]
            if not pending:
                break
            arm = min(pending, key=lambda x: x["written_at"] + x["gap_min"] * 60)
            deadline = arm["written_at"] + arm["gap_min"] * 60
            arm_id = next(k for k, v in arms.items() if v is arm)
            wait_min = (deadline - time.time()) / 60
            print(f"[wait ] {arm_id}: 还需 {max(wait_min, 0):.1f} 分钟后探测")
            sleep_until(deadline)
            r = post_chat(a.base_url, a.api_key, a.user_agent, a.model,
                          make_messages(arm["prefix"], PROBE_USER))
            actual = (time.time() - arm["written_at"]) / 60
            arm["probe"] = r
            arm["actual_gap_min"] = round(actual, 2)
            save_state(state_path, state)
            record(results, phase="ladder", arm_id=arm_id, gap_min=arm["gap_min"],
                   actual_gap_min=arm["actual_gap_min"], event="probe",
                   model=a.model, ua=a.user_agent, **r)
            ratio = (r.get("cached_tokens") / r["prompt_tokens"]
                     if r["ok"] and r.get("prompt_tokens") else None)
            verdict = ("HIT" if ratio is not None and ratio >= HIT_RATIO
                       else "MISS" if ratio is not None else "无缓存字段")
            print(f"[probe] {arm_id}: {verdict} cached={r.get('cached_tokens')}"
                  f"/prompt={r.get('prompt_tokens')} latency={r.get('latency_ms')}ms")
    except KeyboardInterrupt:
        save_state(state_path, state)
        print("\n已中断并保存进度。重跑同一条 ladder 命令即可续跑（已写前缀复用、按实际写入时间计到期）。")
        return
    print(f"\n全部臂完成。汇总：python cache_ttl_probe.py report --results \"{out_dir}\"")


def cmd_refresh(a) -> None:
    """Q-B 刷新语义：单前缀，每 interval 分钟探一次 × count 次，看能否活过单发 TTL 的数倍。"""
    out_dir = Path(a.out_dir); out_dir.mkdir(parents=True, exist_ok=True)
    results = out_dir / f"refresh_{datetime.now():%Y%m%d_%H%M%S}.jsonl"
    arm_id = f"refresh-{a.interval:g}min-x{a.count}-{int(time.time())}"
    prefix = build_prefix(arm_id, a.chars)
    print(f"单前缀 {arm_id}，每 {a.interval:g} 分钟探测一次，共 {a.count} 次"
          f"（总时长 {a.interval * a.count:g} 分钟）。结果 -> {results}")
    r = post_chat(a.base_url, a.api_key, a.user_agent, a.model,
                  make_messages(prefix, WRITE_USER))
    record(results, phase="refresh", arm_id=arm_id, event="write", model=a.model, **r)
    print(f"[write ] ok={r['ok']} prompt={r.get('prompt_tokens')} latency={r.get('latency_ms')}ms")
    t0 = time.time()
    try:
        for k in range(1, a.count + 1):
            sleep_until(t0 + k * a.interval * 60)
            r = post_chat(a.base_url, a.api_key, a.user_agent, a.model,
                          make_messages(prefix, PROBE_USER))
            offset = (time.time() - t0) / 60
            record(results, phase="refresh", arm_id=arm_id, event="probe",
                   offset_min=round(offset, 2), model=a.model, **r)
            ratio = (r.get("cached_tokens") / r["prompt_tokens"]
                     if r["ok"] and r.get("prompt_tokens") else None)
            print(f"[probe {k}/{a.count}] t+{offset:.0f}min "
                  f"cached={r.get('cached_tokens')}/{r.get('prompt_tokens')}"
                  f" ({'HIT' if ratio is not None and ratio >= HIT_RATIO else 'MISS' if ratio is not None else '无字段'})"
                  f" latency={r.get('latency_ms')}ms")
    except KeyboardInterrupt:
        print("\n已中断，已有记录保留。")
        return
    print("\n判读：若命中贯穿全程且总时长 >> 单发 TTL -> 命中会刷新计时（心跳保温理论可行）；"
          "若在单发 TTL 附近掉零 -> 不刷新。")


def cmd_report(a) -> None:
    """汇总 JSONL：按档位统计命中率 + 延迟；refresh 记录按时间序列列出。"""
    files = []
    for p in a.results:
        for c in (sorted(glob.glob(p)) or [p]):
            f = Path(c)
            if f.is_dir():
                files.extend(sorted(f.glob("*.jsonl")))
            elif f.exists():
                files.append(f)
            else:
                print(f"跳过不存在的路径: {p}")
    files = list(dict.fromkeys(files))
    if not files:
        print("没有可读取的结果文件。")
        return
    rows = []
    for f in files:
        for line in f.read_text(encoding="utf-8").splitlines():
            if line.strip():
                rows.append(json.loads(line))
    probes = [r for r in rows if r.get("event") == "probe"]
    if not probes:
        print("没有 probe 记录可汇总。")
        return

    def ratio(r):
        if r.get("cached_tokens") is None or not r.get("prompt_tokens"):
            return None
        return r["cached_tokens"] / r["prompt_tokens"]

    lad = [r for r in probes if r.get("phase") == "ladder"]
    if lad:
        print(f"{'档位(min)':>8} {'探测':>4} {'HIT':>4} {'MISS':>5} {'无字段':>5} "
              f"{'实际间隔':>12} {'探测延迟ms':>10} {'写入延迟ms':>10}")
        writes = {r["arm_id"]: r for r in rows
                  if r.get("event") == "write" and r.get("phase") == "ladder"}
        for g in sorted({r["gap_min"] for r in lad}):
            grp = [r for r in lad if r["gap_min"] == g]
            rs = [ratio(r) for r in grp]
            hit = sum(1 for x in rs if x is not None and x >= HIT_RATIO)
            miss = sum(1 for x in rs if x is not None and x < HIT_RATIO)
            nof = sum(1 for x in rs if x is None)
            ag = [r.get("actual_gap_min") for r in grp if r.get("actual_gap_min")]
            pl = [r.get("latency_ms") for r in grp if r.get("latency_ms")]
            wl = [writes.get(r["arm_id"], {}).get("latency_ms")
                  for r in grp if writes.get(r["arm_id"], {}).get("latency_ms")]
            print(f"{g:>8g} {len(grp):>4} {hit:>4} {miss:>5} {nof:>5} "
                  f"{min(ag):>5.1f}~{max(ag):<6.1f} "
                  f"{sum(pl)//len(pl) if pl else '-':>10} "
                  f"{sum(wl)//len(wl) if wl else '-':>10}")
        print(f"\n（命中判定：cached_tokens/prompt_tokens >= {HIT_RATIO}；"
              f"探测延迟 << 写入延迟 本身也是命中旁证）")

    ref = [r for r in probes if r.get("phase") == "refresh"]
    for arm in dict.fromkeys(r["arm_id"] for r in ref):
        seq = sorted((r for r in ref if r["arm_id"] == arm), key=lambda r: r.get("offset_min", 0))
        print(f"\nrefresh [{arm}]：")
        for r in seq:
            x = ratio(r)
            print(f"  t+{r.get('offset_min', 0):>6.1f}min  "
                  f"{'HIT ' if x is not None and x >= HIT_RATIO else 'MISS' if x is not None else '无字段'} "
                  f"cached={r.get('cached_tokens')}/{r.get('prompt_tokens')} "
                  f"latency={r.get('latency_ms')}ms")


# ---------------------------------------------------------------- 入口

def main() -> None:
    reconfigure = getattr(sys.stdout, "reconfigure", None)
    if reconfigure:
        try:
            reconfigure(encoding="utf-8")
        except Exception:
            pass
    here = Path(__file__).resolve().parent
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    def common(p):
        p.add_argument("--api-key", default=None, help="默认取环境变量 ZHIPU_API_KEY")
        p.add_argument("--base-url", default=DEFAULT_BASE_URL)
        p.add_argument("--model", default=DEFAULT_MODEL)
        p.add_argument("--user-agent", default=DEFAULT_UA,
                       help="编码端点可能按 UA 门禁，默认伪装 Cherry Studio")
        p.add_argument("--chars", type=int, default=24000,
                       help="前缀字符数（中文约 1.5 字符/token，默认 ~16k token）")
        p.add_argument("--out-dir", default=str(here / "results"))

    p = sub.add_parser("preflight", help="门禁/计费预检：两发小请求")
    common(p); p.set_defaults(fn=cmd_preflight)

    p = sub.add_parser("ladder", help="TTL 阶梯：独立前缀，写入->干等->探一次")
    common(p)
    p.add_argument("--gaps", default="1,3,5,8,12,20,30,45,60,90,120",
                   help="逗号分隔的等待分钟档位")
    p.add_argument("--reps", type=int, default=3, help="每档重复次数（默认 3）")
    p.set_defaults(fn=cmd_ladder)

    p = sub.add_parser("refresh", help="刷新语义：单前缀定时连探")
    common(p)
    p.add_argument("--interval", type=float, required=True, help="探测间隔（分钟）")
    p.add_argument("--count", type=int, default=6, help="探测次数（默认 6）")
    p.set_defaults(fn=cmd_refresh)

    p = sub.add_parser("report", help="汇总 results/ 下的 JSONL")
    p.add_argument("--results", nargs="+", required=True,
                   help="JSONL 文件或目录（支持通配符）")
    p.set_defaults(fn=cmd_report)

    a = ap.parse_args()
    if a.cmd != "report":
        import os
        a.api_key = a.api_key or os.environ.get("ZHIPU_API_KEY")
        if not a.api_key:
            sys.exit("缺少 API key：--api-key 或环境变量 ZHIPU_API_KEY")
    a.fn(a)


if __name__ == "__main__":
    main()
