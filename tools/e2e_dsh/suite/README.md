# tools/e2e_dsh/suite — 票08 · E2E Playwright 全链剧本

对票07 沙箱栈的 DSH 热缓存压缩全链验收：一键、零人工、幂等重跑。

```bash
bash tools/e2e_dsh/suite/run.sh     # 全链剧本（setup + Playwright 断言）
```

## 组成

| 文件 | 职责 |
|---|---|
| `run.sh` | 入口：setup → driver → 汇总退出码 |
| `setup.sh` | 栈整备：`../start.sh` 起栈 → **夜链工作树插件换装**进沙箱 profile 两落点（`profiles/web/ferryman-dsh` 与 `profiles/web/node_modules/ferryman-dsh`）→ **模型路由改指沙箱凭据自含的 `zai-coding-cn/glm-5.3`**（生产缺省的 deepseek-official 裸跑无密钥——生产本靠已剥离的渡口覆写；`llm-pi-ai` 适配器随 app 自带，挂上+密钥引用即通，密钥随 `~/.dsh` 副本携带，直连 z.ai 零生产接触）→ config 追加 `[heartbeat].ttl_s`（缺省 0＝压缩链静默，必须给值）＋ `[dsh_compact]` 秒级参数 → 重启 daemon（吃新 config，数据目录不清）＋重启 web 实例（吃新插件，重取登录 token）→ 健康检查 |
| `driver.mjs` | 全链剧本＋断言库（全局 playwright，本仓零依赖；浏览器探针 DOM，账本/gate.log/会话文件断言走沙箱数据目录只读） |

## 剧本与断言（票面清单映射）

| 幕 | 断言 |
|---|---|
| token 登录 → 幂等新建会话 | composer 就绪；会话目录落盘＋**陈旧草稿自愈**（工作区会持久化上次页载的空草稿，点「新建会话」可能复用它——其闲置锚是旧的，首条消息会被拦死；剧本验鲜失败即再点新建，≤3 次） |
| 多轮灌上下文 | 账面前缀（三输入列之和）≥ `min_peak`（4000；glm-5.3 系统提示 ~12K，1-2 轮即过线） |
| 闲置等触发（0.5×TTL=30s） | ① 账本 `kind=compacted` 行且字段齐 ② poll→compactNow→compacted HTTP 往返（daemon 触发日志＋上报行＋`/stats` gate 计数推证） |
| 横幅 | ③ `[data-ferryman-banner]` DOM 出现；③b 随下次用户消息消失（容忍一个 4s 浏览器轮询周期） |
| 拦窗内发消息 | ④ 回合执行不被拦（无选择卡）＋ gate.log `mode=compacted-short-prefix` 新行 |
| 交接 | ⑤ `handoffs/` 有本 run 新文件（无渡口＝骨架降级落盘） |
| 降级·甲（闲置过线照旧拦） | ⑥ 会话闲置过线后发消息→`[data-ferryman-blocked]` 选择卡出现＋`kind=block` 落账＋卡面携带被拦原话（RPC wire 真值） |
| 降级·乙（daemon 不可达） | ⑥b 停沙箱 daemon 后消息照发（pre-step 探测失败→fail-open 放行，回复到达），在办选择卡不增 |

票01（N2 并发查证）结论为「排队/干净报错」而非状态错乱，故无竞态断言需跳过/waiting（`../../.scratch/dsh-hot-compaction/research/n2-concurrency.md`）。

## 已知现状：① ② ③ ④ 断言在夜链插件现状下必红（票05 集成缺口，非剧本问题）

2026-10-07 夜链真机实证（诊断签名=kind=compacted 连续 `ok:false reason:"error"`，
root cause 文件随诊断落盘）：**宿主 web 组成里 compaction 服务对插件域不可达**——
web-app bundle 把 compaction-basic 从 host 面摘掉、presets patch 用
`cordis:group isolate:{compaction:true}` 把它隔离在 preset 组内
（`packages/bundle/web-app/presets/cordis.patch.yml`），插件层
`ctx.inject('compaction')` 与 `agent.ctx.get('compaction')` 均取不到（实测
UNDEFINED）；宿主自己的 `/compact` 命令（组内 command-compact）正常。票05 的
`resolveCompaction` 路径在任何真实 dsh web 宿主（含生产）都会恒 UNDEFINED。
修复属票05 插件面（方向：改走 `agent.followup('/compact')` 原生命令道并观察
`compaction/end` 后上报，或经 preset 行解除 isolate）。修复落地后本剧本①-④
应直接转绿（触发/派发/上报链与 gate/横幅/账本断言全部就绪且已验证）。

次级观察：选择卡「强续重发」动作（Remote resend→`agent.followup`）在本沙箱
真机抛错回「代发失败（会话可能已结束）」——卡片自带降级文案指路手打「强续」，
剧本⑥b 已按该降级路径回落手打。

## 时序参数（setup.sh 追加进沙箱 config；env 可覆写）

`ttl_s=60`、`trigger_ratio=0.5`（触发线 30s）、`min_peak_tokens=4000`（默认 2 万灌不起）、
`command_ttl_ratio=0.5`（指令 30s 有效，盖过插件 10s 轮询下限）、`poll_hint_s=2`（插件取 max(提示,10s)）、
`compressed_flag_ttl_ratio=3.0`（标记 180s，盖过 `block_s=90` 留放行断言窗口）。阈值面沿用栈缺省
（summarize 45 / block 90）。整跑约 10~15 分钟（真模型多轮+两段闲置等待）。

## 失败排查

诊断落 `<沙箱>/logs/e2e-driver-<runstamp>/diag-*.{txt,png,aria.yml}`：本会话全部账本行、
gate.log 尾、handoffs 目录、daemon/web 日志尾、页面截图与 aria 快照。重跑 `run.sh` 即恢复
（幂等）。彻底拆栈：`bash tools/e2e_dsh/stop.sh`。

## 副作用边界

只碰 25xxx 沙箱口与沙箱数据目录（票07 铁闸继承）；剧本末段停沙箱 daemon（降级·乙所需）——
重跑恢复；生产 3080/15700/15722/3081/15900 零接触。
