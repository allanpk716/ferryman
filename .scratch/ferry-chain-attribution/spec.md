# Spec · 摆渡链归因修复与多级备用（ferry-chain-attribution）

> 来源：`.xcheck/20261001-071758/proposal.rev1.md`（评审收敛版快照）+ 同链 decisions.md（D1-D8）/ FINDINGS.md。术语遵守根目录 CONTEXT.md 词汇表。

## Problem Statement

生产环境的交接叙事链只剩骨架兜底：同模型档因渡口快照无法按会话归因而 29/29 瞬时失败（追加重放取不到前缀快照）；第三方档唯一供应商（本地 Qwen）因 GPU 转训练而下线；心跳保活与追加重放共用同一前缀源，同样从未跳动。用户通宵长任务早晨拿到的交接将只剩骨架（确定性骨架，无模型叙事）。

## Solution（用户视角）

渡口把每个 Claude Code 请求正确归因到会话，同模型摆渡与心跳保活两条线复活；第三方摆渡从单点供应商升级为多级顺位链（本地 → 智谱 flash → Kimi k3 → 骨架），任一级失败自动滑落到下一级并推送告警、逐次记账；云端顺位启用前有盲评把关；术语表同步真实链形。

## User Stories

1. 作为通宵跑长任务的用户，我想要本地摆渡模型不可用时交接自动由云端备胎生成，以便早晨拿到的仍是完整叙事交接而不是骨架。
2. 作为用户，我想要每次链路降级收到告警且账本可查，以便知道夜里发生过什么、花的什么钱。
3. 作为 Ferryman 维护者，我想要渡口把 CC 请求正确归因到会话，以便同模型摆渡与心跳保活真正生效。
4. 作为 Ferryman 维护者，我想要云端备胎启用前有盲评把关，以便叙事质量不因换模型而无声滑坡。
5. 作为查阅术语表的人，我想要「摆渡优先序」条目反映真实链形，以便文档与生产一致。

## Implementation Decisions

### A. 渡口归因（项 1）

- 会话提取规则：请求体 `metadata.session_id` 第一优先；缺失时回落读 HTTP 头 `X-Claude-Code-Session-Id`。两处同缝生效：快照捕获（主目标，救活追加重放/心跳的前缀源）与渡口记账归因（账本 kind=dock 行的 session_id 列）。
- 防御（评审建议采纳）：头值须通过 UUID 格式校验，不合格式视为缺失；头与体并存时以体为准，冲突留一行日志。
- 无头且无体 → 维持现有空会话语义（跳过/空值），**绝不伪造会话 ID**。
- 判热时钟**零改动**：其 feed 在守望侧转录 usage 采集（已定案，见 Further Notes）。
- 重放/心跳发送侧（评审建议采纳）：Ferryman 自发的重放与追加重放请求带 `X-Claude-Code-Session-Id` 头（值=目标会话 ID），使其渡口记账成本行可按会话归集。

### B. 供应商表与链配置（项 2 前半）

- 供应商表条目（`[providers.*]`）新增可选键：`protocol`（`openai` 默认 | `anthropic`）与 `extra_body`（逐键并入请求体的透传字典，用于供应商特异参数如 thinking 开关）。
- 新增两条云端条目（凭据复用渡口既有配置，零新增披露面）：
  - 智谱 flash：OpenAI 兼容端点 `https://open.bigmodel.cn/api/paas/v4`，model `glm-5.3-flash`，key 同渡口智谱上游。
  - Kimi k3：Anthropic 协议端点 `https://api.kimi.com/coding/`（渡口生产每天在用的同一端点与 key），model `k3`。
- 摆渡主键从单 provider 变为顺位链：`[ferry] chain = ["local", "zhipu-flash", "kimi-k3"]`（保留 `provider` 单键向后兼容=单元素链）。
- Anthropic 协议适配器：非流式 `/v1/messages`，`x-api-key` + `anthropic-version` 头，取 text 内容块拼接（thinking 块忽略），usage 映射 input/output 两列；`extra_body` 逐键并入请求体。

### C. 链执行器与失败语义（项 2 后半，评审 F5 契约）

- 按顺位逐级尝试，**每级单次尝试、零重试**——滑落到下级即重试哲学；本地级拨号超时 5 秒+单次尝试（既有确认语义），云端级总时限沿用现有 context 超时语义按供应商配置。
- 失败判定（任一即该级失败、滑落）：连接失败/超时；HTTP ≥400；响应解析失败；输出为空；输出未通过交接结构校验（与同模型档产物结构校验同款纪律）。
- 成功判定：输出非空且结构校验通过。
- 记账：每次尝试一行（provider、lane 三档语义不变、outcome、wall_s、token 列、顺位序号），降级轨迹账本可重放。
- 告警：每次从某级滑落到下级推一条（既有 Pushover/Toast 通道，文案含从哪级到哪级）；滑入骨架按进程生命周期只告警一次（评审建议采纳的去重口径）。
- 链尾骨架行为不变（对闸门有效）。

### D. 盲评工具（项 3）

- 提供 CLI 子命令：给定供应商名与样本数 N，取最近 N 份交接的骨架素材，逐样本生成叙事并连同骨架落盘到指定目录，供盲评对照。盲评执行（人工评分）不在本链范围。

### E. 术语表（项 4）

- 改写 CONTEXT.md「摆渡优先序」条目为真实链形（同模型档 → 本地 → 智谱 flash → Kimi k3 → 骨架），含训练期紧时限、降级告警与逐次记账语义；相关条目（心跳、等待窗口）不动。

## Testing Decisions

- 只测外部行为：归因用「体有/体无头有/都无/头格式坏/头体冲突」矩阵的单测钉死；协议适配器与链执行器用 httptest 假上游按协议回放（含 4xx/5xx/空输出/坏 JSON/结构不合五类失败）；链失败语义用「杀掉第 N 级→第 N+1 级产出」的顺序断言；记账断言行数与字段；告警去重断言单进程一次。遵循仓库既有同款测试先例（表格驱动+httptest）。

## Out of Scope

- 项 5（长会话保活/放任的统计分析）——挂起待数据。
- 闸门阈值、闲置定义、心跳适用边界、判热时钟——全部不动。
- DS flash 4.1 / aihubmix 配置——不配。
- 生产 `~/ferryman/config.toml` 的实际切换与云端顺位上线——部署期人工动作（配置样例见 Further Notes）。
- 盲评的人工评分环节本身。

## Further Notes

- 判热时钟 feed 已经查证定案（守望侧转录 usage 采集喂钟，与渡口归因无关、一直正常）——实施中不得按旧表述去改渡口侧时钟（评审 F7 的一致性提醒）。
- 生产配置切换样例（部署期用，不入库）：
  ```toml
  [ferry]
  chain = ["local", "zhipu-flash", "kimi-k3"]

  [providers.zhipu-flash]
  base_url = "https://open.bigmodel.cn/api/paas/v4"
  model = "glm-5.3-flash"
  api_key = "<渡口智谱同 key>"

  [providers.kimi-k3]
  base_url = "https://api.kimi.com/coding/"
  model = "k3"
  api_key = "<渡口 kimi 同 key>"
  protocol = "anthropic"
  ```
- GLM 生成历史经验要求关 thinking（同模型档教训）：需要时经 `extra_body` 配置，不在代码里硬编码。
- 头值与台账会话 ID 的等值性已实证（捕获件=转录文件名=台账三处 join）。
- 骨架告警去重口径：按进程生命周期一次（评审建议）。
