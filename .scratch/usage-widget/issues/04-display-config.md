# 票 04 · 显示配置与设置窗

## What to build

设置窗（**按需创建、关闭即销毁**——防双 WebView 常驻内存）：布局（竖排默认/横排）；每对象一套显示配置（display profile）：显示开关、环色（基色可换）、告警阈值（默认黄20/红10）、月预算（GLM/Kimi 可选=紫环分母；DeepSeek 预算环开关+金额=绿环）、顺序（上移下移）、倒计时行开关。持久化到 widget 本地 JSON（tauri path API，路径 `$APPDATA/ferryman-widget/profile.json`）；daemon 零感知零改动。

月预算环与 DS 预算环的启用逻辑：契约只给原始值（月 tok/花费金额），预算在 widget 侧参与分母计算画环——确认口径：环=预算已耗%还是预算剩余%（与主环剩余制一致取**剩余制**）。

## 验收标准

- [ ] 全部配置项改动即时生效并重启保持
- [ ] 设置窗关闭后 WebView 实例销毁（任务管理器无残留 webview 进程树增长）
- [ ] 开/关月预算环：不设=文字计数（现状），设=紫环出现且 caption 保留
- [ ] DS 预算环开→绿环出现（剩余制）
- [ ] daemon 侧无任何文件/端点改动（契约不变）
- [ ] 配置文件损坏/缺失→安全回落默认值并如实提示，不崩

## Blocked by

03

## 涉及路径

- widget/ui/**、widget/src-tauri/src/**

## 副作用声明

- 独占验证命令：Playwright 断言 + 手工实测（设置窗开闭的进程树观察）

decision_refs: ADR-0012；CONTEXT.md 词条"显示配置/月预算/环"
review_blocks: 无
