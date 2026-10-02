# 票03 · 新命令 ferryman status / stop

## What to build
新增两条顶层命令:①`ferryman status`:守护探活(管理口 /stats,Bearer token 同 provider switch 取法)+版本+渡口监听状态(15722 探测)+台账摘要(在册会话数/闲置数,经 /stats 或既有端点);守护不在线如实报告各面状态不报错退非零——exit 0(状态查询不是失败);②`ferryman stop`:POST /shutdown(既有端点,internal/daemon/httpapi.go)+等待端口释放与进程退场(排水窗语义,预算参照换装监督者 240s 量级可配旗标 --wait),超时如实报告并 exit 1,绝不硬杀。两者 usage 进顶层帮助。输出人话、无密钥。

## 验收标准
- [ ] `ferryman status`:守护在线时输出版本/渡口/台账摘要 exit 0;不在线时各面如实标"不在线"仍 exit 0
- [ ] `ferryman stop`:优雅停(发 /shutdown→等让位),成功 exit 0;超时 exit 1 且不 kill
- [ ] `status -h`/`stop -h` 走票01 契约(exit 0 打印用法)
- [ ] status 不修改任何文件;stop 只经端点(单测用 httptest 桩钉请求形态)
- [ ] go test ./cmd/ferryman/ 绿

## Blocked by
票01(help 契约先定,main.go 同文件避免冲突)

## 涉及路径
cmd/ferryman/main.go
cmd/ferryman/status.go(新建)
cmd/ferryman/status_test.go(新建)

## 副作用声明
无独占验证命令;单测用 httptest,不触碰真实守护

## decision_refs
D9、D8

## review_blocks
无
