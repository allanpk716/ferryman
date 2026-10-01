# 悬浮窗 0.2.5 · Kimi 执行任务书（紧凑档身份标签行 + 交接盘大数字）

分工不变：你实现并自验，Claude 评审代码、跑测试、真机验收、提交。repo 根：
`C:\Users\allan716\orca\workspaces\Ferryman\悬浮窗修复显示问题`（git worktree，基于 ab2b11e）。
**不要 git commit、不要改版本号、不要跑 go test。** Windows 约束：你跑的每个命令都不得弹出
新的控制台窗口（cargo test / node / python 在当前 shell 内跑即可，勿用 start/cmd /c）。

## 用户反馈（2026-09-29，0.2.4 上线后）

1. 紧凑档分不清哪个盘是哪家服务商（用户不常看，记不住；且两套餐盘环色按指标走、一模一样）。
2. 第三个盘（交接/摆渡）紧凑档的字看不清——圆心是完整档的两行布局缩下来，"次 · 本月"那行
   在 48px 盘里只有约 6px 高，物理不可读。

用户已拍板（AskUserQuestion 两问）：
- **方案 A「文字标签+品牌色点」**：每个迷你盘下方一行约 10px 名字（Kimi / GLM / 交接），
  名字前缀品牌色小圆点。文字负责识别，品牌色只是辅助点缀。
- **交接盘紧凑档=大数字**：单行大数字（与其他盘同语言），删掉"次·本月"行；含义靠标签行+悬停提示。

品牌色调研结论（已查，写进代码注释用）：智谱/GLM `#4268FA`、Kimi `#1783FF`、DeepSeek
`#4D6BFE`——三家全蓝系，色相分不开，所以色点只能当辅助、不能当唯一识别。

## 0 背景事实（已核代码，直接信）

- 盘渲染单点：`widget/ui/app.js` `discHTML(p)`（~153 行起）。紧凑档分支已有两处：
  coding_plan（~170，圆心=最紧环剩余% 大数字）与 paygo（~196，圆心=余额）。**handoff 分支
  （~202）没有紧凑档覆盖**——这就是病根：48px 盘里渲染 c-money(26px)+c-sub(13px) 两行，
  ×0.48 缩放后"次·本月"≈6.2px。
- 紧凑档 CSS 块：`widget/ui/style.css` ~164-186（body.compact 段）。`.cap` 在紧凑档
  display:none——**新标签行必须用新 class（mini-tag），不要复用 .cap**。
- 窗口常量：`widget/src-tauri/src/lib.rs` `COMPACT_W_LOGICAL=80 / COMPACT_H_LOGICAL=272`
  （~56-63 有推导注释，与 style.css 紧凑块互指——改一边必须核另一边）。
- daemon 实时盘位（curl 实测）：`kimi`(label Kimi)、`智谱`(label GLM)——注意** live 的上游
  id 是中文'智谱'**；demo 数据（`widget/ui/data.js`）里 id 是 `glm`。品牌色映射必须 id 和
  label 两种键都认。handoff 的 live label 还是"摆渡"（daemon 换装后才会变"交接"）。
- demo 数据 id/label：glm/GLM、kimi/Kimi、deepseek/DeepSeek、handoff/交接（data.js ~55-81）。
- 测试：`widget/ui/tests/assert-static.mjs`（静态断言）；`.scratch/usage-widget/pw_verify.py`
  （Playwright 25 检查，chromium + 本地 http.server + 假 __TAURI__ 注入；stdout 已 UTF-8）。
  测量脚本参考 `.scratch/usage-widget/pw_measure.py`。

## 1 Spec A · 迷你标签行（仅紧凑档渲染）

`discHTML` 返回模板处**单一出口**追加（不要分散进各分支）：

- 仅当 `state.profile.appearance === 'compact'` 时，在 `<svg>` 之后（cap 之前之后皆可——
  cap 在紧凑档本就 display:none）输出：
  `<div class="mini-tag"><i class="dot" style="background:${brandDot(p)}"></i>${tagText(p)}</div>`
- `tagText(p)`：`p.id === 'handoff'` → `'交接'`（widget 侧用 CONTEXT.md 正名，等 daemon
  label 换装后两边自然一致；注释说明这句）；否则 `p.label || p.id`。
- `brandDot(p)`（app.js 内小表+函数，放 labelOf 附近，注释写品牌色调研结论一句）：
  id 或 label 命中即返回：kimi→`#1783FF`；glm 或 智谱→`#4268FA`；deepseek→`#4D6BFE`；
  handoff→`var(--cmonth)`（紫 #9B7EDE，widget 自家强调色，不是品牌色，注释说明）；
  未命中→`#8A93A6` 灰。比较时拉丁字母转小写。
- CSS（style.css 紧凑档块附近，普通类名即可——渲染已被 JS 档位门控）：
  ```css
  .mini-tag{display:flex;align-items:center;justify-content:center;gap:4px;
    margin-top:1px;font-size:10px;line-height:1.15;color:var(--txt-dim);
    max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
  .mini-tag .dot{width:7px;height:7px;border-radius:50%;flex:none}
  ```
- **完整档零变化**：full 模式不渲染 mini-tag（圆心 c-label 已承载名字）。

## 2 Spec B · 交接盘紧凑档大数字

handoff 分支加紧凑档覆盖（与 coding_plan/paygo 的既有写法同构）：

- `hm`（handoffs_month）为数字且紧凑档：`center = <text x="50" y="60" class="c-pct">${hm.value}</text>`
  （不带 fill 属性——色走新 CSS：`body.compact .disc.handoff .c-pct{fill:var(--txt)}`，中性白）。
- `hm` 缺席且紧凑档：`center = <text x="50" y="60" class="c-pct">—</text>`（名字由标签行承载，
  不再重复 label）。
- **完整档两行布局（695 / 次·本月）逐字节不动**。

## 3 Spec C · 查询失败盘紧凑档顺带修

`p.error` 分支紧凑档：圆心只留一个大 ⚠——`<text x="50" y="60" class="c-pct" fill="#E8C33D">⚠</text>`
（告警黄，inline 字面量 hex，与 c-pct 既有 inline fill 用法一致）。原 ⚠+label 两行里的
label 行由 mini-tag 承载。完整档 error 分支不动。

## 4 Spec D · 设置窗帮助补一句

`widget/ui/settings.html` 图例与帮助（details.sec-help）里紧凑档说明处补一句：
「紧凑档每盘下方显示名字+品牌色小点；圆心大数字=套餐盘最紧环剩余% / DS 余额 / 交接本月次数。」

## 5 Spec E · COMPACT_H 重推导（CSS↔Rust 互指）

- 用 pw_measure.py 的路子在紧凑档视口测演示满载（4 盘+grip+标签行）`.widget` 内容尺寸。
- 按既有公式：`H = 内容高 + 面板内缩 12 + 余量 ≥6`，取整。预期 ≈320（标签行约 +13/盘×4）。
  以实测为准，别照抄预估。
- 宽度复核：mini-tag 不应加宽内容（DeepSeek 4 字+点 ≈55px < 60），实测确认；宽 80 不动。
- 更新 `COMPACT_H_LOGICAL` 与 ~56-63 推导注释（保持公式句式，换新实测数）；style.css 紧凑块
  顶部互指注释同步。
- `grep -n "272\|253" widget/` 把所有命中（含 lib.rs 内嵌测试、assert-static、pw_verify 的
  预算断言）逐一核对更新——内容预算断言改成新值（内容实测 ≤ 新设计内高）。

## 6 测试（全部在当前 shell 跑，不弹新窗）

1. `cd widget/src-tauri && cargo test`——全绿（design_size 相关单测若硬编码 272 一并改新值）。
2. `node widget/ui/tests/assert-static.mjs`——90+ 全过；新增静态断言：
   app.js 含 `mini-tag`、三个品牌色 hex、handoff 的 tagText='交接'；style.css 有 .mini-tag
   与 .disc.handoff .c-pct 规则；lib.rs 注释与常量同新值；settings.html 帮助句在场。
3. `python .scratch/usage-widget/pw_verify.py`——25 检查全过 + 新增（d 组）：
   - 紧凑档渲染 4 盘，每盘 `.mini-tag` 存在且文本=GLM/Kimi/DeepSeek/交接；
   - 点色：kimi `rgb(23, 131, 255)`、glm `rgb(66, 104, 250)`、deepseek `rgb(77, 107, 254)`、
     handoff `rgb(155, 126, 222)`；
   - 紧凑档 handoff 盘：恰一个 `.c-pct` 且文本=demo 次数值，无 `.c-sub`；
   - 完整档（同页切档或另一 case）：mini-tag 不存在（querySelectorAll('.mini-tag').length===0），
     handoff 圆心仍是两行（c-money+c-sub）。
   - 紧凑档内容高度实测 ≤ 新预算。
   （脚本若需起 http.server/注入 __TAURI__，沿用现有 pw_verify 的既有做法。）

## 7 自验汇报（写在最后）

逐文件改动清单 + 内容实测数（旧 253→新）+ 三条测试命令的原始输出尾部（如实，失败也照贴）。
