# 票04 · daemon 两类统计端点(Go)【门控票】

## What to build
端到端行为:widget 成品视图轮询 daemon 即可拿到统计数据——聚合端点返回 KPI(四列+requests+命中率所需的原始两数+可算性标志)与逐日序列(四档所需);明细端点按时间范围/项目/模型过滤并分页返回逐请求行(≤500 行/次)。服务端聚合,绝不整段下发流水。经 internal/daemon/queryapi.go 注册表挂 15700,自动继承 Bearer+仅 127.0.0.1+CORS 白名单。

## 验收标准
- [ ] 聚合端点:响应含 KPI 各字段(口径=spec 聚合数据契约:全时段/usage 行计数/命中率两原始数/成本节省额可算性判定与 daemon /report 同法)
- [ ] 逐日序列:自指定起点(缺省=账本最早日)逐日四列 token/请求数/五类事件计数/可算时的金额;日界本地自然日
- [ ] 明细端点:since/until/project/model 过滤 + limit/offset 分页,单次上限 500;返回行字段与快照明细列一致(ts_iso/project/session_id/model/四列 token/cost 可算时)
- [ ] Go 单测:小账本构造断言聚合/分页/过滤;对拍断言=同参数下端点核心聚合与快照脚本锚点一致(2026-09 五类=10/2/50/778/760;2026-10=67/15/63/442/216)
- [ ] 只跑 touched 包测试(go test ./internal/daemon/... 落日志),用户在场不裸跑全仓
- [ ] 注册表方式与现有端点一致(queryapi.go 分派表),无第二份公式副本(节省计算复用 internal/report 单源)

## Blocked by
**用户对 mock 的过目通过(D11 mock-first 门;解除=用户手机评审通过并明确放行)**;另依赖票01(契约对拍基准)

## 涉及路径
- internal/daemon/(新 query_stats.go + queryapi.go 注册行 + 测试文件)

## 副作用声明
go test(仅 touched 包,输出落日志文件);无端口/部署

decision_refs: D2, D3, D9, D11
review_blocks: 无(F3/F8 已解除,本票为其落地;门控来自 D11 而非开放 F)
