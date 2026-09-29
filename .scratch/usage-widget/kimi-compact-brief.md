# 悬浮窗 0.2.3 简报：紧凑版 + 交接盘改版 + 文字溢出修（Kimi 执行 / Claude 评审）

你（Kimi）负责实现与自验；评审、构建、部署、commit、go test 由 Claude 侧做，你**不要**做这些。
先通读本简报，再读「涉及文件」全列表，然后动手。

## 0. 背景（已核实的事实，直接采信）

- 本仓 widget/ 是 Tauri 2 + WebView2 悬浮窗（Win10 19045）。0.2.2 已上：透明窗口 +
  CSS 圆角面板（`.widget::before` fixed inset 6px、12px 圆角、投影向窗内借位）、
  磁吸贴边 + `widget-docked` 事件驱动贴边融入、几何自愈（断言设计尺寸 132×620 逻辑 px）。
- **`.widget` 本体几何绝不能动**：自测 drag 与 jsDrag 以它的 rect 为参照，视觉面板在
  `::before` 上画——这是 0.2.2 踩过的坑（直接内缩面板导致自测 drag 挂掉后改的方案）。
- 完整版 132×620 逻辑 px，面板 inset 6px（面板 120×608）。实测（Playwright，132×620 视口，
  演示满载 4 盘）：内容+padding 高 593（余 15px）、盘本体宽 100（内容区 108，余 8px）。
- 文字溢出机制：`.cap{white-space:nowrap}` + `.widget{overflow:hidden}` → 超宽行**两端裸裁**
  （居中对齐，左右各裁一半）。实测 DeepSeek 三段花费行 213px > 120px；真机交接行
  （旧「摆渡」盘）「月 N 次估 · 周 M 次估」约 130px 同样超。
- 真机 daemon 对 handoff 只发两条计数（`internal/daemon/query_widget.go:199-207`）：
  `handoffs_month`/`handoffs_week`，text 形如「月 23 次」「周 5 次」，source=estimated；
  摆渡供应商无价格表，spend_* 不可算不造数。旧盘 label=「摆渡」。
- 用户已拍板的五项决定（不要重新发明）：
  1. 紧凑版=**迷你环列**：每个可见对象一个 ~48px 小环+圆心缩写，去文字行/倒计时行；
     tooltip、详情卡、断连灰化照常；紧凑窗口是**真实窗口尺寸变更**（不是 CSS 缩放）。
  2. 尺寸下限按 1366×768 逻辑屏设计（紧凑高度 ~260 量级，远小于下限）。
  3. 切换=壳内**双击 grip** 快捷切换 + 设置窗「外观」档；持久化进 profile.json；**不做自动切换**。
  4. 溢出三层修：内容取舍 → 结构化分行 → 省略号兜底（不做 CSS 强折行）。
  5. 旧摆渡盘：label 改「**交接**」，圆心=本月次数大数字，虚线圈保留装饰，tooltip 首行一句人话。

## 1. 涉及文件

- `widget/ui/profile.js`（显示配置纯逻辑；禁 DOM/window/location 顶层访问——被 node 断言直接 import）
- `widget/ui/app.js`（渲染层）
- `widget/ui/index.html`（widget 页：grip/设置浮层）
- `widget/ui/style.css`
- `widget/ui/data.js`（演示契约副本）
- `widget/ui/settings.html` / `widget/ui/settings.js`（票 04 设置窗）
- `widget/src-tauri/src/lib.rs`（双尺寸自愈 + set_appearance 命令 + 启动读档）
- `widget/src-tauri/tauri.conf.json`（**版本号不动**，部署侧统一处理）
- `internal/daemon/query_widget.go`（仅 label 一处字符串）
- 验证脚本/截图放 `.scratch/usage-widget/`

## 2. 铁律（违反=返工）

1. **零闪窗**：conf `visible:false` → setup 末尾 show 的流程与顺序不破；不新增任何会弹窗的东西。
2. **不许动**：磁吸语义与吸附落盘逻辑、MoveThrottle、单实例、托盘结构、window-state.json 只记位置。
   自愈机制只允许一处扩展：设计尺寸从常量改为「按当前外观档取值」。
3. `.widget` 本体定位（position:fixed; left:0; top:0; right:0）不动；紧凑化用 class 在
   内部元素上做，不改 `.widget` 的几何参照角色。
4. 前端栈铁律：vanilla ES 模块 + JSDoc，零构建链、零外部引用、零新依赖。
5. **不许为过测试改测试**：cargo 既有 3 测试、前端七项自测、`query_widget_test.go` 的断言
   都不许改（可新增，不许删改旧的）。
6. 数据诚实：不造数；daemon 不可达=灰化；估/查分标不混。
7. 你不跑 go test、不 commit、不改版本号、不碰 `widget/docs/release.md`。

## 3. 规格

### A. 外观档（appearance）

- `profile.js`：`Profile` 增 `appearance:'full'|'compact'`（默认 full）。
  `normalizeProfile` 归一：非法值→full 且 repaired=true。`PRESETS` 增一条 `compact`
  （`appearance:'compact'`，其余默认——供 URL 注入测试，先例=budgets/hidden）。
- 三处 UI，流向统一收敛到 profile：
  1. **widget 内设置浮层**（index.html `#settings` 浮层，同「布局」radioline 样式加一行
     「外观：完整/紧凑」）——保底通道，click 必达。
  2. **设置窗**（settings.html radioline 同款一行）。
  3. **grip 双击**（快捷通道）：`grip.addEventListener('dblclick')` 壳内分支由「直接 return」
     改为「切换外观」；浏览器语境维持原收起演示不变（自测 #2 依赖）。
  grip 的 title 文案同步（「拖动移动｜双击：完整/紧凑切换」；浏览器语境的收起提示留在演示路径）。
- `app.js` 流向：任何来源改 `state.profile.appearance` → `persistProfile()`（落盘+广播
  profile-changed）→ 主窗既有的 profile-changed 监听里 `applyProfile()`；applyProfile 内
  比对**已生效档位**，变了才 `invoke('set_appearance', { mode })` 并给根容器挂/摘 compact class。
  （自广播会回到自身监听，幂等处理：档位没变不重复 invoke。）
- compact class 挂 `body`（演示/headless 无壳也能模拟）；`.widget`/`.disc` 样式都从它派生。

### B. Rust 双尺寸

- 新增 managed state（如 `Appearance(Mutex<String>)`，"full"/"compact"）。
- 新增 `fn design_size(mode) -> (f64, f64)`：full=(132.0, 620.0)；compact=(你实测定值)。
  **紧凑常量与 CSS 必须一致**：用 Playwright 实测紧凑内容（4 盘满载+grip）后定值，
  留 ≥6px 余量，Rust 常量注释写推导过程，CSS 注释互指。
- `assert_geometry` 改用 `design_size(当前档)`——紧凑态塌缩回紧凑尺寸，完整态回完整尺寸。
  其余（set_size、夹回工作区、±2 容忍、事件流/轮询）零改动。
- setup 启动序：读 `app_data_dir()/profile.json` 的 `appearance` 字段（fs 读 + serde_json
  解析，任何失败=full）→ 初始化 managed state → 若 compact 则 **show 之前** `set_size`
  （零闪窗：用户只见最终尺寸）→ 既有位置恢复/默认位 → 既有 docked 初始 emit → 托盘 → show。
  首启默认位的居中计算用 set_size 之后的实际 outer_size（现逻辑即如此，保持顺序即可）。
- 新命令 `set_appearance(mode)`：校验合法值 → 更新 managed state → `set_size(LogicalSize)`
  → Ok。**不落盘**（落盘归前端 persistProfile）。注册进 invoke_handler。
- 磁吸/贴边/夹回全按 outer_size 现算，天然兼容双尺寸，零改动。

### C. 紧凑版形态（视觉细节你发挥，硬约束如下）

- 紧凑下：`.disc svg` 缩至 ~48px；`.cap`/`.cdline` 全隐；grip 缩小保留（拖动是刚需）；
  gear 隐藏（托盘菜单「设置…」可达）；圆心文字（c-label/c-money/c-sub/c-money-sym）字号
  适配 48px 盘；间距/内边距收紧。
- 断连灰化（filter）、dev 角标、升级通知条（updateNotice）、tooltip、详情卡在紧凑下照常、
  不破版。通知条若与迷你盘打架，给紧凑态专属布局。
- 横排 × 紧凑也须可用（一行迷你盘，总宽 ≤1366）。
- 停靠融入（docked-*）天然继承：`::before` 面板随窗口尺寸缩放，验证别漏。
- 交接盘紧凑态 = 小虚线圈 + 圆心次数（同 D 的数据，字号缩小）。

### D. 交接盘（原「摆渡」盘）

- `query_widget.go`：`"label": "摆渡"` → `"label": "交接"`（仅此一处，metric 键不动）。
- `data.js` 演示 handoff：label `'交接'`；metrics 改为镜像真机形态——
  `handoffs_month`（text 如「月 23 次」，value:23，estimated）、`handoffs_week`（「周 5 次」，
  value:5）。删除演示里的 spend_month/week_cny 两条（真机无价格表不发；app.js 的
  PROVENANCE spend_* 映射**保留不删**——向前兼容将来有价格表的情形）。
- `app.js` handoff 分支重画：
  - 圆心 = 本月次数大数字（`handoffs_month.value`，样式学 DeepSeek 盘的 c-money 层级），
    副标「次 · 本月」；虚线空圈（houtline）保留当装饰。
  - `handoffs_month` 缺席（旧契约/异常）→ 回落现状（label 居中），**不造数**。
  - cap = 月/周各一个 `.cap` div 两行，估角标保留。
- tooltip：handoff 盘首行插一句人话「闲置会话自动交接：读档→提炼→写交接文档；此处计次数与花费」，
  之后照旧列环指标或「无环指标 · 单击看详情」。
- `DETAIL_NAME_OF.handoff` = `'摆渡行 · 交接计数与花费'`。
- index.html 设置浮层帮助文案、任何出现「摆渡」字样的 UI 文案同步为「交接」口径
  （代码注释里的领域术语「摆渡」不用改）。

### E. 溢出三层修（完整版全窗）

- **第一层·内容取舍**：paygo（DeepSeek）盘 cap 只显**月花费**一行（今/周本来就在
  tooltip 与详情卡里）；coding_plan 盘各行维持现状。
- **第二层·结构化分行**：交接盘月/周两行（见 D）。不做 CSS white-space 强折行。
- **第三层·省略号兜底**：`.cap{max-width:100%; overflow:hidden; text-overflow:ellipsis}`
  （`.disc` 配合 `max-width:100%`）。任何未来超宽文本显示省略号，不再两端裸裁。
- **高度不变量**：完整版四盘满载（含交接盘新增一行）内容高 ≤ 面板内高且余量 ≥6px
  （用 Playwright 实测 scrollHeight vs clientHeight 说话）。不够就压 cap 行高/盘间距，
  **不许裁掉内容行**。`?superset=1` 语境回归不炸。

## 4. 验证（必做，报告里逐条给结果）

1. `cargo test`（widget/src-tauri）全绿（既有 3/3；若新增测试须全绿）。
2. Playwright（python + chromium；本机 headless Edge 坏了别用；起本地 http.server 伺服
   widget/ui 再开页，先例可参考 .scratch/usage-widget/ 下 0.2.2 的做法）：
   - a. 完整版 `?static=1&dev=1&selftest=1`：七项 data-* 全 1。
   - b. 完整版几何防回归：悬浮四角 12px 圆+投影在；docked-right 右侧两角直角+右缘贴窗。
   - c. 紧凑版（`?profile=compact&static=1&dev=1`）：盘缩小、无 cap/cdline、内容不溢出
     （scrollHeight ≤ clientHeight+0）、交接盘圆心有次数。
   - d. 溢出：DS 盘单行月花费不超宽；交接盘两行完整显示无裁切；构造超长文本（如 superset
     或临时改演示值）验证省略号出现而非裸裁。
   - e. 伪造壳：`addInitScript` 注入 `window.__TAURI__`（core.invoke 记录调用、event.listen/emit
     可用、`__TAURI_INTERNALS__` 置真使 inShell() 为真）→ 双击 grip 触发 set_appearance
     invoke + profile-changed emit；浮层外观 radio 同链。
   - f. 截图存 `.scratch/usage-widget/`：完整悬浮/完整停靠右/紧凑悬浮/紧凑停靠右/
     交接盘特写/DS 溢出修复特写。
3. 报告末尾给：变更摘要、验证逐条结果、**紧凑设计尺寸的实测推导**（这是 Rust 常量依据）、
   遗留风险/真机待验项。

## 5. 明确不做

版本号/构建/部署/commit；go test；assert-static.mjs harness 改动（本机 Edge 环境坏，
Playwright 等价回归即可）；自动按屏切换；设置窗结构重排（只加一行）；新动效体系；
daemon 契约新增键（label 改值不算新增）。
