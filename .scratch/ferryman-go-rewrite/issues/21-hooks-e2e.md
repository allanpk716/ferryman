# 票 21 · hooks PS1 端到端测试

**What to build**：`internal/installer/test_hooks_test.go`：tests/test_hooks.py 16 例 1:1 移植——每个用例真跑 `powershell -NoProfile -ExecutionPolicy Bypass -File hooks/*.ps1`（stdin 喂 JSON），断言 exit code 与输出 JSON。测试内起真守护监听临时端口（httpapi），钩子端口经环境变量指向它。fail-open 语义用例（守护关/401/超时→exit 0 放行）照抄。仅 `runtime.GOOS=="windows"` 执行，其余 t.Skip。

参照：rev1 Task 21。

**验收标准**：
- [ ] 16 例全部移植且在 Windows 下绿
- [ ] block 场景 exit 2 与 suppressOriginalPrompt 语义与 test_hooks.py 实测一致
- [ ] 非 Windows 跳过不失败

**Blocked by**：15
