# make-gate-dsh-variant.py — 终局修复1 工具：从 hooks/ferryman-gate.ps1 逐字节
# 拷贝生成 hooks/ferryman-gate-dsh.ps1（BOM+CRLF 与同行尾形态原样保留），仅改
# 4 行：头注释、agent 值、闸门端点、block 转发注释。每处替换断言恰命中一次。
# 逐字变体纪律由 internal/provider/dsh_hooks_test.go TestDshGateVariantScriptPins
# 逐行 diff 钉住——本脚本只是生成手段,不是持续保证。
from pathlib import Path

root = Path(__file__).resolve().parents[2]
src = root / "hooks" / "ferryman-gate.ps1"
dst = root / "hooks" / "ferryman-gate-dsh.ps1"

data = src.read_bytes()

repls = [
    (
        "# Ferryman 闸门钩子（CC UserPromptSubmit）—— 任何故障一律放行（DESIGN §4 fail-open）",
        "# Ferryman 闸门钩子（dsh CC 钩子桥 UserPromptSubmit，票01 终局修复1 变体）"
        "—— 任何故障一律放行（DESIGN §4 fail-open）。与 ferryman-gate.ps1 逐字同源，"
        "仅改 agent='dsh' 与 /dsh/gate：桥 base() 恒传空 transcript_path"
        "（index.ts:331-333），cc 键必 miss 台账＝永 no-ledger 放行，dsh 键才命中",
    ),
    (
        "agent           = 'cc'",
        "agent           = 'dsh'",
    ),
    (
        'http://127.0.0.1:$port/gate"',
        'http://127.0.0.1:$port/dsh/gate"',
    ),
    (
        "# 原样转发 daemon 的 block 决策给 CC（decision/reason/suppressOriginalPrompt）",
        "# 原样转发 daemon 的 block 决策给 dsh 桥（decision/reason/"
        "suppressOriginalPrompt；桥折 deny 拒本轮提交）",
    ),
]

for old, new in repls:
    old_b, new_b = old.encode("utf-8"), new.encode("utf-8")
    n = data.count(old_b)
    assert n == 1, f"模式应恰出现 1 次,得 {n}: {old[:50]}..."
    data = data.replace(old_b, new_b, 1)

dst.write_bytes(data)
print(f"wrote {dst} ({len(data)} bytes)")
