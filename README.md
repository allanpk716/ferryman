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

1. 真人全闭环验收（真实会话闲置→警告→交接→/clear→注入→续接；观察周自然覆盖）
2. T26 观察周复盘（2026-09-17 起跑：observe/25min/35min/20k tokens，看误伤率）→ 达标切 enforce + Pushover 真机首拦
3. DeepSeek/GLM API key → E1 横评定默认路由
4. 30 天归档清理 / `ferryman status`·`stop`
5. Pi 适配器（会话格式最友好：JSONL 首行带 cwd/session_id）→ OpenCode（SQLite 轮询 PoC）
6. 多候选清单注入后清场（体验瑕疵）/ 健康信号细化

## 已完成（近）

- 钩子自举 + 唯一化（T35）：任意 agent 钩子触发即拉起 daemon，无需开机自启
- Codex 全链路（T23/37）：schema 实测定案、install-codex、Orca CODEX_HOME 守望、子代理钩子接线
- `ferryman doctor` 体检（T38）：钩子在位/脚本 BOM/控制字符/快照覆盖/daemon 活性

## 开发

```bash
uv run ferryman e0                  # E0a：全量 TTL 曲线
uv run ferryman eval-set            # E1：构建评测集
uv run ferryman eval --provider local [--regrade]
uv run ferryman serve [--smoke]     # 守护进程（配置：~/ferryman/config.toml 或 FERRYMAN_CONFIG）
uv run ferryman install-cc          # 钩子安装（检测到 CC Switch 时自动注入供应商快照）
uv run ferryman install-ccswitch    # 只注 CC Switch 快照（新增供应商后重跑；幂等自动备份）
uv run ferryman install-codex       # Codex 钩子注入 + 开 [features] hooks = true（改后 TUI /hooks 信任）
uv run ferryman doctor              # 一键体检：钩子/脚本/快照/daemon（静默失效类事故现形）
```
