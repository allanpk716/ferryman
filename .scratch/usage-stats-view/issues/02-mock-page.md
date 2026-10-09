# 票02 · mock 页 stats.html(界面初稿由无头界面工具产出)

## What to build
端到端行为:浏览器打开 stats.html 即渲染深色统计仪表盘,数据来自同目录 stats.data.js——KPI 五卡(不可算项如实标注)、GitHub 式日历热力图(四档切换/自适应窗/负值日冷色)、明细表(三筛选+分页浏览抽样数据)。**界面初稿由本机 kimi CLI 无头产出**(任务书由泳道生成,文件头注记出处),泳道 agent 负责验收、静态断言与机械修复,不改设计。

## 验收标准
- [ ] widget/ui/mock/stats.html 存在,kimi 产出注记在文件头(泳道只做机械修复)
- [ ] 深色令牌:复用 widget/ui 现有 CSS 变量族(底 #0b0d12/#101218、面板 #171b26/#1c2230、主蓝 #5B9BD5/#9CC4E8、功能色 #E8A33D/#9B7EDE/#6BBF8A/#4FC3C8、告警 #E8C33D/#E85D5D;system-ui 13px);零构建 vanilla JS、零外部资源(无 CDN/字体/图片外链)
- [ ] KPI 五卡:总成本/总请求数/真实消耗 tokens/缓存命中率(tooltip 公示公式)/节省额;computable=false 的卡显示「不可算」+note 原文,不显示假数
- [ ] 热力图:行=周一..周日、列=周、月份标签;窗口起点=2026-07-30;四档切换(Tokens/请求/成本/节省额,成本/节省额不可算时整档显示不可算态);色阶=非零日四分位蓝色梯度;负值日=冷色(青系)单独梯度+图例注明;格子 hover 显示当日数值
- [ ] 明细表:列=时间/项目/会话(session_id 短截断)/模型/四列 token/成本(可算时);时间范围+项目+模型三筛选(前端过滤抽样数据);分页或滚动加载
- [ ] 页面在 390px 宽(手机)与桌面宽下均可读(媒体查询或自适应布局)
- [ ] 静态断言:widget/ui/tests/assert-stats-mock.mjs 仿 assert-static.mjs 先例(node 直跑:断言文件存在/关键挂载点 id/令牌变量引用/stats.data.js 可被 JSON.parse(去包装)/无外部 URL),跑通零失败

## Blocked by
票01(stats.data.js 是页面数据源)

## 涉及路径
- widget/ui/mock/stats.html(新增)
- widget/ui/tests/assert-stats-mock.mjs(新增)

## 副作用声明
无(不跑全仓测试;断言脚本单独 node 运行)

decision_refs: D2, D4, D7, D8, D9
review_blocks: 无(F4/F5 已解除,本票为其落地)
