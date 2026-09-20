# 票 05 · doctor 工具：进程内复用＋不可达结构化结果

## What to build

第六件工具 doctor（用户视角：agent 跑体检拿结构化结果，daemon 不在线时也能拿到"daemon 活性异常"的结论）：

1. **doctor 检查逻辑可复用化**：internal/installer/doctor.go 的既有检查（钩子在位、脚本 BOM/控制字符、快照覆盖、daemon 活性）重构为可编程调用的结构化结果（逐项：名称、通过/失败/未检查、一句话说明）——CLI 输出行为不变（人面零漂移），只是内部多一条结构化出口。daemon 活性检查经 config 解析目标（同 internal/config 优先级）。
2. **internal/mcp 增加 doctor 工具**：进程内调用上述结构化检查（不经 HTTP），结果原样作为工具响应返回。
3. **doctor 例外语义**：daemon 不可达/超时 → 照常返回结构化体检结果，daemon 活性项＝失败/异常；不缓存、不伪造、不自举。
4. 用户目录相关检查（钩子在位等）面向 config/HOME 解析出的目标；测试中以临时 HOME 覆盖或该检查项显式标"未检查"，不读真实用户目录。

## 验收标准

- [ ] doctor 工具返回逐项结构化结果（名称/状态/说明三要素齐全）
- [ ] daemon 在线（临时 daemon）时活性项通过；daemon 不可达时活性项＝异常且**仍返回完整结构化结果**（不是工具错误）
- [ ] 两次调用结果独立重算（不缓存）；全程无新进程/监听（不自举）
- [ ] CLI `ferryman doctor` 人面输出与改动前一致（既有 doctor 测试保持绿）
- [ ] 测试用临时 HOME/临时 config，不读真实用户目录；断言端口非生产端口
- [ ] 响应无消息内容、无凭据字段、无 token（反向断言）
- [ ] go test ./internal/mcp/ ./internal/installer/ -count=1 全绿；gofmt 干净

## Blocked by
票 04

## 涉及路径
- internal/installer/doctor.go（重构：结构化出口，CLI 行为不变）
- internal/installer/doctor_test.go（补充结构化出口测试；既有用例不动）
- internal/mcp/tool_doctor.go（新建）
- internal/mcp/tool_doctor_test.go（新建）

## 副作用声明
- 独占验证命令：go test ./internal/mcp/ ./internal/installer/ -count=1

## decision_refs: D5、D6、D10（doctor 例外）
## review_blocks: 无
