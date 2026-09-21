# Ferryman — 项目级 Claude 指引

> 术语表见根目录 `CONTEXT.md`;架构决策见 `docs/adr/`。

## 中转 API 问题:先看 cc-switch 的实现,不猜测(2026-09-21 用户令)

遇到中转/代理 API 相关的问题——协议兼容、流式转发、请求体改写、供应商端点的怪癖(quirk)等——**必须先读 cc-switch 的源码,参考它是怎么修复和实现的;禁止凭记忆或推测臆造中转层行为。**

- 仓库:https://github.com/farion1231/cc-switch (Tauri 2 桌面应用,MIT)
- 背景:Ferryman 渡口改为直连各供应商后,原先 cc-switch 中转层修过的兼容坑需自行处理,本条即为此立。
- 查证供应商端点事实时,以各家官方文档为准(模型名换代快,2026 年主力:智谱 glm-5.3 系、Kimi Coding Plan 的 kimi-for-coding/k3 系、DeepSeek 的 deepseek-flash/v4-pro 系)。
