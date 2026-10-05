# ADR 索引

> 架构决策记录（ADR）总索引。一句话主旨从各篇标题/首段提炼，细节以文件原文为准。
> 撞号说明：0016 与 0018 各有两篇（历史编号重复），文件名不改，以文件名后缀区分；本表用别名注记消歧（见下节）。

## 索引（0001–0022，共 24 篇）

| 编号 | 文件 | 别名 | 一句话主旨 |
|---|---|---|---|
| 0001 | [0001-summarize-threshold-before-block.md](0001-summarize-threshold-before-block.md) | — | 总结阈值恒小于拦截阈值（不倒挂）：被拦即有交接可用，通知时机挪到实际拦截 |
| 0002 | [0002-ledger-facts-only-savings-recomputed.md](0002-ledger-facts-only-savings-recomputed.md) | — | 账本只记事实与实收 token、永不落节省金额；成效账由 report 按版本化公式＋钉死价格表现算 |
| 0003 | [0003-backend-migrate-to-go.md](0003-backend-migrate-to-go.md) | — | 后端整体迁 Go（除前端）：单 exe、公式单源、钩子自举 |
| 0004 | [0004-conditioned-session-keepalive.md](0004-conditioned-session-keepalive.md) | — | 「不保活」红线限缩为禁无条件全局保活；条件化会话级保温（问询守望）放行，默认关、三态永不自动升级 |
| 0005 | [0005-one-shot-cutover-go-rewrite.md](0005-one-shot-cutover-go-rewrite.md) | — | 修订 0003 的验收与切换策略：Go 收编按单场战役一次性整体替换，取消三关烧机 |
| 0006 | [0006-wait-window-keepalive-q14-real-send-gate.md](0006-wait-window-keepalive-q14-real-send-gate.md) | — | 等待窗保温（机器等机器）授权实施：双泳道共用发送器，真发送统一以 Q14 保真实验为放行门槛 |
| 0007 | [0007-heartbeat-prefix-source-capture-replay.md](0007-heartbeat-prefix-source-capture-replay.md) | — | 心跳前缀源定为捕获重放：daemon 透传＋内存快照，废弃与 CC 版本耦合的 jsonl 重构 |
| 0008 | [0008-subagent-usage-into-ledger.md](0008-subagent-usage-into-ledger.md) | — | 子代理用量四列随父会话入账：harvest 从整段排除改为只跳摆渡、不跳采数 |
| 0009 | [0009-agent-surface-mcp-readonly.md](0009-agent-surface-mcp-readonly.md) | — | agent 面：MCP 只读起步（stdio 唯一入口），先划禁区清单与降闸禁手 |
| 0010 | [0010-tag-release-self-update.md](0010-tag-release-self-update.md) | — | 发布通道唯一：tag→Release 自动构建；纯手动自升级与停旧后单次原子替换（update/cutover 不混叫） |
| 0011 | [0011-dock-self-owned-cc-relay.md](0011-dock-self-owned-cc-relay.md) | — | 自建渡口：CC 链路中转改写自持，beat 与真流量同路径；cc-switch 退守 Codex 轨 |
| 0012 | [0012-dock-multi-upstream-direct.md](0012-dock-multi-upstream-direct.md) | — | 渡口多上游直连，cc-switch 退役为回退通道——换上游活在代理内，不改写 CC 配置 |
| 0013 | [0013-content-clock-phantom-state-writes.md](0013-content-clock-phantom-state-writes.md) | — | 内容时钟：摆渡重触发守卫与交接有效性按内容推进判，不再吃无时间戳状态块的幻影 mtime |
| 0014 | [0014-widget-tauri-subproject-quota-in-core.md](0014-widget-tauri-subproject-quota-in-core.md) | — | 悬浮窗＝独立 Tauri 2 子项目（monorepo，只认 HTTP 契约）；三家余量查询收进本体，widget 纯读 |
| 0015 | [0015-locked-image-swap-and-hidden-launch.md](0015-locked-image-swap-and-hidden-launch.md) | — | 同映像多进程下的换装与无窗口拉起：改名让位两步式＋隐藏点火 VBS（第 2 条经 0018-gate 部分修订） |
| 0016 | [0016-same-model-ferry-append-replay.md](0016-same-model-ferry-append-replay.md) | 0016-ferry | 同模型摆渡：捕获前缀追加重放复用渡口通道；判热时钟＝距最后一次上游请求；参数走调参三态 |
| 0016 | [0016-widget-geometry-self-heal.md](0016-widget-geometry-self-heal.md) | 0016-widget | 悬浮窗几何自愈：断言设计尺寸拉回＋位置夹回屏内，不追防系统行为（RDP 塌缩事故） |
| 0017 | [0017-dock-error-contract.md](0017-dock-error-contract.md) | — | 渡口错误契约：响应侧零改写透传；自产错误按流状态分流投递 CC 认识的标准形状 |
| 0018 | [0018-quiet-gate-over-edge-proxy.md](0018-quiet-gate-over-edge-proxy.md) | 0018-gate | 升级停顿对策＝静默门（空载准入停旧）；「任何时刻不拒连」的常驻代理延后（触发式） |
| 0018 | [0018-settings-window-resident.md](0018-settings-window-resident.md) | 0018-settings | 设置窗常驻隐藏：运行时建 WebviewWindow 在真机是渲染树永不建成的僵尸——窗口声明式常驻、关闭即隐藏 |
| 0019 | [0019-takeover-ccswitch-go-translation.md](0019-takeover-ccswitch-go-translation.md) | — | 接管 cc-switch＝渡口当总门：翻译层并入 Go 渡口、不转 Rust，pi 走 anthropic-messages 复用 CC 车道，provider 族为统一配置面 |
| 0020 | [0020-warm-attribution-savings-v2.md](0020-warm-attribution-savings-v2.md) | — | 保温归因 v2：need×hit 四象限判兑现、按保温回合结算、report 层纯度量（v1 冻结并存） |
| 0021 | [0021-workbench-single-desktop-shell.md](0021-workbench-single-desktop-shell.md) | — | 工作台升格：widget 为唯一桌面壳，七视图、mock-first、15900 全套平替后退役；10-05 修订＝设置视图先行分期 |
| 0022 | [0022-settings-view-editable.md](0022-settings-view-editable.md) | — | 设置视图可编辑：写经 daemon 写入器、密钥能存不能看、风险分层、配置快照还原、写盘＋安全重启 |

## 撞号消歧与引用注意

- **0016 ×2**：
  - `0016-same-model-ferry-append-replay.md`（同模型摆渡，别名 0016-ferry）——CONTEXT.md「同模型摆渡/追加重放/调参」节、同模型 spec（`docs/superpowers/specs/20260922-same-model-ferry-spec.md`）、`experiments/same-model-arm/` 引「ADR-0016」均指本篇。
  - `0016-widget-geometry-self-heal.md`（widget 几何自愈，别名 0016-widget）——`widget/src-tauri/src/lib.rs` 注释引「ADR-0016」指本篇。
  - 残留笔误注记：`config.example.toml`、`experiments/append-replay-arm/`、`cmd/ferryman/main.go`、`docs/20260930_服务商接管战役计划.md`、`docs/20260930_渡口翻译车道对照表.md` 中写作「ADR-0015」的同模型摆渡引用**实指 0016-ferry**（旧编号残留），与真 0015（锁定映像换装）无关；改正随后续票处理，本表只注记不改。
- **0018 ×2**：
  - `0018-quiet-gate-over-edge-proxy.md`（静默门，别名 0018-gate）——CONTEXT.md 渡口「空载停机」节、`docs/20260929_升级链可靠性战役计划.md` 引「ADR-0018」指本篇。
  - `0018-settings-window-resident.md`（设置窗常驻，别名 0018-settings）——widget 0.2.4 设置窗修法依据，widget 侧文档引用指本篇。
- **引用建议**：新文档引用撞号 ADR 时用别名（ADR-0016-ferry / ADR-0016-widget / ADR-0018-gate / ADR-0018-settings）或文件名全写，不用裸编号。
