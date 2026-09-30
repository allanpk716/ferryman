# 票 01 · 圆心 % 上标＋100% 特判（问题 1）

## What to build
紧凑档套餐盘圆心读数改为"数字 28 号 + 上标 % 12 号"，100% 满格时输出无 % 的 24 号"100"——任何合法余量（0~100）下圆心文本在纵向与横向都不压环弧。完整档渲染产物逐字节不变。

## 验收标准
- [ ] app.js 紧凑档 coding_plan 分支：`${Math.round(...)}<tspan class="c-pct-pct" dy="-4">%</tspan>`；`=== 100` 时输出 `<text ... class="c-pct c-pct-full">100</text>`（无 tspan）
- [ ] style.css 在 body.compact .c-pct 基础规则（≈行179）之后追加 `body.compact .c-pct-pct{font-size:12px}` 与 `body.compact .c-pct-full{font-size:24px}`（同特异性后写覆盖，F4-2）
- [ ] assert-static.mjs 新断言：紧凑（profile 含预算环预设）渲染产物含 c-pct-pct tspan 且 dy=-4；preset=100 产物含 c-pct-full 且无 tspan；完整档产物与改动前逐字节一致
- [ ] 运行时几何断言（Playwright/chrome dump-dom 或既有 pw 通道，?profile=compact，preset {72,78,100}）：`.c-pct` getBBox() 满足 bbox.y ≥ 50−20.25+2 且 bbox.y+bbox.height ≤ 50+20.25−2 且 bbox.x ≥ 50−20.25+1 且 bbox.x+bbox.width ≤ 50+20.25−1（SVG 单位；横向即宽 ≤18.5 逻辑 px）
- [ ] 断言全绿输出留证（命令+计数）写回报

## Blocked by
无（建议排在票 02/04 落地后做——见票内路径互斥说明）

## 涉及路径
widget/ui/app.js
widget/ui/style.css
widget/ui/tests/assert-static.mjs
widget/ui/tests/（如需新增几何断言脚本文件，放此目录）

## 副作用声明
node widget/ui/tests/assert-static.mjs（只读源码+headless DOM 探针，无窗口、无网络）；chrome --dump-dom headless 探针如既有用法

decision_refs: D1, D4, D5, D8
review_blocks: F1（已解除，本票为解除实现）, F4-2
