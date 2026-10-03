# 票13 · 弃用处置:upstream use / install-ccswitch / ccswitch_snapshots

## What to build
cc-switch 替换的配套退役处置:①upstream use 标弃用:命令仍可用但输出首行打弃用警示"冷切换重启守护已弃用,请用 ferryman provider switch(热切换零重启)",usage 与顶层 usage 同步注明"(弃用)";代码保留一版不删;②install-ccswitch 标弃用:usage 注明"cc-switch 替换完成后此命令退役;搬家用 provider import-ccswitch",命令本身保留;③doctor 的 ccswitch_snapshots 检查项:输出文案加"(弃用路线:cc-switch 卸载后此项随卸载移除)"——不删检查逻辑(cc-switch 还在机上时仍有用);④import-ccswitch 保留注记(搬家工具,不弃用);⑤顶层 usage「工具」组补 cutover 族一行(票01 评审建议:灾备命令顶层可发现性,细账仍在 cutoverUsage);⑥顺手修 doctor.go 头注"清单 24→27"陈旧计数(实际 26→27,票11 评审移交)。

## 验收标准
- [ ] upstream use 输出首行弃用警示;provider switch 不受影响
- [ ] install-ccswitch usage 含弃用说明
- [ ] doctor ccswitch_snapshots 文案含退役路线注记
- [ ] 顶层 usage 的 upstream 条目标(弃用);工具组含 cutover 一行
- [ ] go test ./cmd/ferryman/ ./internal/installer/ 绿

## Blocked by
票01(usage 重构先定)、票04(同文件)

## 涉及路径
cmd/ferryman/upstream.go
cmd/ferryman/upstream_test.go
cmd/ferryman/main.go
cmd/ferryman/provider.go(仅 usage 文案)
internal/installer/doctor.go

## 副作用声明
无独占验证命令;go test ./cmd/ferryman/ ./internal/installer/

## decision_refs
D15、D7

## review_blocks
无
