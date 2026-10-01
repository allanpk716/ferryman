# 票 02 · 底部 8 逻辑 px＋顶部顺手修（问题 2）

## What to build
紧凑档 .widget padding 从 `8px 4px 4px` 改 `10px 4px 14px`：内容到面板底边距=8 逻辑 px（12 物理），"交接"标签行不再踩面板外透明条；顶部顶距 2→4（顺手修，晨报单列 D8）。窗高经既有 fitHeight 链自动 +12（235→247）。Rust 零改动。

## 验收标准
- [ ] style.css:167 `body.compact .widget{gap:4px; padding:10px 4px 14px}`；全档通用 .widget（≈行32）不动
- [ ] 断言：compact 下 getComputedStyle(.widget) padding-bottom=14px 且 padding-top=10px；`window.innerHeight − 6 − 最后一个 .disc 底 ≥ 8`；grip.getBoundingClientRect().top − 6 ≥ 3
- [ ] 完整档逐字节 diff 断言不回归
- [ ] 断言输出留证写回报；备注窗高观测值（若 headless 通道可量 scrollHeight）

## Blocked by
无（本票先行定高度基线，其余票排后）

## 涉及路径
widget/ui/style.css
widget/ui/tests/assert-static.mjs

## 副作用声明
node widget/ui/tests/assert-static.mjs（无窗口、无网络）

decision_refs: D1, D4, D8
review_blocks: F3（已解除，本票为断言实现）
