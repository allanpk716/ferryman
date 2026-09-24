# 票 03 · widget M2:网络失败退避 + 恢复即拉(含代际防护)

## What to build
把 widget 数据层轮询从固定 30s 定时器改为自适应 setTimeout 链,并接通"恢复即拉":
1. **退避**:失败后 10s 起指数退避(10→20→40→60s 封顶,连续失败计数封顶 4),成功复位 30s;导出常量 `RETRY_MS=10000`、`RETRY_MAX_MS=60000`。
2. **代际防护**(评审 F2,必须):`let timer=null, stopped=false, fails=0, gen=0`;`loop(myGen)` 入口与 `await tick()` 之后双检 `stopped || myGen !== gen` 失效即 return(不再 setTimeout);`poke()` 先 `gen+=1` 再 `clearTimeout(timer)` 再 `loop(gen)`。不变量:任意时刻至多一条调度链;请求在途时 poke 允许两个幂等 fetch 短暂并行,旧链自弃。
3. **恢复即拉**:Rust 侧在 widget 窗口的两个恢复入口(单实例回调、托盘菜单"显示悬浮窗")各 emit 一个无载荷事件 `widget-restored`;app.js 持有 `poller = data.startPolling(...)` 并 `t.event.listen('widget-restored', () => poller.poke())`(与既有 profile-changed 监听同一 `t` 守护块作用域)。
4. **兜底硬化**(评审 F10 附注,一行级):`resolveDaemonTarget()` 调用纳入 try/catch 失败处理,任何意外异常视同本轮不可达走退避,绝不永久杀死链。
5. **返回值形状**:`startPolling` 返回 `{stop, poke}`(破坏性变更,唯一调用方 app.js 同批适配)。
6. **静态断言追加四条**(评审 F9 教训:**断言正则必须与所开代码形态逐字匹配**,代码是 `setTimeout(() => loop(myGen), delay)`,断言必须用 `/setTimeout\(\(\) => loop/` 这类能命中的形,严禁裸 `setTimeout(loop` 字面量):
   - 退避常量+自适应轮询在场(RETRY_MS/RETRY_MAX_MS/setTimeout(() => loop)
   - 代际防护在场(声明行含 `gen = 0` 且存在 `myGen !== gen`)
   - startPolling 暴露 poke(`poke()` 与 `return { stop:`)
   - app 监听恢复事件即拉(`widget-restored` 与 `poller.poke()`)
7. **SMOKE 人工清单**按修正口径写:灰化后重试间隔 10→20→40 递增;daemon 停够约 2 分钟(退避到 60s 档)再重启,小窗在**当前退避间隔内自愈(最坏 ≤60s)**;托盘收起→显示数据**立即**刷新;"立即"语义只属托盘/单实例路径,daemon 自行复活无事件、靠退避收敛——此边界写进清单,不得写成"≤10s 自愈"。

## 验收标准
- [ ] data.js 新实现含代际防护与退避,`node widget/ui/tests/assert-static.mjs` 全过(计数 +4)
- [ ] 断言四条与代码形态逐字匹配(特别是箭头包装调用的正则),无永假断言
- [ ] app.js 持有 poller 并监听 widget-restored;lib.rs 两处 emit(单实例回调+托盘 show 分支);setup 初显不加 emit
- [ ] `cargo check --manifest-path widget/src-tauri/Cargo.toml` 通过,零新增告警
- [ ] SMOKE.md 新节按修正口径撰写(60s 上限+语义边界)
- [ ] resolveDaemonTarget 调用处于失败处理覆盖内(意外异常走退避不杀链)

## Blocked by
无,可立即开始

## 涉及路径
- `widget/ui/data.js`
- `widget/ui/app.js`
- `widget/ui/tests/assert-static.mjs`
- `widget/ui/tests/SMOKE.md`
- `widget/src-tauri/src/lib.rs`

## 副作用声明
`node widget/ui/tests/assert-static.mjs`;`cargo check --manifest-path widget/src-tauri/Cargo.toml`(两个票内独占验证命令;cargo check 会写 target/ 缓存,不清理)

decision_refs: D5
review_blocks: F9(解除:断言正则与代码形态匹配且实跑全过), F2(解除凭据:代际防护代码+断言落地)
