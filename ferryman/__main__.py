"""ferryman CLI 入口（子命令随开发推进逐步补齐：serve/install/doctor/restore）。"""

from __future__ import annotations

import argparse


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="ferryman",
        description="摆渡人：会话闲置缓存失效后的自动交接守护进程（守望→摆渡→闸门→归还）",
    )
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("e0", help="E0a：CC/GLM 缓存 TTL 全量实测，出 reports/e0a-cc-glm.md")
    sub.add_parser("e0b", help="E0b：Codex 缓存 TTL 实测，出 reports/e0b-codex.md")
    e0c_p = sub.add_parser("e0c", help="E0c：CC 子代理 token 记账实验，"
                                       "出 reports/e0c-cc-subagent-tokens.md")
    e0c_p.add_argument("--projects", default=None,
                       help="会话根目录（默认 ~/.claude/projects）")
    e0c_p.add_argument("--out", default=None,
                       help="报告输出路径（默认 reports/e0c-cc-subagent-tokens.md）")
    e0c_p.add_argument("--recon", default=None,
                       help="对账手记 jsonl 路径（可选；缺省 Q3 记“无样本”）")
    e0c_p.add_argument("--limit", type=int, default=None,
                       help="只扫前 N 个项目目录（真实数据冒烟用）")
    sub.add_parser("eval-set", help="E1：构建评测集（分层抽样+注入样本+问答基准）")
    eval_p = sub.add_parser("eval", help="E1：跑候选模型评测（三层标准+耗时）")
    eval_p.add_argument("--provider", required=True,
                        help="provider 名（~/ferryman/config.toml 的 [providers.X]，见 config.example.toml）")
    eval_p.add_argument("--limit", type=int, default=None, help="只跑前 N 个会话（冒烟用）")
    eval_p.add_argument("--no-qa", action="store_true", help="跳过③端到端续接")
    eval_p.add_argument("--regrade", action="store_true",
                        help="复用已落盘的交接产物，只重跑①③校验（不调摆渡模型）")
    serve_p = sub.add_parser("serve", help="启动守护进程（守望+摆渡+闸门+归还）")
    serve_p.add_argument("--smoke", action="store_true",
                         help="放宽阈值差≥120s 校验（秒级阈值冒烟用）")
    sub.add_parser("install-cc", help="把 Ferryman 钩子追加进 ~/.claude/settings.json"
                                     "（检测到 CC Switch 时自动注入供应商快照）")
    sub.add_parser("install-ccswitch", help="把 Ferryman 钩子注进 CC Switch 全部 claude 供应商快照"
                                            "（新增供应商后重跑；幂等、自动备份）")
    sub.add_parser("install-codex", help="把 Ferryman 钩子注进 ~/.codex/hooks.json 并开 "
                                          "[features] hooks = true（改后需 TUI /hooks 信任）")
    sub.add_parser("doctor", help="一键体检：钩子在位/脚本健康/快照覆盖/daemon 活性")
    acc_p = sub.add_parser("account", help="费用账本：流水查询与成效账")
    acc_sub = acc_p.add_subparsers(dest="acct_cmd", required=True)
    rep = acc_sub.add_parser("report", help="族系账单+净节省+策略对比（附复算附录）")
    rep.add_argument("--since", help="起始日 YYYY-MM-DD（本地时区）")
    rep.add_argument("--until", help="截止日 YYYY-MM-DD（本地时区）")
    rep.add_argument("--project")
    rep.add_argument("--session")
    rep.add_argument("--kind", help="handoff|beat|block|inject|bypass|window")
    rep.add_argument("--provider", help="block 侧经济价格表键（缺省=ferry provider）")
    rep.add_argument("--json", action="store_true")
    args = parser.parse_args(argv)

    if args.cmd == "e0":
        from .e0 import run

        return run()
    if args.cmd == "e0b":
        from .e0b import run

        return run()
    if args.cmd == "e0c":
        from .e0c import run as e0c_run

        return e0c_run(args.projects, args.out, args.recon, args.limit)
    if args.cmd == "eval-set":
        from .eval_set import run

        return run()
    if args.cmd == "eval":
        from .eval import run as eval_run

        return eval_run(args.provider, args.limit,
                        with_qa=not args.no_qa, regrade=args.regrade)
    if args.cmd == "serve":
        from .daemon import serve

        return serve(relax_min_gap=args.smoke)
    if args.cmd == "install-cc":
        from .install import install_cc

        return install_cc()
    if args.cmd == "install-ccswitch":
        from .install import inject_ccswitch

        n = inject_ccswitch()
        return 0 if n >= 0 else 1
    if args.cmd == "install-codex":
        from .install import install_codex

        return 0 if install_codex() >= 0 else 1
    if args.cmd == "doctor":
        from .doctor import run_doctor

        return run_doctor()
    if args.cmd == "account":
        from . import report

        return report.run(args)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
