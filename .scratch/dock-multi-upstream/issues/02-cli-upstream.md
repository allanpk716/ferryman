# 票 02 · CLI:`ferryman upstream list / use`

## What to build

用户用一条命令管理渡口上游:`ferryman upstream list` 列出全部条目——当前 active 标注、base_url、model_map 概要、可用状态(缺 api_key 显示"未配置,需手编 config 填 api_key")、密钥脱敏(只露尾 4 位)。`ferryman upstream use <名>`:条目不存在→拒绝并列出可用条目;缺 api_key→拒绝并提示先填 key;有效→提示"在途请求将被中断"→校验配置→**原子写** config 的 active(临时文件+rename)→触发守护重启(POST /shutdown 停旧;detached 拉起 serve,复用钩子自举同款机制)→轮询健康检查(/stats)→成功输出新 active 与"内存缓存快照已清空,旧会话按冷启动全量重付"提示。健康检查失败→命令非零退出,如实报告"配置已切换为 <名>,守护进程未起来",给手动拉起(ferryman serve/看门)与回退(upstream use cc-switch)指引;**不自动回滚、不自动重试**。

## 验收标准

- [ ] list 输出含:全部条目、active 标注、base_url、model_map 概要、缺 key 状态、密钥脱敏
- [ ] use 对不存在条目/缺 key 条目正确拒绝,报错含可用条目清单或补 key 指引
- [ ] use 成功路径:active 原子写回 config;守护被停旧并拉起;健康检查通过后命令成功退出并输出冷启动提示
- [ ] use 失败路径:健康检查超时→非零退出+如实状态报告+手动指引;配置保持已写状态(不回滚)
- [ ] 在途请求中断提示出现在 use 输出中
- [ ] 重启机制可注入测试(启/停/健康检查接口可替换,不强制真拉长驻进程)
- [ ] `go test ./cmd/ferryman/... ./internal/config/...` 全绿

## Blocked by

01

## 涉及路径

- cmd/ferryman/
- internal/config/

## 副作用声明

- 独占验证命令:`go test ./cmd/ferryman/... ./internal/config/...`
- 测试不得真正拉起长驻守护进程(用注入接口)

decision_refs: D1, D10, D14
review_blocks: F7
