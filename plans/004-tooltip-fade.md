# 004 — tooltip 渐显渐隐（查数悬停浮层软化）

- **Status**: DONE
- **Commit**: 8cfffc1
- **Severity**: LOW
- **Category**: Missed opportunities（防突变，近不可察级）
- **Estimated scope**: 2 文件（style.css +4 行；app.js 3 处微改）

## Problem

`#tl-tooltip` 显隐走 `hidden` 属性硬切（`app.js:543` `tip.hidden = false` / `app.js:553` `tip.hidden = true`；骨架 `app.js:266` `<div id="tl-tooltip" hidden>`）。查数时鼠标快速划过柱/事件/窗口，浮层高频闪现闪灭。

## Target

**纯 opacity 125ms `ease`**（颜色/悬停类用 `ease`；高频查数场景取预算下沿 125ms，近不可察级）。位置跟随保持即时——**绝不对 left/top 加 transition**（浮层要贴鼠标）。

显隐机制从 `hidden` 属性改为 class（display 切换会杀死 transition）：

1. 骨架 `TL_SCAFFOLD`（app.js:266）：`<div id="tl-tooltip" hidden>` → `<div id="tl-tooltip">`（常态在 DOM、透明、`pointer-events: none` 已有不挡交互）
2. `attachTip`（app.js:540-554）：`tip.hidden = false` → `tip.classList.add('show')`；`tip.hidden = true` → `tip.classList.remove('show')`
3. style.css（#tl-tooltip 规则处）：

```css
#tl-tooltip {
  opacity: 0;
  transition: opacity 125ms ease; /* 只动 opacity：位置跟鼠标必须即时 */
}
#tl-tooltip.show { opacity: 1; }
```

降动效：opacity 属"保留类"（助理解非运动），125ms 已是近不可察级，不另设 reduce 规则。

## Repo conventions to follow

- 类切换代替 hidden 切换有先例可循：`svg.classList.add('dragging')`（app.js:1839）
- tooltip 定位数学（app.js:544-551）一字不动

## Steps

1. style.css `#tl-tooltip` 块加 opacity/transition 与 `.show` 规则
2. app.js 骨架去 `hidden`、attachTip 两处改 classList

## Boundaries

- 不动 tooltip 内容构建（tipFill/tipRow/tipReq 等）与定位逻辑
- 不加位移/缩放（高频场景，只有 opacity）
- 全仓 `tip.hidden` 仅 543/553 两处引用，改完即净（防漏改残留）

## Verification

- **Mechanical**: `node --check app.js`；`grep -n "tip.hidden" app.js` 零命中
- **Feel check**：悬停柱子浮层快速淡入；鼠标在多柱间快速移动无闪烁感；移出即淡出；浮层始终贴鼠标无拖影
- **Done when**: 显隐 125ms 柔化 + 定位即时 + 无 hidden 残留
