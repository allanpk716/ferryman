# 保温归因 v2（warm attribution v2）— 实施规格

> 出处：评审链 20261004-192254 → 20261004-193447（rev1，两轮异构评审，终态=夜间收工）。本规格绑定 rev1 快照与同环 decisions/FINDINGS；术语遵守仓库根 CONTEXT.md（保温／保温动作／保温回合／归因双轴／无效保温）。

## Problem Statement

用户无法回答"保温（同模型摆渡重放 + 心跳）到底赚不赚"：成效账 v1 只记闸门拦截的避免额（2026-09 净省 660 / 2026-10 头四天 2224 智谱积分），而保温泳道的收入（缓存命中省下的差价）没有归因代码、支出（智谱档重放实付）未入账——净节省因此虚高且不可辩护。

## Solution（用户视角）

在 cost_report 查询里新增一节"保温盈亏"：按保温回合结算，给出真兑现节省、白保温无害、亏损三类计数与金额、命中率，以及保温支出（优先按渡口流水实收、回落价书并标注）；净额并入成效账 v2（v1 冻结不动）。纯度量：不改任何运行时行为。

## User Stories

1. 作为本机用户，我想在 cost_report 里看到保温盈亏（收入、支出、净额、命中率），以便决定保温的去留与档位。
2. 作为本机用户，我想让每个保温回合按"是否必需（need）×是否命中（hit）"判类并可追溯，以便理解结论的构成而不是一个总数。
3. 作为本机用户，我想让保温支出按实收记账（含冷重放全价的情形），以便净节省不虚高。
4. 作为本机用户，我想盈亏表按保温动作类型（同模型重放／等待窗心跳／问询心跳）拆分计数与结果，以便样本不足的动作不被总论稀释。
5. 作为维护者，我想 v1 公式输出保持逐字节不变，以便既有消费者（cost_report v1 段、既有测试）不受影响。

## Implementation Decisions

- **模块边界**：全部落在 report 层（internal/report 新增保温归因模块）+ 查询挂接（internal/daemon/query_report.go）；不写新账本科目、不动 daemon 写路径、不动 watcher。
- **公式版本化**：SavingsV2 = v1 语义不动 + 保温两腿；`formula` 字段值升 "v2"；v1 有金测锁定。
- **记账合同（决策密集片段，来自 rev1 方案，照抄为准）**：
  - 数据源分工：真实请求（回归锚点、末次真实写入）＝`usage` 行（cc 轨转录采集、重放结构性不落；dsh 轨事件/转录自带 ts）；重放实付四列＝`dock` 行（一切经渡口请求必落，体 metadata.session_id 归因）；保温动作与前缀＝`handoff`/`beat` 行。
  - 配对合同（成本腿）：匹配键 = session_id 相等 ∧ dock.ts ∈ [handoff.ts−2, handoff.ts+wall_s+90]；命中签名 = |dock.(input+cache_read) − handoff.prompt_tokens| ≤ max(512, 2%×prompt_tokens) ∧ dock.output ≤ 8192；多候选 → 记 `ambiguous` 不硬配单独计数披露（**其成本处置是活动约束，见下**）；零候选 ∧ wall_s=0（预派发失败）→ 成本记 0；零候选 ∧ wall_s>0 → 记 `unpaired` 回落价书推算并标注。
  - 回合切分（收入腿）：回归锚点 = 该 lineage 下一条 `usage` 行；回合起点 = 末次真实写入（usage 行 ts）；终点 ∈ {回归, 拦截(block), 放任过期}；dsh 缺时刻回落入账盖章时刻（方向保守，ADR 注明）。
  - 前缀口径：回合前缀 = 回合内首个保温动作行的 prefix（handoff.prompt_tokens / beat.prefix_tokens）；不可得回落末次真实 usage 行四列之和并标注 fallback。
  - 归因双轴：need = 回归时距回合起点 > 自然 TTL（价书实测 1800s，非 τ）；hit = 回归首发实收 cacheRead ≥ 回合前缀 × 80%。need∧hit 计兑现节省（前缀×(p_in−p_cache)/per）；¬need∧hit 白保温无害单列不计省；¬hit 记亏损（成本照计）。80% 与 TTL 阈值做成可配常量（实现定形态：config 或带注释常量，默认值如上）。
  - 跨月归属：回合收益计入回归所在月；保温动作成本计入发生月。
- **活动约束（F7，阻断，开放）**：ambiguous 分支的成本处置未定义（rev1 只写计数披露）。受影响行为：配对合同成本腿的歧义分支。对应票 **paused(F7)**，其余票不受影响。解除条件：补"ambiguous 成本与 unpaired 同处理（价书推算并标注 ambiguous，不冒充实收）"并纳入单测——落盘需用户确认或下次修订。
- **成本计价**：智谱档按配到的 dock 行四列 × [prices.glm] 实价入账；本地 Qwen 档（provider=local）零成本。

## Testing Decisions

- 单元测试（只测外部行为）：回合切分边界（回归/拦截/过期三终型、多跳回合一次结）、双轴判定（含 TTL 边界两侧）、四象限记账、配对合同全条款（匹配键、签名、ambiguous 计数、预派发失败零成本、unpaired 回落标注、**并发窗用例：真实 usage 与重放 dock 同窗不误配**）、前缀口径与 fallback、跨月归属、v1 金测（既有 report_test.go 输出不变）。
- 仓库先例：internal/report/report_test.go 的表驱动+approx 风格。
- 真实账本回放验收：实现后对 C:/Users/allan716/ferryman/accounts/202610.jsonl 跑 cost_report，抽样 3 个保温回合手工核对（核对项含"回合终点确为真实回归而非重放行"）——由协调者在终局评审执行，不拆票。

## Out of Scope

- 任何行为变更（启用等待窗心跳、调 same_model 阈值、动闸门）；
- UI／晨报／widget／工作台；
- 按命中率自动停保温或自动调参；
- dsh enforce 拍板与 [wait_window] 配置补齐、[heartbeat] enabled 死开关清理（等首份盈亏表一并拍板）。

## Further Notes

- 冷重放现象：实测部分会话连续重放 cache_read=0 全价支付（如 9f0e33b5 三连 205k/207k/207k）——保温支出比"缓存读价"毛估更高，正是本票要暴露的成本真相。
- 首份生产盈亏表节点：攒满 50 个保温回合或 2026-10-18（先到为准）；到点附带 ¬need∧hit 占比对 TTL 取值做 sanity-check（F6/F9 留底）。
- 非阻断留底（未获用户采纳，不进任务）：output 上限取配置值的理由注记（F8）；心跳类动作配对条款随启用一并补（F10）。
- ADR-0020 内容：四象限定义、朴素命中判据为何被否（¬need∧hit 不计省的理由）、报表时 join 而非事件时结算、v1 冻结、记账合同与口径全文。
