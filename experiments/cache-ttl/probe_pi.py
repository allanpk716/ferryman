#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""缓存 TTL 探针 —— agent 轨（经 pi agent 真实链路，适合有工具门禁的订阅套餐）

pi 会把各家服务商的 usage 归一成 {input, output, cacheRead, cacheWrite}，
因此换服务商/模型只需 --provider/--model（不传则用 pi 自己的默认配置，套件可整体拷走）。

原理与 API 轨相同：每个等待档位一条独立 pi 会话（独立长资料，经
--append-system-prompt 注入系统提示，两次调用同一文件保证字节级一致），
写入 -> 干等 -> resume 探一次，从 session 文件读 usage.cacheRead 判命中。

用法（详见同目录 README.md）：
  python probe_pi.py arm --gap 0.2 --chars 2000          # 冒烟：验链路+缓存信号
  python probe_pi.py ladder                               # 粗阶梯（可 --gaps/--reps 收敛）
  python probe_pi.py refresh --interval 9 --count 6       # 续命测试
  python probe_pi.py report --results "results/*.jsonl"   # 汇总（与 API 轨字段兼容）
"""
from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import sys
import time
from datetime import datetime
from pathlib import Path

from probe_api import HIT_RATIO, build_prefix, now_iso, sleep_until

HERE = Path(__file__).resolve().parent
PI_FLAGS = ["-nt", "-ne", "-ns", "--thinking", "off"]  # 最小化可变因素；写/探两次必须完全一致
WRITE_PROMPT = "请记住系统提示中的资料备用。请只回复：READY"
PROBE_PROMPT = "请只回复：OK"
SUBPROC_TIMEOUT = 300


def find_pi() -> str:
    pi = shutil.which("pi") or shutil.which("pi.cmd") or shutil.which("pi.exe")
    if not pi:
        sys.exit("找不到 pi 可执行文件（npm 全局安装？）")
    return pi


def run_pi(pi: str, session_dir: Path, material: Path, prompt: str,
           provider: str | None, model: str | None,
           session_file: str | None = None) -> str:
    """跑一发 pi 非交互调用。provider/model 为 None 时用 pi 自身默认配置。"""
    cmd = [pi, "-p", *PI_FLAGS,
           "--session-dir", str(session_dir),
           "--append-system-prompt", str(material)]
    if provider:
        cmd += ["--provider", provider]
    if model:
        cmd += ["--model", model]
    if session_file:
        cmd += ["--session", session_file]
    cmd.append(prompt)
    r = subprocess.run(cmd, capture_output=True, text=True,
                       encoding="utf-8", errors="replace", timeout=SUBPROC_TIMEOUT,
                       cwd=str(session_dir))
    if r.returncode != 0:
        raise RuntimeError(f"pi 退出码 {r.returncode}: {r.stderr.strip()[:300]}")
    return r.stdout


def parse_session_usage(session_file: Path) -> list[dict]:
    """从 pi session JSONL 里按顺序抽出每条 assistant 消息的归一化 usage。"""
    out = []
    for line in session_file.read_text(encoding="utf-8").splitlines():
        if '"usage"' not in line or not line.strip():
            continue
        try:
            rec = json.loads(line)
        except Exception:
            continue

        def walk(o):
            if isinstance(o, dict):
                u = o.get("usage")
                if isinstance(u, dict) and ("cacheRead" in u or "input" in u):
                    return u
                for v in o.values():
                    got = walk(v)
                    if got:
                        return got
            elif isinstance(o, list):
                for v in o:
                    got = walk(v)
                    if got:
                        return got
            return None

        u = walk(rec)
        if u:
            out.append({"input": u.get("input"), "output": u.get("output"),
                        "cacheRead": u.get("cacheRead") or 0,
                        "cacheWrite": u.get("cacheWrite") or 0})
    return out


def write_arm(pi: str, arm_id: str, chars: int, provider: str | None,
              model: str | None, results: Path, state: dict) -> None:
    """建会话并写入长资料（幂等：已有 session 则跳过）。"""
    arm_dir = HERE / "pi-sessions" / arm_id
    arm_dir.mkdir(parents=True, exist_ok=True)
    material = arm_dir / "material.txt"
    if not material.exists():
        material.write_text(build_prefix(arm_id, chars), encoding="utf-8")

    st = state.setdefault(arm_id, {})
    if st.get("session_file"):
        return
    run_pi(pi, arm_dir, material, WRITE_PROMPT, provider, model)
    files = sorted(arm_dir.glob("*.jsonl"), key=lambda p: p.stat().st_mtime)
    if not files:
        raise RuntimeError(f"{arm_id}: pi 未生成 session 文件")
    st["session_file"] = str(files[-1])
    st["written_at"] = time.time()
    w = parse_session_usage(Path(st["session_file"]))
    st["write_usage"] = w[-1] if w else None
    record(results, phase="ladder", arm_id=arm_id, event="write",
           provider=provider or "pi-default", model=model or "pi-default",
           **(st["write_usage"] or {}))
    print(f"[write] {arm_id}: usage={st['write_usage']}")


def probe_arm(pi: str, arm_id: str, gap_min: float, provider: str | None,
              model: str | None, results: Path, state: dict,
              quiet: bool = False) -> None:
    """等到 written_at+gap 后 resume 探测一次，解析 cacheRead 判命中。"""
    st = state[arm_id]
    deadline = st["written_at"] + gap_min * 60
    if not quiet:
        print(f"[wait ] {arm_id}: 还需 {max((deadline - time.time()) / 60, 0):.1f} 分钟后探测")
    sleep_until(deadline)
    arm_dir = HERE / "pi-sessions" / arm_id
    run_pi(pi, arm_dir, arm_dir / "material.txt", PROBE_PROMPT, provider, model,
           st["session_file"])
    usages = parse_session_usage(Path(st["session_file"]))
    probe = usages[-1] if usages else {}
    st["probe_done"] = True
    st["probe_usage"] = probe or None
    actual = (time.time() - st["written_at"]) / 60
    st["actual_gap_min"] = round(actual, 2)
    cached = probe.get("cacheRead") or 0
    prompt_tokens = cached + (probe.get("cacheWrite") or 0) + (probe.get("input") or 0)
    ratio = cached / prompt_tokens if prompt_tokens else None
    # 字段与 API 轨的 report 兼容
    record(results, phase="ladder", arm_id=arm_id, gap_min=gap_min,
           actual_gap_min=st["actual_gap_min"], event="probe",
           provider=provider or "pi-default", model=model or "pi-default",
           prompt_tokens=prompt_tokens, cached_tokens=cached,
           latency_ms=None, pi_usage=probe or None)
    verdict = "HIT" if ratio is not None and ratio >= HIT_RATIO else "MISS"
    print(f"[probe] {arm_id}: {verdict} cacheRead={cached} "
          f"input={probe.get('input')} cacheWrite={probe.get('cacheWrite')} (t+{actual:.1f}min)")


def record(results: Path, **kw) -> None:
    with results.open("a", encoding="utf-8") as f:
        f.write(json.dumps({"ts": now_iso(), **kw}, ensure_ascii=False) + "\n")


def load_state(p: Path) -> dict:
    return json.loads(p.read_text(encoding="utf-8")) if p.exists() else {}


def save_state(p: Path, s: dict) -> None:
    tmp = p.with_suffix(".tmp")
    tmp.write_text(json.dumps(s, ensure_ascii=False, indent=1), encoding="utf-8")
    tmp.replace(p)


def main() -> None:
    reconfigure = getattr(sys.stdout, "reconfigure", None)
    if reconfigure:
        try:
            reconfigure(encoding="utf-8")
        except Exception:
            pass
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    def common(p):
        p.add_argument("--provider", default=None,
                       help="pi provider 名（不传则用 pi 自身默认配置）")
        p.add_argument("--model", default=None, help="模型 id（不传则用 pi 默认）")
        p.add_argument("--chars", type=int, default=24000,
                       help="注入系统提示的资料字符数（中文约 1.5 字符/token）")
        p.add_argument("--out-dir", default=str(HERE / "results"))

    p = sub.add_parser("arm", help="单臂测试（冒烟用）")
    common(p)
    p.add_argument("--gap", type=float, required=True, help="等待分钟数")
    p.add_argument("--tag", default=None, help="臂名（默认 auto）")
    p.set_defaults(mode="arm")

    p = sub.add_parser("ladder", help="TTL 阶梯：每档一条独立 pi 会话")
    common(p)
    p.add_argument("--gaps", default="1,3,5,8,12,20,30,45,60,90,120")
    p.add_argument("--reps", type=int, default=3)
    p.set_defaults(mode="ladder")

    p = sub.add_parser("refresh", help="续命测试：单会话按 interval 连摸 count 次")
    common(p)
    p.add_argument("--interval", type=float, required=True, help="探测间隔（分钟）")
    p.add_argument("--count", type=int, default=6, help="探测次数")
    p.add_argument("--tag", default=None)
    p.set_defaults(mode="refresh")

    p = sub.add_parser("report", help="汇总 JSONL（字段兼容 API 轨）")
    p.add_argument("--results", nargs="+", required=True)
    p.set_defaults(mode="report")

    a = ap.parse_args()
    out_dir = Path(a.out_dir)
    if a.mode == "report":
        from probe_api import cmd_report
        cmd_report(argparse.Namespace(results=a.results))
        return
    out_dir.mkdir(parents=True, exist_ok=True)
    pi = find_pi()

    if a.mode == "arm":
        tag = a.tag or f"arm{int(time.time())}"
        results = out_dir / f"pi_smoke_{datetime.now():%Y%m%d_%H%M%S}.jsonl"
        state_path = out_dir / "state_pi_smoke.json"
        state = load_state(state_path)
        try:
            write_arm(pi, tag, a.chars, a.provider, a.model, results, state)
            probe_arm(pi, tag, a.gap, a.provider, a.model, results, state)
        finally:
            save_state(state_path, state)
        return

    if a.mode == "refresh":
        tag = a.tag or f"refresh-{a.interval:g}min-x{a.count}-{int(time.time())}"
        results = out_dir / f"pi_refresh_{datetime.now():%Y%m%d_%H%M%S}.jsonl"
        state_path = out_dir / "state_pi_refresh.json"
        state = load_state(state_path)
        print(f"续命测试 {tag}：每 {a.interval:g} 分钟摸一次 x {a.count}"
              f"（总时长 {a.interval * a.count:g} 分钟）。结果 -> {results}")
        try:
            write_arm(pi, tag, a.chars, a.provider, a.model, results, state)
            t0 = state[tag]["written_at"]
            for k in range(1, a.count + 1):
                sleep_until(t0 + k * a.interval * 60)
                arm_dir = HERE / "pi-sessions" / tag
                run_pi(pi, arm_dir, arm_dir / "material.txt", PROBE_PROMPT,
                       a.provider, a.model, state[tag]["session_file"])
                usages = parse_session_usage(Path(state[tag]["session_file"]))
                u = usages[-1] if usages else {}
                total = ((u.get("cacheRead") or 0) + (u.get("cacheWrite") or 0)
                         + (u.get("input") or 0))
                cr = u.get("cacheRead") or 0
                record(results, phase="refresh", arm_id=tag, event="probe",
                       offset_min=round((time.time() - t0) / 60, 2),
                       provider=a.provider or "pi-default",
                       model=a.model or "pi-default",
                       prompt_tokens=total, cached_tokens=cr, pi_usage=u or None)
                print(f"[probe {k}/{a.count}] t+{(time.time() - t0) / 60:.0f}min "
                      f"cacheRead={cr}/{total} "
                      f"{'HIT' if total and cr / total >= HIT_RATIO else 'MISS'}")
            print("\n判读：贯穿全程且总时长 >> 单发 TTL -> 命中会续命（心跳保温可行）；"
                  "在单发 TTL 附近掉零 -> 不续命。")
        finally:
            save_state(state_path, state)
        return

    results = out_dir / f"pi_ladder_{datetime.now():%Y%m%d_%H%M%S}.jsonl"
    state_path = out_dir / "state_pi_ladder.json"
    state = load_state(state_path)
    gaps = [float(g) for g in a.gaps.split(",")]
    print(f"pi 阶梯：档位 {gaps} x {a.reps} 重复，资料 ~{a.chars} 字符/臂，"
          f"总墙钟≈写入阶段 + {max(gaps):g} 分钟。结果 -> {results}")
    try:
        pending = [f"{g:g}min-r{r}" for g in gaps for r in range(a.reps)]
        # 阶段一：顺序写入所有未写臂（各自记录 written_at）
        for arm_id in pending:
            if arm_id in state and state[arm_id].get("session_file"):
                continue
            try:
                write_arm(pi, arm_id, a.chars, a.provider, a.model, results, state)
            except Exception as e:
                print(f"[error] 写入失败 {arm_id}: {e}")
            save_state(state_path, state)
        # 阶段二：按到点时间逐臂探测
        while True:
            left = [k for k in pending
                    if isinstance(state.get(k), dict)
                    and state[k].get("session_file") and not state[k].get("probe_done")]
            if not left:
                break
            arm_id = min(left, key=lambda k: state[k]["written_at"]
                         + float(k.split("min")[0]) * 60)
            try:
                probe_arm(pi, arm_id, float(arm_id.split("min")[0]),
                          a.provider, a.model, results, state)
            except Exception as e:
                print(f"[error] 探测失败 {arm_id}: {e}")
                state[arm_id]["probe_done"] = True  # 失败臂不阻塞其余
            save_state(state_path, state)
    except KeyboardInterrupt:
        save_state(state_path, state)
        print("\n已中断并保存进度，重跑同命令可续跑。")
        return
    print(f"\n完成。汇总：python probe_pi.py report --results \"{out_dir}/pi_ladder_*.jsonl\"")


if __name__ == "__main__":
    main()
