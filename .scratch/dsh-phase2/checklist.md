# dsh 后续与近期工作清单（2026-10-03）

> 核对基准（每条核过实物）：PR #4 已开待合（6 commits）；生产守护 v0.5.2、doctor **27/27 全绿**；cc-switch **已卸载退场**（10-03 上午，回退资产齐）；xcheck 夜链在途 29 commits（票03-13，10-03 10:01 仍在提交）冲 v0.5.3。
>
> 记忆修正存档：同模型 34 连败两修已随 v0.5.1 进生产（10-01 21:18 换装），不属发版积压。

## ① 已完成（2026-10-03 上午，留档）

**A. 接管回归修复 + cc-switch 卸载（W4 终验 27/27）**
- [x] `ferryman provider apply` 修复 15721 回归（三份 written、同戳备份 20261003-095014、外加快照 `~/ferryman/rollback-snapshots/20261003-0949/`）
- [x] cc-switch 卸载：杀进程（零退出改写）→ HKCU Run 键删 → MSI 静默卸载（{1376663C-…}，目录/快捷方式全清）；回退资产 `~/ferryman/rollback-snapshots/ccswitch-uninstall-1011/`（db+settings+Run键.reg）；`~/.cc-switch` 813MB 原地保留
- 遗留观察：真重启终验待下次自然开机；数个重启周期无复发后可删 `~/.cc-switch`

**B. 升级残留清理**
- [x] 同版本整套换装 v0.5.2→v0.5.2：旧 PID 退场、新 PID 22868 双口在听、supervisor-copy/swap-tmp 双清（静默门走硬切兜底，切断点距末次请求 40s 应无活跃流；备份 exe.old-v0.5.2 在场）

## ② v0.5.3 合线（顺序有讲究）

- [ ] xcheck 夜链收尾后开 PR **先合**（29 commits：pi 写入器票10、doctor pi 探针票11、pi 可用性位票12、v0.5.3 退役清单票05、--json 面票04、stop PID 时机票03、文档契约票06、弃用处置票13 等）——它在动，不去踩
- [ ] PR #4（dsh）**后合**：两线同动 `cmd/ferryman/provider.go` / `internal/provider/backup.go` / `internal/provider/writer.go` 三文件，dsh 侧 rebase 解冲突（小而已冻结，当后合方）
- [ ] doctor 全绿口径：票11 起 provider 探针 +1（pi）；dsh 探针若按同款模式补，计数再 +1

## ③ 发版 v0.5.3（流程纪律照旧）

- [ ] 全仓 `go test ./...` 落日志过一遍（教训：交接班声称的全绿 ≠ 最终 diff 状态）
- [ ] race 复验走 NUC10（本机 -race 工具链坏；匿名克隆 + GOPROXY=goproxy.cn + 输出落文件）
- [ ] 资产名硬契约：`ferryman_windows_amd64.exe` + `.sha256`（内容 `hash *文件名\n`；名字错=updater 静默死）
- [ ] tag v0.5.3 → CI 出资产 → 监督者 `./ferryman.exe update` → **等旧进程退场**（端口空≠进程走）
- [ ] 换装时顺带自证：v0.4.4 副本自删真机首跳；doctor 首跳旧茬属预期、下次自愈
- [ ] 发版后生产验三件：dsh 流量记账标 `agent=dsh`；票11 pi 探针亮绿；`provider apply` 首跑含 dsh/pi 新目标且全 unchanged
- [ ] **后续票**（票13 已注记退役路线，待实施）：cc-switch 已不在场，doctor `ccswitch_snapshots` 应转 not_checked 而非读死数据；确认无复发后连同删除 `~/.cc-switch` 一起收

## ④ dsh phase 2（发版后开新 worktree/新会话）

> 依据：`docs/research/20261002_dsh-服务商配置与摆渡可行性调研.md` §3（路线 A 桥起步、路线 B 原生插件收尾）。dsh 会话当前对台账/闸门完全隐身——闲置无拦截、缓存凉全价重付，这是本役的账。

**P2-1 守望＋台账（Go 侧，第一块砖）——✅ 2026-10-03 完成（分支 dsh-phase2-p2-1-守望台账）**
- [x] zstd 会话日志读取：`internal/dshtrans`（frames.go 扫描器逐字节对 dsh `zstd.ts scanZstdFrames` 钉死；tail.go 增量尾读只消费完整帧；klauspost/compress **v1.15.13** 单帧 DecodeAll——19.x 要求 go≥1.24 不动仓内 go 1.23.4 工具链）。真机 6 会话全量消费=文件大小、无残帧
- [x] 会话目录名规范化规则：names.go 对 `src/format.ts` 逐条钉（EncodeSegment/DecodeSegment 单射、ProjectKey 分隔符合并+~XXXX+截 251+root；UTF-16 码元域含非 BMP 拆代理对）；真机双重对账=头行 cwd→ProjectKey==磁盘目录名、头行 id==DecodeSegment(目录名)
- [x] 台账四列接入：watcher_dsh.go pollDsh（pollGuarded 挂第三轨）→ Touch("dsh")+观察窗+usage 科目四列直映（白名单零改动）；usage 行带 seq 去重、断点随账本恢复（重启实测不重采）；title/peak 采集顺带回写。真机 daemon 面：登记 6 会话/出行 5 条/幂等通过
- [x] 子代理族系：子头 origin/parentSession 权威判据（随父入账 session_id=父 sid、subagent=子 sid，CC 票01 同款）＋父侧 `CatalogChildren`（subagent/catalog 事件汇总，P2-5 判活数据源）；本机语料暂无真子会话——夹具按源码钉、待真用例回钉
- 遗留注记：① 摆渡/富化不接线（P2-2 键对齐+P2-3/P2-4 钩子面）；② 代文件切换（格式迁移）偏移清零重采——迁移会重编 seq，跨代可能重复记账一次（rc 期罕见，如实接受）；③ `maybeEnqueue` 接线时须同步补 enrich 的 dsh 分支（现为采集回写替身）

**P2-2 会话键对齐——✅ 2026-10-03 完成（分支 dsh-phase2-p2-2-会话键对齐）**
- [x] 调查翻案：**dsh 请求体没有 metadata.session_id**——四源钉死（dsh 克隆 master@639ed015、pi-ai 0.87.1+main、装机 asar、生产账面）：pi-ai anthropic-messages 只把 options.metadata.user_id 映体（dsh 不传）、sessionId 仅映 affinity 头且开关被 dsh catalog 标 'withhold' 不可开；llm-deepseek serialize 亦不写体。PR #4 验收说的「捕获键零改动就位」是把自家 CC 会话（af22e212）的记账行误读成 dsh 行——当日 dsh 流量两行（10-02 09:40:22/27）session_id 恒空
- [x] 三方统一键定案＝会话头行 id（`session-<uuid>`，即目录名本体）：守望 Touch/usage 现行键就是它，零改动已对齐；渡口 ExtractSessionID 加 `X-Deepseek-Harness-Session-Id` 第三回落（dsh 官方归因头，键=头行 id 与守望同源；形校验 `session-` 前缀+9..128+安全字符集）——CC 体>CC 头>dsh 头三级，快照捕获/记账归因两点同生效
- [x] 真会话对账（`TestRealDshSessionsVsDockLedger`，env 门控）：6 会话 5 行对渡口账面，验收行匹配（四列签名+±120s；9169/21 ↔ 09:40:27 空键行）、title 行邻域未匹配（属预期）、键不变式（匹配行 session_id ∈ {"", 头行 id}）零违例；接管后强制匹配断言通过
- [x] maybeEnqueue 仍不接线（摆渡依赖快照，pi-ai 路捕获恒 skipped 必 snapshot_missing）——解锁路径留 P2-3 接法乙（llm-deepseek 路由自带该头）或 P2-4 原生插件（session/event 直报）；beat 发送侧对非 UUID36 形键须改发 dsh 头（P2-5 接线时动，两处：httpsender/appendreplay）
- 遗留给 P2-3/P2-4 的接口结论：渡口侧捕获键已就位（第三回落）；映射机制结论＝无需映射（头行 id 与渡口键同值），缺的是**上线通道**（路由选择或插件上报），不是键换算

**P2-3 闸门 MVP（路线 A：CC 钩子桥）**
- [ ] 桥接：`dsh plugin --profile web add @deepseek-ai/dsh-hooks-claude-code`，`configPath` 指 Ferryman 为 dsh 单发的 hooks.json（现有 CC 钩子脚本复用：UserPromptSubmit 问闸门、block→deny）
- [ ] 归还过渡（桥的 detached 硬伤，官方已知）：新会话第一句手打 /restore 或待续 prompt 引导，接受首请求后一拍注入

**P2-4 原生插件 ferryman-dsh（路线 B：长期正确形态）**
- [ ] `agent/pre-step`：闸门问 daemon → `reject`（用户可见理由）或 `enter`
- [ ] `agent/created`（**awaited**）：同 Agent＋同 cwd 最新交接 MD 经 `agent.inject()` 播种——赶首请求，补桥的硬伤
- [ ] `session/event`：台账活动上报（替代/补充文件守望；含 turn/start、assistant/message 带 usage、compaction/*）
- [ ] `agent/disposed`/`agent/status`：判活与等待窗口信号
- [ ] 挂载自检：渡口路由健康检查/版本对账（社区先例 ZhijiangTang/dsh-handoff）
- [ ] 插件分发：`github:allanpk716/ferryman-dsh` 或 `file:./`

**P2-5 等待窗口/问询守望设计（不阻塞主线）**
- [ ] dsh 侧判活信号重选：`agent/status`→idle、`agent/disposed`、子女目录写入；CC 特有 async 文案判据不移植（与 codex 轨同等待遇）

## ⑤ 决策项（用户拍板）

- [ ] **排期二选一：dsh phase 2 vs 工作台战役 W0**（归并 widget 0.2.6 线，main 停 0.2.1）——倾向 dsh 先：调研正热、有真金白银在漏；W0 是归并杂务无时间压力
- [ ] **observe→enforce 升档**：09-28 复核达标（104 警告零漏拦零 block）——倾向升：证据链够了，继续 observe 不产生新信息；拍板后改生产配置（改前快照）
- [ ] ADR-0016 同模型摆渡灰度：09-29 启用，首周复核 ≈10-06
- [ ] 渡口直连战役评估日 10-07（周扫参任务跑到 10-31）
- [ ] 手机链路：若提示 authentication required，把交接文档（20261003_085019）里的 token 链接发手机开一次
