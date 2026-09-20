# 003 — 反跑结果三线对比条生长动画

- **Status**: DONE
- **Commit**: 8cfffc1
- **Severity**: LOW
- **Category**: Missed opportunities（状态可读）
- **Estimated scope**: 2 文件（style.css +6 行；app.js +2 行）

## Problem

`app.js:1252-1277` btLine 结果条宽度瞬间撑满——三线量级对比是反跑的核心输出，生长过程本身帮人读出"谁大谁小"。

```js
// app.js:1262 — current
if (val != null && maxV > 0) fill.style.width = (val / maxV * 100) + '%';
```

## Target

条从左生长：**280ms `var(--ease-out)`，`transform: scaleX(0→1)`，`transform-origin: left`，三线错峰 40ms**。不动画 `width`（布局属性）；keyframes 而非 transition——结果卡每次重建（`btResult.textContent = ''`），keyframes 天然随新元素重播，偶发频率下无重触发问题。

style.css（`#tl-bt-result .bt-fill` 规则附近）：

```css
/* 反跑三线生长：scaleX 而非 width（不触发布局）；错峰由 JS 逐行加 animation-delay */
#tl-bt-result .bt-fill {
  transform-origin: left center;
  animation: bt-grow 280ms var(--ease-out) both;
}
@keyframes bt-grow { from { transform: scaleX(0); } }
@media (prefers-reduced-motion: reduce) {
  #tl-bt-result .bt-fill { animation: none; }
}
```

app.js btLine 增加行序参数并设错峰：

```js
// btLine 一条对比横条：val=null 显示灰"—（未启用）"；idx 为行序（三线错峰 40ms 生长）。
function btLine(label, val, sub, fillCls, idx) {
  …
  fill.style.animationDelay = (idx * 40) + 'ms';
  …
}
// 调用点（app.js:1279-1292）传 0/1/2：
btLine('若当时这样配', …, 0); btLine('实际发生', …, 1); btLine('什么都不做', …, 2);
```

## Repo conventions to follow

- keyframes 命名带业务前缀（`bt-` 系，同 `bt-fill/bt-track`）
- 降动效规则与动画声明同处一节，一眼对照

## Steps

1. style.css 在 `#tl-bt-result .bt-fill { height: 100%; }`（style.css:536）之后插入目标块
2. app.js btLine 签名加 `idx`，fill 设 `animationDelay`，三个调用点传参

## Boundaries

- 只动反跑结果卡；保活计划卡（plan-result 文本行）不动——数字文本不滚不动画（审计已拒）
- 不改 btLine 的 DOM 结构与数据口径
- 数值照旧来自 /api/backtest（公式单源，前端不自算）

## Verification

- **Mechanical**: `node --check cmd/ferryman/web/app.js`
- **Feel check**（--demo 时序页：选等待窗口 → 反跑此窗口 → 提交）：
  - 三条依次从左长出（绿→灰→红错峰 40ms），能感知相对长短
  - 连续反跑两次：每次都重新生长（卡片重建即重播）
  - prefers-reduced-motion：条瞬达满宽
- **Done when**: 三线生长 + 错峰 + 降动效瞬达
