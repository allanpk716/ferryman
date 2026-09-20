# 票 03 · /report＋/beats（报表与心跳遥测端点）

## What to build

替换票 01 留下的 /report 与 /beats stub（用户视角：agent 能查账本报表与成效账、能看在飞窗口与心跳遥测）：

1. **GET /report?scope=\<project|session|month\>&key=...**：复用 internal/report 公式单源（**禁止出现第二份公式实现**）——按 scope 聚合四列 token 与金额、成效账（价格表缺缓存价时如实标注"节省额不可算"，不硬算）、bypass 与无效保温单列。scope/key 非法 → 400 JSON。
2. **GET /beats**：在飞等待窗/问询窗清单（会话、窗型、停车标志、预估 close_reason）＋逐窗心跳遥测（跳数、hit/miss/error/observe 计数、TTL 观测值、累计实收花费）；无在飞窗返回空数组。

红线同票 01：只读、无消息内容、无凭据；/stats 契约不动。

## 验收标准

- [ ] /report 三种 scope 各有夹具断言（金额与四列聚合正确）；"节省额不可算"标注路径有夹具（价格表缺 p_cache）
- [ ] bypass 与无效保温在报表中单列，不混入成效账
- [ ] /beats：在飞窗字段齐全（含停车标志）；遥测计数与构造的 beat 科目流水一致；空窗返回空数组
- [ ] scope/key 非法 → 400；无鉴权 → 401
- [ ] 响应无消息内容、无凭据字段（反向断言）
- [ ] go test ./internal/daemon/ -count=1 全绿；gofmt 干净

## Blocked by
票 01

## 涉及路径
- internal/daemon/query_report.go（新建，替换 stub）
- internal/daemon/query_beats.go（新建，替换 stub）
- internal/daemon/query_report_test.go（新建）
- internal/daemon/query_beats_test.go（新建）

## 副作用声明
- 独占验证命令：go test ./internal/daemon/ -count=1

## decision_refs: D2、D5、D6
## review_blocks: 无
