# 票 06 · doctor 新增「升级事务残留」检查

**What to build**:
`ferryman doctor` 增加一项检查:发现 `~/ferryman/` 下 `update-journal.json` / `ferryman.exe.new` / `ferryman.exe.swap-tmp` 残留时,结论清单报该项并给一行处置建议(运行 `ferryman update` 会自动恢复/清理;极端缺位时把 swap-tmp 改回的提示)。

**验收标准**:
- [ ] 无残留 → 检查通过不噪声
- [ ] 有 journal/残留 → 结论行 + 一行处置建议
- [ ] `go test ./internal/installer/` 绿

**Blocked by**: 05(journal/残留物语义)
**涉及路径**: internal/installer/doctor.go, internal/installer/doctor_test.go
**副作用声明**: 无
**decision_refs**: D9
**review_blocks**: F4(配套检测)
