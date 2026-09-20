# 002 — 时间线视窗补间（跳断点/事件现场/甘特选行/双击复位）

- **Status**: DONE
- **Commit**: 8cfffc1
- **Severity**: MEDIUM（机会类最高杠杆）
- **Category**: Missed opportunities（空间连续性）
- **Estimated scope**: 1 文件（app.js，~60 行新增 + 4 处调用点改写）

## Problem

四处视窗瞬移，跳完不知道自己在时间轴哪头、朝哪个方向跳的：

1. `app.js:652-661` jumpDead（断缓存卡与断缓存 chip 共用）——直改 `state.t0/t1` 后 `clampView(); pick()`
2. `app.js:822-833` 事件图例跳转——同款直改
3. `app.js:895-907` 甘特行选中扩窗——条件直改
4. `app.js:1872-1876` 双击复位——直改回 full0/full1

```js
// app.js:652 — current（其余三处同构）
function jumpDead() {
  if (!state.zones.length) return;
  var z = state.zones[deadHop++ % state.zones.length];
  var mid = (z.from + z.to) / 2;
  var span = Math.max(600, (z.to - z.from) * 3);
  state.t0 = mid - span / 2;
  state.t1 = mid + span / 2;
  clampView();
  pick({ type: 'zone', i: state.zones.indexOf(z) });
}
```

## Target

视窗变化经 `tweenView` 补间：**300ms，`cubic-bezier(0.77, 0, 0.175, 1)`（= token `--ease-in-out`，屏内移动用 in-out）**，rAF 驱动逐帧 `draw()`；可打断（新跳转/拖拽/滚轮即停）；`navSeq` 变（切页）自毁；降动效偏好下瞬跳。

新增（放在 `buildTimelinePage` 内、`recomputeZones` 定义之后）：

```js
// ---- 视窗补间：跳转类视窗变化 300ms ease-in-out 过渡（可打断；降动效瞬跳）----
var viewTween = null;
function stopViewTween() {
  if (viewTween) { cancelAnimationFrame(viewTween.raf); viewTween = null; }
}
// cubicBezier 贝塞尔求解（牛顿法）：JS 侧复刻 CSS token --ease-in-out 的同一条曲线
function cubicBezier(x1, y1, x2, y2) {
  function bx(t) { return 3 * (1 - t) * (1 - t) * t * x1 + 3 * (1 - t) * t * t * x2 + t * t * t; }
  function by(t) { return 3 * (1 - t) * (1 - t) * t * y1 + 3 * (1 - t) * t * t * y2 + t * t * t; }
  return function (x) {
    var t = x;
    for (var i = 0; i < 8; i++) {
      var e = bx(t) - x;
      if (Math.abs(e) < 1e-5) break;
      var d = 3 * (1 - t) * (1 - t) * x1 + 6 * (1 - t) * t * (x2 - x1) + 3 * t * t * (1 - x2);
      if (Math.abs(d) < 1e-6) break;
      t -= e / d;
    }
    return by(Math.max(0, Math.min(1, t)));
  };
}
var easeInOut = cubicBezier(0.77, 0, 0.175, 1);
function tweenView(to0, to1) {
  stopViewTween();
  var rm = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  if (rm || (Math.abs(to0 - state.t0) < 1 && Math.abs(to1 - state.t1) < 1)) {
    state.t0 = to0; state.t1 = to1; clampView(); draw(); return;
  }
  var seqAtTween = navSeq; // 切页守卫：与回放定时器同款自毁
  var from0 = state.t0, from1 = state.t1, start = performance.now(), DUR = 300;
  function step(now) {
    if (seqAtTween !== navSeq) { viewTween = null; return; }
    var p = Math.min(1, (now - start) / DUR);
    var e = easeInOut(p);
    state.t0 = from0 + (to0 - from0) * e;
    state.t1 = from1 + (to1 - from1) * e;
    clampView();
    draw();
    if (p < 1) viewTween.raf = requestAnimationFrame(step);
    else { state.t0 = to0; state.t1 = to1; clampView(); draw(); viewTween = null; } // 末帧钉死目标值
  }
  viewTween = { raf: requestAnimationFrame(step) };
}
```

四处调用点改写（只换视窗赋值方式，选中/详情逻辑不动）：

1. jumpDead：`state.t0 = mid - span / 2; state.t1 = mid + span / 2; clampView();` → `tweenView(mid - span / 2, mid + span / 2);`
2. 事件图例跳转（app.js:826-828）：`state.t0 = t - span / 2; state.t1 = t + span / 2; clampView();` → `tweenView(t - span / 2, t + span / 2);`
3. 甘特选行扩窗（app.js:900-903）：条件块内三行赋值 → `tweenView(Math.min(state.t0, lo - margin), Math.max(state.t1, hi + margin));`
4. 双击复位（app.js:1873-1874）：`state.t0 = state.full0; state.t1 = state.full1;` → `tweenView(state.full0, state.full1);`

打断钩子（用户抢过操纵权即停）：

- `svg mousedown`（app.js:1830 处理器开头）加 `stopViewTween();`
- `svg wheel`（app.js:1860 处理器开头、`ev.preventDefault()` 之后）加 `stopViewTween();`

## Repo conventions to follow

- 竞态守卫风格：捕获 `navSeq` 局部比对（见回放 `app.js:1890` `seqAtPlay` 同款）
- 注释风格：中文、写"为什么"（见 app.js:1890-1891）
- 纯 vanilla、无依赖（离线铁律）；DOM 纪律不受影响（只改 state 与既有 draw）

## Steps

1. app.js `buildTimelinePage` 内加 viewTween/stopViewTween/cubicBezier/easeInOut/tweenView 五件（位置：`recomputeZones` 之后）
2. 改写四处调用点
3. mousedown/wheel 两处加 stopViewTween()

## Boundaries

- 不碰 draw() 本体、不碰 pick/renderDetail 逻辑
- 不给缩放/拖拽/滑块本身加任何过渡（直操纵必须即时——审计已定）
- 滚轮/拖拽处理器只加一行 stop，不改缩放数学
- 若行号与 commit 戳漂移（代码已变），STOP 上报，不即兴

## Verification

- **Mechanical**: `node --check cmd/ferryman/web/app.js` 无输出；`go build ./...` 通过
- **Feel check**（`ferryman --demo --no-browser` 后开 127.0.0.1:15900，进任一时序页）：
  - 点「断缓存 ×N」chip 连跳：视窗平滑滑向断点，能看出方向与距离；连点不重启从头（每次 stopViewTween 后新起点）
  - 补间中途按住鼠标拖拽：立即跟手，无弹簧回弹
  - 双击复位：滑回全范围，非瞬移
  - DevTools Rendering 面板开 prefers-reduced-motion：全部瞬跳，功能不变
- **Done when**: 四个入口都有 300ms 平滑过渡 + 三种打断路径（新跳/拖拽/滚轮）生效 + 降动效瞬跳
