#!/usr/bin/env python3
"""router-fidelity/scenarios —— 场景矩阵清单（骨架）。

每场景一条：id / 驱使真 CC 的人工操作步骤 / 期望入站形态要点（/L2 探针关联）。
本工具只列矩阵，**不自动跑真 CC**——真实流量验证是人工阶段（夜间不烧积分），
矩阵是人工执行时的操作单与判读单。

矩阵 9 类（票 10）：模型别名四档 / 子代理真名 / [1M] 大小写变体 / 多轮 /
工具往返 / 图片块 / 41k+ 长前缀 / ai-title 小请求（含 session_id 归属实测，
评审 F3 的 L1 验证项）/ count_tokens 存在性。

用法:
    python -X utf8 scenarios.py --list     # 输出全矩阵
    python -X utf8 scenarios.py --json     # 机器可读
    python -X utf8 scenarios.py --selftest
"""
import argparse
import json
import sys

for _stream in (sys.stdout, sys.stderr):
    if hasattr(_stream, "reconfigure"):
        try:
            _stream.reconfigure(encoding="utf-8", errors="replace")
        except Exception:
            pass

# 场景字段：id（稳定唯一）/ cat（类别，9 类之一）/ title / layers（L1/L2/L3 关联）/
# steps（人工步骤，逐条可执行）/ expect（期望入站形态要点，diff.py 判读用）
SCENARIOS = [
    # ---- 1. 模型别名四档 ----
    {
        "id": "s01a-opus", "cat": "模型别名四档", "title": "opus 档别名", "layers": "L1+L2",
        "steps": [
            "建独立 scratch 目录，其 .claude/settings.json 写 env.ANTHROPIC_BASE_URL 指向被测路径（链路：被测路径→tap 15723→上游）",
            "在该目录跑 claude -p --model opus \"只回复两个字：收到\"",
            "从 tap 捕获目录取本发 *_req*.json 与 *_resp*.json 各一份",
        ],
        "expect": [
            "body.model=claude-opus-5（CC 把别名展开为 claude-* 全名再发）",
            "anthropic-beta 头含 claude-code-20250219；stream=true",
            "resp usage 四列非全 0 且 usage_source=message_delta",
        ],
    },
    {
        "id": "s01b-fable", "cat": "模型别名四档", "title": "fable 档别名", "layers": "L1+L2",
        "steps": [
            "同 s01a 的 scratch 目录",
            "跑 claude -p --model fable \"只回复两个字：收到\"",
            "取本发捕获",
        ],
        "expect": ["body.model=claude-fable-5-1", "其余同 s01a"],
    },
    {
        "id": "s01c-sonnet", "cat": "模型别名四档", "title": "sonnet 档别名", "layers": "L1+L2",
        "steps": ["同 s01a 的 scratch 目录", "跑 claude -p --model sonnet \"只回复两个字：收到\"", "取本发捕获"],
        "expect": ["body.model=claude-sonnet-5", "其余同 s01a"],
    },
    {
        "id": "s01d-haiku", "cat": "模型别名四档", "title": "haiku 档别名", "layers": "L1+L2",
        "steps": ["同 s01a 的 scratch 目录", "跑 claude -p --model haiku \"只回复两个字：收到\"", "取本发捕获"],
        "expect": ["body.model=claude-haiku-4-5", "其余同 s01a"],
    },
    # ---- 2. 子代理真名 ----
    {
        "id": "s02-subagent-true-name", "cat": "子代理真名", "title": "子代理模型真名直发", "layers": "L1+L2",
        "steps": [
            "scratch 目录 settings.json 加 env.CLAUDE_CODE_SUBAGENT_MODEL=glm-5.3-flash[1M]",
            "跑 claude -p \"用 Explore 子代理列出 docs 下的文件名\"（派发一个真子代理）",
            "在 tap 捕获中找出 body.model 为真名（glm-5.3-flash…）的那发请求（子代理请求，与主会话请求 model 不同）",
        ],
        "expect": [
            "body.model 原样含真名与 [1M] 后缀（子代理真名直发，不经别名展开）",
            "改写路径须精确保留真名：剥 [1M] 后匹配映射表已知真名则透传，diff 时不得映射成别名档",
        ],
    },
    # ---- 3. [1M] 大小写变体 ----
    {
        "id": "s03a-onem-upper", "cat": "[1M] 大小写变体", "title": "[1M] 大写后缀", "layers": "L1",
        "steps": [
            "scratch 目录 settings.json env.ANTHROPIC_DEFAULT_SONNET_MODEL=claude-sonnet-5[1M]",
            "跑 claude -p \"只回复两个字：收到\"",
            "取本发捕获",
        ],
        "expect": ["入站 body.model 逐字保留 claude-sonnet-5[1M]（大写原样）", "改写路径应剥后缀且大小写不敏感；透传路径原样"],
    },
    {
        "id": "s03b-onem-lower", "cat": "[1M] 大小写变体", "title": "[1m] 小写后缀", "layers": "L1",
        "steps": [
            "同 s03a，但后缀改小写 claude-sonnet-5[1m]",
            "跑一发并取捕获",
        ],
        "expect": ["入站 body.model 逐字保留 claude-sonnet-5[1m]", "改写路径同样剥除（大小写变体兼容）；透传路径原样"],
    },
    # ---- 4. 多轮 ----
    {
        "id": "s04-multi-turn", "cat": "多轮", "title": "同会话多轮对话", "layers": "L1",
        "steps": [
            "scratch 目录跑 claude -p \"记住暗号：芒果\"",
            "同目录跑 claude -p --continue \"暗号是什么\"（同会话第二轮）",
            "取第二轮捕获（messages 最多的那发）",
        ],
        "expect": [
            "messages 为多条且 role 交替（user/assistant/user…）",
            "assistant 历史消息含 text block——多轮 wire 样本（此前从未实测）本次补齐归档",
        ],
    },
    # ---- 5. 工具往返 ----
    {
        "id": "s05-tool-roundtrip", "cat": "工具往返", "title": "tool_use/tool_result 往返", "layers": "L1",
        "steps": [
            "scratch 目录跑 claude -p \"用 Bash 工具执行 echo hi 并告诉我输出\"",
            "等工具执行完拿到底答后，取工具往返相关捕获（通常不止一发：发起与回填各一发）",
        ],
        "expect": [
            "某条 assistant 消息含 type=tool_use block；其后 user 消息含 type=tool_result block",
            "diff.py 消息摘要维度：两消息的 blocks type 计数分别含 tool_use / tool_result 非零",
        ],
    },
    # ---- 6. 图片块 ----
    {
        "id": "s06-image", "cat": "图片块", "title": "图片消息入站形态", "layers": "L1",
        "steps": [
            "准备一张小 png（几十 KB 级即可）",
            "scratch 目录跑 claude -p \"这张图里主要是什么颜色\" 并附上该图（或先 Read 该图片再问）",
            "取含图那发捕获",
        ],
        "expect": [
            "user 消息含 type=image block（source.type=base64）",
            "这是 text-only 降级路径的入站形态锚点（GLM-5.3 在 text_only 名单，改写路径应降级为文本占位）",
        ],
    },
    # ---- 7. 41k+ 长前缀 ----
    {
        "id": "s07-long-prefix-41k", "cat": "41k+ 长前缀", "title": "长前缀构造与 cacheRead 探针", "layers": "L1+L2",
        "steps": [
            "q14s3 造臂法：scratch 目录让 CC 读数个大文件凑 41k+ token 前缀（claude -p 一次长任务）",
            "取该会话体最大的一发捕获作样本",
            "L2 阶段：同前缀分别在两条被测路径重放（replay.py -a/-b），对齐 cacheRead",
        ],
        "expect": [
            "body 序列化体积大（百 KB 级）；system/messages 存在 cache_control 断点",
            "L2 cacheRead 主探针场景：同前缀两条路径的 cache_read_input_tokens 必须一致",
        ],
    },
    # ---- 8. ai-title 小请求（session_id 归属——评审 F3 的 L1 验证项） ----
    {
        "id": "s08-ai-title-attribution", "cat": "ai-title 小请求（session_id 归属）", "title": "后台标题请求归属实测", "layers": "L1",
        "steps": [
            "正常会话跑起来，等 CC 自动生成会话标题（或主动换话题触发一次）",
            "在 tap 捕获目录筛小请求：body.max_tokens 明显小（几十至数百）、model 为 haiku 档、messages 仅 1 条",
            "对比该请求 X-Claude-Code-Session-Id 头与 body.metadata.user_id 内 session_id 是否与主会话相同",
            "把结论记入验证报告：辅助请求 session_id 与主会话【相同/不同】",
        ],
        "expect": [
            "归属结论二选一且都要归档：相同→largest-wins 主快照选取天然排除小请求（小请求体远小于主对话）；不同→快照按会话分桶天然隔离",
            "评审 F3 的 L1 验证项：主快照策略不依赖此结论，但必须实测归档",
            "该请求 model=haiku 档、max_tokens 小、单条 user 消息",
        ],
    },
    # ---- 9. count_tokens 存在性 ----
    {
        "id": "s09-count-tokens", "cat": "count_tokens 存在性", "title": "count_tokens 端点存在性实测", "layers": "L1",
        "steps": [
            "全场景跑完后，在 tap 捕获目录查 path=/v1/messages/count_tokens（或非 /v1/messages 的其他路径）",
            "若存在：单独取其捕获记录入站形状",
        ],
        "expect": [
            "记录存在与否（存在性待实测确认）",
            "若存在：记录其入站形状（是否含 model 字段）；处置规则——非 messages 路径一律透传、含 model 则同模型映射——以此实测为准",
        ],
    },
]


def categories():
    seen = []
    for s in SCENARIOS:
        if s["cat"] not in seen:
            seen.append(s["cat"])
    return seen


def entry_text(s):
    lines = ["  %s  %s  [%s]" % (s["id"], s["title"], s["layers"])]
    lines.append("    步骤:")
    for i, st in enumerate(s["steps"], 1):
        lines.append("      %d. %s" % (i, st))
    lines.append("    期望入站形态:")
    for e in s["expect"]:
        lines.append("      - %s" % e)
    return lines


def print_list():
    cats = categories()
    print("router-fidelity 场景矩阵：%d 类 / %d 条（只列不跑——真实流量是人工验证阶段）" % (
        len(cats), len(SCENARIOS)))
    print("=" * 72)
    for c in cats:
        print("[%s]" % c)
        for s in SCENARIOS:
            if s["cat"] == c:
                print("\n".join(entry_text(s)))
        print("-" * 72)
    return 0


def selftest():
    assert SCENARIOS, "自测失败：矩阵为空"
    cats = categories()
    assert len(cats) == 9, "自测失败：应 9 类场景，实得 %d 类 %r" % (len(cats), cats)
    ids = [s["id"] for s in SCENARIOS]
    assert len(ids) == len(set(ids)), "自测失败：场景 id 重复"
    for s in SCENARIOS:
        for k in ("id", "cat", "title", "layers", "steps", "expect"):
            assert s.get(k), "自测失败：%s 缺字段 %s" % (s.get("id"), k)
        assert isinstance(s["steps"], list) and len(s["steps"]) >= 2, \
            "自测失败：%s 步骤应>=2 条" % s["id"]
        assert isinstance(s["expect"], list) and s["expect"], "自测失败：%s 期望为空" % s["id"]
    # 票 10 硬性项：ai-title 场景必须含 session_id 归属步骤（评审 F3 的 L1 验证项）
    s8 = [s for s in SCENARIOS if s["cat"].startswith("ai-title")]
    assert s8, "自测失败：缺 ai-title 场景"
    blob = json.dumps(s8, ensure_ascii=False)
    assert "session_id" in blob, "自测失败：ai-title 场景须含 session_id 归属步骤"
    print("scenarios.py selftest: 9 类 %d 条全过" % len(SCENARIOS))
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description="场景矩阵清单（只列不跑真 CC）")
    ap.add_argument("--list", action="store_true", help="输出全矩阵")
    ap.add_argument("--json", action="store_true", help="矩阵 JSON 输出")
    ap.add_argument("--selftest", action="store_true", help="内置自测")
    args = ap.parse_args(argv)
    if args.selftest:
        return selftest()
    if args.json:
        print(json.dumps({"categories": categories(), "scenarios": SCENARIOS},
                         ensure_ascii=False, indent=2))
        return 0
    return print_list()


if __name__ == "__main__":
    sys.exit(main())
