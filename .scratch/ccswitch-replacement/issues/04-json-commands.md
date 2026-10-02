# 票04 · doctor/upstream list/provider list 补 --json(含脱敏契约)

## What to build
三命令增加 --json 机器可读输出:①`ferryman doctor --json`:复用 internal/installer 既有 DoctorStructured(agent 面 MCP 同源)序列化,顶层含 version 与总判定;②`ferryman upstream list --json` 与 `ferryman provider list --json`:同表结构化(active/base_url/dialect/codex 与 pi 可用性(若票09 已落)/model_map 概要/密钥一律尾4位掩码)。**脱敏契约(硬性)**:三命令 --json 输出不得包含任何明文 api_key/token——统一走既有 maskKey(尾4位)形态;每命令加"输出不含明文 api_key"断言单测(用带真实形态假钥的夹具,断言 JSON 全文不含该钥原文);字段表底稿写入各命令 usage 注释(票06 成文用)。

## 验收标准
- [ ] 三命令 --json 输出合法 JSON、含上述字段
- [ ] 假钥夹具下,三命令 --json 全文不含明文钥(断言测试)
- [ ] 无 --json 时文本输出行为不变
- [ ] doctor --json 与 MCP 面 DoctorStructured 字段同源
- [ ] go test ./cmd/ferryman/ ./internal/installer/ 绿

## Blocked by
票01(同文件冲突避免;usage 注释同处)

## 涉及路径
cmd/ferryman/main.go
cmd/ferryman/provider.go
cmd/ferryman/upstream.go
cmd/ferryman/provider_test.go
cmd/ferryman/upstream_test.go
internal/installer/doctor.go
internal/installer/doctor_test.go

## 副作用声明
无独占验证命令;go test ./cmd/ferryman/ ./internal/installer/

## decision_refs
D8;F3 脱敏契约

## review_blocks
无
