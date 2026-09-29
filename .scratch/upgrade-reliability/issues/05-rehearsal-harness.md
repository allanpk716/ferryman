# 05 · 升级彩排台（影子实例+三场景+故障注入）

## What to build
可复用彩排资产 tools/rehearsal/：
1. 影子实例装配：独立 DataDir（临时目录）、独立双端口、独立 start-daemon.cmd、本地桩发布端点（假 release 给两版本与 exe 路径）。用 update.Supervisor 可注入 Config（DataDir/Port/Endpoints/StartCmd/HTTP/PollTimeout）。
2. 两版 exe：同二进制+版本注入差异的轻形态（/stats version 可区分）；注入不可行则 go build 两次不同 ldflags。
3. 三场景（D10 口径）：静默路径（间歇流量间隙 ≥10s→门放行→零截断+拒连窗 ≤10s+连续 10 次事务）；强制路径（--force+1rps→如实记录不判 ≤10s，断言事务完成）；长流场景（在途流 >60s 慢速 SSE→零截断+硬切边界有记录）。
4. 故障注入集（通用）：拉起即杀（StartCmd 换秒退假脚本）、蜂群抢拉旧版（停旧后旧 exe 抢绑管理口）、陈旧锁、备份位被占、排水中强断——逐项断言按设计恢复。
5. 结果落盘 .scratch/upgrade-reliability/rehearsal/（场景×断言×耗时）。
6. 一切端口从装配注入，禁止触碰生产 15700/15722。

## 验收标准
- [ ] 一条命令可跑（go run ./tools/rehearsal -all 或 go test ./tools/rehearsal/）
- [ ] 三场景断言全绿（静默路径含连续 10 次事务）
- [ ] 五项故障注入逐项恢复断言通过
- [ ] 零生产端口访问（装配注入可证）
- [ ] 结果文件落盘可查

## Blocked by
01、02、03、04

## 涉及路径
tools/rehearsal/（新目录）

## 副作用声明
go build 与本地高位端口监听允许；go test ./tools/rehearsal/ 允许

## decision_refs
D3、D6、D10

## review_blocks
无
