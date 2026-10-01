# spec · 悬浮窗紧凑档视觉修复（widget-compact-fixes）

来源：.xcheck/20260930-234451（评审链 20260930-233210→234451，终态收敛 1 轮修订）；object=proposal.md（rev1）＋同环 decisions/FINDINGS。术语遵守仓库 CONTEXT.md。

## Problem Statement

用量悬浮窗 0.2.6 紧凑档有四个视觉缺陷，用户已逐条点名：圆心百分比数字顶住环弧且 % 符号过大；最底"交接"标签行紧贴面板底边（实际踩在面板外）；顶部拖动把手完全不可见导致"不协调"；贴边停靠时内容与两侧黑边不对称。另有一个顺带发现：紧凑档 tooltip 最大宽超出窗口宽度必然被裁。生产 0.2.6 源码在分支 allanpk716/悬浮窗修复显示问题（本夜链分支即基于其 tip），main 与工作分支尚停在 0.2.1。

## Solution（用户视角）

紧凑档四问题全部消失：百分比不再碰环（含 100% 满格）；"交接"标签行距面板底边留出肉眼舒适的空隙（8 逻辑像素）；面板顶部中央出现一条细胶囊把手（静态淡、悬停变亮，可拖动性可发现）；悬浮与贴边两种状态下内容与面板左右对称。悬停 tooltip 不再被窗口右缘裁切。完整档（132 宽）外观逐字节不变。

## User Stories

1. 作为悬浮窗使用者，我想让圆心百分比不再压住圆环，以便读数时视觉干净。
2. 作为悬浮窗使用者，我想让最底下的标签行与面板底边留出空隙，以便不再有"贴出边界"的难受感。
3. 作为悬浮窗使用者，我想一眼看出面板顶部哪里可以拖动，以便 confidently 移动窗口。
4. 作为悬浮窗使用者，我想让贴边停靠时内容与两侧边距对称，以便窗口看起来居中稳定。
5. 作为悬浮窗使用者，我想让悬停提示完整显示不被裁切，以便看清每个环的数字含义。
6. 作为悬浮窗使用者，我想让满格（100%）的盘也不出现数字压弧，以便任何余量状态下界面都干净。

## Implementation Decisions

- 全部改动限定在紧凑档专属 CSS 选择器（body.compact …）与 app.js 紧凑档 coding_plan 渲染分支；完整档零触碰（D4）。
- 问题 1：% 包 tspan 上标（dy=-4 属性、字号 12）；100% 特判 class c-pct-full（字号 24、省略 %）；追加 CSS 须位于 style.css:179 基础规则之后（同特异性后写覆盖，F4-2 自检）。
- 问题 2：紧凑 .widget padding 改 10px 4px 14px（底距=14−6=8、顶距顺手 8→10）；Rust 零改动（scrollHeight 自适应吃进）。
- 问题 3：grip 字符藏（font-size:0）、::after 画 22×3 胶囊条、hover 提亮；不新增任何 click/dblclick 监听（0.2.3 原生拖动区吞双击红线）。
- 问题 4：body.compact .disc{max-width:60px}＋docked-left/right padding ±3 重心补偿。
- P2：body.compact .tooltip{max-width:64px; font-size:10px; padding:5px 6px}。
- 像素推导全部以逻辑像素给出（150% DPI，1 逻辑=1.5 物理；60×1.5=90 等整数换算优先）。
- 每票改动后窗高经既有 fitHeight 上报链自动收敛（预期序列 235→247→240）。

## Testing Decisions

- 静态：扩展 widget/ui/tests/assert-static.mjs——紧凑渲染产物含 tspan/dy=-4；preset=100 产物含 c-pct-full 且无 tspan；完整档渲染产物与改动前逐字节一致（diff 断言）。
- 运行时几何：Playwright（演示页 ?profile=compact，preset {72,78,100}）——bbox 纵向+横向双向钳位（宽 ≤18.5 逻辑 px）；.widget computed padding-bottom=14/padding-top=10；grip ::after 22×3 与 hover alpha 变化；首末盘 left 与 80−right 对称（±0.6）；手动挂 docked-left 后内容中心对称；tooltip right ≤ 80。
- 真机 PrintWindow 实拍像素断言（150% DPI）作为最终验收（人晨间复核截图）。
- 仓库既有同类先例：assert-static.mjs 的 profile 预设机制、.scratch/usage-widget/pw_verify.py。

## Out of Scope

- 完整档底距/贴边重心同款缺陷（受逐字节保护，单列一票另议，见方案 P3）。
- 横排紧凑档 grip 竖条变体；投影补偿。
- 工作台（mock→实现）任何部分；W0 归并动作本身（由人开 PR 合并完成）。
- 100% 省略 %、padding-top 10、盘限宽 60 三处超用户原话的细化——保留 D8 未决，晨报逐条供否决。

## Further Notes

- 夜链终态＝推分支＋晨报 compare 链接；不自动开 PR、不合并（D9）。
- 落地顺序：02-padding → 04-收口+tooltip → 01-%tspan+特判 → 03-胶囊把手（grip 高度改动放最后避免基线反复）。
- kimi 复审三则自检（F4）：票内顺手落实，不新增票。
