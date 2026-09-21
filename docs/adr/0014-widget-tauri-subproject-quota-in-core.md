# 悬浮窗独立 Tauri 子项目，余量查询收进本体

2026-09-21 用量悬浮窗选型调研（`docs/research/20260921_悬浮窗选型评估报告.md`，五项串行 Agent 队轮）查明三条硬事实：五家服务商（智谱/Kimi/DeepSeek/Claude 订阅/中转）无一家提供 token 级周/月用量 API——"用了多少"只能出自本地台账，显示端与数据面天然强耦合；各家余量/配额端点形状、鉴权、解析坑互不相同（且全部"未文档化但稳定"），天然是"每上游一个查询器"的集合；WebView2 系在 Windows 上"透明背景+点击穿透"互斥（Wails #6088），但常驻用量面板不需要穿透，不阻塞。

决定（2026-09-21 grill 定稿）：

- **悬浮窗 = 独立 Tauri 2 子项目**，放本仓 `widget/` 子目录（monorepo 先行，非独立仓库）。同仓理由：通信契约早期必然反复，契约改动须与两侧消费方同 commit 原子落地。三个对冲条件保证将来可拆：①自带 Cargo.toml/package.json/CI（路径过滤），Rust/pnpm 工具链永不进主构建；②**边界铁律：widget 永不 import Ferryman Go 代码，只认 HTTP 契约**——数据口径单源在主程序；③拆分出口写明：`git subtree split widget/` 带全历史迁出。
- **数据分工**：三家（GLM CodingPlan / Kimi Coding Plan / DeepSeek 官方 API）的余量查询器与 5h/周聚合**统一在 Ferryman 本体实现**；悬浮窗纯读消费（daemon 新增只读聚合端点，一次给齐），**永不自持任何服务商凭据**。Claude 订阅行不做（省掉 OAuth 端点限流管理）；中转站暂不涉及。
- **口径分标**：DeepSeek 官方无 usage API，其用量为台账本地口径；查询值与估算值在 UI 上必须区分标注，禁止混排。
- **独立升级线（2026-09-21 补定）**：悬浮窗的升级查询与自升级**在 widget 内完成**，不经 daemon、不由主程序代查代装——Tauri updater 插件 + GitHub Releases 签名 `latest.json`，样板照抄 AntFeedingLog（`C:\WorkSpace\agent\AntFeedingLog` 的 `docs/release.md` 与其 ADR-0001：updater/autostart/single-instance 三插件齐备）。与本仓发布通道（ADR-0010 tag→Release）互不牵动：widget 独立打 tag、独立发版。接口防御随行：聚合端点契约**带版本号字段**，widget 解析对未知字段向前兼容——两侧版本独立漂移时优雅降级而不是崩。
- **节奏**：悬浮窗壳先行（Tauri 骨架+mock 数据+设计稿，零冲突）；数据面等渡口多上游 worktree（`不同LLM供应商`）合并后立项——两边会改同一批配置解析与 /stats 接线文件。

## Considered Options

- **Wails v3 library 挂主 exe**（调研推荐案）：被否——beta 依赖进生产 observe 周的主 exe 不可接受，且用户要悬浮窗后续独立开发节奏。
- **独立仓库**：被否（用户裁定）——契约早期反复期跨仓协调成本高于工具链隔离收益；monorepo+三条件对冲后独立仓库收益仅剩发布节奏，可后补。
- **Electron**：常驻 93-180MB、安装包 150MB+，资源红线出局（其唯一优势"穿透配方最成熟"本场景用不上）。
- **Fyne / WPF / WinUI 3**：Fyne 无 Windows 透明且 HTML 零复用；WPF/WinUI 3 独立 .NET 栈零复用、窗口基础问题未清。

## 后果

双常驻进程（daemon + widget exe），各自托盘；Rust 工具链进仓但不进主构建；契约（聚合端点 JSON 形状）变更必须同 commit 改两侧——这是同仓的初衷，也是它的纪律；本 ADR 与渡口多上游 spec（20260921）在"每上游 balance_url"上衔接：spec 票 03 只保证 active 单条余额，全量上游余量查询是悬浮窗战役在主程序侧的新增面。
