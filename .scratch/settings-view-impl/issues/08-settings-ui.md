# 票08 · 设置视图 UI 实现（Kimi 承担）

## What to build
把定稿 mock（widget/ui/mock/settings.html）原地长成真实现：新建 `widget/ui/ferryman-settings.html`（或等价命名）+配套 js/css（零构建 vanilla、零外部资源、内联 SVG 图标）。数据层把 mock 的假数据换成真端点调用（GET /settings 渲染全部 8 组+备份区；节级 PUT、实体 PUT/DELETE、POST snapshots、{id}/restore、POST dock/switch；daemon 地址与鉴权随 widget 既有 data.js 机制）。交互严格按 mock 定稿：密钥只显尾四位+「留空则不修改」（提交时省略该字段）；高风险二次确认（文案照 mock）；中风险改动预览；常规 toast；生效徽章按 GET effects 操作级渲染；「新增上游后须重启才能切」提示。**重启按钮渲染为禁用态+提示「安全重启等待后端定案（见晨报）」**（票07停靠）。新窗口在 tauri.conf.json conf 声明、启动常驻隐藏（ADR-0018-settings 铁律），入口=托盘菜单「Ferryman 设置」+现有显示设置窗加一个入口按钮。文案自查 renhua（无黑话、单位人话化）。

## 验收标准
- [ ] 断网双击打开零网络请求、零外部资源引用（静态断言）
- [ ] 8 组+备份区全部渲染真数据（数字对账：界面值=GET /settings 同字段值）
- [ ] 密钥任何界面状态只见尾四位；提交未改密钥时请求体不含该字段
- [ ] 三类风险交互与 mock 定稿一致；徽章操作级
- [ ] 新窗口 conf 声明+启动隐藏+关闭=隐藏；托盘与设置窗两个入口可用
- [ ] 重启按钮禁用态+提示文案
- [ ] cargo 既有测试零回归（不新增 Rust 逻辑测试；UI 静态断言可用 node/python 脚本或人工清单）

## Blocked by
票02、票03、票04、票05、票06

## 涉及路径
- widget/ui/（新文件：ferryman-settings.html 等，不改 index.html/settings.html 既有行为，settings.html 仅加入口按钮）
- widget/src-tauri/tauri.conf.json（新窗口声明）
- widget/src-tauri/src/（入口接线最小改动：托盘菜单项+open 指令，如已有 open_settings 模式照抄）

## 副作用声明
- 验证：静态断言脚本+浏览器实开冒烟（协调者做）；`cargo test`（widget 侧既有套件）在协调者允许时跑
- 不改 widget 主窗与显示设置既有行为

## decision_refs
D9（UI 由指定工具实施）、ADR-0021、ADR-0022、ADR-0018-settings（窗口铁律）、mock 定稿

## review_blocks
无
