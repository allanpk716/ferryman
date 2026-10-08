# tools/e2e_dsh/suite — 票08 E2E Playwright 全链剧本 ＋ 票03 多会话拓扑矩阵

对票07 沙箱栈的双面验收：**多会话拓扑矩阵**（票03 dsh-cross-inject，T1-T8，
"多会话拓扑"一等测试维度）＋ **DSH 热缓存压缩全链**（票07/08）。一键、零人工、
幂等重跑。

```bash
bash tools/e2e_dsh/suite/run.sh     # setup + 三相剧本（矩阵 → 跨profile → 压缩链）
```

## 组成

| 文件 | 职责 |
|---|---|
| `run.sh` | 入口：setup → driver → 汇总退出码；EXIT 时拆 web2 第二实例 |
| `setup.sh` | 栈整备：`../start.sh` 起栈 → **夜链工作树插件换装**进沙箱 profile 两落点（`profiles/web/ferryman-dsh` 与 `profiles/web/node_modules/ferryman-dsh`）→ **模型路由改指沙箱凭据自含的 `zai-coding-cn/glm-5.3`**（生产缺省的 deepseek-official 裸跑无密钥——生产本靠已剥离的渡口覆写；`llm-pi-ai` 适配器随 app 自带，挂上+密钥引用即通，密钥随 `~/.dsh` 副本携带，直连 z.ai 零生产接触）→ **会话转录落明文**（profile patch 追加 `session-persistence-jsonl` 的 `compression: none`——矩阵硬门禁要 driver 直读转录；none 后端拒收根内 zstd 工件，故顺带清空沙箱 sessions）＋ **web2 第二 profile**（`profiles/web` 整拷＋独立口 25903 起第二实例，跨 profile 拓扑用）→ config 追加 `[heartbeat].ttl_s`（缺省 0＝压缩链静默，必须给值）＋ `[dsh_compact]` 秒级参数 → 重启 daemon（吃新 config，数据目录不清）＋重启 web 实例×2（吃新插件，重取登录 token）→ 健康检查 |
| `driver.mjs` | 三相剧本＋断言库（全局 playwright，本仓零依赖；浏览器探针 DOM，账本/gate.log/index.json/会话转录断言走沙箱数据目录只读） |

## 票03 · 多会话拓扑矩阵（T1-T8，断言按票01 修复后行为）

三相编排：**A 拓扑矩阵@web1 → B 跨 profile@web2 → C 票08 压缩链@web1**。种子会话
（同目录四场）并行闲置攒交接后逐幕推进；单场景失败=记录后继续；场景间隔清残留
锚（残留 pending 会让零注入场景吃到锚定归还→假阳性——逐锚向 `/dsh/gate` 发强续
bypass 清场，不惊动任何会话 UI）。

| 幕 | 形态 | 断言（门禁层） |
|---|---|---|
| T1 接班基线 | 单人：A 被拦 → 手动新开同目录会话 | **硬**：接班（账本 inject 痕＋新会话转录含 `[Ferryman 交接`/被拦原话） |
| T2 事故复刻 | 同目录多主题：A 收工闲置（交接落盘）→ 新会话 B 问新主题 | **硬**：B 零注入（账本无 kind=inject＋转录无交接文本）；**软**：B 的回答不含 A 主题专名（敦煌星图）——只告警不拦截 |
| T3 多候选 | 同目录 2+ 会话各有新鲜交接 → 新会话 | **硬**：零注入（候选清单也不进上下文） |
| T4 锚窗边界 | 窗内（<2h，PendingAnchorS=7200 不动）被拦 / 无被拦待领 | **硬**：窗内→接班；无锚→零注入 |
| T5 子代理探针 | 被守望会话内模型经原生 `subagent` 工具 spawn 子代理 | **记录事实**（供票04）：created 是否触发（锚窗探针双向判定：线内留未消费锚，触发⟺子会话有注入痕）＋识别形态（子会话头行 `origin=subagent`/`parentSession`/`delegationDepth`；目录名=裸 uuid）；模型不调用=如实记"沙箱不可造" |
| T6 跨 profile | web/web2 两 profile 同目录各开会话（共用 DSH_HOME） | **硬**（无锚面）：profile2 新会话零注入；**观察项**（窗内锚+跨 profile）：只记录实际行为不断言 |
| T7 续用/强续 | 强续首条（A 相）／压缩后回来首条（C 相 t7c） | **硬**：放行＋bypass 落账＋零注入（与 v0.9.7 一致） |
| T8 一键新会话 | 被拦后走卡片「新会话继续」按钮（RPC newSession→agents.create） | **硬**：接班照旧（created 即锚定归还→inject 先落账；首条经宿主网关 `session/prompt` 送达——RPC 创建的 blank 会话不进侧栏列表，run2-4 实锚） |

**硬/软门禁分层（F6）**：硬门禁=账本（`kind=inject` 行有无）＋新会话转录（交接
文本标记集：`[Ferryman 交接`/`[Ferryman] 本项目有`/`完整交接文档`/`被拦时的原话`）
两面，红即拦合入；软信号=语义面（B 的回答不含对方主题专名），模型措辞不可控，
泄漏≠注入回归，独立输出（SOFT-WARN）不拦截。闸门 additional_context 的警告文案
（「建议新建会话，开场自动注入交接」）不含上述标记，不与交接注入混淆。

**压缩参数与拦截窗的互泳**：种子会话首轮计费即 ≈12-13K（glm-5.3 提示地板 ~9.4K
＋运行时上下文），普遍过 min_peak=12000 触发线——闲置会话周期性再压缩（标记窗
180s），拦截窗在「标记失效 ∧ 闲置≥block_s」的开窗段内（run-grace 300s > 标记窗
180s，每周期留 ~2min 可拦窗）；`waitBlockable` 循环等窗，两发式拦截（首发撞分支7
警告放行时紧接第二发验分支6）。产物：`matrix-report.md`（全量红绿）＋
`t5-subagent-probe.md`（探针结论）落 `<沙箱>/logs/e2e-driver-<runstamp>/`。

**矩阵跑出来的两个产品行为注记**（不属本票修复面，供后续票参考）：
1. **交接按 (agent,cwd) 线去重生成**——同目录多会话并行闲置时，只有"内容最新"的
   会话拿到自属交接（既有交接 covers+60s 容差盖过后写内容即不再生成）；T3 的
   "2+ 会话各有新鲜交接"前置靠错峰播种（covers 容差窗越过后再种第二场）造出。
2. **一键新会话的排队注入会在长闲置后被闸门当用户输入拦下**（2026-10-07 run4
   实锚：按钮创建的空白会话 3 分钟未发首条，排队的交接注入进 pre-step 批次→
   gate prompt=注入文本→block，且 pending 被注入文本覆写）——插件面把 injected
   消息与用户消息同批送审是根因；T8 剧本因此在按钮后立即经网关送首条避开。

## 票08 · 压缩链剧本与断言（票面清单映射，15 项零回归）

| 幕 | 断言 |
|---|---|
| token 登录 → 幂等新建会话 | composer 就绪；会话目录落盘＋**陈旧草稿自愈**（工作区会持久化上次页载的空草稿，点「新建会话」可能复用它——其闲置锚是旧的，首条消息会被拦死；剧本验鲜失败即再点新建，≤3 次） |
| 多轮灌上下文 | 账面前缀（三输入列之和）≥ `min_peak`（12000；glm-5.3 系统提示 ~12K，1-2 轮即过线） |
| 闲置等触发（0.5×TTL=30s） | ① 账本 `kind=compacted` 行且字段齐 ② poll→compactNow→compacted HTTP 往返（daemon 触发日志＋上报行＋`/stats` gate 计数推证） |
| 横幅 | ③ `[data-ferryman-banner]` DOM 出现；③b 随下次用户消息消失（容忍一个 4s 浏览器轮询周期） |
| 拦窗内发消息 | ④ 回合执行不被拦（无选择卡）＋ gate.log `mode=compacted-short-prefix` 新行 |
| 交接 | ⑤ `handoffs/` 有本 run 新文件（无渡口＝骨架降级落盘） |
| 降级·甲（压缩红利失效后照旧拦） | ⑥ 等闲置过线∧最后 ok 压缩标记过期（返工注记:上报加 2s 稳定窗后标记不再自杀,④ 回合后的二次压缩会以新鲜标记盖住本幕——只等闲置会把「照旧拦」测成「红利仍在、正确放行」;两发序内再遇标记落地的窄竞态以重试收口）→会话闲置过线后发消息→`[data-ferryman-blocked]` 选择卡出现＋`kind=block` 落账＋卡面携带被拦原话（RPC wire 真值） |
| 降级·乙（daemon 不可达） | ⑥b 停沙箱 daemon 后消息照发（pre-step 探测失败→fail-open 放行，回复到达），在办选择卡不增 |

票01（N2 并发查证）结论为「排队/干净报错」而非状态错乱，故无竞态断言需跳过/waiting（`../../.scratch/dsh-hot-compaction/research/n2-concurrency.md`）。

## 已知现状：主链①-④已随票05 返工转绿（2026-10-07；15/15 全绿两轮实证）

原缺口（票05 集成层）：宿主 web 组成里 compaction 服务对插件域不可达——web-app
bundle 把 compaction-basic 从 host 面摘掉、presets patch 用 `cordis:group
isolate:{compaction:true}` 把它隔离在 preset 组内，插件层 `ctx.inject('compaction')`
恒 UNDEFINED。返工后插件执行臂改双道：**首选原生命令道**（懒注入 `ctx.commands`
——命令注册表留在 host 面，未被 disable/isolate；带 receiver 调
`execute(agent,'/compact',[],signal)`，与宿主 UI /compact 同入口；execute 的
promise 即完成信号——handler 直 await compactNow，解析=压缩收口），compaction
服务面降为次选，两道皆缺如实上报 `reason:"no-compaction-channel"`。
连带三修：①新前缀读取从 `stateOf`（单元原始态，无 projectedTokens）改为
`snapshot` wire 视图——否则恒落 `pressureTokens` 兜底（压缩盲的旧请求压值，
prefix_tokens 报旧数）；②`min_peak` 4000→12000——glm-5.3 提示地板（system+tools
≈9.4K）高于 4000，gate 放行比较 `prefix<min_peak` 被垫成结构性不可达（实锚：
真压缩 4534 tokens 后 projected≈9320）；③成功上报前加 2s 落盘稳定窗——压缩
事件批落盘（200ms 批窗）的 mtime 晚于上报时，daemon 守望 Touch 顶新 LastWrite
按 `LastWrite>标记TS` 判死自家 compressed 标记（±0.2s 掷硬币；实锚：事件
42.598→上报 42.605→drain mtime 42.798→标记自杀→gate 放行链断）。克隆 file:line
锚点全录 `plugin/ferryman-dsh/src/compact.ts` 头注「执行」节与 `REPORT_SETTLE_MS`
常量注。

次级观察（未变）：选择卡「强续重发」动作（Remote resend→`agent.followup`）在本沙箱
真机抛错回「代发失败（会话可能已结束）」——卡片自带降级文案指路手打「强续」，
剧本⑥b 已按该降级路径回落手打。

## 时序参数（setup.sh 追加进沙箱 config；env 可覆写）

`ttl_s=60`、`trigger_ratio=0.5`（触发线 30s）、`min_peak_tokens=12000`（须卡在 glm-5.3
提示地板 ~9.4K 与可达峰值 ~13.4K 之间——4000 时 gate 放行比较 prefix<min_peak 被模型
地板垫成结构性不可达，真压缩后 projected ≈8.9-9.6K 也压不进 4000；生产缺省 2 万语义
本就如此）、`command_ttl_ratio=0.5`（指令 30s 有效，盖过插件 10s 轮询下限）、`poll_hint_s=2`（插件取 max(提示,10s)）、
`compressed_flag_ttl_ratio=3.0`（标记 180s，盖过 `block_s=90` 留放行断言窗口）。阈值面沿用栈缺省
（summarize 45 / block 90）。整跑约 10~15 分钟（真模型多轮+两段闲置等待）。

## 失败排查

诊断落 `<沙箱>/logs/e2e-driver-<runstamp>/`：本会话全部账本行、gate.log 尾、
handoffs 目录、pending_prompts、daemon/web 日志尾、页面截图与 aria 快照，另有
`matrix-report.md`（矩阵红绿）与 `t5-subagent-probe.md`（T5 结论）。重跑 `run.sh`
即恢复（幂等）。彻底拆栈：`bash tools/e2e_dsh/stop.sh`（web2 由 run.sh 的 EXIT
trap 自拆，不留第二实例）。

## 副作用边界

只碰 25xxx 沙箱口（daemon/panel/web/web2 四口铁闸）与沙箱数据目录（票07 铁闸
继承）；剧本末段停沙箱 daemon（降级·乙所需）——重跑恢复；生产
3080/15700/15722/3081/15900 零接触。
