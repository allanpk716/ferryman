# dsh 后续与近期工作清单（2026-10-03）

> 核对基准（本清单每条都核过实物，非凭记忆）：PR #4 已开待合（dsh 接管，2 commits）；生产守护 v0.5.2；xcheck 夜链 27 commits 在途（票03-13，10-03 09:42 仍在提交）冲 v0.5.3；doctor 27 项 22 绿 5 红；cc-switch.exe 在跑（PID 12284）。
>
> 过时记忆已修正一条：同模型 34 连败两修**已随 v0.5.1 进生产**（2026-10-01 21:18 换装，memory 正文有全记录；索引行的"均随下次发版"已过时），不再属发版积压。

## ① 立即修：生产面三件事（今天就值得做）

**A. 接管又被打回 cc-switch（4 红：provider_cc_dock / provider_codex_dock / provider_orca_codex / codex_hooks）**
- [x] 跑 `ferryman provider apply` 修复：CC/codex/orca-codex 回渡口 15722。（✅ 10-03 09:50 完成：三份 written、同戳备份 20261003-095014、未触发他人文件拒写；外加快照 `~/ferryman/rollback-snapshots/20261003-0949/` 三份原件）
- [x] 根因实锤一半：cc-switch.exe 活着（PID 12284），且 10-02 apply 修完后又被写回——观察期结论=不卸会一直复发。（✅ 10-03 实证：修复后 90 秒+ 未被再写回，改写是事件触发非轮询；但进程仍在，复发风险未除）
- [ ] 根治=走 W4 收尾（白天用户动作）：托盘退出 cc-switch → 关自启 → 卸载 → 重启后 doctor 复检（渡口 responses 翻译车道 v0.4.0 起已顶上，cc-switch 的翻译不再需要）
- [x] 修后复验：doctor provider_* 三项 + codex_hooks 回绿。（✅ 10-03 09:51 起 26/27，update 后 27/27 全绿）

**B. 升级残留（1 红：update_residues）**
- [x] `ferryman.exe update` 同版本整套换装自愈（清 ferryman.exe.supervisor-copy），或留到 v0.5.3 换装一并清。（✅ 10-03 09:53 完成：静默门 60s 未等到位走硬切兜底——恰有 1 笔周期性在途（大概率心跳），切断点距末次请求 40s 应无活跃流；换装后旧 PID 11668 退场、新 PID 22868 双口在听、supervisor-copy/swap-tmp 双清、doctor 27/27 无首跳旧茬；备份 exe.old-v0.5.2 在场）

注：生产 v0.5.2 的 apply 不含 dsh 目标（dsh 分支合并发版后才进生产）；`~/.dsh/` 两文件现状不受影响。

## ② v0.5.3 合线（顺序有讲究）

- [ ] xcheck 夜链收尾后开 PR **先合**（27 commits：pi 写入器票10、doctor pi 探针票11、pi 可用性位票12、v0.5.3 退役清单票05、--json 面票04、stop PID 时机票03 等）
- [ ] PR #4（dsh）**后合**：两线同动 `cmd/ferryman/provider.go` / `internal/provider/backup.go` / `internal/provider/writer.go` 三文件，后合方必须 rebase 解冲突——dsh 侧小而已冻结，当后合方成本最低
- [ ] 合并后 doctor 全绿口径更新：票11 起 provider 探针计数 +1（pi）；dsh 探针若按同款模式补，计数再 +1

## ③ 发版 v0.5.3（流程纪律照旧）

- [ ] 全仓 `go test ./...` 落日志过一遍（教训：交接班声称的全绿 ≠ 最终 diff 状态，只跑 touched 包漏过 cutover 烧过一轮 CI）
- [ ] race 复验走 NUC10（本机 -race 工具链坏；匿名克隆 + GOPROXY=goproxy.cn + 输出落文件）
- [ ] 资产名硬契约：`ferryman_windows_amd64.exe` + `.sha256`（内容 `hash *文件名\n`；名字错=updater 静默死无日志）
- [ ] tag v0.5.3 → CI 出资产 → 监督者 `./ferryman.exe update` → **等旧进程退场**（端口空≠进程走）→ 冷启动 3s
- [ ] 换装时顺带自证：v0.4.4 副本自删真机首跳；supervisor-copy 残留清掉；doctor 首跳旧茬（差 1-2 项）属预期、下次自愈
- [ ] 发版后生产验三件：dsh 流量记账标 `agent=dsh`（UA 分岔）；票11 pi 探针亮绿；`provider apply` 首跑含 dsh/pi 新目标且全 unchanged

## ④ dsh phase 2（发版后开新 worktree）

- [ ] **P2-1 Go 解 zstd**：读 `~/.dsh/sessions/` 会话日志进台账——dsh 会话闲置判定的前提（调研定案：root 编码唯一性禁改 compression，必须 Go 侧解）
- [ ] **P2-2 会话键对齐**：目录名 `session-<uuid>` ≠ 请求体 `metadata.session_id`，台账/心跳统一对齐到后者（渡口捕获键已零改动就位）
- [ ] **P2-3 闸门**：dsh agent/pre-step 拦截（CC 钩子桥 or 原生插件，调研已定落点）；GLM 生成注入 thinking:disabled 教训沿用（ADR-0015/0016）
- 调研底稿：`docs/research/20261002` 两篇（随 PR #4 入库）

## ⑤ 决策项（用户拍板，不急但别忘）

- [ ] observe→enforce 升档：09-28 复核达标（104 警告零漏拦零 block），至今未拍板
- [ ] ADR-0016 同模型摆渡灰度：09-29 启用，首周复核 ≈10-06
- [ ] 渡口直连战役评估日 10-07（周扫参任务跑到 10-31）
- [ ] 排期二选一：dsh phase 2 vs 工作台战役 W0（归并 widget 0.2.6 线，main 停 0.2.1）——两者都吃主力开发时间
- [ ] 手机链路：若提示 authentication required，把交接文档（20261003_085019）里的 token 链接发手机开一次
