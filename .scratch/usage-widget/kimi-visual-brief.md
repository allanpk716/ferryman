# Kimi 交接简报 · 悬浮窗枝二：圆角 + 贴边融入（2026-09-29 grill 定稿，不得推翻）

## 背景

Ferryman 用量悬浮窗：`widget/` 子目录的 Tauri 2 + WebView2 应用，运行环境 Windows 10 19045、三显示器、150% 缩放、用户经 RDP 远程使用。上一提交 `0d25629`（0.2.2）刚落地"几何自愈"（RDP 断连塌缩的断言制修复）。本任务是视觉层：**圆角 + 贴边融入**。

## 定稿设计（已与用户 grill 定案，照做，不要重新设计）

1. **实现路线：透明窗口 + CSS 圆角**。Win10 无系统级窗口圆角，唯一路线是窗口透明化后由网页层画圆角矩形：
   - `widget/src-tauri/tauri.conf.json` 的 widget 窗口加 `"transparent": true`（`decorations:false`/`shadow:false` 已有，不动）。
   - `widget/ui/` 的 html/body 背景改透明，原底色 `--wbg:#101218`（style.css:6）移到圆角容器上。
2. **圆角半径 12px**；悬浮（未停靠）时四角全圆 + 柔和投影（CSS box-shadow，克制，深色底上别过亮）。
3. **贴边融入**：窗口停靠在屏幕边缘时，**贴边那一侧的圆角转直角、该侧投影消失**，其余三角保持圆——看起来像从屏幕边缘"长出来"。停靠侧判定由 Rust 端给出：
   - `snap_if_near`（lib.rs）求值吸附时已经知道吸了哪条边——**新增** emit（如 `widget-docked`，载荷 `{side:"left"|"right"|"top"|"bottom"}` 或吸附为零边时 `null`），前端监听后给根容器加/删 `docked-left` 等 class，由 CSS 控制对应角 `border-radius: 0` 与投影裁剪。
   - **启动也要给初始态**：setup 位置恢复完成后，按恢复位置与所在屏工作区各边距离（≤2px 视为停靠）emit 一次。
   - 注意 snap 是四边独立判定、角落可两边同吸——载荷要能表达多边（建议 `{left:bool,right:bool,top:bool,bottom:bool}` 或 side 数组，你定，前后端一致即可）。
4. **本轮明确不做**：backdrop-blur 毛玻璃（RDP 合成性能风险）、窗口尺寸变化、任何动效大改。

## 铁律（违反即返工）

- **不许动**几何自愈（`assert_geometry`/geom worker/相关常量）、磁吸吸附行为本身、位置记忆持久化格式。对 `snap_if_near` 只允许"新增 emit"，不许改吸附/落盘逻辑。
- **零闪窗**：`visible:false` 先载后显的流程不动；不新增任何会弹控制台/弹窗的东西。
- **不新增依赖**（Cargo/npm 都不加）；**不动版本号**（保持 0.2.2）。
- **不动数据层**：app.js 的取数、渲染、profile 逻辑不许改，只允许加 docked class 的挂接（listen 事件 + class 切换）。
- 注释用中文，风格贴现有代码（写"为什么"，不写"是什么"）；改动最小化，不顺手重构。
- 透明窗口在 WebView2 上 resize/重绘有已知怪癖，而自愈断言依赖 set_size——完成后若发现透明化影响自愈或磁吸行为，**停下来，在最终报告里如实说明，不要硬绕**。

## 涉及文件（先读再改）

- `widget/src-tauri/tauri.conf.json`（transparent）
- `widget/src-tauri/src/lib.rs`（snap 处 emit + 启动初始 emit；先通读 41-106 的磁吸与 worker、397-439 的事件处理）
- `widget/ui/index.html` / `widget/ui/style.css` / `widget/ui/app.js`（只挂 docked class）

## 完成标准

1. `cd widget/src-tauri && cargo test` 仍 3/3 通过（不许为过测试改测试）。
2. 悬浮态：四角 12px 圆 + 柔和投影，底色不变，内容布局零变化（132×620 逻辑 px 装得下所有圆控件）。
3. 停靠态：贴边侧直角无投影，对侧圆角保留。
4. 不 commit——改完留工作区，最终输出一段变更摘要（改了哪些文件、每处为什么）。
