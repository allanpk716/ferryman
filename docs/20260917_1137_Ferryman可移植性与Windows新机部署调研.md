# Ferryman 可移植性 / Windows 新机部署调研

> 日期：2026-09-17 · 方法：仓库源码/脚本/文档/测试静态通读 + 本机测试套件实跑（114 passed, 1 deselected slow）
> 基线 commit：`ecb55b6`（2026-09-17 11:19）。所有结论以仓库一手材料为准，逐条标注 `文件:行号`。
> 本报告为调研产物；其发现的 A1 硬编码问题已于同日修复（T39）：代码不再内置任何默认
> provider，未配置时 worker 警告 + doctor 检查，作者自用网关配置迁至仓库外本机 config.toml。
> 注意：git 历史与 GitHub 远端旧提交中该 IP 仍存在（是否重写历史待定），本报告已脱敏。

---

## ① 项目一句话概述与运行原理

**Ferryman（摆渡人）是本机常驻的 Windows 守护进程**：轮询 Claude Code（`~/.claude/projects/**/*.jsonl`）与 Codex CLI（`~/.codex/sessions/**/rollout-*.jsonl`，外加 Orca 运行时目录）的会话文件（`ferryman/daemon.py:57-59`、`daemon.py:29-44`），闲置超过总结阈值（默认 25min，`config.py:33`）后调用廉价模型把会话总结成"交接 MD"（`ferryman/ferry.py`），并通过各 CLI 的提交前钩子拦截"凉会话"（闲置 ≥35min，`config.py:34`），引导 `/clear` 开新会话时自动注入交接（`ferryman/server.py:202-224`）。四个动作：守望→摆渡→闸门→归还（`README.md:5`）。

**装在哪 / 怎么被拉起（关键链路）**：

1. 代码本身**不需要"安装"**：git clone 后零运行时依赖（`pyproject.toml:7` `dependencies = []`，纯 Python 标准库），仓库放哪都行。
2. `uv run ferryman install-cc` 做三件事（`ferryman/install.py:87-127`）：
   - 生成点火脚本 `~/ferryman/start-daemon.cmd`（`install.py:27-58`）——内含**仓库绝对路径** + 绝对 venv python 路径（无 venv 则回落 `uv --directory` 运行，`install.py:37-42`）；
   - 把四段钩子（UserPromptSubmit / SessionStart / SubagentStart / SubagentStop）**以绝对路径 PowerShell 命令**追加进 `~/.claude/settings.json`（改前自动备份，`install.py:95-97`）；
   - 检测到 CC Switch（`~/.cc-switch/cc-switch.db`）时把钩子同步注入全部 claude 供应商快照（`install.py:118-127`、`install.py:202-254`）。
3. **不做开机自启**（DESIGN §8 明确延期，`docs/DESIGN.md:103`）：任意 agent 的任意钩子触发时先 TCP 探测 `127.0.0.1:7311`（250ms 上限），不在则隐藏窗口拉起 `~/ferryman/start-daemon.cmd`（`hooks/ferryman-ensure.ps1:5-28`），daemon 进程独立于钩子存活；并发点火由"绑定失败→/stats 探测→唯一化跳过"三层收敛（`ferryman/server.py:300-307`、`daemon.py:233-240`）。
4. daemon 绑定 `127.0.0.1:7311`（`config.py:40`、`server.py:365`），Bearer 鉴权 token 自动生成于 `~/ferryman/daemon.token`（`server.py:265-274`）；数据目录 `~/ferryman/`（handoffs/、index.json、daemon.pid、日志）自动创建（`config.py:65`、`daemon.py:213`）。
5. Codex 侧由 `uv run ferryman install-codex` 注入 `~/.codex/hooks.json` + 确保 `config.toml` 的 `[features] hooks = true`（`install.py:130-199`）；改后需在 Codex TUI `/hooks` 重新信任（`install.py:196-197`）。
6. `uv run ferryman doctor` 一键体检：钩子在位/脚本 BOM 与控制字符/快照覆盖/点火脚本/daemon 活性（`ferryman/doctor.py:147-187`）。

源码获取：GitHub 公开仓库 `https://github.com/allanpk716/ferryman.git`（`git remote -v` 实查；2026-09-17 网页验证为 Public 可克隆）。

---

## ② 他人 Windows 机器上的困难清单（按严重程度排序）

### A. 会直接失败 / 核心功能缺失（必须处理才能正常用）

| # | 问题 | 后果 | 依据 |
|---|------|------|------|
| A1 | **默认摆渡模型路由曾指向作者私有内网网关**：内置唯一 provider `local` 指向作者自建网关（**地址已脱敏**），且 `ferry_provider` 默认曾为 `"local"`。**✅ 2026-09-17 T39 已修复**：移除内置默认（现默认为空=未配置），provider 全量经 config.toml | 新机器不可达该网关 → **每次摆渡 HTTP 失败**。不会崩（优雅降级为"骨架交接"，`daemon.py:188-191`），闸门/归还仍工作，但**交接只有程序化骨架、没有模型叙事**——产品核心价值（模型总结）全丢。必须写 `~/ferryman/config.toml` 配 DeepSeek/GLM 并设 `[ferry] provider` | 原 `ferryman/ferry.py:65-71`、`ferryman/config.py:61`；修复后 worker 警告 + doctor 检查 |
| A2 | **Python ≥ 3.12 硬要求**：`requires-python = ">=3.12"`，且代码用 `tomllib`（3.11+ 标准）、`list[str]`/`X \| None` 原生语法 | 机器上没有 3.12+ 则 uv 直接拒绝创建环境；推荐直接装 uv（会自动管 Python） | `pyproject.toml:6`、`config.py:12`、`uv.lock:3` |
| A3 | **uv（或手动建 venv）必须可用**：`.venv/` 被 gitignore（`.gitignore:3`），clone 后不存在；点火脚本优先用 `repo/.venv/Scripts/python.exe`，否则回落 `uv --directory "<repo>" run ferryman serve` | 不装 uv 也不手动建 venv → 钩子自举时点火脚本落到 uv 分支而 uv 不在 PATH → daemon 永远拉不起来（钩子侧 fail-open 静默，无任何报错） | `.gitignore:3`、`install.py:37-42`、静默性 `hooks/ferryman-ensure.ps1:15-19` |
| A4 | **强 Windows + Windows PowerShell 假设**：钩子命令写死 `powershell -NoProfile -ExecutionPolicy Bypass -File ...`（Windows PowerShell 5.1，Win10 自带）；notify 的 Toast 走 WinRT | 另一台 **Windows 10/11 机器自带，没问题**；但 macOS/Linux 上 `powershell` 不存在（那边叫 `pwsh`）→ 全部钩子 fail-open 静默 → Ferryman 完全不工作。本项目就是 Windows-first 设计，跨平台未支持 | `install.py:63`、`install.py:144`、`notify.py:35-56` |
| A5 | **Claude Code 本体必须已安装且在用**：Ferryman 监视的是 CC 的会话目录，钩子要 CC 来触发 | 新机器若没装/没用 Claude Code → 没有会话可守望、没有钩子触发 → daemon 即使拉起也空转。Codex 同理（可选） | `daemon.py:57`（`~/.claude/projects`）、`config.py:23-24` |
| A6 | **Codex 集成的隐藏门槛**：Codex 钩子默认关闭，需 `[features] hooks = true`（install-codex 自动开）；**改完 hooks.json 必须在 Codex TUI 里 `/hooks` 人工重新信任**，否则整包静默 fail-open；`codex exec` 不派发钩子是上游已知 bug | 只跑 install-codex 不做 TUI 信任 → Codex 侧完全静默失效（表面无异常）。这是文档明示的"静默失效"事故类型 | `install.py:132-139`、`install.py:196-197`、`docs/T23-CODEX-TRUST.md:10-12,20-22` |
| A7 | **CC 版本要求**：SubagentStart/SubagentStop 钩子需 CC ≥ 2.1.273 | 旧版 CC 不触发子代理事件 → 子代理运行中会被误判闲置（有 T31 悬空 tool_use 检测兜底，功能弱化而非失败） | `install.py:75-76`、`docs/DESIGN.md:35` |

### B. 需要手动配置（不配则功能残缺或体验异常）

| # | 问题 | 依据 |
|---|------|------|
| B1 | **`~/ferryman/config.toml` 必须手动创建**（仓库不带真实配置，永不入库）：至少处理 `[providers.*]` 的 api_key 与 `[ferry] provider`；可选 `[notify]`、阈值、端口 | `config.example.toml:1-2`、`config.py:72-114` |
| B2 | **CC Switch 顺序陷阱**：若新机器日后才装 CC Switch，切换供应商会把 settings.json 里的钩子逐字抹掉——install-cc 已在没检测到 db 时打印提醒，但需要用户记得重跑 `install-cc` / `install-ccswitch`（新增供应商后也要重跑） | `install.py:120-122`、`install.py:206-208`、`docs/DESIGN.md:23` |
| B3 | **端口改动三处不联动**：daemon 端口在 config.toml `[server] port`；但钩子只认环境变量 `FERRYMAN_PORT`（用户级）或默认 7311，不读 config.toml；`doctor` 更是硬编码探测 7311 | `config.py:40`、`hooks/ferryman-gate.ps1:13`、`doctor.py:171`。默认 7311 不冲突就别改；要改必须 config.toml + 用户环境变量 FERRYMAN_PORT 两处同步（doctor 仍会误报） |
| B4 | **`[server] data_dir` 改动的 token 联动陷阱**：daemon 把 token 写到 `data_dir/daemon.token`（默认 `~/ferryman`），但钩子写死读 `%USERPROFILE%\ferryman\daemon.token`（除非设 `FERRYMAN_TOKEN_FILE`），doctor 也读 `~/ferryman` | 改 data_dir → 钩子读到旧/无 token → 401 → fail-open 全放行（闸门静默失效）。默认值下无问题 | `server.py:265-274`、`daemon.py:214`、`hooks/ferryman-gate.ps1:14-15`、`doctor.py:150,168` |
| B5 | **通知默认全关**：`[notify] enabled` 默认 false；要手机 Pushover 需 token/user（config 或 `PUSHOVER_TOKEN`/`PUSHOVER_USER` 环境变量），桌面 Toast 仅 Win10+ | `config.py:46-50`、`notify.py:69-70`、`config.example.toml:25-33` |
| B6 | **仓库路径一经安装不可随意移动/改名**：settings.json / hooks.json 里注册的是安装那一刻的仓库绝对路径；移动后钩子指向不存在路径（fail-open 静默）。重跑 install-* 可修复，doctor 能查出 | `install.py:67,73,78,83`（`repo / "hooks" / ...`）、`doctor.py:47-49` |
| B7 | **整目录拷贝迁移会带坏 .venv**：venv 内是绝对路径（pyvenv.cfg、脚本头），换机器/换路径后失效 | 必须 `uv sync` 重建（或删掉 `.venv` 让 launcher 走 uv 回落分支） | `.gitignore:3`、`install.py:37-42` |

### C. 仅提示 / 边缘情况（默认配置下一般无感）

| # | 问题 | 依据/说明 |
|---|------|-----------|
| C1 | 防火墙/杀软：daemon 只绑 `127.0.0.1`（回环），通常不触发 Windows 防火墙弹窗；但"隐藏窗口拉起 cmd"行为可能被激进杀软标记（**未实测，推测**） | `server.py:365`、`ferryman-ensure.ps1:17-19` |
| C2 | 中文编码三重防护已做，新机基本无感：钩子显式 UTF-8 Console 编码 + UTF-8 字节 body（对抗 PS5.1 的 ANSI/GBK 默认）；7 个 ps1 全带 UTF-8 BOM（对抗 PS5.1 按 ANSI 解析中文注释；本机逐文件验证 `ef bb bf`），doctor 有 BOM 与控制字符检查 | `hooks/ferryman-gate.ps1:10-11,26-31`、`doctor.py:55-72`；BOM 随 git 内容字节走，`core.autocrlf=true` 不影响（无 .gitattributes，实测 `git config core.autocrlf` = true） |
| C3 | `serve.out.log`/`serve.err.log` 编码：Python stdout 重定向到文件时按 locale 编码（中文 Windows 为 GBK）——日志在 VS Code 等按 UTF-8 打开的编辑器里可能显示乱码；极端字符理论上可触发 UnicodeEncodeError（**推测，未实测**） | `install.py:47-48`（重定向写法） |
| C4 | 端口 7311 被其他程序占用：serve 区分"已是本程序（唯一化跳过）"与"非本程序占用（启动失败退出 1）"，钩子侧 fail-open 不受影响 | `daemon.py:233-240`、`server.py:286-297` |
| C5 | 多用户/服务账户不适用：一切状态在当前用户 home（`Path.home()`），无系统级安装 | `config.py:17,65`、`install.py:21-24` |
| C6 | `daemon.token` 的 `chmod 0o600` 在 Windows 上尽力而为（try/except 吞掉），权限弱于 POSIX | `server.py:270-273` |
| C7 | 评测（eval/e0/e0b）产物含真实会话内容但**不入库**：`eval/`、`.e2e/` 已 gitignore——新机器 clone 不会带来也不会泄露作者会话数据；但想复现 eval 需要自己机器上已有 CC 历史会话 | `.gitignore:6-8`、`README.md:19`、`eval_set.py:160-163` |
| C8 | Orca 集成为可选自动发现：`%APPDATA%\orca\codex-runtime-home\home\sessions` 存在才追加扫描，不装 Orca 无影响 | `daemon.py:40-43`、`config.example.toml:35-38` |
| C9 | 深层路径/长路径：`**/*.jsonl` glob 未做 MAX_PATH 特殊处理；`~/.codex/sessions/年/月/日/rollout-*.jsonl` 常规长度安全（**未实测极端长度**） | `daemon.py:75,93` |

---

## ③ 新人机器配置步骤清单（从零到全功能）

前置条件：Windows 10/11；已在使用 Claude Code（想覆盖 Codex 则还需 Codex CLI ≥0.153 并完成 TUI 信任）；能访问外网（DeepSeek/GLM API 或自有 OpenAI 兼容推理端点）。

```text
1. 安装 uv（官方 https://docs.astral.sh/uv/ 的安装命令；会自带/托管 Python ≥3.12）
   —— 这是唯一的"全局工具"依赖（git 亦需要，用于 clone）。

2. clone 仓库（路径随意；但装好后不要移动/改名）：
   git clone https://github.com/allanpk716/ferryman.git
   cd ferryman

3. 建环境（创建 .venv，锁定的依赖极小：运行时零依赖，dev 仅 pytest）：
   uv sync

4. 写运行时配置（唯一必改项是摆渡模型；不改则摆渡永远降级骨架，见 A1）：
   mkdir ~/ferryman   # 若不存在
   cp config.example.toml ~/ferryman/config.toml
   编辑 ~/ferryman/config.toml：
     a) 在 [providers.deepseek] 或 [providers.glm] 填 api_key（或自建 [providers.xxx] 指向
        任何 OpenAI 兼容端点，给足 base_url/model/window）；
     b) 追加并设置  [ferry] provider = "deepseek"（或你的 provider 名）
        —— 不设置则摆渡永远降级骨架（T39 后默认即未配置；worker 警告、doctor 提示）；
     c) 可选：[notify] enabled = true + Pushover 凭据（或设 PUSHOVER_TOKEN/PUSHOVER_USER
        用户环境变量）；可选调 [thresholds]（校验铁律：summarize_s < block_s 且差 ≥120s，
        违例拒启，config.py:117-133）。
   注意：不要改 [server] port / data_dir，除非愿意处理 B3/B4 的联动。

5. 安装 Claude Code 钩子：
   uv run ferryman install-cc
   —— 自动：生成 ~/ferryman/start-daemon.cmd + 写 ~/.claude/settings.json（自动备份）
   + 检测到 CC Switch 则同步注入全部供应商快照。
   —— 若本机装有 CC Switch：以后新增供应商、或切换后钩子被抹，重跑
      `uv run ferryman install-ccswitch`。

6.（可选）Codex 集成：
   uv run ferryman install-codex
   然后【必须】打开 Codex TUI 执行 /hooks 完成信任（不做则静默失效，见 A6）。

7. 体检：
   uv run ferryman doctor
   —— 期望全部 [OK]。若 daemon 未运行属正常（尚未拉起）：开一个新 CC 会话随便发一条
   消息，SessionStart/UserPromptSubmit 钩子会自动点火 start-daemon.cmd（钩子自举）；
   或直接手动运行 %USERPROFILE%\ferryman\start-daemon.cmd。

8. 验证闭环：
   a) 再跑一次 uv run ferryman doctor —— daemon 活性应为 OK；
   b) 看 %USERPROFILE%\ferryman\serve.out.log 有 "serve: 127.0.0.1:7311 ..." 启动行；
   c) 干活 25+ 分钟闲置后应看到警告/拦截与 ~/ferryman/handoffs/ 下生出交接 MD；
   d) 应急开关：环境变量 FERRYMAN_DISABLE=1 可让全部钩子短路（hooks/ferryman-gate.ps1:4）；
      被拦时以「强续」开头发消息可单次放行（server.py:113-115）。

9.（可选）跑测试确认环境健康：
   .venv/Scripts/python.exe -m pytest -q -m "not slow"   # 本机实测 114 passed
```

卸载（文档未写，此处从代码反推）：删除 `~/.claude/settings.json` 中 command 含 `ferryman` 的钩子条目（安装时的 `settings.json.bak-ferryman-*` 备份可对照）、`~/.codex/hooks.json` 同理、`~/ferryman/` 整个数据目录、关闭 daemon 进程（pid 在 `~/ferryman/daemon.pid`）。

---

## ④ 代码中所有硬编码 / 机器特定假设清单表

| 位置 | 内容 | 他人机器上的影响 | 严重度 |
|------|------|------------------|--------|
| `ferryman/ferry.py:65-71`（T39 已移除） | `DEFAULT_PROVIDERS["local"]` 曾指向作者私有内网网关（IP/模型名已脱敏；git 历史中仍存在） | 已修复：无内置默认，未配置即显式警告+骨架降级 | ~~**高**（A1）~~ 已修复 |
| `ferryman/config.py:61` | `ferry_provider` 默认曾为 `"local"`（T39 改为空=未配置） | 已修复：未配置时 worker 警告 + doctor 提示 | ~~**高**（A1）~~ 已修复 |
| `config.example.toml:4-9` | 同一网关注释与模型名（有 `<你的本地推理网关>` 占位，示例性质） | 误导风险低，但读者可能以为 local 可用 | 中 |
| `ferryman/doctor.py:171` | 探测硬编码 `http://127.0.0.1:7311/stats`，不读 config/FERRYMAN_PORT | 改端口后 doctor 误报"daemon 未运行"（**算缺陷**） | 中 |
| `hooks/ferryman-gate.ps1:13-15`（及 restore/subagent/codex 各钩子同款） | 端口只认 `FERRYMAN_PORT` 环境变量或默认 7311；token 路径写死 `%USERPROFILE%\ferryman\daemon.token`（只认 `FERRYMAN_TOKEN_FILE` 覆盖） | 与 config.toml 的 `[server]` 不联动（B3/B4） | 中 |
| `ferryman/install.py:47-48` | 点火脚本日志写死 `%USERPROFILE%\ferryman\serve.{out,err}.log`（不走 data_dir 参数） | 自定义 data_dir 时日志位置与数据分离 | 低 |
| `ferryman/install.py:37` | venv python 路径按 `os.name == "nt"` 选 `Scripts/python.exe` | 跨平台分支已写（`bin/python`），但钩子仍是 powershell（A4） | 低 |
| `ferryman/daemon.py:40-43` | Orca 运行时目录 = `home/AppData/Roaming/orca/codex-runtime-home/home/sessions`（Windows 布局） | 非 Windows 永远 exists()=False，不炸；Windows 无 Orca 同样跳过 | 低 |
| `ferryman/install.py:21-24` | CC Switch / Codex 配置位置假定 `~/.cc-switch/cc-switch.db`、`~/.codex/hooks.json`、`~/.codex/config.toml` | 标准布局，无 Codex/CC Switch 时优雅跳过（`install.py:211-213`） | 低 |
| `hooks/ferryman-gate-codex.ps1:24-25` | 抓包开关写死 `~/ferryman/hook-debug/ON` 标记文件 | 仅调试用，不存在即关闭 | 低 |
| `docs/DESIGN.md:92` | 文档写死仓库路径 `C:\WorkSpace\agent\Ferryman` | 仅文档表述；install 实际按 `__file__` 推导（`install.py:90`），任意克隆路径可用 | 低 |
| `tests/test_ccswitch.py:29` | 测试夹具含 `C:/Users/allan716/...` 字符串 | 仅测试数据（模拟既有 Orca 条目），不影响运行时 | 无 |
| 全仓库（除上述） | **未发现**用户名、机器名、盘符的其他硬编码；所有用户级路径均经 `Path.home()` / `$env:USERPROFILE` / `%USERPROFILE%` 展开 | —— | —— |
| 隐含假设合集 | `powershell` 在 PATH（钩子、`notify.py:52`）；`uv` 在 PATH（launcher 回落，`install.py:42`）；`$env:ComSpec`（`ferryman-ensure.ps1:17`）；CC ≥2.1.273（`install.py:75`）；Codex 0.153 钩子 schema（`docs/T23-CODEX-TRUST.md:16-18`）；ASCII/UTF-8 文件系统语义（`store.py:96,153` 路径小写归一，Windows 不区分大小写前提） | Win10/11 默认满足；跨平台不满足 | 视平台 |

---

## ⑤ 文档缺口与改进建议

现有文档状况：`README.md:37-48` 列了开发命令（与 `__main__.py` 实际子命令一致，已核对无漂移）；`docs/DESIGN.md` 是设计定案非部署手册；`config.example.toml` 注释质量高；`docs/T23-CODEX-TRUST.md` 详尽记录 Codex 信任流。**缺一份"全新机器部署指南"**。具体缺口：

1. **无安装前置条件章节**：没写需要 uv、Python ≥3.12（或 uv 自动托管）、Windows PowerShell、Claude Code 在用；README 的 `uv run ...` 命令对没装 uv 的人无法起步。
2. **无首次配置 walkthrough**：没写"复制 config.example.toml → ~/ferryman/config.toml → 填 key → 必须改 `[ferry] provider`"这条主线；`config.example.toml:2` 只说"未配置时仅内置 local 默认项可用"，没有点破 local 是作者私有内网 IP、新机器必然不可达（本调研 A1）。
3. **Codex /hooks 信任这一步埋在 T23 文档里**：README `install-codex` 行（`README.md:46`）只提"改后 TUI /hooks 信任"，但没解释不做会"静默全失效"。
4. **环境变量无汇总**：`FERRYMAN_DISABLE` / `FERRYMAN_PORT` / `FERRYMAN_TOKEN_FILE` / `FERRYMAN_CONFIG` / `FERRYMAN_HOOK_DEBUG` / `PUSHOVER_TOKEN` / `PUSHOVER_USER` 散落在各钩子脚本头注释与 config 注释中，无统一表。
5. **端口/data_dir 的联动陷阱无任何记载**（B3/B4），doctor 端口硬编码建议改为读 config（顺手修的缺陷）。
6. **无卸载/重装文档**（备份文件命名规则 `*.bak-ferryman-<ts>` 也未记载）。
7. **无"如何验证装好了"章节**：doctor 的期望输出、serve.out.log 的启动行样式未记载（doctor 输出样式见 `doctor.py:181-186`）。
8. 建议顺手补：`.gitattributes` 显式声明 `*.ps1 text eol=crlf`（当前依赖 autocrlf 默认；虽然 PS5.1 兼容 LF，统一可避免极端工具链问题）；~~`ferry.py` 的 DEFAULT_PROVIDERS 把 IP 抽到 config 并在缺省时打印醒目警告~~（✅ 已于 T39 落地）。

---

## ⑥ 没能确认 / 没能验证的点（局限）

1. **未做真正的异机实测**：全部结论来自源码静态阅读 + 原作者机器上测试套件实跑（114 passed, 1 deselected，2026-09-17）。"新机器会不会炸"的第一手验证缺失，尤其 uv 全新安装 → `uv sync` → install-cc 的完整链路未在干净环境复现。
2. **防火墙/杀软行为**（C1）为推测：仅依据"绑定 127.0.0.1 回环通常不触发防火墙弹窗"的一般知识，未实测；杀软对 `Start-Process -WindowStyle Hidden cmd /c start-daemon.cmd` 的拦截行为完全未验证。
3. **`serve.out.log` 的实际编码**（C3）未验证：Python 3.13 Windows 重定向 stdout 的 locale 编码行为按文档推断，未实际写入检查字节。
4. **Codex TUI `/hooks` 信任流程**只依据 `docs/T23-CODEX-TRUST.md` 的记录（作者机器已验），未在新机器验证"全新 Codex 安装 + install-codex + 信任"链路。
5. **CC Switch 场景**只在代码与 DESIGN §3/T21 记录层面确认，未在新机器验证 install-cc 对真实 cc-switch.db 的注入。
6. **macOS/Linux 行为**（A4）是代码推断（`powershell` 命令名不存在 → fail-open），未实测；POSIX 分支的点火脚本（`install.py:49-54`）与 powershell 钩子组合的实际效果未知。
7. **GitHub 仓库公开性**是 2026-09-17 单次网页验证，无法保证持续公开；作者若转私有，"clone 即用"路径失效。
8. **长路径/超大目录**（C9）、**睡眠唤醒后台娃存活**（TEST_PLAN T30 承认未完成）未验证。
9. `eval/`、`.e2e/`、`reports/` 中的历史产物在本机存在，但 `eval/` 与 `.e2e/` 不入库（`.gitignore:6-8`）——新机器 clone 后内容为空属预期，未逐一核对每个产物文件的入库状态（`reports/e0a-cc-glm.md`、`reports/e0b-codex.md` 已确认入库）。
