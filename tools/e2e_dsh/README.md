# tools/e2e_dsh — DSH E2E 沙箱栈（票07 · 可复用夹具）

隔离 daemon + 隔离 profile web 实例，一键起停 + 健康检查。给 DSH 热缓存压缩
战役（`.scratch/dsh-hot-compaction/spec.md`）的 Playwright E2E（票08）当验收栈，
也是任何需要「第二个 Ferryman daemon ＋ 第二个 dsh web 实例」场景的通用夹具。

## 用法

```bash
bash tools/e2e_dsh/start.sh     # 起栈（幂等：已在则复用，不叠进程）
bash tools/e2e_dsh/health.sh    # 健康检查（daemon /stats、面板口、web 口）
bash tools/e2e_dsh/stop.sh      # 拆栈（优雅 /shutdown 优先；逐口核验无残留）
bash tools/e2e_dsh/selftest.sh  # lib.sh 纯函数自测（不起栈不占口）
```

## 端口与路径（默认值，env 可覆写）

| 项 | 默认 | 覆写变量 |
|---|---|---|
| daemon 控制口 | 25900 | `E2E_DSH_DAEMON_PORT` |
| serve 面板口 | 25901 | `E2E_DSH_PANEL_PORT` |
| dsh web 实例口 | 25902 | `E2E_DSH_WEB_PORT` |
| 沙箱根 | `%TEMP%/ferryman-e2e-dsh` | `E2E_DSH_ROOT` |
| dsh CLI | 本机 DeepSeek Harness 安装位 | `DSH_CLI` |
| dsh home 复制源（只读） | `~/.dsh` | `DSH_HOME_SRC` |

铁闸：三口必须全落 25xxx 段（`require_port_25xxx`，越界即拒跑）——生产口
3080/15700/15722/3081 天然越界。

## 隔离契约（实现方式与实证）

- **沙箱 daemon 是本仓测试构建**：`go build -o <沙箱>/bin/ferryman-e2e.exe
  ./cmd/ferryman`，不经生产 exe。测试 config（`<沙箱>/config.toml`，由
  `FERRYMAN_CONFIG` 注入）：`[server] port=25900 + data_dir=<沙箱>`；秒级阈值
  `summarize_s=45 / block_s=90`（配 `serve --smoke` 放宽阈值差≥120s 校验）；
  `[gate] dsh_mode="enforce"`（E2E 真拦）；**无 `[dock]` 节＝渡口完全不启动**
  （沙箱永不绑 15722）；watch 三目录全指沙箱内。
- **USERPROFILE 钉进沙箱**（关键，别删）：daemon 以
  `USERPROFILE=<沙箱根>` 启动。`watcher.CodexWatchDirs`（internal/daemon/
  watcher.go）会**无条件自动追加** `<home>/AppData/Roaming/orca/
  codex-runtime-home/home/sessions`（生产 codex 会话目录，配置关不掉）——不钉
  USERPROFILE 沙箱 daemon 就会读生产会话转录并写 skeleton handoffs（实测泄漏
  14 个生产会话）；钉了之后 codex/CC/DSH 三个 watch 面全落沙箱，附带给
  `ferry.LoadProviders` 硬编码的 `~/ferryman/config.toml` 读取也钉进了沙箱
  （缺文件＝空 provider 骨架，零生产接触）。
- **沙箱数据目录每次全新**：fresh start 时 `rm -rf <沙箱>/ferryman-data`，
  上次运行的台账/handoffs/日志不带入。
- **隔离 dsh web 实例**：`prepare_sandbox_home` robocopy 整份 `~/.dsh` →
  `<沙箱>/dsh-home`（junction 跟随 deref，profile 自含），然后：
  - sessions 清空（生产会话转录不进沙箱）；remote-link/start-web 脚本/
    runkeys/`*.bak-*` 剔除；
  - home 层 `cordis.patch.yml` 摘除 `llm-deepseek` 条目（其 baseURL 钉生产渡口
    127.0.0.1:15722——沙箱模型流量不得经生产口；两个 disabled 遥测条目的生产
    姿态保留）；
  - profile 层换生成版 `cordis.patch.yml`：只留 ferryman-dsh 插件挂载；
    phone-remote bundle 条目剔除（其 config 钉生产 3081）、四条 MCP 注入剔除
    （npx 子进程＝闪窗+网络双风险）、密钥零携带；`package.json` 的 bundles 表
    同步摘除该 bundle；
  - 启动 env：`DSH_HOME=<沙箱>/dsh-home`＋`FERRYMAN_PORT=25900`＋
    `FERRYMAN_TOKEN_FILE=<沙箱>/ferryman-data/daemon.token`（插件
    `src/config.ts` resolveConfig 的环境约定，daemonURL 由此指沙箱）＋
    `env -u FERRYMAN_DISABLE`。
- **起栈前隔离扫描**（`isolation_scan`）：沙箱 config.toml、home 层 patch、
  profile 层 patch、package.json 四文件过 `15700|15722|3080|3081|15900` 残迹
  扫描，命中即拒跑。

## 生产接触面审计（诚实清单）

- `~/.dsh` 与 `~/ferryman`：**只读复制源**，沙箱从不写这两处；生产数据目录、
  生产端口、生产 profile 全程零接触（实测：起停前后生产四口 PID 不变，
  `~/ferryman` 无新文件）。
- `.env`/`.credentials.yaml`（模型凭据）会随 `~/.dsh` 副本进入沙箱 home——
  同机同用户盘内复制，供票08 E2E 跑真会话用；沙箱用完可整目录删
  （`rm -rf <沙箱根>` 即彻底清除）。
- dsh 自家 bundle（`@deepseek-ai/dsh-web-app` 等）不经副本解析——生产源的
  `$DSH_HOME/profiles/node_modules` 是指向宿主 app 资源的 junction 群（生产侧
  本就大量悬垂，dsh 回落 app 自带副本），本机同宿主启动等价；副本断言只钉
  真实自含面（ferryman-dsh 实体、dsh-hypatia、eventsource 等）。
- robocopy 退出码 bit-8 容忍：生产源 `profiles/node_modules` 悬垂 junction 群
  （`cp -L` 同报 607 处）属源侧既有状态；真实完整性由起栈前的结构断言兜底。

## Windows 零闪窗口径

所有脚本一律 bash 入口（CC Bash 工具/CI shell——自带隐藏控制台，子进程
`go build`、`ferryman-e2e.exe`、cmd 包装的 dsh.cmd 继承隐藏控制台）；dsh web
宿主是 GUI 子系统的 DeepSeek Harness.exe（ELECTRON_RUN_AS_NODE），天生无控制
台；沙箱 profile 已剔 MCP（无 npx）。禁 PowerShell Start-Process 形态。

## 拆栈语义

daemon 走 POST /shutdown（Bearer，token 在 `<沙箱>/ferryman-data/daemon.token`）
优雅收口；15s 不退按端口找 PID 强杀进程树兜底。web 实例按端口找 win PID 杀树。
收尾逐口（25900/25901/25902）核验，任何残留非零退出。栈未起时跑 = 空操作。

## 旋钮补充

- `E2E_DSH_REBUILD_HOME=1`：丢弃 dsh home 副本重新整备（生产 profile 大改后用）。
- `E2E_DSH_KEEP_ON_FAIL=1`：起栈失败不自动拆栈，留现场排障。
- 票08 起栈后可直接复用：web UI 地址打印在 start.sh 尾部
  （`http://127.0.0.1:25902/?token=...`，登录 token 也在 `<沙箱>/logs/web.log`）；
  daemon 三口约定（`/dsh/gate`、`/dsh/event`、`/dsh/handoff` 与管理口 /stats）
  同 token。
