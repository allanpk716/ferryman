# Ferryman 合并 exe（守护+面板+托盘+CLI，票22）

> 入口在本目录（`cmd/ferryman`，吸收原 `cmd/viewer`），内部包在
> `internal/viewer/{server,demo,ledger}`，web 前端与 `icon.ico` 随本包放置
> （`go:embed` 只能引用包目录子树）。以下构建/开发命令一律在仓库根执行。

单一 exe：守护（127.0.0.1:7311）+ 时间线面板（15900）+ 托盘 + 全部 CLI 子命令。
面板读取 `accounts/*.jsonl` 流水，提供会话列表、单会话 token 时序图（SVG 手绘）与
心跳"反跑"仿真面板。前端为原生 HTML/JS/CSS 三件套，经 `go:embed` 打进 exe，无任何
外部资源（离线铁律：无 CDN、无字体、无图标库）。

## 构建

需 Go 1.23+（路由用了方法+路径 pattern）。**console 子系统**（spec C9：doctor/
report/install/serve 横幅在终端全部可见；窗口性交给启动方——点火脚本经
ensure.ps1 的 `Start-Process -WindowStyle Hidden` 拉起，快捷方式
WindowStyle=minimized）：

```
powershell -File build.ps1            # = go build -o ferryman.exe ./cmd/ferryman
powershell -File build.ps1 -Release   # -ldflags "-s -w" 变体（同样无 windowsgui）
```

## 运行

```
ferryman.exe                # 无参 = serve：守护(7311)+面板(15900)+托盘
ferryman.exe serve [--no-browser]    # 同上（点火脚本 start-daemon.cmd 调它；面板口被占探到活面板时不自动开浏览器）
ferryman.exe serve --no-tray --smoke    # 前台 Ctrl+C 退出；放宽阈值差校验
ferryman.exe doctor         # 一键体检
ferryman.exe install-cc [--events SessionStart,SubagentStart,SubagentStop]
ferryman.exe install-ccswitch
ferryman.exe install-codex [--events ...]
ferryman.exe account report --json

# 面板族（viewer 原样，只起面板不起守护）：
ferryman.exe --demo --no-tray --no-browser             # 演示合成账本
ferryman.exe --data D:\data\ferryman --port 8787      # 根下无 *.jsonl 而有 accounts/ 时自动下钻
ferryman.exe --install-shortcuts                       # 建桌面+开始菜单快捷方式后退出
```

- 数据目录来源优先级：`--data` > 环境变量 `FERRYMAN_DATA` > `~/ferryman`；三者同为
  **数据根语义**——目录本身没有 `*.jsonl` 而其下有 `accounts/` 子目录时自动下钻一层。
- 端口缺省随机，启动横幅打印实际 URL（固定 `127.0.0.1`，不对外监听）。
- **托盘**（T47）：帆船图标常驻通知区，菜单「打开面板 / 退出」（合并 exe 的退出 =
  停守护）。面板固定口被占且探到 `/api/sessions` 活着 = 面板已在跑 → 直接开浏览器
  （快捷方式因此"点一下必达面板"）。
- **快捷方式**：`--install-shortcuts` 建「Ferryman 面板.lnk」（桌面 + 开始菜单，
  目标 `ferryman.exe serve --port 15900`、WindowStyle=minimized——双击不闪黑窗、
  托盘常驻；图标取 exe 同目录 `icon.ico`）。想钉任务栏：右键 .lnk → 固定到任务栏。

**只读声明**：查看器对数据目录只读——每次请求现读账本、不缓存、绝不写任何文件。

## 参数配置页（T47）

页面右上「参数配置」（`#/cfg`）：只读展示守护进程 `config.toml`（数据根下，与
`accounts/` 同级）——段名即 TOML 表（嵌套拍平 `prices.glm`），段内按键序。形似密钥
的键（`api_key`/`token`/末段 key/auth…）后端脱敏为「••• 已隐藏」，明文密钥永不进页面；
数量词如 `min_ctx_tokens` 不误伤。找不到/解析失败页面如实说明，不编造。改参数请编辑
文件本身（守护进程重启后生效），查看器不在页面上改。

## 反跑口径

反跑 = 对某个已关闭的等待窗口，按假设的心跳配置重算成本，与真实发生、什么都不做三方对比。
参数预填 GLM 口径（`config.toml [prices.glm]` v2026-09-17：p_in=6.9 / p_cache=1.7 /
p_out=24 / per=10000；ttl_s=600 为实测缓存寿命，见
`docs/20260917_1630_GLM缓存TTL实测与心跳保温可行性_实验报告.md` §4）。

- **跳点**：首跳 T0+τ，此后每 τ 一跳（τ = safety × TTL，safety 缺省 0.8）；停在
  `min(窗末, T0+cap)`，窗口不够长自然少跳。
- **cap（划算上限）**：`cap = τ × (全款 − 缓存读) / 单跳`；手动 `max_wait_s` **只能往下收**，
  收窄即截跳（结果卡注脚同时给出 auto 值）。
- **三线对比**：若当时这样配（跳数 × 单跳）vs 实际发生（窗口内真实 beat 行的
  Σcost_actual，未启用心跳则灰"—（未启用）"）vs 什么都不做（整窗 ≤ TTL 存活为 0
  即"存活无损"，否则过期一次付全款）。
- **拒绝口径**：p_cache 缺省或 ttl_s ≤ 0 → 拒绝推导（宁可不算不造数），结果卡红字展示，
  不返回任何估算数。

## 公式同步规则（ADR-0003）

按 `docs/adr/0003-backend-migrate-to-go.md` 定案，策略公式**单源迁移**：
票 03 起公式单源在 `internal/policy`（viewer 手抄副本已销毁）——
**任何 `ferryman/policy.py` 的公式改动只需同步 `internal/policy`**，
黄金测试锚定同一批实验数。同步时同时核对
`internal/server` 的响应键名（前端依赖 `result`/`beats`/`beats_cost`/`do_nothing_cost`）。

## 开发

```
go vet ./... && go test ./...
```

- `internal/viewer/ledger`：账本读取与聚合（对齐 `ferryman/accounts.py` 的字段口径）。
- `internal/policy`：心跳推导公式（对齐 `ferryman/policy.py`，黄金数字锚定；票 03 起全仓单源，
  viewer 的 server/demo 与反跑端点同引此包）。
- `internal/viewer/server`：只读 JSON API（`/api/sessions`、`/api/timeline`、`/api/backtest`、
  `/api/config`）。
- `cmd/ferryman/web/`：前端三件套。纪律：账本数据只经 createElement/textContent/setAttribute 进 DOM
  （不拼 innerHTML）；跨页与连点均有竞态守卫；畸形数值一律兜底不白屏。
