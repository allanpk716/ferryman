# 票 06 · doctor 吸收 L0/L1 常驻检查项＋gate-dsh 脚本入清单

## What to build

`ferryman doctor`（internal/installer/doctor.go，CLI 与 MCP doctor 共用 doctorResults）新增 dsh 检查项：

1. `dsh_plugin_static`：调 internal/dshverify 的 L0 检查（票 02 包），三 profile 汇总（任一 profile 有 fail → 该项 warn/fail，detail 按 profile 列）；生产根显式传入。
2. `dsh_poll_age`：经 daemon 管理口 GET /dsh/health（票 01）读全局 poll 年龄＋宿主旁证：超龄且宿主在跑 → fail（"插件失联"）；超龄且宿主未运行 → warn（"宿主未运行，无法判定"）；未超龄 → ok。daemon 不可达时按既有 daemon_liveness 项的 fail-open 惯例处理（注明"daemon 不可达，跳过"）。
3. `ferryman-gate-dsh.ps1` 补进 doctorScriptNames 七脚本清单（hooks/ferryman-gate-dsh.ps1 已存在但不在清单，doctor.go:782-787）。
4. 检查项计数随配置浮动（现有注释模式 23→26→27→28 的写法照旧更新），--json 输出含新项。
5. 黄灯语义：当前 DSH 版本不在 dshledger 流水 → doctor 输出提示行（黄，不告警）。

## 验收标准
- [ ] doctorResults 表驱动测试：插桩 L0/health 假返回，断言三态（ok/warn"宿主未运行"/fail"失联"）
- [ ] doctorScriptNames 含 ferryman-gate-dsh.ps1，缺文件时报 fail（与其他脚本同款行为）
- [ ] `go test ./internal/installer/ -run Doctor` 绿；`ferryman doctor` 本地跑通（可注入环境）
- [ ] 计数注释与实际一致

## Blocked by
票 01、票 02

## 涉及路径
- internal/installer/doctor.go（doctorResults＋doctorScriptNames）
- internal/installer/doctor_dsh_test.go（新）

## 副作用声明
仅单测；doctor 试跑只读

decision_refs: D6（doctor 吸收）、D10（黄不推）、F5 钉死项
review_blocks: 无
