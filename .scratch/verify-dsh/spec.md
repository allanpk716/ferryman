# Spec · ferryman verify-dsh（DSH 插件验证与已知良好档案）

> 来源：`.xcheck/20261008-175411/proposal.md`（rev1，两轮盲评收敛稿）＋同环 decisions.md（D1-D13）＋FINDINGS.md（F1-F11）。
> 术语遵守 CONTEXT.md（契约面/契约锚/已知良好/插件探针等已入表）与 ADR-0026。

## Problem Statement

DSH（DeepSeek Harness）高频发版，ferryman-dsh 插件以 junction＋node_modules 拷贝装进三个 profile（desktop/web/headless）、由双宿主（Electron 桌面＋web 3080）各自加载。插件失效有四种形态且多数无声：不加载（A）、接口变（B）、配置坏致流量绕渡口（C，无感漏钱）、会话键变（D）。doctor 28 项零 dsh 插件检查，插件注册表在进程内存外部不可读——用户只能在出问题后人工排查，且没有"出问题后回到哪个 DSH 版本"的记录。

## Solution（用户视角）

一条手动命令 `ferryman verify-dsh`：DSH 更新后跑一次，几分钟内得到分层灯色（三个 profile 的静态/挂载结果＋web 沙箱功能探针结果），10-15 分钟含压缩链全验；全绿时把"这个 DSH 版本＋当前插件＋当前 daemon"记为已知良好（降级回退目标），红灯时推送告警并直接告诉用户"回到哪个版本、安装包在哪"。日常 doctor 一眼可见插件挂载状态；DSH 升到没验过的版本时 doctor 亮黄提醒（不推送）。

## User Stories

1. 作为 DSH 用户，我想在 DSH 更新后跑一条命令知道插件是否仍有效，以便不靠猜、不靠出问题后倒查。
2. 作为 DSH 用户，我想让压缩链两道（命令道/服务面）都被各自验证，以便不留"只验了一半"的风险面（用户拍板"都做"）。
3. 作为 DSH 用户，我想红灯告警直接给出降级目标（已知良好版本＋登记的安装包路径），以便快速回退。
4. 作为 DSH 用户，我想宿主没开时不被"插件死了"的误报吵醒，以便红灯保持可信（超龄＋宿主进程在=红；超龄＋无宿主=黄"宿主未运行"不推）。
5. 作为 ferryman 维护者，我想 doctor 常驻显示三 profile 插件安装完整性与挂载状态，以便日常体检覆盖 dsh 面（补 `.scratch/dsh-phase2/checklist.md` 挂账缺口）。
6. 作为 DSH 用户，我想看到当前 DSH 版本与已知良好的差异（`verify-dsh --status`），以便决定要不要跑完整验证。
7. 作为 ferryman 维护者，我想验证结果按 profile 分层输出、绿灯语义明示"web 沙箱功能验证＋三 profile 静态/挂载验证"，以便不被单一绿灯误导（desktop 功能层验证是明示的后置边界）。

## Implementation Decisions

**三层检测与进程模型（F4/F6 钉死）**
- L0 静态：三 profile 的 junction 可解析、node_modules 拷贝在、cordis.patch.yml insert 行在、package.json manifest 形状（dsh 键/exports["."]/exports["./package.json"]/main）、生产配置面仍指向渡口、DSH 版本可读。L0/L1 一律**生产侧求值**（CLI/生产 daemon 上下文），生产根显式传入，不进沙箱 env。
- L1 挂载：daemon 侧记录全局最近 poll 年龄＋按 sid 的 last-seen（宿主近似粒度）；**宿主进程旁证**（枚举 DeepSeek Harness 进程/3080 监听）；poll 载荷协议 v1 不改。
- L2 插件探针：沙箱起栈——隔离 DSH_HOME＋25xxx 四口铁闸；**沙箱 daemon＝已安装生产 exe 起第二实例**（`--port 25901` 形态＋沙箱 env），不用 go build（消除 Go 工具链/Git Bash/仓库洁净依赖）；**沙箱插件落点复刻生产 junction 布局**（junction 指向备料快照＋node_modules 拷贝）；探针宿主=`$DSH_CLI web --no-open --port 25902`；单会话单消息真模型（最便宜上游）。
- 探针断言：①闸门到达（/dsh/gate 落痕）②事件上报（usage 有该 sid 行）③会话键格式（session-<uuid>）④注入路径（/dsh/handoff 可达）⑤压缩链**两道各自执行并各自见上报，互不抵消**——任一道失败即该道红灯，契约缺失记"契约缺失"红灯，禁止"至少一道即可"（任何放宽需用户重新确认）。

**灯色、输出与告警（F2/F5/F6 钉死）**
- 求值时机：红灯只在 verify-dsh/doctor 运行时判定并推送，无常驻循环（哨兵不做，D1）。
- 输出分层：三 profile 各一行（L0/L1 各自结果）＋L2 单行明标"仅代表 web 宿主形态（沙箱）"；绿灯总义＝"web 沙箱功能全验证＋生产三 profile 静态/挂载验证全过"。
- 黄：版本未验证（流水无记录）或 poll 超龄但宿主未运行——只挂 doctor/verify 输出不推送。红：断言失败（含压缩链任一道）或超龄＋宿主在跑——推 Pushover/Toast 一条（复用摆渡路由降级告警通道），文本区分"插件失联（有宿主无 poll）"与"验证失败（断言未过）"，带降级目标。
- L1 超龄阈值＝3×生效 poll 间隔（daemon 已知 poll_hint_s/实测节律推导；无已知间隔时 90s 兜底并注明假设）。
- 自动处置（重装/降 DSH/降档）一律不做。

**契约锚与已知良好档案（F8/F9 钉死）**
- 契约锚：四类契约面（发现面/事件面/鸭子面/浏览器半面）的形状签名快照，绑 DSH 版本；全绿自动滚动、保留最近 N 份历史；diff **仅用于红灯定位与诊断聚焦，L0/L1/L2 执行范围不缩减、L2 含压缩链两道每次全跑**。daemon 五端点不锚（插件侧 MIN_DAEMON_VERSION 版本对账已有）。
- 判定流水：append-only 存 daemon 数据目录（与台账同区，不入 config.toml）；每行＝DSH 版本＋插件版本＋daemon 版本三元组（daemon 版本取**生产 daemon 自报**，与沙箱实例解耦）、判定（绿/黄/红）、日期、契约锚哈希、可选 installer 路径。
- 已知良好指针＝流水最近全绿三元组；不声称支持范围；`--status` 展示当前版本 vs 已知良好 vs 流水。

**载体（D6）**
- 核心逻辑 daemon 内单源实现；`ferryman verify-dsh` CLI 子命令（先例：provider apply / upstream use）；doctor 吸收 L0/L1 为常驻检查项（计数更新），`ferryman-gate-dsh.ps1` 补进 doctorScriptNames 七脚本清单。
- MCP 面/HTTP 端点不进本版（ADR-0009 只读红线；安装管理端点另行规划时 verify 变被调用原语）。

**其他**
- 沙箱与 e2e 套件并跑端口避让走文件锁。
- 探针消息流水行标记来源，报表可区分。
- 本机约束：所有新起的测试进程遵守零闪窗铁律（HideWindow）；本机 -race 工具链坏（race 复验走 NUC10，不在票内）。

## Testing Decisions

- 只测外部行为不测实现细节。
- 灯色规则表驱动单测：超龄×宿主旁证×流水状态×断言结果的矩阵 → 绿/黄/红＋是否推送（含压缩链一道 stub 掉→该道红灯不抵消的失败注入用例）。
- L0 检查项单测：临时目录搭 junction/缺失 insert 行/坏 manifest 的夹具（Windows junction 用 `mklink /J`，测试跳过非 Windows）。
- daemon poll 记账单测：伪造 /dsh/poll 序列断言全局年龄与 sid last-seen、阈值推导（3×hint、90s 兜底注明）。
- 沙箱起栈为集成级：可注入假 DSH_CLI/跳过（无 DSH 安装环境），有则跑起栈→断言四痕；压缩链两道断言复用 e2e 既有断言思路（kind=inject/新会话标记两面）。
- doctor 新检查项进 doctorResults 表驱动测试（既有先例：internal/installer/doctor.go 检查项测试）。
- 遵守仓库测试纪律：不裸跑全仓 go test 于用户在场时（零闪窗），测试构建产物落票声明的目录。

## Out of Scope

- 常驻哨兵（daemon 持续盯"宿主活而账本无痕"）——独立设计面未立项。
- desktop（Electron）/headless 宿主的功能层验证——后置项；本版对其仅 L0/L1。
- `--production` 生产侧探针——后置可选模式；形态 C 运行时证据缺口由 L0 静态三件兜底（已知取舍）。
- 自动处置任何失效形态；MCP/agent 面触发；安装管理端点。
- 修改插件 poll 协议（加宿主身份字段，零会话宿主盲区的缓解路径）——随插件侧改动另行立项。

## Further Notes

- 已知盲区（ADR-0026 已登记，实施不再另行处理）：验证间盲窗（无常驻求值）、零会话宿主（poll sessions:[] 无 sid）、desktop 功能覆盖边界、形态 C 运行时缺口、活树近似（沙箱 junction 指备料快照 vs 生产指活树）。
- 决策快照 D5/D13 已按 rev1 口径补注（锚 diff 仅诊断、阈值 3×间隔）。
- 锚历史保留份数 N 取 10（首版，可后调）。
- 本 spec 与票提交于夜链分支 xcheck-night-20261008-174028；同提交带入 ADR-0026、CONTEXT.md 四词条、ADR 索引（评审收敛的文档产物）。
