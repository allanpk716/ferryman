# 接管 cc-switch：翻译层并入 Go 渡口，不转 Rust

状态:已接受(2026-10-02;cc-switch 替换 spec 定案,决策 D1/D2/D4,底稿 `.scratch/ccswitch-replacement/spec.md`)。

**背景**:cc-switch 是 Tauri 2 桌面应用,Rust 核心约 4.5 万行,双形态并存——本地代理(15721)为 codex 做协议翻译转发,配置切换器(SQLite)改写各家 CLI 配置;代价是每次开机/切档会擦掉 codex 的钩子与旗标(用户须人工三件核查),且 codex/pi 流量不经渡口,费用记账、上游热切换与体检覆盖不到。对面一侧,渡口(15722)经 9-30 服务商接管战役(收官记录 `docs/20260930_服务商接管战役收官记录.md`)已有成套 Go 资产:codex 翻译车道(`internal/dock/codexlane.go` 等,请求向/SSE 响应向/缓存注入全链,约 70 表驱动用例)、provider 外科写入器(apply/`--restore`/同戳成组备份)、doctor 生效链体检、`/provider_switch` 热切换(COW 原子换绑、在途不断流)。翻译这一最难啃的环节已经在 Go 里且在生产跑着——问题只剩"要不要再引一门语言"。

## 决策

接管 cc-switch,渡口当总门;翻译层并入 Go 渡口,不转 Rust。三件:

1. **卸载级替换**(D1/D7):供应商配置管理与协议翻译全部收编进 Ferryman,cc-switch 最终从本机卸载;回退靠 git 与 `provider apply --restore`,不靠 cc-switch。
2. **全 Go**(D2):摆渡核心/渡口代理/协议翻译/pi 写入器全部继续 Go;Rust 仅保留 widget/workbench 的 Tauri 壳。翻译层已是 Go 且在生产;本机回环 I/O 无性能压力,不构成换语言的理由。
3. **pi 走 anthropic-messages 复用 CC 车道**(D4):`provider apply` 第四写入目标把 pi 的供应商条目写成 api="anthropic-messages"+baseUrl=渡口+占位钥,流量复用渡口既有 messages 车道;不为 pi 建 chat completions 入站新车道。`ferryman provider` 命令族(list/switch/add/remove/apply/doctor)由此成为 CC/codex/pi 三类 agent 宿主配置的统一配置面(见 CONTEXT.md「agent 配置面」)。

## 备选对比

- **独立 Rust 翻译服务**(把 cc-switch 的 Rust 代理核剥出来单跑):翻译与本机代理本是一件事,单拆即多一个常驻进程、多一套部署/升级/探活/记账接线——回到两层代理的老问题,cc-switch 时代的中转层正是 ADR-0011/0012 拆进渡口的那层;且响应重建丢 retry-after 这类中转层兼容坑已有 Go 侧对应契约(ADR-0017 响应保真),搬 Rust 等于重付学费。**否决**。
- **daemon 整体转 Rust**:全量重写,soak/灰度/换装演练的全部验证结果作废;2026-09-19 的 Go 一次性切换(ADR-0003/0005)刚清完语言栈债,再翻一次是把同一笔债再借一遍。**否决**。
- **维持 cc-switch 过渡态**:切换器与渡口并存,开机擦钩子事故继续复发,codex/pi 记账与体检永远缺位,与 D1 目标相悖,只是拖延。**否决**。

## Consequences

- cc-switch 进卸载路径:程序卸载、自启清除、开机三件核查退役;`import-ccswitch` 保留为搬家工具,`install-ccswitch` 与 doctor 的 ccswitch_snapshots 检查标弃用、进退役路径。
- 非 anthropic 方言的入站车道(chat completions 入站翻译、pi 的 openai-completions 入站)进远期池;现有两方言(anthropic 入站、openai_responses 翻译/原生透传)已覆盖 CC/codex/pi 供流。
- widget/workbench 的 Tauri 壳仍是 Rust,与本决策不冲突——壳不碰翻译层。
- 翻译车道对 codex/pi 维持供流+记账 only 的能力边界(快照/心跳/摆渡/会话面不扩),扩面另行立票。

## 重开条件

**无。** 本决策不设任何重开触发条件——性能、生态、语言趋势等任何未来论据都不自动触发重评;翻案须立新 ADR 推翻本篇,并说明立项时已否决的论据为何如今成立。
