# Spec · 替换 cc-switch:渡口当总门(v0.5.3 收尾批 + v0.6.0 pi 面与统一供应商配置面)

> 夜链 20261002-080416 固化;对象=评审收敛稿(.xcheck/20261002-081606/proposal.md,两轮评审/一轮自动修订);决策=D1-D16(.xcheck/20261002-081606/decisions.md)。

## Problem Statement

cc-switch 作为本机供应商代理与配置切换器,每次开机/切档会擦掉 codex 的钩子与旗标(用户须人工"三件核查"),且 codex/pi 流量不经 Ferryman 渡口,费用记账、上游热切换与体检覆盖不到。用户要:Ferryman 完整替换 cc-switch,渡口当总门,优先智谱 Coding Plan,CC/codex/pi 三类 agent 统一接管;全部 Go 实现。另欠两笔债:v0.5.2 换装机制换代后的过渡代码未清(退役清单五处);CLI help 与文档面停在前版本(README 仍在教 Python 时代命令,help 存在 -h 触发真实写操作的缺陷,--json 覆盖率低且无脱敏契约)。

## Solution

- 三 agent 全部经渡口供流:CC(已接)、codex(翻译车道已备,生产切换人工执行)、pi(新增 provider apply 第四写入目标,anthropic-messages 复用 CC 车道);渡口管翻译/鉴权/上游热切换/费用记账。
- `ferryman provider` 成为统一供应商配置面:list/switch(热切换;对 pi 不可用上游默认拒绝、--cc-only 放行)/add/remove/apply(四份配置外科写入)/doctor 生效链体检。
- v0.5.3 收尾:退役五处清债、help/exit code/--json 契约统一、status/stop 新命令、文档面(AGENTS.md/CLI.md/README/遗留改正/ADR 索引/风险哨兵表)。

## User Stories

1. 作为用户,我想要 codex 的供应商流量经渡口,以便费用记账与体检覆盖 codex(生产切换人工执行;本规格补配置文档化与体检)。
2. 作为用户,我想要 pi 被同一套 provider 命令接管,以便 CC/codex/pi 一个入口管理供应商。
3. 作为用户,我想要 doctor 的 pi 体检按完整生效链判定,以便体检绿=pi 真走渡口,不会假绿误导我停用 cc-switch。
4. 作为用户,我想要切换上游时对 pi 不可用的条目默认拒绝并告知原因(--cc-only 显式放行),以便不会静默弄断 pi。
5. 作为用户,我想要 --json 输出永不含明文密钥,以便把 CLI 输出交给 agent 与日志不泄密。
6. 作为用户,我想要 ferryman status/stop,以便 agent 与脚本探活/优雅停守护不裸敲端点。
7. 作为用户,我想要每条命令 -h 都安全(绝不触发真实动作)且退出码契约一致,以便脚本与 agent 稳定消费。
8. 作为用户,我想要 AGENTS.md 与 CLI.md 自包含,以便新会话 agent 不读源码就会用本程序。
9. 作为用户,我想要 v0.5.2 换装换代的过渡代码清掉,以便代码面与机制一致。
10. 作为用户,我想要风险哨兵表,以便知道还有什么会炸、炸了怎么早点发现。

## Implementation Decisions

1. 全 Go,不转 Rust;ADR-0019 记录对比且不设重开触发条件(D2)。
2. pi 走 anthropic-messages 复用渡口 CC 车道,不建 chat completions 入站(D4)。
3. pi 写入器=provider apply 第四目标:models.json 供应商条目(api="anthropic-messages"/baseUrl=渡口/占位钥/models 列表自上游 model_map pi 键派生,空=异形拒绝)+settings.json defaultProvider 与 defaultModel 成对改写;两文件同批落盘,任一异形整体拒绝转人工;同戳备份+幂等短路+解析树自证照搬既有 writer 纪律;清理指向 15721 的死条目;条目形态对照旧 anthropic-messages 条目不带 compat(F1/F6)。
4. doctor 新项 provider_pi_dock:与写入器同一套解析(单源);绿=生效链全中(effective defaultProvider 解析到渡口条目 ∧ defaultModel ∈ 该条目 models ∧ api=anthropic-messages ∧ baseUrl=渡口);~/.pi 缺失→not_checked;四钉用例(错默认供应商/错模型/残留旧条目/未装 pi)(F1/F8)。
5. pi 可用性位:对标 codex="unsupported" 既有先例——provider switch 默认拒绝并报因、--cc-only 显式放行明示"pi 暂断供";provider apply 对 pi 目标同判,不可用拒写该目标、回显如实、其余目标照常不半写;model_map 增 pi 主模型键(F2)。
6. --json 脱敏契约:doctor/upstream list/provider list 三命令输出禁明文密钥/认证材料,统一尾4位掩码形态;CLI.md 字段表用脱敏示例;每命令加"输出不含明文 api_key"断言测试;导出真钥须显式受控命令,本链不建(F3)。
7. help/exit code:-h 一律 exit 0 且绝不执行真实动作(doctor/install-ccswitch 现状缺陷修复:非空未知参数报用法错退出);手工分发族(account/tuning/autostart/watchdog/upstream/provider/cutover)专属 usage 页;顶层 usage 补 update 条目并分组重构;0=成功/1=失败/2=用法错,数据空态与失败分离(backtest 空态改结构化标注+exit 0)。
8. status/stop:status=守护探活(管理口)+版本+渡口监听+台账摘要;stop=POST /shutdown 既有端点+排水窗等待,超时如实报告不硬杀(D9)。
9. 退役五处:internal/update 锁判活家族②、relayCopyPath、--self-relay 旗标、对应回归钉、supervisor-copy* 清扫模式(D12)。
10. upstream use 标弃用指向 provider switch;install-ccswitch 与 doctor 的 ccswitch_snapshots 检查进退役路径(标弃用/移除时机注记);import-ccswitch 保留为搬家工具(D15)。
11. 文档:AGENTS.md(repo 根,agent 使用契约)、docs/CLI.md(命令×旗标×退出码×--json 字段表×危险面)、README 瘦身(清 Python 残留)、DESIGN.md §4/TEST_PLAN.md 就地改正、config.go 与 cutover 注释 7311 清理、docs/adr/README.md 索引消歧、docs/20261002_剩余风险与哨兵.md(含配额应急边界与卸载前提两行)(D8/D10/D11/D13)。

## Testing Decisions

- 只测外部行为,不测实现细节:help 退出码契约(-h=0/用法错=2/未知参数不执行)、--json 无明文钥断言、pi 写入器异形拒绝/幂等/备份/两文件成对与部分失败恢复、doctor 生效链四用例、可用性位拒绝/放行文案、status/stop 输出形态。
- 仓库既有同类测试先例:internal/update、internal/provider 的表驱动单测(路径全参数化、真机零触碰)。
- 票级验收底线=涉及包测试绿;批二不改变换装/渡口/闸门运行行为。

## Out of Scope

- pi 会话面(守望/闸门/台账 agent 枚举/JS extensions);codex 与 pi 的快照/心跳/摆渡归因;非 Anthropic 方言上游(chat completions 入站翻译、dialect=openai_responses 真机验证);生产切换与 cc-switch 卸载操作(人工白天执行,晨报附步骤单);workbench 供应商视图;help --json(机器可读 help,后置)。

## Further Notes

- 实现期注记(非阻断):doctor 对"残留旧条目但生效链正确"=绿+警告行,与写入器清理测试分开钉(F9);pi 两文件部分失败=按同戳备份恢复或幂等重跑收敛,单测钉"第二文件写败→恢复后与基线一致"(F10);pi thinking 行为真机验证列入晨报(F6)。
- 晨报须附生产切换步骤单:补渡口智谱条目 model_map codex 键(小写 glm-5.3 别名或 codex 键)→ferryman provider apply→codex 真机验证→cc-switch 停自启退出→观察一天→重启终验→(确认智谱基线后)卸载;卸载前提与非 anthropic 方言边界见风险表(F4/F5)。
- 出处:本 spec 由夜链 to-spec 流程固化(改编自 Matt Pocock to-spec/to-tickets,ADR 0011)。
