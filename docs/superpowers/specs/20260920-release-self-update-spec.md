# 发布通道与自升级 规格(spec)

> 2026-09-20 · 夜链 20260920-171026 · 对象 = `.xcheck/20260920-171026/proposal.rev1.md`(已审快照)
> 决策依据:D1-D15(见同环 decisions.md);开放约束 F4/F8/F9 以 seam A/B/C 裁定烘焙(见 FINDINGS.md),受影响票记 review_blocks。
> 术语遵守 CONTEXT.md:**自升级(update)** = 同一 Go 代码线内换版本;**切换(cutover)** = Go↔Python 语言栈切换(既有 `ferryman cutover`,本特性不触碰);**发布通道(release channel)** = tag 触发自动构建产出的 GitHub Release,唯一正式分发途径。

## Problem Statement

Ferryman 目前没有版本号、没有发布通道、没有升级机制:exe 靠本地手工编译、手工备份改名(`ferryman.exe.old-20260920` 即手工痕迹);当前版本无处可查;升级 = 人肉换文件 + 重启守护,出错无人回滚。需要:打 tag 自动测试编译发布 Release;程序内可手动触发升级;版本多处可见。

## Solution(用户视角)

- 你给仓库打一个语义化 tag(如 `v0.1.0`),几分钟后 GitHub 上出现一个 Release,里面是编译好的 `ferryman_windows_amd64.exe` 和校验文件——这是唯一正式发布口。
- 你在任何地方能看到当前版本:`ferryman version`、`ferryman doctor`、面板页脚、托盘菜单第一项。
- 你在托盘右键选「立即升级」,或敲 `ferryman update`:程序自己下载→校验→换文件→重启守护,升完托盘/doctor 显示新版本;中途任何一步失败,自动回滚旧版本,服务不断。
- 「检查更新」只看不动手;升级永远手动触发,程序绝不自己偷偷升级;预发布版(rc/beta)默认不进更新通道。

## User Stories

1. 作为本机用户,我想要打 tag 后自动测试、编译并发布 Release,以便不用手工编译分发,每次发版有据可查。
2. 作为本机用户,我想要在命令行、doctor、面板、托盘看到当前版本号,以便确认升级是否生效、报告问题时说明版本。
3. 作为本机用户,我想要托盘右键「检查更新」/「立即升级」或命令行 `ferryman update --check` / `ferryman update`,以便自己决定何时升级。
4. 作为本机用户,我想要升级失败(下载损坏、校验不过、新版起不来)时自动回滚旧版本,以便守护服务不中断。
5. 作为本机用户,我想要默认更新通道只出稳定版(预发布须显式 `--prerelease`),以便不被未验证版本波及。
6. 作为本机用户,我想要升级过程互斥(二次触发被拒并提示),以便并发升级不破坏现场。
7. 作为本机用户,我想要升级中途断电/崩溃后,下次升级或 doctor 能识别残留并恢复/清理,以便不留半残状态。
8. 作为本机用户,我想要显式指定版本安装(含降级,如 `ferryman update v0.1.0`),以便从坏版本退回。

## Implementation Decisions

### A. 版本与注入
- main 包持可注入版本变量,缺省 `dev`;CI 与 `build.ps1 -Release` 以 `-ldflags -X` 注入(本地值取 `git describe --tags --always`,无 tag 即 dev)。
- 版本暴露五处:`ferryman version` 子命令;doctor 结论行;agent 面 MCP doctor 工具响应加 `version` 字段(仍只读,动词面零变化);daemon `/stats` JSON 加 `version`(兼作升级探活校验;实施订正:面板页脚数据源为面板装配层 `GET /api/version`——同 main.version 单源,免面板跨口取守护 /stats);托盘菜单首项「版本 vX.Y.Z」(disabled 展示项)。

### B. 发布流水线(CI)
- 新 workflow,触发 `push: tags: ['v*']`;`permissions: contents: write`;runner `windows-latest`(本仓 installer 无条件依赖 Windows registry,Linux 无法编译/测试——实测);每个 run 步骤显式 `shell: bash`。
- 步骤:checkout → setup-go → `go vet ./...` + `go test ./...`(全绿才继续)→ `go build -trimpath -ldflags "-s -w -X main.version=<tag>"` 产 `ferryman_windows_amd64.exe`(固定名,不含版本号,供 `releases/latest/download/` 直链)→ `sha256sum` 产同名 `.sha256` → **tag 校验**:必须严格 semver(`vX.Y.Z` 或 `vX.Y.Z-<pre>`),含预发布后缀 → Release 标 `prerelease: true`(GitHub latest 通道只返回非 prerelease 非 draft,兑现默认忽略预发布)→ 直接 publish(不加 draft),body 取 tag 注释。
- 发布操作须用 annotated tag(`git tag -a vX.Y.Z -m "..."`)——lightweight tag 的注释为空。

### C. 自升级(核心)
**统一监督者**:CLI `ferryman update` 进程即监督者;托盘「立即升级」spawn detached 隐藏的 `ferryman update --supervise`(内部旗标,行为与无参一致);「检查更新」/`--check` 为纯只读路径。两执行入口收敛同一状态机:

1. **锁**(`~/ferryman/update.lock`):O_CREATE|O_EXCL 原子创建;存 PID+进程映像路径+generation。存活判定 = PID 活**且**映像路径 == 换装目标 exe 路径(seam B);持有者存活 → 打印「升级进行中」退出;陈旧 → 原子接管(generation 递增)。
2. **journal**(`~/ferryman/update-journal.json`):阶段 staging / swap / verify,先写后动。
3. **staging**:解析目标版本(latest API;`--prerelease` 走列表接口;显式版本直取该 tag 的 release)→ 下载 exe 到旁路 `ferryman.exe.new` + `.sha256` → SHA256 校验;下载走环境代理(HTTP(S)_PROXY)。
4. **换装目标解析**(seam E):解析 `~/ferryman/start-daemon.cmd` 内引号 exe 路径为目标;解析失败回落 `os.Executable()`。
5. **停旧**:POST `127.0.0.1:7311/shutdown`(新内部管理端点:loopback-only、`daemon.token` 鉴权、优雅取消,与 os.Interrupt 同路径;**不在 agent 面 MCP 动词面**)→ 等端口释放(上限 30s)。端点不可达且守护在跑 → 兜底 kill:**先验证 PID 的进程映像路径 == 换装目标 exe 路径,不匹配/stale 一律拒杀并报错**(seam B)。
6. **swap**(seam A):copy 当前 exe → `ferryman.exe.old-<旧版本>`(停旧后复制,无运行锁)→ 单次原子替换 `MoveFileEx(new → exe, MOVEFILE_REPLACE_EXISTING)`(NTFS 元数据日志保证要么旧要么新,**无 exe 缺位窗口**)→ 旧备份自动只留 2 份 → 所有退出路径清理 `.new`/`.swap-tmp` 残留。
7. **拉起 + 校验**:detached 隐藏拉起 `~/ferryman/start-daemon.cmd`(Run 键/看门/钩子自举三件套不动,留作兜底)→ 轮询 `/stats` 上限 90s,且 **version == 目标版本**才算成功。失败判定前先查 7311 持有者(seam C):若为旧版本(看门/自举抢跑重拉),复停一次 → 重拉起 → 重校验一轮;仍败才回滚。
8. **回滚**:杀新进程(kill 前同样过身份校验,seam B)→ **copy** 恢复 `.old-<旧版本>` 为正式 exe(保留备份份)→ 重拉起旧版并按同法校验 → 报错退出。
9. **崩溃恢复**:任何 `ferryman update` 启动先读 journal:staging → 清残留续跑;swap/verify → 核对当前 exe 与运行版本,健康清账,不健康按备份回滚。doctor 新增「升级事务残留」检查(journal/.new/.swap-tmp)。
10. **结果通知**(seam F):升级结果(成功/失败+已回滚)经既有 internal/notify 通道推送;CLI 路径同时保留 stdout。

### D. 托盘
菜单次序:「版本 vX.Y.Z」(disabled)→「打开面板」→「检查更新」(只读 CheckLatest,结果走 notify/气泡,按 systray 能力择一)→「立即升级」(spawn detached `--supervise`)→「退出」。

### E. 清理与 ADR
- 未跟踪陈旧产物删除(工作区操作):`viewer/ferryman-timeline.exe`(空目录一并)、`ferryman.exe.old-20260920`、`ferryman.exe.old-20260920-premerge`——D13 用户已确认的精确清单。
- `.gitignore` 的 `ferryman-timeline` 行删除(被跟踪文件,入本链提交)。
- 新增 `docs/adr/0010-tag-release-self-update.md`:发布通道唯一、纯手动自升级、监督者+事务日志要点、**seam A 原子替换与缺位窗口消除的论证**、prerelease 标记、annotated tag 要求、windows runner 选型(含 Linux 编译不可行实录)、公开仓库信任模型(2FA;SHA256 防损坏不防恶意 release)。

## Testing Decisions

- 只测外部行为,不测实现细节;每票 TDD(先红后绿)。
- internal/update:httptest 伪造 GitHub API/资产端点(latest/列表/下载/sha256);版本比较、prerelease 过滤、代理变量、校验失败拒替换。
- 监督者:journal 各阶段的崩溃恢复演练(staging 中断/swap 后中断/verify 中断);锁并发(双取锁仅一胜、陈旧接管);身份校验(假 PID 指向无关映像 → 拒杀);看门抢跑演练(模拟旧版被重拉 → 复停重试路径);回滚(copy 语义、备份仍在)。真实进程启停用测试内编译的替身 exe,不碰生产 7311 与仓库根 exe。
- /shutdown 端点:token 对/错、非 loopback 拒绝、优雅停机语义。
- 版本可见:各面(version/doctor/MCP doctor//stats/页脚)字段断言;`internal/mcp` 禁词与只读红线测试零回归。
- CI workflow:票内做关键字段静态断言(permissions/shell/runner/prerelease 分支);真实 tag 触发属验收期人工动作,不在票内。

## Out of Scope

非 Windows 平台;后台自动检查/自动升级;代码签名、GitHub attestation、镜像/CDN 配置;agent 面触发升级(MCP update 工具);自动开 PR/自动合并;`cutover` 机制任何变更;消除 NTFS 原子替换之外的更极端假设(如磁盘级损坏)。

## Further Notes

- 开放约束与裁定全文见 `.xcheck/20260920-171026/FINDINGS.md`(F4/F8/F9 + seam A/B/C;F10-F14 采纳)。受影响票:监督者票记 `review_blocks: F4, F8, F9`;托盘票记 F12;CI 票记 F11;doctor 残留检查票记 F4(配套)。
- 上下文:本链评审两轮(round0 七条、round1 复核+新增),round1 分歧点 F4 由主会话查证裁定(停旧后单次原子替换消除窗口),晨报披露。
- CONTEXT.md「发布与自升级」词条已在操作者工作区(白天会话),是否随合并提交由人决定,夜链不提交该文件。
