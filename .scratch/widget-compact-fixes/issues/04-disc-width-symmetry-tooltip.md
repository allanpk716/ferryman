# 票 04 · 内容收口对称＋贴边重心补偿＋tooltip（问题 4＋P2）

## What to build
紧凑档 .disc 限宽 60px（面板净宽 68−左右各 4 缝，物理 6/6 整数对称；超宽 mini-tag 走既有 ellipsis）；docked-left/right 时 padding 左右 ±3 重心补偿（内容中心对齐面板可视区中心）；紧凑 tooltip 限宽 64px（不再伸出 80 宽窗口被裁）。

## 验收标准
- [ ] style.css 追加：`body.compact .disc{max-width:60px}`；`body.compact .widget.docked-left{padding-left:1px;padding-right:7px}` 与 docked-right 镜像；`body.compact .tooltip{max-width:64px;font-size:10px;padding:5px 6px}`
- [ ] 断言（?profile=compact，悬浮态）：首/末盘 getBoundingClientRect() 的 left 与 80−right 相等（±0.6）
- [ ] 断言（手动 classList.add('docked-left')）：内容中心−0 ≈ 74−内容中心（±0.6）
- [ ] 断言：hover 触发 tooltip 后 tip.getBoundingClientRect().right ≤ 80
- [ ] 完整档逐字节 diff 断言不回归
- [ ] 断言输出留证写回报

## Blocked by
票 02（同文件路径互斥）

## 涉及路径
widget/ui/style.css
widget/ui/tests/assert-static.mjs

## 副作用声明
node widget/ui/tests/assert-static.mjs（无窗口、无网络）

decision_refs: D1, D4, D5, D8（盘限宽 60 属晨报否决清单）
review_blocks: 无
