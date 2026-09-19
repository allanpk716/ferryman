# Ferryman Go 一次性切换 · 核对表（runbook）

- 日期：2026-09-19（票23 产出；工具面 `internal/cutover` + `ferryman.exe cutover *`）
- 依据：spec `docs/superpowers/specs/20260919-ferryman-go-rewrite-spec.md` §Further Notes 切换时序；rev1 Task 26/27；评审附录 #4/#5/#6/#9/#10/#11
- 决策依据：ADR-0005（一次性切换：无并行期、无烧机期，切换即生产）
- 铁律：**本核对表由用户晨间拍板后执行**；夜链（票23）只交付工具与本文档，不执行任何生产切换。

---

## 0. 前置条件（全部满足才允许进入切换日）

### 0.1 全绿清点（对账基准 315）

- [ ] `go build ./...` 零错误；`go vet ./...` 零告警；改动文件 `gofmt` 干净。
- [ ] `go test ./...` 全绿，**按 315 基准清点移植测试**（387 总 − 冻结面 72：e0c 四文件 66 + eval_checks 5 + 冻结面复核调整 1；hooks 16 计入 315）。
  - 注：spec §Testing Decisions 初版记 316/71，夜链票面定稿 **315/72**——切换日以"实测全绿 + 冻结面清单"双口径对账，差一必须给出归因后再放行。
- [ ] 本机有 gcc 且 `go test -race ./internal/daemon/...` 可跑则必须全绿（C6 锁序实证）。已核实本开发机 mingw64 gcc 的 -race 产物无法加载（`exit status 0xc0000139`，连未改动的 pathsx 包同败）——此类"gcc 在位但 -race 起不来"视同无 gcc：以 C6 锁序人工评审记录补偿，不许默默跳过。

### 0.2 沙箱冒烟——工具面（自动，直打 API）

- [ ] `ferryman.exe cutover smoke` 四行全 PASS（①observe 警告 / ②摆渡 fresh+账本行 / ③归还注入 / ④enforce block 契约）。
  - 沙箱纪律已内建：独立临时端口 + 独立临时数据目录 + 恒成功假 provider（httptest）+ 通知关闭，**不占 7311、不写 ~/ferryman**（附录#11：直打 API 触发，不依赖钩子）。
  - ④ enforce block 契约断言：`decision=block` + reason 全文含交接文档路径 + `suppressOriginalPrompt=true` + `handoff_path` 与在库交接一致 + 账本 block 行在账。

### 0.3 沙箱冒烟——真 provider 摆渡链路（人工，直打 API）

- [ ] 写一份沙箱配置（复制 `config.example.toml`，`[gate] cc_mode="observe"`、`[server]` 换临时端口 + 临时 `data_dir`、`[ferry] provider` 与 `[providers.*]` 指**真实 provider**）。
- [ ] `FERRYMAN_CONFIG=<沙箱配置> ferryman.exe serve --no-tray`（前台）起沙箱守护——确认横幅端口 = 临时端口，**不是 7311**。
- [ ] 三链路逐条过（直打 API 或用真实小会话触发）：
  - [ ] ① gate 警告：闲置会话 POST /gate → `decision=allow` + `additional_context` 警告（observe 只提醒不拦）；
  - [ ] ② 真 provider 摆渡：会话达总结阈值 → handoff **fresh** 落盘 + 账本 `handoff` 行（outcome=fresh，model/provider/price_ver 与真实配置一致）；
  - [ ] ③ 归还注入：GET /restore → context 含「不可信」声明 + 注入层 + 完整交接文档路径（被拦场景还须含待续原话）。
- [ ] 收尾：Ctrl+C 停沙箱守护，删临时数据目录与沙箱配置。

### 0.4 enforce 沙箱 block 契约（附录#11：独立端口+独立数据目录）

已由 `cutover smoke` 链路④自动覆盖；如需复验加大样本，可用同款沙箱（`[gate] cc_mode="enforce"`）+ 预置交接直打 /gate，断言同 0.2-④。

---

## 1. 切换日步骤（按序执行，一步一勾）

> 仓库根 = `<repo>`（exe 由 build.ps1 出到仓库根，钩子安装默认以 exe 所在目录为 repo）。数据目录 = `~/ferryman`。

- [ ] **1.1 打 tag 存档 Python 最终态**
  ```bat
  cd /d <repo>
  git tag archive/python-final
  git push --tags
  ```

- [ ] **1.2 构建**
  ```bat
  powershell -ExecutionPolicy Bypass -File build.ps1
  ```
  （可选 `build.ps1 -Release`：-ldflags "-s -w" 变体；两者均为 console 子系统、无 windowsgui——C9。）

- [ ] **1.3 数据全量备份（附录#6）**
  ```bat
  ferryman.exe cutover backup
  ```
  - 备份到 `~/ferryman 同卷上级\ferryman-backup-<YYYYmmdd-HHMMSS>\`；内容：accounts/*.jsonl、handoffs/*.md、index.json、config.toml、daemon.token（daemon.pid 不备）。
  - **核对清单逐行 SHA-256**（工具自带复读回比对）；并 `diff -r` 抽查一份账本与一份交接。

- [ ] **1.4 回退工件生成 + 演练（附录#4/#5；演练通过才允许 1.8 的删除）**
  ```bat
  ferryman.exe cutover rollback-write
  ferryman.exe cutover rollback-drill
  ```
  - 工件 = `~/ferryman/rollback-to-python.cmd`（幂等批处理，内容为 ASCII 英文回显——中文说明见本文档；经实跑验证：临时 fixture 上完整走通 worktree 建立 + uv sync 建venv + 启动行改写，重复执行幂等）；内容：幂等 `git worktree add <repo>-py archive/python-final`（已存在则复用）→ `uv sync` → 点火脚本启动行指回 `<repo>-py` venv python。
  - 演练在临时目录走同款机制（临时 worktree + 临时启动器副本），**绝不碰真实 start-daemon.cmd**；tag 刚打上，演练应走全路径（worktree 建立 + `uv sync --dry-run`），退出码 0；重跑一次确认幂等。
  - **实跑校验（推荐）**：直接执行一次 `rollback-to-python.cmd`（幂等，即真实回退动作本身），确认 `<repo>-py` worktree 建立、`<repo>-py\.venv\Scripts\python.exe -m ferryman doctor` 可跑（rev1 Task27 Step4 的"演练通过才允许删除"钉子）。**校验后必须照常走完 1.6**——install-cc 的 EnsureLauncher 会把点火脚本改回 Go 形态，否则钩子自举会把 Python 版拉起来。
  - **注意：Tag 未打时演练自动降级为路径探测（不失败）——所以务必在 1.1 之后跑本步。**

- [ ] **1.5 停 Python 守护**
  - 托盘「退出」或关闭 start-daemon 窗口；确认 `netstat -ano | findstr :7311` 无监听。
  - 删 `~/ferryman/daemon.pid`（易失运行态，留着会让 doctor 误报）。

- [ ] **1.6 装三类钩子（C12 用户指令：UserPromptSubmit 闸门暂不装）**
  ```bat
  ferryman.exe install-cc --events SessionStart,SubagentStart,SubagentStop
  ferryman.exe install-codex --events SessionStart,SubagentStart,SubagentStop
  ferryman.exe --install-shortcuts
  ```
  - 检测到 CC Switch 则再跑 `ferryman.exe install-ccswitch`（地雷自动注入供应商快照，防切换供应商抹钩子）。
  - 核对：`~/.claude/settings.json` 只新增三类事件条目；`~/.codex/hooks.json` 同款 + `[features] hooks = true`。

- [ ] **1.7 首启验横幅 → 钩子自举接管 → doctor 全绿**
  ```bat
  ferryman.exe serve --no-tray
  ```
  - 前台确认横幅：`[ferryman] serve: 127.0.0.1:7311 · gate cc=… codex=… · provider=… · 数据目录 …`（C9：终端可见）。
  - Ctrl+C 停；之后由钩子自举拉起（agent 开会话即探 7311 不在则隐藏启动）。
  ```bat
  ferryman.exe doctor
  ```
  - **全绿**要求：钩子在位/脚本健康/快照覆盖/daemon 活性/provider 配置全 [OK]；「缺闸门钩子 UserPromptSubmit」与「心跳真实发送未实装」两条为**提示行，不判失败**（C12 / 附录#14），出现属预期。

- [ ] **1.8 删 Python 源（.venv 保留至观察期结束）**
  ```bat
  cd /d <repo>
  git rm -r ferryman tests pyproject.toml uv.lock
  git commit -m "feat(go): 一次性切换——Python 退役，tag archive/python-final + rollback 工件在位"
  ```
  - **`.venv/` 不删**（附录#5：观察期内保留离线重建条件——真回退时即使断网也有 venv 可续用）；24h 观察期结束且无回退后手动清。

---

## 2. 24h 观察清单

- [ ] **serve 日志**（`~/ferryman/serve.out.log` / `serve.err.log`）：
  - 无连续 ERROR/panic/任务异常刷屏；摆渡日志 `→ handoff` 正常出现、骨架降级（`降级骨架-only`）零星可解释；
  - 守护未被杀软/系统误杀（7311 常在：`netstat -ano | findstr :7311`）。
- [ ] **账本字段**（`~/ferryman/accounts/*.jsonl`）：
  - 行结构与切换前一致（`v,kind,ts,ts_iso,agent,session_id,lineage_id,project` + 科目字段）；
  - 科目白名单不出现未知 kind；`handoff` 行 outcome∈{fresh,failed}、tokens 字段非空；`block`/`inject`/`bypass` 行随实际事件成行。
- [ ] **三链路人工各过一遍**（真实会话，非沙箱）：
  - ① 闲置会话收到 observe 警告条（若线上 gate=observe）或按配置行为一致；
  - ② 一次真实摆渡产物可读、注入层/全文结构完整、路径与命令从骨架逐字引用（发现乱码/空壳立即触发回退评估）；
  - ③ 一次 /clear 后开场自动收到交接 + 被拦原话（若走了被拦路径）；子代理 Start/Stop 事件计数在 /stats 上涨。
- [ ] **面板数据**（http://127.0.0.1:15900 或托盘菜单）：
  - 时间线与账本一致、反跑可用、无"数据目录空"假象（若面板空而账本有数 → 见 §4 脑裂警示）。

---

## 3. 回退

### 3.1 触发条件（评审附录 #9/#10 定稿）

观察期内出现以下任一，即执行回退：

1. **摆渡产物损坏**——交接 MD 乱码/空壳/结构节缺失，或骨架降级率异常升高且不可热修；
2. **账本字段错乱**——accounts/*.jsonl 行字段缺失/类型错/科目键异常（影响计费口径即触发）；
3. **三类已装钩子异常**——SessionStart / SubagentStart / SubagentStop 不触发、超时或持续报错（交接注入/子代理计数失灵）；
4. **守护或面板崩溃且不可热修**——重复崩溃、无法以日志定位修复。

**不在列**：gate 误拦/漏拦——生产未装 UserPromptSubmit 闸门钩子（C12 用户指令），该通道**不可观察**，无从触发也不作为回退依据；疑似闸门行为请先核对是否真闸门（大概率是钩子/会话自身问题）。

### 3.2 回退步骤

```bat
C:\Users\%USERNAME%\ferryman\rollback-to-python.cmd
```

- 脚本幂等可重跑（回显为 ASCII 英文，`[rollback]` 前缀）；执行后点火脚本 `~/ferryman/start-daemon.cmd` 启动行已指回 `<repo>-py` venv python。
- 前置：Go 守护先停（托盘「退出」或关 start-daemon 窗口）。
- 脚本失败排查：`git worktree add failed` → 确认 tag 已打/`<repo>-py` 未被非 worktree 目录占用；`uv sync failed` → 断网时若 `<repo>-py\.venv` 尚存可直接续用（这正是 1.8 保留 .venv 的原因）。
- 钩子无需改动：`ferryman-*.ps1` 只探 127.0.0.1:7311，谁在监听谁接管。
- 回退后按 §2 清单反向核对 Python 版恢复正常，再决定修复后重切或停留。

---

## 4. 已知取舍与遗留（夜链停靠清单要点，如实声明）

- **面板数据目录脑裂警示（票22 Minor1）**：守护的数据目录读 `config.toml [server].data_dir`（缺省 `~/ferryman`），面板只认 `FERRYMAN_DATA` / `--data` / `~/ferryman`，**不读 config.toml**——若自定义了 `server.data_dir` 而不给面板对齐 env，面板会显示旧位置（数据"看起来没了"）。处置：自定义 data_dir 的用户必须同时设 `FERRYMAN_DATA` 指向同目录；缺省部署两者自然一致。
- **serve 不自动开浏览器（决策）**：`ferryman serve` 正常启动不弹浏览器——serve 常由钩子自举/快捷方式拉起（隐藏窗口），弹浏览器会让每次会话启动都开网页。面板入口 = 桌面快捷方式 / 托盘「打开面板」/ 手动 URL；仅"面板口被占且探到活面板"时转开浏览器（快捷方式语义：点一下必达面板）。
- **HttpBeatSender 功能退化声明（附录#14）**：心跳真实 HTTP 发送不实装（Q14 未授权），接口位保留；enforce 心跳模式自动回落 observe 演练并告警。切换日 doctor 会输出该声明行（提示，不判失败）——线上心跳表现为"只演练记账，不真发"。
- **骨架交接最坏情况短暂入生产**（spec §Further Notes 已声明）：一次性切换无烧机期，切换初期的摆渡失败会以骨架交接兜底——观察清单 §2-② 对此重点盯防。
