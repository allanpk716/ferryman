#!/usr/bin/env python3
"""TTL 绝对年龄裁决探针（渡口路径，B1——docs/20260928_同模型摆渡启用前评测_战役计划.md）。

裁决矛盾：E0a 探针"≤10min 确定命中、11min 起风险"（ttl_s=600 据此）vs
ADR-0016 桶口径"15-25min 桶中位 0.98"。若 15-20min 仍命中，同模型档无心跳
也能吃缓存；若 12-15min 即死，同模型档严格依赖心跳保温。

用法:
    python probe_dock_abs.py [--offsets 5,12,15,20,25] [--deadline-s 2400]

方法（防刷新污染——命中会刷新计时，同臂多探测的是间隔不是年龄）:
    每臂: 独立 claude -p 造 ~33k 研究型前缀（经转发器 15724→渡口 15722→智谱），
    T0=末次请求捕获时刻；到 T0+offset 只探一次（原样重放 max_tokens=1）。
    5min 臂为对照组（验证机械，应命中）。臂顺序建造，调度器到点即探。

纪律: 硬死线看门狗；miss 绝不重试；逐事件落盘。
产物: ~/ferryman/captures-ttl-abs/ttl_abs_results.json
"""
import argparse
import datetime as dt
import json
import os
import sys
import threading
import time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "same-model-arm"))
import arm_append as kit  # noqa: E402  复用: post_messages/load_captures/setup_arm/run_claude/log/iso_to_epoch

CAPDIR = os.path.join(kit.HOME, "ferryman", "captures-ttl-abs")
RESULTS = os.path.join(CAPDIR, "ttl_abs_results.json")
FWD = "127.0.0.1:15724"  # 与实跳臂(15723)并行不冲突

_state = {"meta": {}, "arms": []}
_lock = threading.RLock()


def save():
    with _lock:
        with open(RESULTS, "w", encoding="utf-8") as f:
            json.dump(_state, f, ensure_ascii=False, indent=1)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--offsets", default="5,12,15,20,25", help="逗号分隔的分钟数")
    ap.add_argument("--deadline-s", type=int, default=2400)
    args = ap.parse_args()
    offsets = [float(x) for x in args.offsets.split(",")]

    def _watchdog():
        time.sleep(args.deadline_s + 60)
        kit.log("硬死线到点，强制退出")
        os._exit(3)
    threading.Thread(target=_watchdog, daemon=True).start()

    os.makedirs(CAPDIR, exist_ok=True)
    kit.CAPDIR = CAPDIR  # load_captures 读 kit 的捕获目录——指向本实验目录
    _state["meta"] = {"started": dt.datetime.now().isoformat(timespec="seconds"),
                      "offsets_min": offsets, "fwd": FWD}
    save()

    # 转发器（15724 → 渡口）
    import subprocess, urllib.request
    fwd = None
    try:
        with urllib.request.urlopen("http://%s/__capture/ping" % FWD, timeout=2):
            kit.log("转发器已在运行，复用")
    except Exception:
        logf = open(os.path.join(CAPDIR, "forwarder.log"), "ab")
        fwd = subprocess.Popen(
            ["go", "run", "./experiments/capture", "-listen", FWD,
             "-upstream", kit.DOCK, "-out", CAPDIR],
            cwd=kit.REPO, stdout=logf, stderr=subprocess.STDOUT)
        for _ in range(60):
            try:
                with urllib.request.urlopen("http://%s/__capture/ping" % FWD, timeout=2):
                    break
            except Exception:
                time.sleep(1)
        kit.log("转发器就绪 (pid %s)" % (fwd.pid if fwd else "?"))

    # 逐臂: 建 → 登记到点 → 依序等到点探针（建臂期间先到的点先探）
    def _setup_arm_fwd(name):
        # 自带端口版 setup_arm——kit.setup_arm 硬编码 15723（首跑曾因此把臂流量
        # 打进旧转发器、捕获落错目录），本实验必须写自己的 FWD。
        import json as _json
        d = os.path.join(os.environ.get("TEMP", os.path.join(kit.HOME, "AppData", "Local", "Temp")), name)
        os.makedirs(os.path.join(d, ".claude"), exist_ok=True)
        with open(os.path.join(d, ".claude", "settings.json"), "w", encoding="utf-8") as f:
            _json.dump({"env": {"ANTHROPIC_BASE_URL": "http://%s" % FWD}}, f)
        return d

    t_mark = time.time()
    arms = []
    try:
        for i, off in enumerate(offsets):
            name = "ttl-arm%d-%dmin" % (i + 1, int(off))
            arm_dir = _setup_arm_fwd(name)
            kit.run_claude(arm_dir, ["-p", kit.RESEARCH_PROMPT,
                                     "--allowedTools", "Read,Grep"], "造%s" % name)
            caps = kit.load_captures(t_mark - 5)
            mine = [c for c in caps if c["path"] not in {a["cap"] for a in arms}]
            if not mine:
                kit.log("%s 无新捕获，跳过" % name)
                continue
            a = {"name": name, "offset_min": off, "cap": mine[-1]["path"],
                 "entry": mine[-1]["entry"], "t0": float(mine[-1]["epoch"]),
                 "probed": False}
            arms.append(a)
            _state["arms"] = [{"name": x["name"], "offset_min": x["offset_min"],
                               "t0": x["t0"], "probed": x["probed"]} for x in arms]
            save()
            kit.log("%s 建成 T0=%s（messages=%d）" %
                    (name, dt.datetime.fromtimestamp(a["t0"]).strftime("%H:%M:%S"),
                     len(a["entry"]["body"].get("messages", []))))
            # 建臂间隙里，先到点的臂先探
            probe_due(arms)

        while any(not a["probed"] for a in arms):
            probe_due(arms)
            if all(a["probed"] for a in arms):
                break
            time.sleep(5)
        kit.log("全部臂探完")
        return 0
    finally:
        if fwd:
            fwd.terminate()


def probe_due(arms):
    for a in arms:
        if a["probed"]:
            continue
        due = a["t0"] + a["offset_min"] * 60
        now = time.time()
        if now >= due - 1:
            body = json.loads(json.dumps(a["entry"]["body"]))
            body["max_tokens"] = 1
            r = kit.post_messages(a["entry"], body)
            a["probed"] = True
            rec = {"name": a["name"], "offset_min": a["offset_min"],
                   "age_s": int(now - a["t0"]),
                   "outcome": r.get("outcome"), "cr": r.get("cr"),
                   "in": r.get("in"), "ratio": r.get("ratio"),
                   "hit": r.get("ratio", 0) >= kit.RATIO_HIT if r.get("outcome") == "ok" else None,
                   "note": r.get("note", "")}
            with _lock:
                for row in _state["arms"]:
                    if row["name"] == a["name"]:
                        row.update(rec)
                        row["probed"] = True
                save()
            kit.log("%s @+%ds → %s cr=%s ratio=%s" %
                    (a["name"], rec["age_s"], rec["outcome"], rec.get("cr"), rec.get("ratio")))


if __name__ == "__main__":
    sys.exit(main())
