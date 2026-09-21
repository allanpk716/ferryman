# 票 03 · UI 移植：mock 等效搬进壳

## What to build

把 `docs/research/20260921_悬浮窗mock.html` 移植为壳内正式 UI：去掉演示台 chrome（mockbar 按钮组改为托盘/设置入口；图例并入设置窗的帮助区）；数据层抽离——渲染只吃契约 v0 JSON（内嵌演示副本），建立 30s 轮询框架（先对内嵌数据空转）与 daemon 不可达整体灰化+⚠ 态；倒计时行逐分钟本地 ticker（演示基准=generated_at，接真数据后=真实时钟）；provenance 文案改为 widget 内置映射（kind+key → 固定文案，**不进契约**）；演示数据角标仅在 dev 构建显示。

保留全部已验收语义：环剩余制/基色/告警联动、倒计时行颜色随环、圆心标识、估算紫虚线角标与查询绿实线角标分标、tooltip、详情卡（值/上限/重置时刻+倒计时/最后更新）、横竖切换。

## 验收标准

- [ ] 断言集对 ui/ 静态产物在 headless 浏览器等价通过（圆心/dasharray/环色/倒计时格式/交互六项/控制台零报错，与 mock 同机制）＋ 壳内人工冒烟清单（载入/拖动/托盘/倒计时走字）（rev1 按 F4 改写：壳内自动化系未验证路径，静态产物+人工冒烟与零构建链对齐）
- [ ] 数据层与渲染层分离：换一份契约 JSON 即换全部显示，无散落硬编码
- [ ] daemon 不可达灰化态可演示（dev 开关注入）
- [ ] provenance 映射表覆盖全部 metric key，文案与设计稿一致
- [ ] dev 构建显示"演示数据"角标，release 构建不显示

## Blocked by

01（02 可并行）

## 涉及路径

- widget/ui/**
- widget/src-tauri/src/**（dev/release 区分注入）

## 副作用声明

- 独占验证命令：Playwright 断言脚本（随票提交于 widget/ui/tests/ 或 scripts/）

decision_refs: 报告决策记录 10/11；笔记 Round 3
review_blocks: 无
