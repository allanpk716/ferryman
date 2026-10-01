# 0018 · 设置窗常驻隐藏（运行时建 WebView2 真机僵尸事故）

日期：2026-09-29 · 版本：widget 0.2.4 · 状态：已实施

## 背景

设置窗（settings.html）原设计（票 04）：托盘/齿轮触发时**运行时按需创建** WebviewWindow，关闭即销毁——目的是防第二个 WebView 常驻内存。该设计在票 04 验收期（0.2.0 时代）真机可用。

## 事故（2026-09-29 真机钉死）

运行时创建的 WebView2 在本机（Win10 19045 / RDP 混合 DPI 会话 / tauri 2.11.6 + wry 0.55.1）稳定产出**僵尸 WebView**：

- 窗口本体与 WRY_WEBVIEW 宿主 HWND 都在、尺寸/居中正确；
- 浏览器侧 `Chrome_WidgetWin_0` 恒 0×0 不可见，渲染树（Chrome_WidgetWin_1 / RenderWidgetHostHWND / D3D Window）从不建立；
- 表现：窗口能显示但内容**永白**（用户肉眼确认，非截图假象）；
- 无关线程（invoke 实测跑主线程 ThreadId(1)，与托盘同语境）、无关创建可见性（visible(false) 与创建即显都试过）、无关 WM_SIZE（外部轻推尺寸无效）；
- 对照组：**conf 声明、启动时由 Builder 创建**的 widget 主窗从未出过此问题（同为隐藏创建、同为第二个窗口也不受影响）。

## 决定

设置窗改为 **conf 声明、启动时常驻创建（visible:false 隐藏）**：打开=显示，关闭=隐藏不销毁（`CloseRequested` 对两窗统一 prevent_close+hide）。齿轮（invoke）与托盘同路径 `open_settings` = unminimize+show+set_focus。

## 代价与取舍

- **代价**：一个常驻隐藏的 WebView2 控制器（实测整机空闲 CPU ~1% 单核、宿主进程 RSS 29MB，可接受）；票 04 的「防常驻内存」设计就此让位。
- **换来**：重开永远可靠（同句柄显示/隐藏循环真机验证）；绕开未钉死的 wry/WebView2 僵尸机制——按可用性优先，不押注上游修复时点。

## 遗留

- 僵尸的确切上游机制（wry `wait_with_pump` 完成回调后浏览器侧为何不建渲染窗）未继续深挖；若未来升级 tauri/wry 后想恢复按需创建，先在真机复测「运行时建窗→检查 Chrome 子树全活」再回退本 ADR。
- 诊断手法沉淀：EnumWindows+EnumChildWindows 看 `Chrome_WidgetWin_0` 是否 0×0 是判 WebView2 生死的一手证据（`.scratch/usage-widget/enum-children.ps1`）；PrintWindow 像素统计（暗色主题占比）判内容渲染（同目录 show-win.ps1 + 像素脚本）。
