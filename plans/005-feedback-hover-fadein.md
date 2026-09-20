# 005 — 全局反馈批：按压反馈 + hover 色过渡 + 页面淡入

- **Status**: DONE
- **Commit**: 8cfffc1
- **Severity**: LOW（覆盖面最广的体感批）
- **Category**: Missed opportunities（反馈 + 防突变）
- **Estimated scope**: 2 文件（style.css ~+30 行；app.js +10 行）

## Problem

三件体感缺口（均为高频/常驻元素）：

1. **零按压反馈**：style.css 全文无 `:active` 规则——chips、按钮、可点卡片按下去毫无回应
2. **hover 硬切**：表格行背景（style.css:63）、chip 边框（:218）、按钮背景等全部瞬间跳变
3. **页面弹现**：renderList/renderTimeline/renderCfg 内容替换"啪"地出现（app.js:196/403/1941）

## Target

### A. 按压反馈（反馈目的；`:active { transform: scale(0.97) }` + `transition: transform 160ms var(--ease-out)`）

适用（点击后**留在本页**的可点元素——表格行点击即跳页，按压态不可见，明确排除）：

```css
/* 交互反馈：按压缩放 + 颜色过渡（transform 位移类，reduce 下停） */
.tl-lg .chip,
.tl-lg .segview button,
.tl-cards .card.clickable,
#tl-play,
#tl-detail .d-close,
#tl-bt-result .bt-close,
#tl-detail .win-backtest,
#tl-detail .bt-go,
.plan-card .bt-go,
.plan-card summary {
  transition: background-color 130ms ease, border-color 130ms ease, color 130ms ease,
    transform 160ms var(--ease-out), opacity 130ms ease;
}
.tl-lg .chip:active, .tl-lg .segview button:active, .tl-cards .card.clickable:active,
#tl-play:active, #tl-detail .d-close:active, #tl-bt-result .bt-close:active,
#tl-detail .win-backtest:active, #tl-detail .bt-go:active, .plan-card .bt-go:active,
.plan-card summary:active { transform: scale(0.97); }
```

### B. hover 色过渡（防突变；高频行取近不可察级）

```css
/* 表格行：只过渡颜色（点击即跳页，无按压态）；甘特行名同理 */
#session-table tbody tr[data-lineage] { transition: background-color 130ms ease; }
#tl-gantt .grow .gname { transition: color 130ms ease; }
```

### C. 页面淡入（防突变；150ms 纯 opacity，不挡交互）

```css
/* 页面内容淡入：renderXxx 成功后由 fadeIn() 触发；纯 opacity 不拦点击 */
@keyframes app-fade { from { opacity: 0; } }
.fade-in { animation: app-fade 150ms var(--ease-out); }
```

app.js 工具函数（放在 setNote 之后）：

```js
// fadeIn 页面内容淡入：各 render 成功收尾时调一次；remove+回流+add 保证重复导航可重播。
function fadeIn(el) {
  el.classList.remove('fade-in');
  void el.offsetWidth; // 强制回流重启动画
  el.classList.add('fade-in');
}
```

调用点（各 render 的成功路径末尾）：renderList 表格 innerHTML 之后（app.js:203 后）；renderTimeline `buildTimelinePage(app, …)` 之后（app.js:338 后）；renderCfg viewerNote 挂完后（app.js:2007 后）。

### D. 降动效

```css
@media (prefers-reduced-motion: reduce) {
  /* 位移类停（瞬达），颜色/透明度保留：反馈仍在、运动消失 */
  .tl-lg .chip, .tl-lg .segview button, .tl-cards .card.clickable, #tl-play,
  #tl-detail .d-close, #tl-bt-result .bt-close, #tl-detail .win-backtest,
  #tl-detail .bt-go, .plan-card .bt-go, .plan-card summary {
    transition: background-color 130ms ease, border-color 130ms ease, color 130ms ease, opacity 130ms ease;
  }
  .tl-lg .chip:active, .tl-lg .segview button:active, .tl-cards .card.clickable:active,
  #tl-play:active, #tl-detail .d-close:active, #tl-bt-result .bt-close:active,
  #tl-detail .win-backtest:active, #tl-detail .bt-go:active, .plan-card .bt-go:active,
  .plan-card summary:active { transform: none; }
  .fade-in { animation: none; }
}
```

## Repo conventions to follow

- 选择器按既有分节归位（表格节/时序页节/控制条节），不新造顶层结构
- fadeIn 与既有工具函数（esc/fmtTS/setNote）同风格：中文注释写为什么

## Steps

1. style.css：A/B/C/D 四块按分节插入
2. app.js：fadeIn 函数 + 三个调用点

## Boundaries

- 表格行与甘特行**不加** scale（跳页/整行缩放违和——审计拒绝清单）
- 不动任何布局/颜色值本身，只加过渡
- fadeIn 不用于加载态 `<p class="loading">`（一闪而过的中间态不动画）
- TTL 输入框聚焦行为不受影响（scale 仅 :active 瞬态）

## Verification

- **Mechanical**: `node --check app.js`；`go build ./...`
- **Feel check**：按任一 chip 有轻微下压回弹；列表页行 hover 背景柔变；列表↔时序页切换内容淡入；连续快速导航淡入不叠加不残影；reduce 下按压/hover 即时但仍有颜色反馈、页面无淡入
- **Done when**: 所有留页可点元素有按压反馈 + hover 全柔化 + 三页淡入 + 降动效四项全对
