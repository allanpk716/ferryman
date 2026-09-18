# 票 18 · ferry 摆渡执行器

**What to build**：`internal/ferry`（ferry.py 1:1）：Provider/LoadProviders（[providers.*]）；Chat（OpenAI 兼容 /chat/completions、temperature 0.2、**http.NewRequestWithContext**、HTTPError 文案 `HTTP <code> from <name>: <body500>`、usage 三键+wall_s Round 1）；TrimInjectLayer（>2200 token 硬截+「…(已截断)」）；ParseOutput（标记缺失全文兜底）；HandoffMarkdown（头部文案逐字）；FerrySession（L1：mat≤window−8192−4096；L2：分块预算 max(16000,input/3)+逐段纪要[≤1200,max_tokens 2048]+reduce；agent=codex 走 codextrans 提取；meta 键逐字含 call_walls/covers_until_iso）。SystemPrompt **逐字平移**（防注入声明+六节结构）。完成后把票 17 的 daemon.Provider 占位替换/桥接到 ferry.Provider（或统一为 ferry.Provider 提前定义——选一种，账本注释）。

参照：rev1 Task 22。

**验收标准**：
- [ ] tests/test_ferry_providers.py → ferry_providers_test.go 全部用例 1:1 移植且绿（httptest 假端点：成功/HTTPError/L2 分块/max_tokens/usage 汇总）
- [ ] 票 17 的全部测试重跑仍绿（占位替换无回归）
- [ ] Chat 全链 context 取消（超时请求不悬挂 goroutine）

**Blocked by**：17, 07, 08

**追加验收（票 08 缓交占位回填）**：test_codex_extract.py 的 test_ferry_session_dispatches_codex 在本票转绿（agent=codex 分派走 codextrans 提取）。
