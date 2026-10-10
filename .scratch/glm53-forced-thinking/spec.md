# Spec · GLM-5.3 强制思考适配（glm53-forced-thinking）

> 状态：**已立项、未开发**（2026-10-10 用户确认"同样方式记一张票"）。
> 本文是立项记录＋方案草案；开发前按惯例过一轮 grill 定案，再改写为正式 spec。
> 术语遵守根目录 CONTEXT.md。

## Problem Statement

智谱 GLM-5.3 系（`glm-5.3` / `glm-5.3-flash`）现为**强制思考**模型：请求带 `thinking: {type: "disabled"}` 一律 HTTP 400，业务码 1210，报错原文"该模型始终思考，不支持关闭思考；请使用 low、high 或 max。"官方「深度思考」文档原文："GLM-5.3 GLM-5.3-FLASH 不再支持关闭思考（API 请求中 thinking.type 传 disabled 将会报错），请确保开启思考"。唯一合法形态是 `thinking: {type: "enabled"}`＋顶层 `reasoning_effort ∈ {low, high, max}`，默认 max。

本仓有三处仍按"关思考"写死：

1. **摆渡供应商 zhipu-flash（配置层）**：10-07 盲评定案的 `extra_body = { thinking = { type = "disabled" } }` 已失效 → 每次尝试 400 → 链静默滑到 kimi-k3。滑落告警按顺位状态变化制只推首条（ADR-0026-notify），此后无感。生产机待核。
2. **渡口 codex 车道（代码层）**：`internal/dock/codexlane.go:274-276` 对 GLM anthropic 上游无条件覆盖 `thinking: disabled` → codex 经渡口走 glm-5.3 的请求很可能全挂。生产机待核。
3. **渡口 CC 车道（代码层）**：CC 自身的辅助/工作流请求会发 `thinking: disabled`（cc-switch issue #6737），而渡口改写五件不碰 thinking → 这类请求透传到 glm-5.3 即 400。影响面（具体哪些 CC 功能失效）待核。

另：`config.example.toml` 两处过时：

- `[providers.glm]`（glm-5.3-flash）未设思考强度，按默认 max 跑会吃掉大部分输出额度、交接被截断（实测见下）；
- `[providers.deepseek]` 模型名 `deepseek-v4.1-flash` 是版本名，不是官方模型 ID（应为 `deepseek-flash`）；DeepSeek 默认开思考，需 `extra_body = { thinking = { type = "disabled" } }`（DeepSeek 支持关闭，NUC10 已实测通过）。

## 证据（2026-10-10 NUC10 实测）

| 请求（glm-5.3-flash，真实摆渡 SystemPrompt＋虚构骨架素材） | 结果 |
|---|---|
| `thinking: disabled` | HTTP 400，code 1210 |
| `enabled`＋`reasoning_effort: low`，max_tokens=4096 | finish=stop，reasoning_tokens=0，正文 2364 字，14s |
| `enabled`（默认 max），max_tokens=4096 | **finish=length**，reasoning_tokens=3515/4096，正文被截断，66s |
| `enabled`＋`low`，max_tokens=2048（L2 分块额度） | finish=stop，reasoning_tokens=0，24s |

NUC10 已按 low 修好（`~/ferryman/config.toml`，仓库外），冒烟三家全通。

## Solution（用户视角）

智谱这一级重新可用：摆渡链里的 zhipu-flash 真正写出交接，不再静默滑到 Kimi；codex 经渡口走智谱不再被 400；CC 的辅助请求经渡口也能正常拿到智谱回复。

## 草案决策（开发前定案）

1. **配置层（零代码，先做）**：生产机 `[providers.zhipu-flash]` 改为 `extra_body = { thinking = { type = "enabled" }, reasoning_effort = "low" }`；同步修 `config.example.toml` 上述两处。
2. **渡口改写（代码层）**：新增 thinking 改写属于"改写五件"之外的第六件 → 按 CONTEXT.md 纪律**须先过 ADR**。按仓库 CLAUDE.md 规矩先读 cc-switch 的实现：#6737 / PR #6739 方案为——
   - 仅对模型 ID **精确匹配** `glm-5.3`、`glm-5.3-flash`（不碰 `glm-5.2`、`glm-5.30`、`glm-5.3-flashx` 等近名，不碰第三方聚合方言）；
   - `disabled` / `none` / `off` → `enabled`＋`reasoning_effort: low`；其余强度就近收到 low / high / max；
   - 注意报错是纯中文：按英文 "thinking" 匹配的重试/识别逻辑会漏。
3. **codex 车道**：`codexlane.go:274-276` 的无条件 disabled 覆盖改为按上表映射（与 CC 车道共用一处判定），而不是直接删掉——GLM-5.2 及更早仍可关思考。

## 验收（草案）

- 生产机：账本 handoff 行 provider=zhipu-flash 的 failed 行（err 含 1210）清零、出现 fresh 行；渡口流水无 glm-5.3 的 400/1210。
- 单测：精确匹配表（含近名反例）、各强度映射、GLM-5.2 原样放行。
- 真机冒烟：codex 经渡口一轮对话；CC 辅助请求一次（如会话标题生成）。

## Further Notes

- 发现经过：2026-10-10 NUC10 部署原生 CC，按生产配置给 zhipu-flash 冒烟即 400。
- 来源：智谱官方文档「深度思考」 https://docs.bigmodel.cn/cn/guide/capabilities/thinking ；cc-switch issue https://github.com/farion1231/cc-switch/issues/6737 （修复 PR #6739，2026-10-10 尚未合并）。
- 关联：`docs/20261007_摆渡云端顺位盲评_报告.md`（当时的 disabled 修法）；ADR-0026-notify（滑落告警只推首条，是本问题无感的原因之一）；`docs/20261002_剩余风险与哨兵.md`（风险登记行）。
