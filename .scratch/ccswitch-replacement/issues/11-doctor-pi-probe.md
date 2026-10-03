# 票11 · doctor 新项 provider_pi_dock(生效链判定)

## What to build
internal/provider 探针增 provider_pi_dock,installer doctor 聚合接入(与 cc/codex/orca 三项同层):①判定与写入器同一套解析(单源,doctor_probe.go);②绿条件=完整生效链:settings.json 的 effective defaultProvider 解析到渡口条目 ∧ defaultModel ∈ 该条目 models ∧ 条目 api="anthropic-messages" ∧ baseUrl=渡口地址;③~/.pi 目录缺失→not_checked(不产红);④"残留旧 15721 条目但生效链正确"→绿+输出附一行警告(非生效残留不阻断,与写入器清理测试分开钉,F9 注记);⑤单测四钉:错默认供应商(渡口条目在但 defaultProvider 指他处)/错模型(defaultModel 不在 models)/残留旧 15721 条目/未装 pi。

## 验收标准
- [ ] 四钉用例全绿:前两红、第三绿+警告、第四 not_checked
- [ ] 判定与票10 写入器同源(共用解析函数,不是第二套判据)
- [ ] CLI doctor 输出含该新项;agent 面 DoctorStructured 同步含
- [ ] go test ./internal/provider/ ./internal/installer/ 绿

## Blocked by
票10(同单源解析先有)

## 涉及路径
internal/provider/doctor_probe.go
internal/provider/doctor_probe_test.go
internal/installer/doctor.go
internal/installer/doctor_test.go

## 副作用声明
无独占验证命令;go test ./internal/provider/ ./internal/installer/

## decision_refs
D16;F1(体检侧)、F8、F9

## review_blocks
无
