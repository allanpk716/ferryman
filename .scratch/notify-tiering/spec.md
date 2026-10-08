# Ferryman 通知分级与降噪 · 规格

> 出处:xcheck 夜链 20261008(评审环 20261008-184054/185312,object=.xcheck/20261008-185312/proposal.md,decisions D1~D8,FINDINGS F1~F4)。本规格只继承已确认共识、必要修复与验收预期。

## Problem Statement

Ferryman 九类事件全部双通道推送(Pushover 手机 + Windows Toast 桌面),无分级。生产实测 2026-10-01~10-08 约 120~160 条,日均 15~20 条,用户被手机推送淹没;真正需要人知道的故障信息被常规提醒稀释。两个大户:摆渡链级间滑落告警(每次滑落一条、无节流,日均 5~10 条)与拦截通知 block(每次闸门拦截一条,日均 8 条,而人被拦时必在电脑前、界面已有反馈)。

## Solution

通知按事件分级:每类事件一个三值开关(off 不发 / toast 仅桌面 / both 双通道),缺省只把关键故障推到手机;滑落告警从「每次滑落一条」改为「顺位级状态变化制」——只在顺位下移时推一条,同级持续失败不推,恢复静默重置。用户生产 config 零写入即生效;按生产数据手机推送从日均 15~20 条降到约 1~2 条。

## User Stories

1. 作为用户,我只想在手机上收到关键故障(链全死、升级失败、熔断、静默门硬切),以便不被常规提醒打扰。
2. 作为用户,我提交被闸门拦截时想在桌面当场看到气泡(而非手机事后推送),以便立刻知道该开新会话。
3. 作为用户,我想在本地模型挂掉时收到一条(且仅一条)「链已滑落」的推送,以便知道摆渡质量与成本结构变了,但不想每次摆渡失败都被重复轰炸。
4. 作为用户,我想通过 config.toml 或工作台设置视图逐事件调整 off/toast/both,以便将来找回某类推送。
5. 作为用户,我想在不手写任何配置的情况下拿到新缺省行为(未配置即回落内置缺省、盘上零写入),以便缺省值随程序升级演进。
6. 作为用户,我在设置视图保存任何配置时,手写的事件开关不被静默清除或物化,以便我的显式选择保留、未配置项继续跟随缺省。

## Implementation Decisions

- **事件枚举与缺省表**(D2):九事件——block=toast;chain_degrade=both(仅首推);chain_skeleton=both;breaker=both;upgrade=both;hard_cut=both;drift=both;tuning=off;tray_reply=toast。事件名即配置键。
- **配置形状**(D5+F1):events 为 [notify] 节内的**内联表键**(`events = { block = "toast", ... }`),全文统一记法「notify.events 配置组」,禁止 [notify.events] 独立子表头(子表头触发设置视图节级整写原语对带子表节的保守拒,整个 [notify] 节将不可写)。
- **取值合法性**:off/toast/both 三值;非法值按配置加载既有错误路径处理。
- **开关判定层级**:[notify].enabled=false 仍全静默(总开关优先);[notify].pushover / .toast 通道开关语义不动;事件值 both 且通道开关关则该通道不发(通道开关是通道维度上限,事件值是事件维度分派)。
- **滑落状态机**(D3+D8):守护进程内存态,状态=最近一次成功到达的顺位,冷启动=0(链首);顺位下移(本次到达顺位>记录)推一条,文案含新顺位与失败原因,滑至链尾(kimi-k3,再失败即骨架)时文案体现「最后一站」严重度;同级持续失败不推;上移恢复静默重置。不持久化(重启丢状态属已确认取舍)。全链骨架 sync.Once 与逐次记账(err 字段)不动。
- **通知函数形态**:notify 包提供按事件名分派的能力(NotifyAlert 增事件参数或等价新函数);九个触发点各自传事件名:gate→block;worker 滑落→chain_degrade、骨架→chain_skeleton;watcher/watcher_dsh 六处→breaker;main.go 升级结果与 supervisor 事务告警→upgrade;静默门硬切→hard_cut;drift→drift;tuning 三函数→tuning;trayCheckUpdate 与 restart→tray_reply。文案与标题构造(BuildTitle 降级链)不动(D6)。
- **设置视图读写面适配**(F1 必要修复):读面(settings_read)notify 段返回 events 键(生效值=用户配置回落缺省,仅供展示);UI(ferryman-settings)通知节加九事件三值选择,notifyBody() 回传完整 events 对象;**写入器等值省略规范化**——写盘前对 events 各键与内置缺省表比对,等值键不落盘:未配置键保存后仍缺省、盘上不新增、缺省可随代码演进。已知取舍:用户显式配置值恰等于当前缺省时被省略,该键回落随缺省演进(记入 ADR)。密钥合并、写前快照、审计行纪律不动(D6)。
- **文档**(D7):CONTEXT.md「摆渡路由」词条降级告警句改状态变化制;新增「通知分级」词条;ADR-0026 记录噪音数据、两大户、状态变化制取舍、T25 行为修订依据、等值省略协议及其取舍。

## Testing Decisions

- 单元/行为测试(不测实现细节,测外部行为):
  - config:events 解析(内联表键、三值、非法值拒载)、缺省回落、与既有 notify 五键共存。
  - notify:事件分派(九事件×三值×通道开关×总开关组合的发送矩阵,可枚举全覆盖)。
  - worker 状态机:同级失败零推送/顺位下移恰一条/恢复零推送/链尾文案/冷启动重置(纯函数化状态转移,不真发送)。
  - 设置视图往返(F1 解除证据):①读→保存(不改 events)→盘上 events 相关字节不变;②UI 改一事件→盘上仅该键变化(等值省略规范化生效);③等值省略取舍例:显式配值=缺省→盘上无该键。
  - 既有测试更新:notify_test/tuning_test/gate_async_e2e/notify_wiring 断言随新分派更新;cutover smoke 的 NotifyBlock 空操作 seam 兼容。
- 仓库先例:internal/notify 既有 httptest 假端点+mock runToast 惯例;settings_write_test 节级写测试惯例。

## Out of Scope

- claude-notify 插件侧(共用 Pushover 凭据但独立;用户砍完仍吵再另开线,D1)。
- 事件×通道细分矩阵扩展(off/toast/both 已覆盖全部已确认需求)。
- 通知文案/标题改写、时间窗聚合摘要(已被状态变化制取代)。
- 滑落状态持久化与恢复抑制窗口(部署后观测交替模式,日均≥3 独立故障周期再回用户决策)。

## Further Notes

- 验收口径:手机 Pushover 端条数(桌面 Toast 不计入);行为断言引用生产账本重放(10-08 现网 12 条→状态制 2 条,塌缩 6:1,.xcheck/20261008-184054/exp/replay-chain-degrade.md)。
- 部署后观测项:交替模式每故障周期 1 条属预期;守护重启后首个滑落重推一条属已确认取舍。
- 发布形态:代码入夜链分支;生产换装走既有发版通道(tag 触发,发版资产名硬契约不变)。
