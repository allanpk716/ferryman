# DSH 宿主插件失效防护——实施规格(dsh-host-guard)

> 来源:夜链 20261008-154613;object=.xcheck/20261008-160103/proposal.md(rev1)+同环 decisions/FINDINGS。
> 事故背景与全部取证事实见 object「背景与事故事实」节(7 条,全部 2026-10-08 生产实锚),本规格不重复,只引用。

## Problem Statement

2026-10-08 事故暴露三个结构性缺口:

1. daemon 的热缓存压缩指令不区分「谁领取后能执行」——只有播种条目(无活 agent)的宿主也会领走指令,执行不了、每 270 秒重发,5 个会话空转一下午;真正持有活 agent 的宿主从未参与。
2. daemon 与 doctor 对「宿主侧插件死亡」完全失明——桌面端插件 10:22 随换装重启死亡,没有任何检测面,直到 15:12 用户回流以 69,978 tokens 全价重付的形式撞上。
3. doctor 的 daemon liveness 判定与事实不符——daemon 双口监听中仍报「未运行」,狼来了效应侵蚀真报警的可信度。

## Solution(用户视角)

- 空转不再发生:压缩指令只发给「本次轮询时声明自己持有该会话活 agent」的宿主;没有任何宿主能执行时根本不发。
- 宿主插件死亡变得可见:每个宿主插件带身份心跳,daemon 维护持久化基线;「应在线而沉默」的宿主在 5 分钟内进入看门日志与 daemon 日志,doctor 能点名;正常关闭与间歇宿主不误报。
- doctor 的 daemon 活性判定与端口监听事实一致。

## User Stories

1. 作为用户,我希望闲置会话的压缩指令只到达能执行它的宿主,以便压缩链不再空转、日志不再被无界重发刷屏。
2. 作为用户,我希望升级窗口期(新 daemon+旧插件)行为与今天完全一致,以便换装不引入新的静默失效。
3. 作为用户,我希望某个宿主的插件死亡时,看门日志和 daemon 日志在几分钟内留下显著痕迹、doctor 能点出它的名字,以便我不必以全价重付的形式发现事故。
4. 作为用户,我希望正常关闭宿主、或间歇使用的宿主(headless)不产生 doctor 误报,以便报警保持可信。
5. 作为用户,我希望 daemon 换装重启后仍记得哪些宿主该在线,以便「插件先死、daemon 后重启」的形态不逃过检测。
6. 作为用户,我希望 doctor 报 daemon 活着就是真的活着,以便我信任它的每一声报警。
7. 作为用户,我希望旧协议轮询体(不带新字段)被宽容接收,以便插件与 daemon 不必严格同步升级。

## Implementation Decisions

### A. 轮询协议扩展(票 01/02 共用载体)

`/dsh/poll` 请求体从 `{agent:"dsh", sessions:[{sid, idle_s}]}` 扩展:

- 每会话增 `live`:`true`=注册表条目持有活 agent 引用(RegistryEntry.agent 非空);`false`=无(播种条目/事件残影);**键缺失=未知**(旧插件体)。
- 顶层增 `poller`:宿主身份名。插件侧从自身模块路径(`import.meta.url`)解析 `~/.dsh/profiles/<name>/node_modules/ferryman-dsh/` 形态取 `<name>`;解析不出时用 `unknown-<短随机后缀>`(进程内稳定,区分非标准路径的多个实例;F10)。
- 未知字段向前兼容:daemon 端 Go 侧按 map 收,插件侧 TS 按宽容解析,双方对未知键忽略。

### B. 触发面聚合语义(票 01;夜链裁定,评审记录 F5)

daemon 对某 sid 是否「可执行压缩」的聚合规则钉死为:

- **近窗(90 秒)内任一 poller 对该 sid 报 `live=true` → 可执行,照常按既有六条件触发;**
- **仅当近窗内有显式上报、且所有上报该 sid 的 poller 全部 `live=false` → 压制(不下发);**
- **近窗内无任何显式 live 上报(全缺键/无轮询)→ 未知 → 维持现状照旧触发**(与今天行为一致;旧插件空转由 D 退避兜底)。

「健康双宿主形态」(桌面 true + web 播种 false 交错)在此语义下恒为可执行——false 不压制 true。

### C. 领取(派发)面路由(票 01;夜链裁定,评审记录 F6)

指令在槽内时,DshPoll 应答规则从「谁把 sid 列进清单就给谁」收口为:

- **本次轮询对该 sid 报 `live=true` 的宿主 → 可领取;**
- **报 `live=false` 的宿主 → 不派给(即便列了该 sid);**
- **键缺失(旧协议体)→ 可领取(兼容例外,维持现状);其空转由 D 退避兜底。**

派发判定只看「本次轮询体里该 sid 的 live 值」,不依赖跨轮询记忆(无新锁序)。

### D. no-agent 退避(票 01)

- 同一会话连续 no-agent 失败后进入退避:**固定 30 分钟,不递增**(F7:噪音有界=每 30 分钟一轮,如实接受;递增律不引入)。
- 退避解除条件(任一):该 sid 再次被报 `live=true`;该会话台账 last_write 前进(用户回流)。
- 退避期内触发扫描对它早退,不再入槽;`领取即上报 no-agent` 的既有行为保留(可观测性)。

### E. poller 基线与生命周期(票 02)

- daemon 数据目录维护 `dsh-pollers.json`:`poller → {first_seen, last_seen, offline}`;变化时惰性落盘(心跳节律下至多每轮一次);损坏即弃、按后续心跳重建。
- daemon 启动时加载基线(跨重启记忆);**首建前盲区与 24h 检测视界(F8)为显式验收边界**:基线文件不存在时不判任何沉默;静默超 24h 的 poller 按自然退役处理(doctor pass 注记),不作 fail。
- 生命周期:插件 dispose 时尽力上报下线一次(best-effort,不重试;F9 残余=若恰逢 daemon 不可达则丢标记,最长 24h fail 误报窗、下轮心跳即消除——显式接受,不做重试)。
- doctor 检查项(新增):`fail` 仅当「24h 内有过心跳 ∧ 无下线标记 ∧ 静默 >90 秒(3× 默认轮询间隔)」;其余 pass,附名字与 last_seen 注记;从未见过任何 poller → pass 注记。

### F. 主动暴露面(票 03)

- `/stats` 应答增 `pollers` 段:每个 poller 的 `{name, last_seen, age_s, state(online/stale/retired/offline)}`。
- daemon 在 poller 状态「活→静默」跃迁时打显著日志行:`[poller] <名> 静默>90s(最近心跳 <时间>)`(跃迁制,不逐轮刷屏)。
- `ferryman watchdog` 单次探活(internal/installer/watchdog.go RunWatchdogCLI)在 daemon 可达时顺带检查 /stats 的 pollers 段:发现 stale(应在线而沉默)在看门日志记显著行——既有 5 分钟计划任务节奏原样复用,不改任务定义。
- 推送通知(Pushover 等外呼)不进本期(daemon 禁外呼纪律);晨报声明为后续票候选。

### G. daemon liveness 判定修正(票 04)

- 先读码钉死 doctor 现有 liveness 判据(internal 内 doctor 实现处),针对「daemon 监听中但被判死」的形态修正(候选:探测超时过短/并发探针失效/pid 文件陈旧,以读码结论为准)。
- 回归断言:daemon 端口监听中 → liveness 必 pass。

## Testing Decisions

- 票 01:internal/daemon/compact_trigger_test.go(聚合语义三分支+双宿主混合用例)、compact_test.go(派发路由:live=true 可领/false 不派/缺键可领;交错领取用例)、退避用例(连续 no-agent→30min 早退;live=true 再报解除)。插件侧 test/compact.test.ts+registry.test.ts(live 值=agent 引用非空;poller 名推导含 unknown 后缀)。只测外部行为(poll 请求/应答、触发/派发/退避可观测结果),不测内部函数。
- 票 02:基线持久化 roundtrip(daemon 重启恢复)、损坏重建、生命周期矩阵(online→stale→fail;offline→pass;>24h→retired→pass;首建前→pass 注记)、doctor 检查项各分支。插件侧 dispose 上报触发用例。
- 票 03:/stats pollers 段形状、跃迁日志行、watchdog 消费行为(模拟 stale 基线→看门日志行)。
- 票 04:liveness 回归(监听中必 pass;真死必 fail)。
- 既有套件零回归:全仓 `go test ./...` 与插件 `node --test`。

## Out of Scope

- 桌面宿主重启与插件加载失败根因闭合(用户操作,晨报给命令)。
- 闸门状态机、摆渡链、DSH 宿主侧代码(本仓只管 daemon 与插件)。
- 推送通知外呼。
- 看门计划任务定义/节奏改动(只消费不改任务)。

## Further Notes

- 修复 1/2 的生效前置=宿主换装新插件并重启(D3:夜链不碰宿主);「代码已修≠生产已防护」,晨报验收口径两分(F11)。
- 事故取证事实、评审两环全部记录:.xcheck/20261008-154613/(round0)与 .xcheck/20261008-160103/(round1+附录)。
- F5/F6 裁定先例:20260919-202930 链 F11(渡口 opt-in)。
