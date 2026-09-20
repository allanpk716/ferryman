# webui 会话列表/时序页改进（2026-09-20 定案）

## 这个战役回答了什么

用户在 webui 会话列表看到三条无意义路径（`c:/users/allan716/appdat`、`.codex`、`.claud`）——
grill 会话查明：**无 ai-title 会话的前端回退显示 lineage_id（转录文件归一路径）前 24 字符**，
三条分别是 Orca runtime codex / 主目录 codex / CC 的真实被守望会话；顺带发现子代理用量
（e0c 实测 47% output）完全不入账、webui 不可见。

## 决策（全部经用户确认）

| # | 问题 | 结论 |
|---|---|---|
| Q1 | 无标题会话标题列 | `项目尾段 · agent · 月日` → `agent · 月日 · uuid前8` 兜底 |
| Q2 | 一次性低流水行（107/314） | 不过滤，修标题即可 |
| Q3 | 子代理可见性 | 采 usage 随父会话入账（ADR-0008）＋UI 分层，两票落地，仅 CC 轨 |
| Q4 | 时序页呈现 | **B+C 组合（D 变体）**：双泳道＋逐 agent 甘特；理由：子代理跑时主会话仍可能正常对话，主轨必须并行可见 |
| Q5 | 列表页 tokens | 单列内联 `120k+45k`；"不含 output"口径不动 |
| Q6 | Orca runtime 副本目录 | 继续守望 |

## 原型 verdict

`prototype.html`（双击打开，底部条或 ←/→ 切换）：**D（泳道+甘特组合）胜出**；
A（同轨双色）、B（双泳道）、C（子代理甘特）为落选方案留档。合成数据——真实账本
还没有子代理 usage 行（票 01 落地前不可能有）。

## 票

- `issues/01-subagent-usage-harvest.md` — 入账（守望分流＋harvest 复合键＋白名单标记字段）
- `issues/02-session-list-and-timeline-ui.md` — 呈现（标题兜底链＋tokens 拆分＋时序页 D 形态），blocked by 01
