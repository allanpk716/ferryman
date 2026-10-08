# 票 04 · CLI 接线 `ferryman verify-dsh`（L0+L1＋灯色分层＋--status＋红灯告警）

## What to build

`ferryman verify-dsh` CLI 子命令（照 cmd/ferryman/main.go 既有子命令先例接线）。本轮接 L0+L1 与档案面，L2 留显式接缝（接口或 stage 枚举，票 05 填充；L2 未实现时输出"L2 探针：未装配"不算绿也不算红，整体判定降为"未完成验证"）：

1. **编排**（internal/dshverify/run.go）：L0（票 02 包）→ L1（经 daemon 管理口 GET /dsh/health，票 01）→ 灯色判定 → dshledger 记流水（daemon 版本取生产 daemon /stats 自报；daemon 不在则记 CLI 自身版本并注明）。
2. **灯色规则**（表驱动，可测）：红 = L0 任一 profile fail ／ poll 超龄且宿主进程在跑；黄 = 当前 DSH 版本不在流水（未验证）／ poll 超龄但宿主未运行；绿 = 全过（且 L2 装配后全绿才落锚——L2 未装配时不落锚不滚已知良好）。
3. **输出分层**：三 profile 各一行（L0/L1 各自结果）＋L2 单行（明标"仅代表 web 宿主形态（沙箱）"）；绿灯总义在输出尾注明＝"web 沙箱功能全验证＋生产三 profile 静态/挂载验证"。
4. **`--status`**：只读显示当前 DSH 版本 vs 已知良好 vs 最近流水行。
5. **红灯告警**：复用既有降级告警发送器（internal/notify 中摆渡路由 Pushover/Toast 通道，找现有 sender 复用，勿新造）；文本区分"插件失联（有宿主无 poll）"与"验证失败（××项未过）"，附降级目标（已知良好三元组＋installer 路径若登记）。黄不推。**只在命令运行时判定与推送，无任何常驻循环。**

## 验收标准
- [ ] 灯色矩阵表驱动单测全绿（超龄×宿主在/不在×流水有无×L0 成败×L2 未装配各态）
- [ ] 压缩链无关本票；但"L2 未装配→不落锚不滚指针"有专测
- [ ] --status 输出含当前/已知良好/最近流水三要素
- [ ] 红灯路径调用告警 sender（注入假 sender 断言文本含降级目标）；黄路径零调用
- [ ] `go test ./internal/dshverify/` 绿；`go build ./cmd/ferryman` 过

## Blocked by
票 01、票 02、票 03

## 涉及路径
- internal/dshverify/run.go（新，编排＋灯色＋输出）
- internal/dshverify/run_test.go（新）
- internal/dshverify/health_client.go（新，/dsh/health 与 /stats 的 HTTP 客户端）
- cmd/ferryman/main.go（子命令注册与 usage）

## 副作用声明
仅单测与本地构建；测试注入假 HTTP/假 sender，不打真端口不真推送

decision_refs: D2、D4、D5、D6、D10（黄不推红才推）、F2/F5/F6 钉死项
review_blocks: 无
