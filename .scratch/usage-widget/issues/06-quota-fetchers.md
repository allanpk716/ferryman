# 票 06 · 余量查询器 ×3（GLM/Kimi/DeepSeek）

> **状态（2026-09-25 夜）**：已完成——internal/quota/（glm.go/kimi.go/deepseek.go + quota_test.go）
> 验收标准六分支全覆盖、零真实外呼、错误串防钥/URL 断言在位。真机干跑实证：
> 智谱 max 档实回 TIME_LIMIT(unit:5)+TOKENS_LIMIT(unit:3)，**无 unit:6 周窗**（解析按
> cc-switch 先例降级单环）；level="max"。阻塞条件已解除：渡口多上游已入 main；
> observe 周约束由用户 2026-09-25「本机部署余额显示」明示指令取代（改动纯增量，
> 不触闸门/摆渡热路径）。

## What to build

Ferryman 本体内每上游一个余量查询器（独立 `internal/quota/` 包或 dock 内子模块，实施期定）：

- **GLM**：`GET {open.bigmodel.cn|api.z.ai}/api/monitor/usage/quota/limit`，鉴权=**裸 key（Authorization 头不带 Bearer）**；`unit:3`=5h、`unit:6`=周（唯一分类锚，禁按 nextResetTime 排序——周期末尾必标反，cc-switch #3036）；`type` 大小写不敏感兼容 TOKENS_LIMIT/CREDIT_LIMIT；`percentage`=已用%（剩余=100−p，端点无绝对值）；`nextResetTime` 毫秒→ISO；老套餐单条降级单环；信封有无 data 两形状。
- **Kimi**：`GET api.kimi.com/coding/v1/usages`（Bearer）；顶层 `usage`=周窗、`limits[].detail`=5h；**口径漂移防御**：一律 remaining/limit 归一化，limit≈100 整数疑百分比口径不展示绝对数；resetTime 多格式兼容。
- **DeepSeek**：`GET api.deepseek.com/user/balance`（Bearer）；金额字符串字面量（json.Number 语义，照 balance.go）；total=granted+topped_up 拆分；is_available。

公共纪律：5s 墙钟超时；错误只出类别（未配置/网络错误/超时/上游状态 NNN/解析失败），**永不携带 api_key 与 URL 原文**；解析宽容（未文档化端点）。

## 验收标准

- [ ] table-driven 单测夹具覆盖发现六全部分支：GLM 双窗/老套餐单条/CREDIT 两态/大小写/毫秒时间戳/无 data 信封；Kimi usage+limits/limit≈100 口径/resetTime 两格式；DS 三金额拆分/is_available false
- [ ] 测试用 httptest 假端点，零真实外呼
- [ ] 错误类别断言不含钥/URL（含 net/url.Error 包裹文本防御）
- [ ] 结构即白名单：只取所需字段，其余不落（照 BalanceInfo 先例）

## Blocked by

**渡口多上游 worktree 合并 + observe 周满（2026-09-27）**——两边会改同一批配置解析文件；observe 周内不动生产主 exe

## 涉及路径

- internal/quota/（新）或 internal/dock/
- 对应 *_test.go

## 副作用声明

- 独占验证命令：`go test ./internal/quota/...`（或所在包）
- 与渡口多上游的配置结构（[dock.upstreams.<名>]）耦合：以合并后代码为准

decision_refs: 笔记发现六；cc-switch coding_plan.rs/balance.rs（参考实现，禁止猜测）
review_blocks: 有（动主程序）
