#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""缓存 TTL 探针 —— Claude Code 轨（真实 CC 链路校准）

用途：pi 轨测出的服务端 TTL/续命语义，在 CC 真实链路上做轻量校准
（CC 前缀形态更大、带 cache_control 断点、端点可能不同，结论需在此轨复核）。

做法：每臂一个独立 scratch 工作目录 + 独立 CC 会话（--session-id 固定），
首轮用 stdin 灌入长资料，到点后 claude -p --resume 问一句，
从 ~/.claude/projects/<munged-cwd>/<session-id>.jsonl 读最后一条 assistant 的
message.usage（cache_read_input_tokens / cache_creation_input_tokens / input_tokens）。

注意：CC 用什么 provider 就测什么——本轨测的是"当前 claude CLI 实际走的链路"。
若主控 CC 挂 Anthropic 官方，TTL/续命是官方已知事实（5 分钟、命中刷新），无需本轨。

用法：
  python probe_cc.py arm --gap 0.2 --chars 2000        # 冒烟：验链路+usage 解析
  python probe_cc.py ladder --gaps 10,14,20,30 --reps 2  # 边界校准（轻量）
  python probe_cc.py refresh --interval 9 --count 4      # CC 版续命
  python probe_cc.py report --results "results/*.jsonl"  # 汇总（三轨字段兼容）
"""
from __future__ import annotations

import argparse
import json
import re
import shutil
import subprocess
import sys
import time
import uuid
from datetime import datetime
from pathlib import Path

from probe_api import HIT_RATIO, build_prefix, now_iso, sleep_until

HERE = Path(__file__).resolve().parent
WRITE_PROMPT = ("以下是内部技术参考资料，请原样记住备用（后续只会问你其中一行）。\n\n"
                "{material}\n\n请只回复：READY")
PROBE_PROMPT = "请只回复：OK"
SUBPROC_TIMEOUT = 600
HOME = Path.home()


def find_claude() -> str:
    c = shutil.which("claude") or shutil.which("claude.cmd") or shutil.which("claude.exe")
    if not c:
        sys.exit("找不到 claude 可执行文件")
    return c


def cc_projects_dir(workspace: Path) -> Path:
    """CC 按 munged cwd 存 session：非字母数字全变 '-'（C:\\WorkSpace → C--WorkSpace）。"""
    munged = re.sub(r"[^A-Za-z0-9]", "-", str(workspace).replace("\\", "/"))
    return HOME / ".claude" / "projects" / munged


def run_claude(claude: str, workspace: Path, session_id: str,
               prompt: str, resume: bool) -> str:
    """跑一发 claude -p：首轮 --session-id 新建并 stdin 灌资料；resume 轮 --resume + stdin。"""
    cmd = [claude, "-p"] + (["--resume", session_id] if resume
                            else ["--session-id", session_id])
    r = subprocess.run(cmd, input=prompt, capture_output=True, text=True,
                       encoding="utf-8", errors="replace",
                       timeout=SUBPROC_TIMEOUT, cwd=str(workspace))
    if r.returncode != 0:
        raise RuntimeError(f"claude 退出码 {r.returncode}: {r.stderr.strip()[:300]}")
    return r.stdout


def parse_cc_usage(session_file: Path) -> list[dict]:
    """按顺序抽 CC session jsonl 里每条 assistant 消息的 message.usage。"""
    out = []
    for line in session_file.read_text(encoding="utf-8").splitlines():
        if '"usage"' not in line or not line.strip():
            continue
        try:
            rec = json.loads(line)
        except Exception:
            continue
        if rec.get("type") != "assistant":
            continue
        u = (rec.get("message") or {}).get("usage") or {}
        if not u:
            continue
        out.append({"input": u.get("input_tokens") or 0,
                    "output": u.get("output_tokens") or 0,
                    "cacheRead": u.get("cache_read_input_tokens") or 0,
                    "cacheWrite": u.get("cache_creation_input_tokens") or 0})
    return out


def write_arm(claude: str, arm_id: str, chars: int, results: Path, state: dict) -> None:
    """建 scratch 工作区 + CC 会话，stdin 灌长资料（幂等）。"""
    workspace = HERE / "cc-workspaces" / arm_id
    workspace.mkdir(parents=True, exist_ok=True)
    st = state.setdefault(arm_id, {})
    if st.get("session_id"):
        return
    sid = str(uuid.uuid4())
    material = build_prefix(arm_id, chars)
    run_claude(claude, workspace, sid, WRITE_PROMPT.format(material=material), resume=False)
    sf = cc_projects_dir(workspace) / f"{sid}.jsonl"
    if not sf.exists():
        raise RuntimeError(f"{arm_id}: 未找到 CC session 文件 {sf}")
    st.update(session_id=sid, session_file=str(sf), written_at=time.time())
    w = parse_cc_usage(sf)
    st["write_usage"] = w[-1] if w else None
    record(results, phase="ladder", arm_id=arm_id, event="write",
           provider="claude-code", model="cc-default",
           **(st["write_usage"] or {}))
    print(f"[write] {arm_id}: usage={st['write_usage']}")


def probe_arm(claude: str, arm_id: str, gap_min: float, results: Path,
              state: dict) -> None:
    st = state[arm_id]
    deadline = st["written_at"] + gap_min * 60
    print(f"[wait ] {arm_id}: 还需 {max((deadline - time.time()) / 60, 0):.1f} 分钟后探测")
    sleep_until(deadline)
    workspace = HERE / "cc-workspaces" / arm_id
    run_claude(claude, workspace, st["session_id"], PROBE_PROMPT, resume=True)
    usages = parse_cc_usage(Path(st["session_file"]))
    probe = usages[-1] if usages else {}
    st["probe_done"] = True
    st["probe_usage"] = probe or None
    actual = (time.time() - st["written_at"]) / 60
    st["actual_gap_min"] = round(actual, 2)
    cached = probe.get("cacheRead") or 0
    prompt_tokens = cached + (probe.get("cacheWrite") or 0) + (probe.get("input") or 0)
    ratio = cached / prompt_tokens if prompt_tokens else None
    record(results, phase="ladder", arm_id=arm_id, gap_min=gap_min,
           actual_gap_min=st["actual_gap_min"], event="probe",
           provider="claude-code", model="cc-default",
           prompt_tokens=prompt_tokens, cached_tokens=cached,
           latency_ms=None, cc_usage=probe or None)
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
        p.add_argument("--chars", type=int, default=24000,
                       help="灌入资料的字符数（CC 自身 system+tools 前缀另计）")
        p.add_argument("--out-dir", default=str(HERE / "results"))

    p = sub.add_parser("arm", help="单臂冒烟")
    common(p)
    p.add_argument("--gap", type=float, required=True)
    p.add_argument("--tag", default=None)
    p.set_defaults(mode="arm")

    p = sub.add_parser("ladder", help="CC 轨阶梯（建议只跑边界档位做校准）")
    common(p)
    p.add_argument("--gaps", default="10,14,20,30")
    p.add_argument("--reps", type=int, default=2)
    p.set_defaults(mode="ladder")

    p = sub.add_parser("refresh", help="CC 版续命测试")
    common(p)
    p.add_argument("--interval", type=float, required=True)
    p.add_argument("--count", type=int, default=4)
    p.add_argument("--tag", default=None)
    p.set_defaults(mode="refresh")

    p = sub.add_parser("report", help="汇总（三轨字段兼容）")
    p.add_argument("--results", nargs="+", required=True)
    p.set_defaults(mode="report")

    a = ap.parse_args()
    out_dir = Path(a.out_dir)
    if a.mode == "report":
        from probe_api import cmd_report
        cmd_report(argparse.Namespace(results=a.results))
        return
    out_dir.mkdir(parents=True, exist_ok=True)
    claude = find_claude()

    if a.mode == "arm":
        tag = a.tag or f"ccarm{int(time.time())}"
        results = out_dir / f"cc_smoke_{datetime.now():%Y%m%d_%H%M%S}.jsonl"
        state_path = out_dir / "state_cc_smoke.json"
        state = load_state(state_path)
        try:
            write_arm(claude, tag, a.chars, results, state)
            probe_arm(claude, tag, a.gap, results, state)
        finally:
            save_state(state_path, state)
        return

    if a.mode == "refresh":
        tag = a.tag or f"ccrefresh-{a.interval:g}min-x{a.count}-{int(time.time())}"
        results = out_dir / f"cc_refresh_{datetime.now():%Y%m%d_%H%M%S}.jsonl"
        state_path = out_dir / "state_cc_refresh.json"
        state = load_state(state_path)
        print(f"CC 续命测试 {tag}：每 {a.interval:g} 分钟 resume 一次 x {a.count}。"
              f"结果 -> {results}")
        try:
            write_arm(claude, tag, a.chars, results, state)
            t0 = state[tag]["written_at"]
            for k in range(1, a.count + 1):
                sleep_until(t0 + k * a.interval * 60)
                workspace = HERE / "cc-workspaces" / tag
                run_claude(claude, workspace, state[tag]["session_id"],
                           PROBE_PROMPT, resume=True)
                usages = parse_cc_usage(Path(state[tag]["session_file"]))
                u = usages[-1] if usages else {}
                total = ((u.get("cacheRead") or 0) + (u.get("cacheWrite") or 0)
                         + (u.get("input") or 0))
                cr = u.get("cacheRead") or 0
                record(results, phase="refresh", arm_id=tag, event="probe",
                       offset_min=round((time.time() - t0) / 60, 2),
                       provider="claude-code", model="cc-default",
                       prompt_tokens=total, cached_tokens=cr, cc_usage=u or None)
                print(f"[probe {k}/{a.count}] t+{(time.time() - t0) / 60:.0f}min "
                      f"cacheRead={cr}/{total} "
                      f"{'HIT' if total and cr / total >= HIT_RATIO else 'MISS'}")
        finally:
            save_state(state_path, state)
        return

    results = out_dir / f"cc_ladder_{datetime.now():%Y%m%d_%H%M%S}.jsonl"
    state_path = out_dir / "state_cc_ladder.json"
    state = load_state(state_path)
    gaps = [float(g) for g in a.gaps.split(",")]
    print(f"CC 阶梯：档位 {gaps} x {a.reps}。结果 -> {results}")
    try:
        pending = [f"{g:g}min-r{r}" for g in gaps for r in range(a.reps)]
        for arm_id in pending:
            if arm_id not in state or not state[arm_id].get("session_id"):
                try:
                    write_arm(claude, arm_id, a.chars, results, state)
                except Exception as e:
                    print(f"[error] 写入失败 {arm_id}: {e}")
                save_state(state_path, state)
        while True:
            left = [k for k in pending
                    if isinstance(state.get(k), dict)
                    and state[k].get("session_id") and not state[k].get("probe_done")]
            if not left:
                break
            arm_id = min(left, key=lambda k: state[k]["written_at"]
                         + float(k.split("min")[0]) * 60)
            try:
                probe_arm(claude, arm_id, float(arm_id.split("min")[0]),
                          results, state)
            except Exception as e:
                print(f"[error] 探测失败 {arm_id}: {e}")
                state[arm_id]["probe_done"] = True
            save_state(state_path, state)
    except KeyboardInterrupt:
        save_state(state_path, state)
        print("\n已中断并保存进度，重跑同命令可续跑。")
        return
    print(f"\n完成。汇总：python probe_cc.py report --results \"{out_dir}/cc_*.jsonl\"")


if __name__ == "__main__":
    main()
