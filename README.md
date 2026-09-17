# Ferryman / 摆渡人

本机常驻守护进程：监控 Claude Code / Codex 的会话文件，闲置超时后自动用廉价模型总结成"交接 MD"，经提交前钩子拦截"凉会话"，引导 /clear 后开新会话并自动注入交接——避免为死缓存全量重付 input 费用/额度。

四个动作：**守望（watch）→ 摆渡（ferry）→ 闸门（gate）→ 归还（restore）**

## 文档

- [docs/DESIGN.md](docs/DESIGN.md) — 设计定案 v3（开发依据）
- [CONTEXT.md](CONTEXT.md) — 领域词汇表
- [docs/adr/](docs/adr/) — 架构决策记录
- [docs/](docs/) — 原始调研与交接文档

## 状态

- [x] 设计定案（三轮异构交叉评审收敛）
- [x] E0a：CC/GLM 缓存 TTL 实测（[reports/e0a-cc-glm.md](reports/e0a-cc-glm.md)）
- [x] E0b：Codex 侧 TTL（数据薄，拐点未定位，gate_mode=off 维持）与计费语义（cached≈10%，代价成立）（[reports/e0b-codex.md](reports/e0b-codex.md)）
- [x] E1 基建 + local 候选实测（③续接 15/15、防注入通过、SLA 校准 L1 9-78s / L2 217-252s；评测产物含本机会话内容不入库，本地跑 eval 复现）——待 DeepSeek/GLM key 横评定路由
- [x] daemon 最小闭环（台账+lineage / 守望轮询 / 摆渡队列+skeleton降级 / /gate 状态机全分支 E2E 验证 / /restore 注入 / /stats 健康 / Bearer 鉴权；E2E 产物在 `.e2e/`，gitignored）

## 待办（按优先级）

1. DeepSeek/GLM API key → E1 横评定默认路由
2. `ferryman install-cc` 实装验证——检测到 CC Switch 时**自动**把钩子注进全部 claude 供应商快照（DESIGN §3，2026-09-17 T21 定案：切换=快照逐字写入，不在快照里的 hooks 会被抹）。新增供应商后重跑 `ferryman install-ccswitch`。
3. claude-notify 接入（拦截时 Toast/Pushover）
4. 稳定快照双读协议（DESIGN §6.13，现为单读 + covers_until）
5. Codex 钩子（E0b 数据积累后 observe → enforce）
6. `ferryman doctor` / 30 天归档清理（开机自启已由**钩子自举**取代：任意 agent 的钩子触发时探测 :7311，不在则拉起 `~/ferryman/start-daemon.cmd`，见 DESIGN §3）

## 开发

```bash
uv run ferryman e0                  # E0a：全量 TTL 曲线
uv run ferryman eval-set            # E1：构建评测集
uv run ferryman eval --provider local [--regrade]
uv run ferryman serve [--smoke]     # 守护进程（配置：~/ferryman/config.toml 或 FERRYMAN_CONFIG）
uv run ferryman install-cc          # 钩子安装（检测到 CC Switch 时自动注入供应商快照）
uv run ferryman install-ccswitch    # 只注 CC Switch 快照（新增供应商后重跑；幂等自动备份）
```
