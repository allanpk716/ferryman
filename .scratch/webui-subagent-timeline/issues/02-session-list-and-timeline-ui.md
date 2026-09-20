# 02 · 列表页与呈现：标题兜底链＋tokens 主/子内联拆分＋时序页泳道+甘特（D 形态）

## What to build

1. **列表页标题兜底链**（cmd/ferryman/web/app.js renderList，纯前端）：`s.title` → `项目尾段 · agent · MM-DD`（project/agents 已在响应中）→ project 也缺时 `agent · MM-DD · <uuid前8>`（uuid 取 lineage_id 尾段文件名）。修复现状"无 ai-title 会话显示 lineage 前 24 字符"= 文件路径截断（用户实测三条 case：`.claude/projects/…`、`.codex/sessions/…`、`appdata/roaming/orca/…`——分别为 CC、主目录 codex、Orca runtime codex 会话，均为真实被守望会话，**不过滤**）。
2. **tokens 主/子内联拆分**（internal/viewer/ledger Summarize + app.js）：usage 行带子标记（票 01）→ Summarize 拆主/子两个 token 桶；口径不动（input+cache_read+cache_creation，**不含 output**——改口径另立票）；前端单列内联 `120k+45k`，子=0 只显主数；表头改"tokens（主+子）"。
3. **时序页 D 形态**（app.js 时序渲染重排，目标形态见原型 D 变体）：图区分双泳道——上道主会话、下道子代理合计，共用时间轴、各自 y 轴；窗口带/断缓存斜纹/TTL 绿带/事件行贯穿两道；**主会话在子代理跑的同时可能正常对话，主轨必须并行可见**（Q4 定案理由）。下方甘特区逐 agent 一行：条=首末请求跨度、刻点=每次请求、尾标 tokens/时长；点击行在两道打同色参考线。子代理 chip 显隐沿用科目 chips 形态。
4. codex 会话（无 usage 行）下道空置即可，不特殊处理。

## 验收标准

- [ ] 无标题会话按兜底链显示（拿真实账本 107 个无 title lineage 里的 codex/Orca case 抽验，含 project 也缺的 inject-only 行）
- [ ] tokens 拆分与账本手算一致（server 测试：主/子两桶、子=0 会话只显主数）
- [ ] 时序页：主/子请求各归其道、悬停可辨 agentId；甘特行点击参考线落在该 agent 首末请求
- [ ] 既有功能零回归：token/积分视图切换、断缓存跳转、窗口反跑表单、事件图例计数、演示模式 note
- [ ] `go test ./...` 全绿（Summarize/JSON API 测试随拆分更新）

## Blocked by

01（数据面：usage 行带子标记）。

## 涉及路径

- cmd/ferryman/web/app.js（列表页+时序页）
- cmd/ferryman/web/style.css（泳道/甘特样式）
- internal/viewer/ledger/ledger.go（Summarize 拆桶）
- internal/viewer/server/（响应字段，如需）

## 副作用声明

- 前端无自动化测试：验收含手工过一遍真实账本页面（列表页 + 至少一场带等待窗的 CC 会话时序页）。
- 独占验证命令：`go test ./internal/viewer/...`。

decision_refs: 2026-09-20 grill 会话 Q1(a)/Q2(a)/Q4(B+C)/Q5(c)、ADR-0008
review_blocks: 无
原型: .scratch/webui-subagent-timeline/prototype.html（变体 D=定稿；A/B/C 为落选方案留档）
