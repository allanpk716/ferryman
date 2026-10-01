# 悬浮窗 0.2.4 · Kimi 执行任务书（紧凑圆心大数字 + 设置窗卡片式重排 + 浮层退役）

分工不变：你实现并自验，Claude 评审代码、跑测试、真机验收、提交。repo 根：
`C:\Users\allan716\orca\workspaces\Ferryman\悬浮窗修复显示问题`（git worktree，工作区干净，基于 eb7a571）。
**不要 git commit、不要改版本号、不要跑 go test。**

用户已拍板（2026-09-29）：紧凑档圆心=「大数字」方案；设置窗=「分组卡片式」方案；窗内设置浮层退役（下述 Spec C）。两案的效果图（用户看过的承诺）：

```
迷你档 · 每盘圆心只放一个大数字
   ◜◝        ◜◝        ◜◝        ◕◕
  (62%)     (23%)     (¥37)    (691)
   ◟◞        ◟◞        ◟◞       次·本月
  智谱盘     Kimi盘     DS盘      交接盘
· 套餐盘：数字 = 最紧一条环的剩余%（告警时数字变黄/红）
· DS盘：余额大数字 · 交接盘：不动 · 谁是谁 → 悬停/点击

显示配置 · 660×640 可调
├ ▸ 外观与布局（外观/布局/倒计时 三行）
├ ▸ 服务商（每家一张卡，卡角↑↓排序；字段各带一句人话 hint）
├ ▸ 图例与帮助（默认收起，点开展开）
└  已保存 · 即时生效
```

## 0 背景事实（已核代码，直接信）

- 紧凑档现状圆心：coding_plan=label+plan 两行、paygo=CNY/金额/可用 三行、48px 盘里全是小字——用户嫌小嫌两行。交接盘（次数+次·本月）用户已认可，**不动**。
- disc 渲染单点：`widget/ui/app.js discHTML(p)`；紧凑档 CSS 块在 `widget/ui/style.css` 尾部 `body.compact` 段；窗口常量 `COMPACT_W/H_LOGICAL=80/272`（`widget/src-tauri/src/lib.rs`）。
- 设置窗：独立窗口 settings.html，Rust `open_settings`（lib.rs ~492）按需创建，现 560×520 逻辑px。设置逻辑 `widget/ui/settings.js`（CATALOG×profile→表格→collect()→persist 落盘+广播）。
- 窗内设置浮层：`widget/ui/index.html` 的 `#settings .overlay`（布局/外观/倒计时 radio + 图例帮助）。用户反馈它随窗口缩水（紧凑档窗口 80×272，浮层跟着变一小条）→ 退役。
- 既有测试：`widget/ui/tests/assert-static.mjs`（静态断言）+ `.scratch/usage-widget/pw_verify.py`（Playwright 16 项）+ `pw_measure.py`（紧凑档高度实测 60×253）+ index/settings 各自 `?selftest=1`。

## 1 文件清单（预计）

改：`widget/ui/app.js`、`widget/ui/settings.js`、`widget/ui/settings.html`、`widget/ui/index.html`、`widget/ui/style.css`、`widget/src-tauri/src/lib.rs`、`widget/ui/tests/assert-static.mjs`、`.scratch/usage-widget/pw_verify.py`（断言随动+新增项）。不改：`data.js`、`profile.js`（归一化/净化语义一行不动）、`tauri.conf.json`、daemon/Go 侧任何文件。

## 2 铁律

1. **完整档视觉零改动**：discHTML 的完整档输出必须逐字节不变（assert-static 现有绿断言尽量原样通过）；紧凑分支只在 `state.profile.appearance==='compact'` 时走。
2. 零闪窗：`open_settings` 的 `visible(false)`→建好→`show` 顺序不破。
3. 不碰：磁吸/节流/持久化/托盘语义、`.widget` 几何与 padding 规则、data.js 契约、profile.js 纯函数语义、COMPACT_* 常量（紧凑档高度预算不增：圆心两行→一行只会更矮，80×272 不动）。
4. 原生 ES modules，零新增依赖。
5. 不弱化测试：每条改动的断言须对应本任务书规格；新增断言见 §4。
6. 收集语义不变：settings collect() 产出的 profile 结构与净化规则与现状完全一致（显隐/顺序/阈值/环色/月预算/DS预算/布局/外观/倒计时全字段）。

## 3 规格

### A · 紧凑档圆心大数字（app.js + style.css）

discHTML 内按 `state.profile.appearance==='compact'` 分支换 center（svgInner/cap 不动）：

- **coding_plan**：单行 `<text>` 大数字 = `Math.round(最紧剩余%) + '%'`。「最紧」= 本盘**实际画出的环**里 remaining_pct 最低的那条（喂给 ringSVG 的 m1/mid/m3 中非空者：window_5h、周窗或工具环占位、月预算环）。数字 fill = 该环的 `ringColor(p, key, pct)`（即随该对象阈值黄/红告警联动）。垂直居中单行（字号约 28–30 SVG 单位，自测截图定 y 值）。误差兜底：一条环都没有 → 回落 label 单行大字。
- **paygo（DS）**：单行大数字 = 余额（`bal.text` 去 `¥` 前缀，与完整档同源；无 bal → `'—'`）。CNY/可用 两行小字在紧凑档不渲染。字号沿用 c-money 26（余额可到 5–7 字符，勿超内径；Playwright 用最宽演示值验证不触环）。
- **handoff**：分支不进（现状即大数字）。
- **error 盘**：不动（⚠+label）。
- 新 class 建议 `c-pct`（仅紧凑分支渲染，完整档无此元素）；告警色用 inline fill，不用 CSS 变体。

### B · 设置窗卡片式重排（settings.html + settings.js + style.css + lib.rs）

- lib.rs `open_settings`：`inner_size(560,520)→(660,640)`；`min_inner_size(460,380)→(560,480)`。
- settings.html 结构（settings.js 生成服务商卡）：
  - 区块「外观与布局」：外观 radio（完整/紧凑+托盘提示）、布局 radio、倒计时 checkbox——三行，行距放宽。
  - 区块「服务商」：**每对象一张卡**（根元素带 `data-id`，保留 f-visible/f-color/f-th/f-mb/f-dsb-on/f-dsb-amt/f-up/f-down 类名与控件类型，collect() 只换遍历容器 tr→card）：
    - 卡头：`${label}（${plan}）` + 显隐 checkbox + ↑↓ 排序按钮（右角）。
    - 卡体每字段一行，**hint 就地一句人话**：环色（coding_plan=5h/周/月预算 三个色选器带名；paygo=预算环 一个；handoff=「无环，仅显隐与顺序」）、告警阈值（黄 __ ／红 __ %，handoff 无）、月预算（coding_plan=`[__] tok/月 · 不设=文字计数`；paygo=`开 ☐ ¥[__]/月`）。
  - 区块「图例与帮助」：`<details>`（默认收起）——迁入 index 浮层的 .help 全部内容（色例、剩余制、估角标、倒计时点、30s 轮询/灰化说明）+ 现 settings.html 两大段 note 的信息拆到各字段 hint 与帮助区（note 墙删除，顶部只留一句「配置存 widget 本地 JSON，daemon 不感知；改动即时生效、自动保存。」）。
  - 底部 statusline 不动；resetNotice 机制不动。
- 字号：基准 ~13px，控件 12.5px；卡片间距充分，整体可在 660×640 一屏放下（Overflow 允许滚动但目标一屏）。
- settings.js：renderTable→renderCards（同 CATALOG 排序逻辑）；selftest 选择器随卡片结构更新，六项语义不减。

### C · 浮层退役（index.html + app.js + lib.rs + style.css）

- index.html：删除 `#settings` overlay 整块；tooltip/detail/dev-badge/selftest 载体保留。
- lib.rs：新增 `#[tauri::command] fn open_settings_window(app: &tauri::AppHandle)` → 调 `open_settings(app)`；注册进 invoke_handler。
- app.js：
  - gear 点击：壳内 `invoke('open_settings_window')`；非壳（浏览器演示）语境 fallback=控制台提示+无操作（不得报错）。
  - 删 overlay 相关接线（radios 监听/data-close/点击遮罩关闭/optCdline 监听）。倒计时行显隐此后只由 settings 窗的 profile-changed 广播 → applyProfile→render 驱动（渲染已吃 show_countdown，验证该链路真通）。
  - `applyAppearance` 的 radio 同步改为安全空集（querySelectorAll 空 forEach）。
  - `toggleAppearance`（托盘事件）不变。
  - index selftest 第 1 项（横竖切换）不再有 radio：直接调 `setLayout('horizontal')`/`('vertical')` 断言 class；六项数量不减。
- style.css：`.overlay` 规则删除；`.sheet` 保留（settings.html 用）；widget 页相关残留清理。
- 齿轮 title 改「打开显示配置（独立窗口）」；grip title 不动。

### D · 断言随动与新增

- assert-static.mjs：先跑一遍列出红项——每条红要么按新规格更新期望、要么揭示实现偏差（修实现优先）；**逐条在报告里给出改断言的理由**。新增静态断言：
  - settings.html：含「外观与布局」「图例与帮助」区块字样、`<details`、无 6 列表头残留（不含 `>环色</th>` 旧表头结构按新结构断言）。
  - index.html：**不含** `id="settings"`（浮层退役的负断言）。
- pw_verify.py（16 项基础上改+增，数量只增不减）：
  - 紧凑档演示页：每盘圆心单行大数字——按 data.js 演示值算出期望（如 glm 最紧=min(5h,周,月预算环) 的整数%、DS=余额串、交接=23），断言文本与「无第二行 c-sub/c-label」；断言 80×272 预算不破（重跑 pw_measure 同法，内容高 ≤253）。
  - 完整档演示页：现有断言全绿（完整档零改动的回归证明）。
  - settings.html：卡片结构（4 卡、每卡控件齐全、帮助默认收起 `details[open]` 为假、点开后内容可见）、collect 往返（改一个阈值→读回 DOM 状态符合）。
  - index.html：`?selftest=1` 六项全 1（含改版后的第 1 项）。

## 4 你的自验清单（全部执行并在报告给结果）

a. `cargo test`（在 widget/src-tauri/，现有 4 项全过；新命令编译过）。
b. `node tests/assert-static.mjs` 全绿。
c. `.scratch/usage-widget/pw_verify.py` 全绿（改后清单逐项列出 N/N）。
d. pw_measure 紧凑内容高 ≤253（80×272 预算不破）。
e. 演示页截图：紧凑档 4 盘（圆心大数字、告警色示例至少一盘黄或红）、完整档 4 盘（与改前一致）、settings 卡片全页、帮助展开态。存 `.scratch/usage-widget/`，报告里给路径。
f. 报告：改了哪些文件、每条断言改动的理由、遗留疑问。

## 5 不做清单

go/daemon 侧、版本号、commit、COMPACT_*/DESIGN_* 常量、磁吸/自愈/托盘语义、profile.js/data.js 语义、Kimi 自创的第四种圆心样式或新配色主题。
