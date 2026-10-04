# -*- coding: utf-8 -*-
"""backfill_orphan_cwd.py — 2026-10-04 空键事故存量回填（会话 3793e74e 案配套）。

背景：同模型摆渡泳道（same_model）自 09-29 启用起，因派发前不跑懒富化，
交接一律存进空项目键（index.json 里 cwd=""）——闸门按真实 worktree 路径
ValidHandoff 查不到，被拦会话首条消息放行（分支7→第二条才分支6）。
根治补丁已在 watcher.go maybeSameModel 派发前补 enrich；本脚本修存量：
按生产同口径（internal/extract/extract.go:245 首条带 cwd 的 user 记录取 cwd；
store.resolvePath = abs + 符号链接解析）把孤儿条目重新落键。

用法：
  python backfill_orphan_cwd.py            # 预演（dry-run，只打印计划）
  python backfill_orphan_cwd.py --apply    # 真写（原子替换，格式与 Go flush 同）

铁律：daemon 在跑时禁止 --apply——内存态 index 会把盘上修改整个覆写回去
（flush 是全量重写）。脚本默认读 daemon.pid 探活，活着就拒绝（--force 可
强闯，自担风险）。换装停机窗内执行最稳。
"""
import io
import json
import os
import subprocess
import sys
from pathlib import Path

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

FERRYMAN_HOME = Path.home() / "ferryman"
INDEX = FERRYMAN_HOME / "index.json"
DAEMON_PID = FERRYMAN_HOME / "daemon.pid"
CC_PROJECTS = Path.home() / ".claude" / "projects"
USABLE_STATUS = ("fresh", "skeleton")  # 闸门可见态；stale/pending 重键无意义


def daemon_alive() -> bool:
    if not DAEMON_PID.exists():
        return False
    try:
        pid = int(DAEMON_PID.read_text().strip())
    except (OSError, ValueError):
        return False  # pid 文件坏 = 无法判定，按不在跑（但 --apply 前提示人工确认）
    out = subprocess.run(
        ["tasklist", "/FI", f"PID eq {pid}", "/NH"],
        capture_output=True, text=True,
    ).stdout
    return str(pid) in out and "ferryman" in out.lower()


def find_transcript(session_id: str) -> Path | None:
    """按 session_id 全项目扫 ~/.claude/projects（glob 一层 */<sid>.jsonl）。"""
    for p in CC_PROJECTS.glob(f"*{os.sep}{session_id}.jsonl"):
        return p
    return None


def transcript_cwd(transcript: Path) -> str:
    """生产同口径：首条带非空 cwd 的 user 记录（extract.go:245）。"""
    with open(transcript, encoding="utf-8", errors="replace") as f:
        for line in f:
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue
            if row.get("type") == "user" and row.get("cwd"):
                return row["cwd"]
    return ""


def go_resolve(path_str: str) -> str:
    """store.resolvePath 同义：abs + 符号链接尽力解析（resolve 非严形）。"""
    return str(Path(path_str).resolve())


def main() -> int:
    apply = "--apply" in sys.argv
    force = "--force" in sys.argv
    if apply and daemon_alive() and not force:
        print("拒绝：daemon 存活（daemon.pid 探活命中）——内存态 flush 会覆写盘上"
              "回填。请在换装停机窗内执行，或 --force 自担。")
        return 2

    idx = json.loads(INDEX.read_text(encoding="utf-8"))
    orphans = [e for e in idx["handoffs"]
               if not e.get("cwd") and e.get("status") in USABLE_STATUS]
    print(f"条目 {len(idx['handoffs'])}，空键孤儿(fresh/skeleton) {len(orphans)}")
    fixed = 0
    for e in orphans:
        sid, agent = e["session_id"], e.get("agent", "cc")
        if agent != "cc":
            print(f"  跳过 {e['handoff_id']}: agent={agent}（same_model 泳道 cc-only，"
                  f"此孤儿另有来历，人工看）")
            continue
        tr = find_transcript(sid)
        if tr is None:
            print(f"  跳过 {e['handoff_id']}: 找不到转录（已清理/未落盘）")
            continue
        cwd = transcript_cwd(tr)
        if not cwd:
            print(f"  跳过 {e['handoff_id']}: 转录无 cwd 字段")
            continue
        resolved = go_resolve(cwd)
        print(f"  重键 {e['handoff_id']}: {sid[:8]} cwd='' -> {resolved}")
        e["cwd"] = resolved
        fixed += 1

    if not apply:
        print(f"预演：可回填 {fixed} 条（--apply 才真写）")
        return 0
    tmp = INDEX.with_suffix(".json.tmp")
    tmp.write_text(json.dumps(idx, ensure_ascii=False, indent=2), encoding="utf-8")
    os.replace(tmp, INDEX)  # 原子替换（store.atomicWrite 同形）
    print(f"已写回 {fixed} 条（原子替换 {INDEX}）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
