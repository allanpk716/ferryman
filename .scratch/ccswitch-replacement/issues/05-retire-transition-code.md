# 票05 · v0.5.3 退役清单五处(删换装过渡代码)

## What to build
删除 v0.5.2 换装机制换代留下的过渡代码(代码内已留注记,v0.5.3 既定):①internal/update 锁判活"家族②"(supervisor-copy 过渡判据,lock.go holderAlive 及其三族注释改两族);②relayCopyPath 函数(supervisor.go);③cmd/ferryman/main.go 的 --self-relay 旗标行(现解析但忽略);④对应回归测试钉(lock_test 中钉家族②/supervisor-copy 的用例随之删改);⑤cleanSwapResidues 的 supervisor-copy* 清扫模式。注意:doctor 的 update_residues 检查若引用该清扫模式需同步调整其残留判定文案(旧茬已在 v0.5.2→v0.5.3 换装时自愈,清扫模式删除后 doctor 不再把 supervisor-copy 当已知残留形态——核实该检查的判定源后同步)。

## 验收标准
- [ ] 全仓 grep 无 relayCopyPath/self-relay/supervisor-copy 残留(除 git 历史与 .xcheck)
- [ ] internal/update 测试全绿且锁判活用例反映两族口径
- [ ] go build ./... 与 go vet ./... 过
- [ ] go test ./... 全绿

## Blocked by
无,可立即开始(main.go 仅删一行,与票01 调度互斥)

## 涉及路径
internal/update/lock.go
internal/update/supervisor.go
internal/update/lock_test.go
internal/update/supervisor_test.go(仅首行注释——票14 R1 已改 15700,本票核实在位即可)
internal/update/swap_residues.go(如清扫模式在此;核实实际文件后以目录 internal/update/ 为准)
cmd/ferryman/main.go

## 副作用声明
无独占验证命令;go test ./internal/update/ ./cmd/ferryman/

## decision_refs
D12

## review_blocks
无
