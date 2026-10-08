# 票 05 · L2 沙箱插件探针（四痕＋压缩链两道互不抵消）

## What to build

新包 `internal/dshsandbox/` ＋填票 04 留下的 L2 接缝。这是全链最重的票，规格钉死项一条不许放宽：

1. **沙箱起栈（Go 实现，无 bash/go build 依赖）**：
   - 沙箱 daemon = **已安装的生产 ferryman exe** 起第二实例（定位方式：os.Executable() 自身路径），带沙箱 env（FERRYMAN_CONFIG/FERRYMAN_DATA/USERPROFILE 钉临时沙箱目录）＋`--port 25901 --no-tray --no-browser` 形态（参数照 tools/e2e_dsh/start.sh:85-98 的既有形状）。**一切子进程 Windows 分支加 SysProcAttr{HideWindow:true}（零闪窗铁律）。**
   - 沙箱 DSH 宿主 = `$DSH_CLI web --no-open --port 25902`（DSH_CLI 缺省=本机安装位 `...\resources\runtime\cli\bin\dsh.cmd`，照 tools/e2e_dsh/lib.sh:27；DSH_HOME 钉沙箱）。DSH 不可用时 L2 明确报"环境缺 DSH"跳过（不算绿）。
   - **插件备料同构**：沙箱 profile 的 `ferryman-dsh` 以 junction 指向备料快照目录（从仓库 plugin/ferryman-dsh 快照拷贝），`node_modules` 拷贝保持——镜像生产安装形态。
   - 端口避让：与 e2e 并跑时文件锁（固定锁文件路径，抢不到换 25904-25909 段并记录）。
2. **探针**：起栈→建/复用探针会话→发一条真消息（真模型，走沙箱 daemon 配置的最便宜上游）→ 断言：
   - 闸门到达：该消息的 /dsh/gate 问询在沙箱 daemon 落痕（日志或账本行）
   - 事件上报：usage 科目有该 sid 行
   - 会话键：sid 呈 session-<uuid> 形态
   - 注入路径：/dsh/handoff 通道可达（零副作用探针形态，插件 selfcheck 有先例）
   - **压缩链两道各自执行并各自见上报：命令道（execute '/compact'）与服务面（compactNow）各自跑一次压缩、各自见到上报；任一道失败该道红灯，禁止以另一道通过抵消；某道契约缺失记"契约缺失：××道"红灯**（用户拍板"都做"，任何放宽需用户重新确认）
3. **收尾**：沙箱栈全停（进程树击杀）、临时目录清理、探针消息的流水行带探针标记；结果填入票 04 接缝，全绿才落锚/滚已知良好。
4. **契约锚采集**：从沙箱实际形状采集四类契约面签名交 dshledger（票 03 的 Write/DiffFaces）。

参考：tools/e2e_dsh/setup.sh:59-72,184-238（备料/env/宿主拉起形状）、suite 的断言思路（kind=inject＋新会话标记）。**参考其形状但用 Go 重写，不 shell 出 bash 脚本。**

## 验收标准
- [ ] 沙箱起栈/收尾单测（注入假 exe/假 DSH_CLI，断言 env、参数、junction 备料、端口锁、进程全停）
- [ ] 断言器表驱动单测：四痕各自缺一→对应项 fail；压缩链 stub 掉服务面→**服务面红灯且命令道通过不抵消**（两道独立判定）
- [ ] 契约缺失形态：注入"compactNow 不可用"→记"契约缺失：服务面"红灯
- [ ] HideWindow 全覆盖（代码评审项＋进程创建集中一处工厂函数）
- [ ] `go test ./internal/dshsandbox/` 绿；`go build ./cmd/ferryman` 过
- [ ] 真机端到端冒烟（有 DSH 环境）：跑一次 verify-dsh 得到 L2 行——若环境不可用记 DONE_WITH_CONCERNS 说明

## Blocked by
票 04

## 涉及路径
- internal/dshsandbox/（新包：stack.go/probe.go/lanes.go/anchor_collect.go 及测试）
- internal/dshverify/run.go（L2 接缝填充，一处）

## 副作用声明
单测注入假进程不真起栈；真机冒烟（若执行）独占 25900-25909 端口段与临时目录，全部子进程隐藏窗口；不碰生产 15700/3080；不跑全仓测试

decision_refs: D3（沙箱+真模型+一次性）、D9（压缩链都做，两道互不抵消）、F1/F3/F4/F8 钉死项
review_blocks: 无
