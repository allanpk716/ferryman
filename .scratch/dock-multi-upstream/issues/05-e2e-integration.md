# 票 05 · 端到端集成验收

## What to build

用集成测试把整条链钉死:httptest 假上游(两条,不同 base_url/密钥/模型名)验证渡口按 active 选目的地与换出站密钥;改写模式验证档位映射(opus/sonnet/haiku→各自模型名、[1M] 剥离、default 兜底);透传模式验证字节保真与 SSE 冲刷不回归;守卫验证上游为本地地址时透传不改写;迁移首启 e2e(临时 config 目录,旧单值→cc-switch 条目+三条预置);`upstream use` 命令链路(注入启停接口)验证写回与失败分支。

## 验收标准

- [ ] 双假上游:切 active 后请求到达新目的地、带新密钥、model 改写为新条目映射值
- [ ] 透传模式:体字节级不变、SSE FlushInterval 即时冲刷行为不回归(既有口径测试全绿)
- [ ] 本地地址上游:守卫透传、不改写
- [ ] 迁移 e2e:旧单值 config 首启产物=cc-switch 回退条目(active)+三条未激活预置;二次启动幂等
- [ ] use 链路(注入):成功写回+健康检查;失败分支如实报错不回滚
- [ ] `go test ./internal/dock/... ./internal/config/... ./cmd/ferryman/...` 全绿

## Blocked by

01, 02

## 涉及路径

- internal/dock/
- internal/config/
- cmd/ferryman/

## 副作用声明

- 独占验证命令:`go test ./internal/dock/... ./internal/config/... ./cmd/ferryman/...`
- 测试内假上游用 httptest,不真实外呼

decision_refs: D1, D2, D3, D10
review_blocks: 无
