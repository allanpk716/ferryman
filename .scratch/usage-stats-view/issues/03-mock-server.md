# 票03 · mock 服务:随机路径 + 到期自杀 + 本机验证

## What to build
端到端行为:后台启动服务脚本后,mock 页在 http://<本机IP>:8080/<随机段>/stats.html 可访问,根路径与无随机段路径一律 404;服务到设定截止时间自动退出;启动输出两条手机可达 URL(同 Wi-Fi 与虚拟局域网)。链接的推送由主会话完成(本票不推送)。

## 验收标准
- [ ] .scratch/usage-stats-view/tools/serve.py 存在:参数 --root/--token/--deadline(缺省明晨 08:00);仅用 Python 标准库
- [ ] 行为:GET / → 404;GET /<token>/stats.html → 200 且含页面标记;目录穿越防护(token 段白名单)
- [ ] 启动即打印:http://192.168.100.102:8080/<token>/stats.html 与 http://100.121.249.122:8080/<token>/stats.html
- [ ] 自杀:超过 --deadline 立即退出(时间判断在请求循环与启动时各查一次)
- [ ] 以 run_in_background 由 Claude Code 会话启动(继承隐藏控制台,零闪窗;禁 wscript/计划任务形态)
- [ ] 本机验证记录:curl 根路径 404、随机段 200 的退出码与状态

## Blocked by
票02(要服务的页面)

## 涉及路径
- .scratch/usage-stats-view/tools/serve.py(新增;评审临时件,不进产品树)

## 副作用声明
独占端口 8080;一个后台长驻进程(带截止)

decision_refs: D5, D6, D10
review_blocks: 无(F4/F10 已解除,本票为其落地)
