# 服务商接管 spec(v0.4.0 载具;2026-09-30 夜链固化)

> 来源:战役计划 rev1(评审收敛,1 轮修订,F1-F6 解除)+ 决策快照 D1-D12。术语遵 CONTEXT.md(渡口=本机代理 15722;摆渡=闲置会话上下文整理;蜂群=会话自带探活拉起钩子)。目标终态:卸载 cc-switch 后重启三件全绿。

## Problem Statement

本机 CC 与 codex 的供应商配置目前由 cc-switch 托管:它开机自启、随会话重写两家编辑器配置(曾整文件重写抹掉别人的行、曾把 CC 流量偷旁路),且 codex 只有经它做协议翻译才能用智谱。用户要 Ferryman 自己接管这两面:渡口成为唯一门,切换供应商不再改任何编辑器配置,cc-switch 可以卸载。

## Solution(用户视角)

接管后,CC 与 codex(含 orca 生态的 codex)的配置一次性写死指向渡口;此后切供应商=一条 `ferryman provider switch` 命令,渡口内部换上游,**在跑会话不断流、不用重开会话**;供应商条目(含密钥)从 cc-switch 一键导入;接管动作自带三份配置备份与一键还原;每次开机体检能立刻发现配置被动过。对 codex 不支持的供应商,切换会被明确拒绝而不是悄悄弄断。

## User Stories

1. 作为本机用户,我想让 CC 与 codex(含 orca 生态)都只认渡口一个门,以便供应商配置从此只有一个真相源、不再被外部工具改来改去。
2. 作为本机用户,我想用一条命令切换供应商且在跑会话不断流,以便不打断手里的工作。
3. 作为本机用户,我想把 cc-switch 里已配好的供应商(含密钥)一键导入,以便迁移不用重抄。
4. 作为本机用户,我想接管动作自带备份与一键还原,以便出问题能立刻回到接管前的状态。
5. 作为本机用户,我想体检(doctor)能发现"配置被谁动过"(CC 指向/codex 指向/orca codex 健康),以便"开机被偷"类事故不再静默。
6. 作为开发者,我想让 codex 翻译车道严格按经评审的两方言对照表实现并被回放夹具验证,以便协议怪癖不靠猜(仓库既有规矩)。
7. 作为本机用户,我想切到对 codex 不支持的供应商时得到明确拒绝与原因,以便不会不知不觉把 codex 弄断;显式放行时也能得到明确后果告知。
8. 作为本机用户,我想渡口新增车道首版只记账(事件+账本四列),以便先观察再扩展。

## Implementation Decisions

**术语**:Anthropic 方言=CC 说的 /v1/messages 线协议;responses 方言=codex 说的 OpenAI /responses 线协议;翻译车道=渡口内把 responses 逐句转成 Anthropic 的新车道。

1. **供应商数据模型**(扩既有渡口上游表,不另起炉灶):每条目新增
   - `dialect`:"anthropic"(缺省,既有行为不变)| "openai_responses"(原生 responses 线协议);
   - codex 可用性由 dialect 推导:anthropic→需翻译;openai_responses→原生透传;另设显式 `codex = "unsupported"` 否决位(推导可被显式覆盖);
   - 模型位映射扩位:既有档位键(default/opus/sonnet/haiku)之外加 codex 主模型键;
   - 既有字段(api_key、text_only、balance_url 等)语义不变;密钥仍只进本机 config(T39)。
2. **渡口路径分流**:渡口 HTTP 入口按路径分车道——`/responses` 及其变体(变体清单以对照表为准)进 responses 车道;`/v1/messages` 走既有车道,**既有车道行为零变化**。
3. **responses 车道职责**:
   - 请求向:responses→Anthropic 翻译(按对照表;GLM 目标注入 thinking:disabled,ADR-0015);model 字段按当前活跃供应商的模型位映射逐请求改写;未知模型名透传+记告警事件,不静默替换;
   - 响应向:Anthropic SSE→responses SSE 翻译(tool_use 流/用量/错误事件按对照表);
   - 原生 responses 供应商:同车道但透传不译(仅鉴权注入+model 改写);
   - 鉴权:剥入站占位凭证,按供应商注入真实密钥(与既有车道同机制);
   - 可用性消费:供应商被标"不支持"时不可成为 codex 上游——switch 默认拒绝(见 5);被显式放行后 codex 请求到站必须**即刻返回带原因的显式错误**,不挂起不静默超时(F11);
   - 记账:仅 dock 事件+账本四列;快照/摆渡不进 codex 车道(首版,D7)。
4. **切换语义(D5/F4/F2)**:渡口对每个新请求读内存态活跃供应商(逐请求取值);在途请求/SSE 流持有既有上游连接自然跑完;`switch` 通过守护管理口触发内存态原子换绑+落盘持久化,**不重启守护、无端口空窗**——"即时生效不断流"由此达成。既有 `upstream use`(排水重启)流程保持不动,不在本役改造。
5. **CLI `ferryman provider`**:`list`(条目+active+dialect/codex 可用性+模型位+密钥脱敏)/ `switch <名>`(不支持→拒绝并报因;`--cc-only` 显式放行并明示"codex 暂断供";切换后回显新 active 与 codex 车道模式)/ `add` / `remove` / `import-ccswitch`(读 ~/.cc-switch/cc-switch.db,sqlite 驱动已在依赖;claude/codex 两类供应商映射入表,密钥只落本机;dialect 按端点线协议推断)/ `apply`(跑写入器)/ `apply --restore`(按接管前备份还原)。
6. **配置写入器(apply)**:
   - CC:外科式改 ~/.claude/settings.json 的 env.ANTHROPIC_BASE_URL→渡口,其余键一字不动;
   - codex 两份(~/.codex/config.toml 与 orca CODEX_HOME 那份):base_url→渡口、保持 wire_api="responses"、`[features] hooks=true` 保住、PROXY_MANAGED 占位换成 Ferryman 占位(渡口注入真钥)、其余节(mcp_servers 等)不碰;前置校验认证形态为 apikey(非 chatgpt-OAuth),不符报错转人工(F7);
   - 备份:首次写前对三份配置逐一落时间戳备份(含 orca 份——现状无备份,本项补齐);`--restore` 按最近一次接管前备份还原回 interim 拓扑(F5);
   - 幂等:重跑=校验+补缺,不整文件重写。
7. **doctor 扩三项体检**:CC 指向渡口;codex 两份指向渡口且 wire_api=responses 且 hooks 旗标在位;orca codex 健康(配置存在、指向渡口、认证形态合法)。
8. **对照表(W1a)是翻译车道的规格**:先产出、先审表后审码;表必含"model 字段改写规则"节与端点变体清单;实现以参考实现(.scratch/cc-switch-ref,Rust 源码)为准,禁止臆造中转怪癖(仓库 CLAUDE.md 规矩)。

## Testing Decisions

- 翻译:对照表驱动的表测试(请求向/响应向/tool_use 流/用量/错误事件逐项往返断言);对未知字段透传不丢弃的用例。
- 车道端到端:httptest 假上游(Anthropic 方言假上游×翻译分支;responses 方言假上游×原生透传分支),渡口全链路回放四形夹具(正常/工具调用/长流/错误注入)。
- 夹具纪律(F3):全部为脱敏/重构的合成数据;测试辅助函数强制过"零真实密钥"扫描(鉴权头占位化);真流量重录工具只给白天 W4 用,夜链不联网录制。
- 切换边界(F4):假上游故意拖长流,切换后断言在途流在旧上游跑完、下一请求走新供应商、进程不重开。
- 拒绝语义(F2/F11):switch 到"不支持"→拒绝+报因;--cc-only 放行后 codex 请求→即刻显式错误。
- 写入器:临时家目录单测(三份配置各种现状形态:cc-switch 形/直连形/orca 镜像形),断言外科性(他人键原样)、幂等(二跑零差异)、备份与 --restore 往返。
- doctor:三项体检的真/假形态断言。
- 测试禁联网、禁真实密钥;进程派生遵守 Windows 零闪窗铁律;scoped `go test ./internal/...`。

## Acceptance(夜链内可验部分)

夹具回放四形全绿;切换边界/拒绝语义/写入器幂等与还原/doctor 三项的单测全绿;`go vet`+build 净。生产接管与一天实证、cc-switch 退出/重启终验/卸载三件复检属白天用户动作(W4 runbook 随票交付),不在夜链执行。

## Out of Scope

- gemini/opencode/pi 等 cc-switch 其他托管面(D1);
- codex 车道快照/同模型摆渡(D7);
- 面板 provider 页(D3 后补);
- 自动卸载 cc-switch、自动重写 orca 配置的常驻看护(风险表对策,未触发不建);
- 既有 /v1/messages 车道行为改造(D11);`upstream use` 排水重启流程改造;
- 生产 apply/W4 验收本身(用户白天执行)。

## Further Notes

- interim 拓扑(CC→渡口/codex→cc-switch 15721/会话自动同步已关)保持到用户白天 apply;cc-switch 自启在 W4 前不动。
- orca CODEX_HOME 已实证会被重写(2026-09-30 镜像 ~/.codex 一次);apply 后首周观察,若再被抢写,写入器加"修复"子命令(风险表对策,未触发不建)。
- W4 白天 runbook:apply→观察一天→托盘退出+自启关→重启+三件全绿终验(任一红=--restore 回 interim+手动拉起 cc-switch)→卸载→三件复检;切换验收建议各覆盖一次"需翻译"与"原生 responses"分支(F12);跨供应商混合历史第五夹具形未采纳、不立票(F8,白天可低成本顺手录)。
- 首周观察:doctor 报"CC 指向"即视为 cc-switch 会话自动同步复开(F9 归因指引)。
- v0.4.0 上线后自身升级照走既有全自动链(已 2/2 验收)。
