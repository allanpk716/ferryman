# 票 08 · /widget/summary 聚合端点

## What to build

daemon 新增只读聚合端点 `GET /widget/summary`（127.0.0.1:7311，复用现有 Bearer token 鉴权）：一次返回契约 v0——`{version, generated_at, upstreams[], handoff{}}`；metric=`{key, remaining_pct|text, abs?, breakdown?, source: fetched|estimated, as_of, resets_at?}`（ISO）；provenance 不传（widget 内置映射；upstreams/handoff/error 结构按 spec 契约 v0 节，rev1）。数据 = 票 06 查询器 + 票 07 台账聚合的拼装。缓存策略：远端余量 5–15 分钟（按上游可配，默认 10；各家限流差异）、台账聚合秒级；单上游失败=该 metric 缺席或 error 类别，**整体不失败**。

## 验收标准

- [ ] golden JSON 测试：version 字段存在、source 枚举仅两值、resets_at 为 ISO、固定输入固定输出
- [ ] 无 token/错 token → 401（与现有端点一致）
- [ ] 错误类别串断言不含 api_key 与 URL 原文
- [ ] 上游单条失败时其余上游正常返回（部分降级不塌）
- [ ] 缓存生效：同一缓存窗口内重复请求零外呼（httptest 计数）
- [ ] 未配置任何上游 → 空数组而非错误

## Blocked by

06, 07

## 涉及路径

- internal/daemon/httpapi.go、queryapi.go（路由+处理器）
- 对应 *_test.go

## 副作用声明

- 独占验证命令：`go test ./internal/daemon/...`
- 只读端点：不写任何状态；与 agent 面（ADR-0009）四格禁区无交集

decision_refs: 报告决策记录 4/8/10；spec 契约 v0
review_blocks: 有（动主程序）
