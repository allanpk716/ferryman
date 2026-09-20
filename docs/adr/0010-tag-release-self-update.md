# 发布通道唯一:tag→Release 自动构建;纯手动自升级与停旧后单次原子替换

Ferryman 是单机守护、又被 AI agent 高频触碰,但一直没有版本号、没有发布通道、没有升级机制:exe 靠本机手工编译,备份靠改名(`ferryman.exe.old-20260920` 即手工痕迹),升级 = 人肉换文件 + 重启守护,出错无人回滚。2026-09-20 定稿发布与自升级规格(`docs/superpowers/specs/20260920-release-self-update-spec.md`),评审两轮(F1-F14),开放约束 F4/F8/F9 以 seam A/B/C 裁定烘焙。术语遵守 CONTEXT.md:**自升级(update)** = 同一条 Go 代码线内换版本;**切换(cutover)** = Go↔Python 语言栈往返(`ferryman cutover`);两者不共用机制、不混叫——版本轴的升降是 update,语言栈轴的往返才是 cutover。

状态:已接受(2026-09-20 夜链 20260920-171026 定稿;决策依据 D1-D15,裁定 F2/F4/F5/F7/F8/F9 经 seam A/B/C 烘焙入规格)。

决定(2026-09-20 定稿):

- **发布通道唯一**:打语义化 tag(`v*`,首版 v0.1.0)→ GitHub Actions 自动跑 vet+test(全绿才继续)→ 编译 `ferryman_windows_amd64.exe`(固定名不含版本号,`-ldflags -X` 注入版本,供 `releases/latest/download/` 直链)→ 出同名 `.sha256` → GitHub Release 直接 publish(不 draft,body 取 tag 注释)。这是唯一正式分发途径;本地 `go build`/`build.ps1` 产物只算开发产物,不算发布。
- **windows-latest 单平台选型(交叉编译否决的实测依据)**:本仓 installer 无条件导入 `golang.org/x/sys/windows/registry`,且全仓零 `//go:build` 条件编译,`GOOS=linux go build` 实测失败(round0 在案)——Ubuntu runner 与交叉编译矩阵没有立足点;平台范围收窄为 Windows amd64 是事实约束,不是偏好。workflow 每个步骤显式 `shell: bash`(windows runner 默认 pwsh,`${GITHUB_REF_NAME}`/`sha256sum` 是 bash 语法)。
- **annotated tag 是发布前置**:Release body 取 tag 注释,lightweight tag 的注释为空(等于发布空说明);发布操作必须 `git tag -a vX.Y.Z -m "..."`。
- **prerelease 标记保 latest 通道纯净**:tag 过严格 semver 校验(`vX.Y.Z` 或 `vX.Y.Z-<pre>`);含预发布后缀 → Release 标 `prerelease: true`。GitHub latest 通道只返回非 prerelease 非 draft,默认更新路径天然只见稳定版;预发布须显式 `--prerelease` 才可达。
- **自升级纯手动,两入口收敛一个监督者**:`ferryman update`(含 `--check` 只读检查、指定版本含降级、`--prerelease`)与托盘「检查更新/立即升级」;托盘路径 spawn detached 隐藏的 `ferryman update --supervise`,内部旗标、行为与无参一致。监督者进程跨旧守护存活,统一状态机:锁 → journal → staging(下载+SHA256 校验)→ 停旧 → swap → 拉起+版本校验 →(失败)回滚。无后台自动检查、无自动升级。
- **agent 面不开放升级**(沿 ADR-0009):MCP 不加 update 工具,"update" 保持协议禁词,禁词/只读测试零回归;新 `/shutdown` 内部端点属守护 HTTP 管理面(loopback-only + token 鉴权),不在 agent 面 MCP 动词面。
- **锁(单飞)**:`~/ferryman/update.lock`,`O_CREATE|O_EXCL` 原子创建;存 PID+进程映像路径+generation;存活判定 = PID 活**且**映像路径 == 换装目标 exe 路径(防 PID 复用误判);持有者存活 → 打印「升级进行中」退出;陈旧 → 原子接管(generation 递增)。
- **journal(事务日志)**:阶段 staging/swap/verify,先写后动;任何 `ferryman update` 启动先读 journal 做崩溃恢复——staging 中断清残留续跑,swap/verify 中断核对当前 exe 与运行版本,健康清账、不健康按备份回滚;doctor 增「升级事务残留」检查项(journal/`.new`/`.swap-tmp`)。
- **原子替换论证(缺位窗口整体消除,seam A)**:停旧(POST `/shutdown` → 等端口释放,上限 30s)之后旧进程已退出,"运行中 exe 不可覆写"的约束不再适用,双 rename 舞步失去存在前提。替换序列定为:**copy** 当前 exe → `ferryman.exe.old-<旧版本>`(copy 语义保留备份,停旧后无运行锁问题,自动只留 2 份)→ **单次 `MoveFileEx(ferryman.exe.new → 目标 exe, MOVEFILE_REPLACE_EXISTING)`**。NTFS 元数据操作有日志,替换要么落在旧版、要么落在新版,**不存在 exe 缺位窗口**——断电/崩溃最坏结果是旧版或新版在位,Run 键/看门/钩子自举三件套永远有 exe 可拉。早期"双 rename 微秒缺位窗口 + 只承诺可检测可手工恢复"草案,以及"例外声明+自动改回"提案,一并作废:该窗口是保守实现的产物,不是 Windows 固有约束。
- **kill 前身份校验(seam B)**:任何 kill 路径(端点不可达时的兜底停旧、回滚阶段杀新进程)强制先验证目标 PID 的进程映像路径 == 换装目标 exe 路径;不匹配/stale 一律拒杀并如实报错——PID 复用不误伤无关进程。
- **看门抢跑复停重试(seam C)**:升级校验失败判定前先查 7311 持有者;若为旧版本(被看门/自举抢跑重拉),复停一次 → 重拉起 → 重校验一轮,仍失败才回滚。三件套原样不动(D10),让行处理收在监督者侧。
- **公开仓库信任模型**:信任锚 = GitHub 账号 2FA;`.sha256` 只防下载损坏,且与 exe 同放一个 release,**不防恶意 release**(校验文件可被同批篡改);不上代码签名,GitHub attestation 留作将来项。上游凭据不入库(T39 规矩:真钥只活本机 config.toml)。

## Considered Options

- **双 rename + 微秒缺位窗口 + 手工恢复(早期草案)**:为"旧 exe 还在跑"设计的 rename 舞步;round1 主会话查证指出规格已先停旧守护,前提不成立,单次 MoveFileEx 即可消除窗口(F4/seam A)——否决,窗口整体消失而非收窄。
- **draft release 两段式**:多一步人工 publish,无实际收益——否决,直接 publish。
- **ubuntu runner / 交叉编译矩阵**:Linux 编译实测失败(见上),硬做只会让首个 tag 的 CI 翻车——否决(F5 实录)。
- **后台定时检查/自动升级**:何时升级必须由人决定——否决(纯手动,连后台自动检查都不做)。
- **agent 面触发升级(MCP update 工具)**:升级是系统级变更动作,agent 面只读红线不可破(ADR-0009)——否决。
- **代码签名/attestation 先行**:单人单机工具链,2FA + SHA256 与风险匹配,签名链维护成本不匹配——后置。

## 后果

发布收敛到打 tag 一个动作,每次发布有据可查;本地编译产物与 Release 可能版本漂移,版本报告以注入值(`ferryman version`)为准,`dev` 值即提示非正式渠道产物。自升级只覆盖 Windows amd64;原子性以 NTFS 日志为据,磁盘级损坏等更极端假设不在设防范围。停旧→拉起段仍存在看门竞态窗,由复停重试收敛,验收含抢跑演练。agent 面动词面零变化。随本决策落地的工作区清理(D13):删 `viewer/ferryman-timeline.exe`(空目录一并)与两个 `ferryman.exe.old-20260920*` 手工备份,`.gitignore` 的 `ferryman-timeline` 行随之删除(该文件本就被跟踪,ignore 行是冗余)。update 与 cutover 互不共用:版本轴升降走 update,语言栈往返仍走 `ferryman cutover`。
