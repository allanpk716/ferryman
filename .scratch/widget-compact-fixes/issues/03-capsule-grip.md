# 票 03 · 胶囊条把手（问题 3）

## What to build
紧凑档 grip 从隐形 ⠿ 字符改为 22×3 逻辑 px 胶囊条：font-size:0 藏字符（DOM 与 data-tauri-drag-region 原样），::after 画条，hover 提亮 0.30→0.60 作唯一"可拖"提示。不新增任何 click/dblclick 监听（0.2.3 原生拖动区吞双击红线）。高度预算 17→10，窗高链 247→240。

## 验收标准
- [ ] style.css 追加 body.compact .grip 三条规则（藏字符/::after 胶囊/hover 提亮，照 spec 值）
- [ ] 断言：compact 下 grip ::after 计算尺寸 22×3；hover 后背景 rgba alpha 0.30→0.60；grip 元素仍有 data-tauri-drag-region 属性
- [ ] 既有自测中向 grip 派发 dblclick/mousedown 的断言不回归（app.js 自测项）
- [ ] 完整档逐字节 diff 断言不回归
- [ ] 断言输出留证写回报

## Blocked by
票 02（同文件路径互斥＋高度基线先定；建议同时等票 01 落地后最后做）

## 涉及路径
widget/ui/style.css
widget/ui/tests/assert-static.mjs

## 副作用声明
node widget/ui/tests/assert-static.mjs（无窗口、无网络）

decision_refs: D1, D4, D5
review_blocks: 无
