# 悬浮窗 0.2.6 · Kimi 执行任务书（窗高随可见盘数自适应）

分工不变：你实现并自验，Claude 评审代码、跑测试、真机验收、提交。repo 根：
`C:\Users\allan716\orca\workspaces\Ferryman\悬浮窗修复显示问题`（git worktree，基于 432a0cc）。
**不要 git commit、不要改版本号、不要跑 go test。** Windows 约束：所有命令在当前 shell 内跑，
不得弹新控制台窗口。

## 用户反馈（2026-09-29，0.2.5 上线后）

> 现在窗体只显示了三个圆圈，还空了一节黑色的留白，希望能自动适应、不要留空。

## 0 病根（已核代码，直接信）

- 窗高是**常量按演示数据 4 盘推导**的：完整档 `DESIGN_H_LOGICAL=620`、紧凑档
  `COMPACT_H_LOGICAL=322`（`widget/src-tauri/src/lib.rs` ~54-63 推导注释）。live 只有
  3 个盘（Kimi/智谱/交接），内容 ~234 逻辑 px，窗口仍 322——底部剩 ~76px 黑面板留空
  （面板 `.widget::before` fixed 贴窗缘，窗口多高面板多高）。完整档同理（620 vs 3 盘 ~450）。
- 设计尺寸的三个消费点（都要跟着改）：
  1. 几何自愈断言 `assert_geometry`（~204-222）：`design_size(&current_appearance(app))`
     对比物理尺寸，偏离即 `set_size(LogicalSize)` 拉回；
  2. `set_appearance` 命令（~374-390）：换档时按档 `set_size`；
  3. setup 启动序（~552-560）：紧凑档用户在 show 前 `set_size`。
- `Appearance(Mutex<String>)` 是 managed state（~71）。前端渲染单点 `render()`（app.js ~219），
  显隐/顺序由 profile 决定，30s 轮询与 profile-changed 后都会全量重渲染。
- `.widget` 纵排 `position:fixed;left:0;top:0;right:0`（无 bottom→高度=内容高），横排显式
  `bottom:auto`；有 `max-height:100vh;overflow:hidden`——**量内容高度必须用 scrollHeight**
  （rect 会被 max-height 钳制，scrollHeight 报完整内容需求）。
- 非壳语境的 invoke 已有静默回落惯例（gear 的 open_settings_window：无壳 console.info）。

## 1 决策（Claude 定，按此实现）

**窗高从常量改为"缺省+运行时上报"**：宽仍是常量（完整 132 / 紧凑 80，不动）；高保留现有
常量作**缺省值**（出生/换档/前端未上报时的尺寸），前端每次渲染后量内容高上报，Rust 钳位后
更新状态并 set_size。几何自愈的断言目标同步读状态——塌缩仍被拉回到**自适应后的**高度。

不做的：宽度自适应（内容恒窄于窗宽，做了只会抖）；按盘数在 Rust 侧复算布局（布局归前端，
Rust 只收一个数，避免两份布局公式）。

## 2 Spec · Rust（lib.rs）

1. 常量注释改口径：`DESIGN_H_LOGICAL`/`COMPACT_H_LOGICAL` = **缺省高度**（0.2.6 起实际
   高度由前端 `fit_height` 按可见盘数上报覆盖；推导注释保留作缺省依据）。
2. 新 managed state：`DesignHeights(Mutex<(f64, f64)>)`（full, compact），init=(620, 322)。
   `design_size(mode)` 改为读它（与 `current_appearance` 同风格：取不到锁回落缺省常量）。
   纯函数抽出来供单测：`design_size_for(mode: &str, full_h: f64, compact_h: f64) -> (f64,f64)`
   （宽仍取常量）。
3. 新命令 `fit_height(mode: String, content_h: f64)`：
   - mode 归一（非法→取当前档）；content_h 钳位 `[60, 1500]` 逻辑 px、四舍五入到整数；
   - 与状态现值差 <1 → no-op（返回当前值）；否则更新 `DesignHeights` 该档位，然后对主窗
     `set_size(LogicalSize(W(mode), new_h))`（W=该档常量宽）。**先更状态再 set_size**——
     set_size 触发的 Resized 事件流进自愈断言时必须已能对上新值，否则会被"拉回旧值"打架。
   - 位置不动（top-left 锚定）。注释记一个已知边界：贴下缘停靠的窗缩短后会留下缝隙
     （snap 只在拖动时触发），属可接受化妆问题，不做底缘锚定。
4. setup 启动序、`set_appearance` 里对 `design_size` 的调用改走新状态（换档先落该档缺省高，
   前端随后的 fit_height 再收细——两次 set_size 可接受）。
5. 单测（lib.rs tests）：`design_size_for` 两档取值与宽度；`fit_height` 的钳位纯函数
   （<60→60，>1500→1500，小数取整，|Δ|<1 no-op 判定）——把钳位/no-op 判定写成纯函数再测。

## 3 Spec · 前端（app.js）

渲染后量高上报，一处出口：

- 新小函数 `fitHeight()`：`document.body.classList.contains('tray-collapsed')` 或
  `!state.summary` → 直接返回（防启动期收缩成光杆 grip；托盘收起时 #widget display:none
  量出 0）。否则 `const h = Math.ceil(widget.scrollHeight)`（widget=现有 #widget 元素引用），
  与上次已上报值差 ≥1 才 `invoke('fit_height', { mode: state.profile.appearance, contentH: h })`
  （参数名按 Rust 命令签名约定 snake/camel 与既有命令一致——先看 set_appearance 的前端
  调用形态照抄）。非壳语境静默跳过（照 gear 的惯例）。
- 调用点：`render()` 末尾；`setAppearance` 换档路径在 body class 切换并 render 之后。
  上次已上报值按"mode+值"记，换档后强制重发一次。
- 注释写明量法为什么是 scrollHeight（max-height:100vh 钳 rect 不钳 scrollHeight）。

## 4 测试（全部当前 shell 内）

1. `cd widget/src-tauri && cargo test`——全绿（含新单测）。
2. `node widget/ui/tests/assert-static.mjs`——96+ 全过；新增：
   - lib.rs 注册 `fit_height`、`DesignHeights` 在场、`design_size_for` 纯函数在场；
   - app.js 有 `fitHeight`/scrollHeight 量法/tray-collapsed 与无 summary 的守卫；
   - 常量注释口径已改"缺省"（grep '缺省'）。
3. `python .scratch/usage-widget/pw_verify.py`——30 项保持全绿 + 新增（e 组后顺延编号）：
   - 伪壳捕获 `fit_height`：紧凑档 4 盘演示渲染后被调用，contentH ≈303（±3）；完整档
     ≈599-613；连续两次 render 不重复上报（调用计数不涨）；
   - 显隐一盘（localStorage profile 藏掉 deepseek）→ render → 新上报值比前值小 ~50-70
     （DS 盘+标签行高）；
   - tray-collapsed（body 加 class）下 fitHeight 不发（计数不涨）。
   （伪壳注入做法照抄现有 e 组。）

## 5 自验汇报

逐文件改动清单 + 新单测名 + 三条测试命令输出尾部（如实照贴）。
