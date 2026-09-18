# Ferryman 后端 Go 一次性重写 · 实施 Spec

- 日期：2026-09-19（夜链 20260919-001437 产出）
- 输入：`docs/superpowers/plans/2026-09-18-ferryman-go-one-shot-rewrite.rev1.md`（含文末评审附录）+ 两轮盲评结论
- 决策依据：ADR-0003（全 Go 路线）、ADR-0005（一次性切换）、ADR-0004（问询守望授权边界，本 spec 不越界）
- 词汇：遵守 CONTEXT.md（守望/摆渡/闸门/归还/台账/账本/交接库/等待窗口/停车/问询守望/公式单源/一次性切换/规格冻结）

## Problem Statement

Ferryman 后端是双工具链：Python 守护进程（守望/闸门/摆渡/账本/报表，26 文件 6631 行）+ Go 查看器。代价：策略公式两处维护（viewer 手抄版已识别出 4 处漂移）；Python 冷启动慢一个量级且钩子自举闪控制台窗；部署要两份运行时。目标：单一 `ferryman.exe` 承载全部常驻职能，一次性整体替换 Python（无并行期、无烧机期），切换后 Python 打 tag 存档退役。

## Solution（用户视角）

重写完成后，用户拥有：

1. **一个 exe**：`ferryman.exe` 无参/`serve` 启动即同时提供守护 API（127.0.0.1:7311，5 端点 + Bearer）、内嵌时间线面板（127.0.0.1:15900，原查看器全部能力含 demo/反跑/托盘）、系统托盘一个图标；终端裸跑能看见全部输出（console 子系统，无黑窗问题交给启动方隐藏）。
2. **一组子命令**：`doctor`（体检）、`install-cc / install-ccswitch / install-codex`（钩子安装，支持事件子集）、`account report`（族系账单+成效账+策略对比）——输出在终端全部可见。
3. **行为不变**：闸门判定、账目字段、HTTP 响应、交接/警告/日志中文文案与 Python 版逐字段一致；数据落盘（账本 jsonl 10 科目白名单、交接库 index.json、config.toml 语义）互换兼容——回退到 Python 不需要数据迁移。
4. **钩子零改动**：7 个 PowerShell 钩子原样保留；点火脚本指向新 exe。
5. **一次性切换**：构建 → 数据全量备份 → 回退工件生成并演练 → 装三类钩子（SessionStart + SubagentStart/Stop，**UserPromptSubmit 闸门钩子本阶段不装**——用户指令，防"子代理久跑→交接异常→主会话输入被吞"）→ Python 退役（tag 存档）。切换后问题靠 serve 日志/账本流水/`/stats` 定位热修；命中回退触发条件即跑 rollback 工件回 Python。
6. **公式单源**：策略/价格公式全仓唯一实现（Go 单包），报表、面板、反跑共用；出现第二份即缺陷。

## User Stories

1. 作为 CC/Codex 重度用户，我想要闲置会话被守望、达阈值后自动摆渡成交接 MD，以便缓存失效后开新会话不丢上下文。
2. 作为被拦场景的用户，我想要 block 时看到原文保存与交接路径、/clear 后交接+原话自动注入——该能力由守护与测试保障，**生产闸门入口本阶段按用户指令关闭**（钩子不装，能力不删）。
3. 作为多项目开发者，我想要按项目/族系的费用账单与节省额复算（`account report` / 面板），口径与旧账一致（half-even 舍入、版本化价格钉死）。
4. 作为运维者，我想要 `doctor` 一键体检（钩子在位/BOM/控制字符/快照覆盖/daemon 活性/provider 配置），并对"闸门钩子缺位"按用户策略提示不报错。
5. 作为换机用户，我想要 `install-cc/-ccswitch/-codex` 幂等安装钩子（CC Switch 地雷自动注入快照），并支持只装部分事件。
6. 作为查看器用户，我想要时间线/反跑/保活计划推演/demo 全部保留，托盘与快捷方式钉 15900 口不变。
7. 作为问询守望（observe）用户，我想要提问潮检测、等答复窗、心跳调度/熔断/演练记账照旧工作（真实发送仍留接口位，Q14 未授权）。
8. 作为等待窗口的用户，我想要停车/恢复/懒过期/两窗互斥语义逐字段保持（含 90s ack 宽限与 1h 过期上界）。
9. 作为数据所有者，我想要切换前账本/交接库/配置全量备份，Go 版写坏数据时能恢复（git 恢复不了数据，备份才能）。
10. 作为万一要回退的用户，我想要 rollback 工件幂等可重复执行、演练过且不污染真实启动配置、观察期内保留离线重建条件（.venv 不提前清）。

## Implementation Decisions

- **仓库形态**：根部单一 Go module（module 名 `ferryman`），查看器收编（server/demo/账本读取并入 internal；其手抄 policy 副本销毁）。资产（web 前端、图标）随消费它们的 cmd 包放置——go:embed 只能引用包目录子树，不得置于仓库根或跨包引用。
- **包边界**（Python 模块 → Go 包 一一对应）：prices→prices；policy→policy（含 viewer 三函数并入，全仓唯一公式源）；accounts/config/notify/store/ledger/harvest/beat 同名；transcripts→cctrans；codex_transcripts→codextrans；extract→extract；qwatch→qwatch；ferry→ferry；report→report；install+doctor→installer；daemon+server 的 Python 逻辑→daemon（gate/httpapi/watcher/worker/serve 五件）；viewer 收编件→viewer。
- **守护 API 契约**（与 Python 版逐字段一致）：POST /gate、POST /subagent、POST /qwatch_stop、GET /stats、GET /restore；Bearer token（数据目录 daemon.token，64 hex）；401/404/400 语义与响应 Content-Type 不变；仅绑 127.0.0.1；绑定排他（Windows 双绑必败）。
- **闸门状态机**：七分支顺序、pending 三清除条件、machine-waiting 三道豁免、停车窗/两窗互斥、qwatch 死线——以 server.py 语义为唯一权威，逐字段平移。
- **并发模型**：双锁同序（windowsMu 外层 → ledgerMu 内层）；凡需双资源的开窗临界区两把全拿、同序获取；台账公共方法自带锁、临界区用无锁内方法；SessionState 为共享可变引用，**读写均须持锁**；禁止同 goroutine 重入加锁。
- **数值**：全系统 float64；舍入 = FormatFloat/ParseFloat 实现的 half-even（14 万组跨语言对照 0 失配；naive 缩放 109 失配，禁用）；时间 = float64 epoch 秒（mtime=UnixNano/1e9），测试注入时钟。
- **文本**：码点语义（len/截断按 rune）；CJK token 估算范围逐字平移；正则 RE2 化且字符类 Unicode 化（\s=显式 Unicode 空白集、\d=\p{Nd}、\w=[\p{L}\p{Nd}\p{Nl}\p{No}_]），全角样本进测试。
- **I/O**：jsonl 行读无上限（ReadBytes 循环，禁 Scanner 行上限）；账本 append-only 月度滚动；index.json 原子写（tmp+rename）、键 snake_case 显式 tag；坏行静默跳过+stderr 告警的防御纪律全面保持。
- **CLI/构建**：console 子系统构建（不带 -H windowsgui）；窗口隐藏归启动方（点火脚本 start /min、快捷方式 WindowStyle、ensure.ps1 既有 -WindowStyle Hidden）；单 exe 行为面 = serve(默认)/doctor/install-*/account report/--demo/--port/--no-tray/--no-browser/--install-shortcuts。
- **钩子安装子集**：installer 支持按事件列表安装（CC 与 Codex 两侧一致）；切换日装 SessionStart+SubagentStart+SubagentStop 三类；doctor 对闸门事件缺位"提示不失败"。
- **摆渡**：OpenAI 兼容 chat、L1/L2 漏斗、墙钟 480s（context 取消防 goroutine 悬挂）、失败降级骨架；HTTP 发送器（HttpBeatSender 真实心跳发送）**不实现**——接口位保留，enforce 模式回落 observe 演练并告警，属已知功能退化，切换日 doctor 输出声明。
- **回退工件**：rollback 脚本幂等（worktree 已存在则复用或清理后重建）；演练在隔离环境（临时 worktree 路径 + 临时启动器副本，不触碰真实启动配置）；触发条件 = 摆渡产物损坏 / 账本字段错乱 / 三类已装钩子异常 / 守护或面板崩溃不可热修（gate 误拦/漏拦不在列——生产无闸门钩子，不可观察）；切换前全量备份 ~/ferryman 数据至带时间戳目录。

## Testing Decisions

- **规格 = 测试**：316 个可移植 Python 测试 1:1 转绿（387 总 − 71 冻结：e0c 四文件 66 + eval_checks 5；hooks 16 计入 316）。先测试后实现（红→绿）。
- **命名**：`test_x.py → x_test.go` 去前缀映射（codextrans 包内两源文件分别成 codextrans_transcripts_test.go / codextrans_extract_test.go 形态）；同包跨源重名以来源前缀消歧（已知点：viewer 的 TestNoCachePriceRefuses 加 Viewer 前缀）。
- **舍入边界电池**：含 2.675/3.175/6.335/0.0005/123.4565 五个实验失配例（期望值以 Python 实测为准钉死）。
- **并发**：有 gcc 则 `go test -race ./internal/daemon/...` 强制绿；无 gcc 以锁序人工评审补偿并记账（存疑项 #R——触发点：daemon 并发测试编写/切换验收；命中动作：停下反馈，别默默跳过）。
- **成功路径装配**：Worker + 恒成功摆渡替身的端到端测试（fresh 落盘 + 账本 handoff 行 + 有效交接命中）；恒败替身覆盖骨架降级路径。
- **enforce 沙箱冒烟**：独立端口 + 独立数据目录的沙箱守护，直打 /gate 断言 block 契约（reason 全文/suppressOriginalPrompt/handoff_path）；不依赖钩子、不占 7311。
- **观察冒烟**：observe 三链路（警告/真 provider 摆渡/归还注入）同款沙箱直打 API 触发。
- **键名断言**：index.json 落盘 JSON 直接比对 snake_case 键（含 null/空数组语义）。
- **测试账目对账**：T26 验收按 316 基准清点；viewer 既有测试迁移保留（合并 exe 任务不得删除）。

## Out of Scope

- 心跳真实发送（HttpBeatSender 实现）——Q14 保真实验 + 用户放行前不做。
- e0/e0b/e0c/eval_set/eval 实验子系统——随 Python tag 冻结，不迁。
- 闸门钩子（UserPromptSubmit）的生产安装——用户指令本阶段关闭；改进方向重构后再议。
- 并行双跑/影子模式/烧机门禁——ADR-0005 已废弃。
- 心跳等待窗保温的执行器扩展（问询守望 observe 演练之外的一切真实网络动作）。

## Further Notes

- 迁移期 Python 侧规格冻结（只收 bugfix；387 测试为行为规格）。
- 切换时序：全部任务完成 → 全绿 + 冒烟 → tag → 构建 → 数据备份 → rollback 工件 + 演练 → 停 Python → 装三类钩子 → 首启验横幅 → doctor 全绿 → 删 Python 源（.venv 保留至观察期结束）→ 24h 观察清单。
- 评审产物：round 0 `.xcheck/20260918-235459/`（16 条已修入 rev1）；round 1 `.xcheck/20260919-001437/`（14 条已烘焙进本 spec §Implementation/Testing 与回退条目）。
- 已知取舍（如实声明）：切换即生产，未验回归直接命中日常路径，靠日志/账本热修；骨架交接在最坏情况下短暂入生产。
