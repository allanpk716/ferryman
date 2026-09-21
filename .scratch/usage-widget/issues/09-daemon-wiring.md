# 票 09 · 壳 ↔ 真数据接线

## What to build

widget 从内嵌演示数据切到真端点：daemon 端点发现与凭据（`~/ferryman/daemon.token` 读取 + 127.0.0.1:7311，失败走灰化态）；30s 轮询 + 从收起恢复即拉 + 网络错误退避；倒计时 ticker 基准从演示 generated_at 切真实时钟；估算/查询分标端到端核对；契约 version 协商：version 高于预期或出现未知字段 → 优雅降级（忽略未知、缺字段按"无数据"渲染），不崩。

## 验收标准

- [ ] 三家真实数据显示，5h/周/倒计时/余额与 cc-switch 面板人工对账一致
- [ ] 台账月度/花费与 /report 口径人工对账一致
- [ ] daemon 停止 → 整体灰化+⚠；重启 → 自动恢复
- [ ] 喂超集契约 JSON（模拟未来版本新字段）→ 不崩、未知字段忽略
- [ ] daemon.token 变更（重启主程序）→ 下次轮询自动用新 token
- [ ] 悬浮窗进程内存中不落服务商凭据（只有 daemon 的本机 token）

## Blocked by

05, 08

## 涉及路径

- widget/ui/**、widget/src-tauri/src/**

## 副作用声明

- 独占验证命令：Playwright 断言（mock 注入）+ 真实 daemon 手工对账清单
- 对账记录写进本票或笔记（数字留痕）

decision_refs: spec 错误语义/刷新缓存节
review_blocks: 无
