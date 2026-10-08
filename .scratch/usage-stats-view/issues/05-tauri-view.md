# 票05 · Tauri 壳接线:统计视图窗【门控票】

## What to build
端到端行为:用户在桌面打开 Ferryman Widget 程序,可进入「成本成效账」视图窗看到与 mock 同款的统计页,数据轮询 daemon 端点(30s 级);关闭窗口=隐藏,再开秒回。窗口声明式创建(运行时创建=永白屏,禁)。

## 验收标准
- [ ] tauri.conf.json 增加窗口声明(label/stats.html 入口/visible:false 起步/尺寸合理),**conf diff 入验收**
- [ ] src-tauri Rust 侧:入口菜单/托盘项(与设置窗同款先例)触发显示;本票 conf/Rust 侧归本泳道实施(界面文件已由票02 定稿)
- [ ] widget/ui/stats.html(成品页):基于 mock 页改造,数据源从 stats.data.js 换为 daemon 聚合/明细端点轮询(Bearer token 经壳 get_daemon_config,先例=widget/ui/data.js)
- [ ] 加载失败/不可算态显示与 mock 同款
- [ ] 静态断言扩展(assert-stats-mock.mjs 或新脚本覆盖成品页)
- [ ] 不弹新控制台窗口(零闪窗);`npm run build`/tauri 构建按仓库 widget 先例落日志验证(仅 widget 目录)

## Blocked by
**用户对 mock 的过目通过(D11 mock-first 门)**;票04(端点是数据源)

## 涉及路径
- widget/src-tauri/tauri.conf.json、widget/src-tauri/src/(Rust 入口)
- widget/ui/stats.html(新增,源自 mock 改造)

## 副作用声明
widget 目录构建验证(npm/tauri,输出落日志);无部署、不发版

decision_refs: D1, D4, D6, D11
review_blocks: 无(F6 已解除,本票即其落地;门控来自 D11)
