# 票 06 · doctor 吸收 L0 常驻检查项＋gate-dsh 脚本入清单

> **rev 2026-10-08 夜链改票（协调者裁定）**：并行夜链 xcheck-night-20261008-154613（dsh-host-guard）已在 doctor 加 `dsh_poller_sentinel`（宿主插件哨兵检查，消费其 poller_baseline.go 的 per-poller 心跳状态机）。本票**撤销原 `dsh_poll_age` 检查项**（避免同能力两份实现）；L1 的 doctor 呈现由并行链承担，合并两分支时以它为准。本票聚焦我们独有面：L0 静态检查、gate-dsh 脚本清单、版本黄灯提示。

## What to build

`ferryman doctor`（internal/installer/doctor.go，CLI 与 MCP doctor 共用 doctorResults）新增：

1. `dsh_plugin_static`：调 internal/dshverify 的 L0 检查（票 02 包），三 profile 汇总（任一 profile 有 fail → 该项 warn/fail，detail 按 profile 列）；生产根显式传入。daemon 侧插件活性不在本票（见上注）。
2. `ferryman-gate-dsh.ps1` 补进 doctorScriptNames 七脚本清单（hooks/ferryman-gate-dsh.ps1 已存在但不在清单，doctor.go:782-787）。
3. 检查项计数随配置浮动（现有注释模式 23→26→27→28 的写法照旧更新），--json 输出含新项。
4. 黄灯语义：当前 DSH 版本不在 dshledger 流水 → doctor 输出提示行（黄，不告警）。
5. **合并协调注**：代码注释与提交信息注明"与并行链 dsh_poller_sentinel 互补不重复——本项管安装面静态完整性，彼项管挂载活性"。doctor.go 与并行链有文本级合并冲突预期（追加位），属已知、晨报已登记。

## 验收标准
- [ ] doctorResults 表驱动测试：插桩 L0 假返回，断言 ok/warn/fail 三态与 per-profile detail
- [ ] doctorScriptNames 含 ferryman-gate-dsh.ps1，缺文件时报 fail（与其他脚本同款行为）
- [ ] 版本不在流水→黄提示行；在流水→无提示（dshledger 用临时目录夹具）
- [ ] `go test ./internal/installer/ -run Doctor` 绿
- [ ] 计数注释与实际一致；不新增任何 poll 活性检查项（撤销项）

## Blocked by
票 01（/dsh/health 供版本对照用的 daemon 版本读取可走 /stats，无需票01——改为：无硬依赖，但排波次 2 避免与票04 抢 doctor 引用面）、票 02

## 涉及路径
- internal/installer/doctor.go（doctorResults＋doctorScriptNames）
- internal/installer/doctor_dsh_test.go（新）

## 副作用声明
仅单测；doctor 试跑只读

decision_refs: D6（doctor 吸收——L0 面）、D10（黄不推）
review_blocks: 无
