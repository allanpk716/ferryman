# 票 03 · prices + policy 移植，公式单源收口

**What to build**：`internal/prices`（Python ferryman/prices.py 1:1：PriceVersion[PCache 用 *float64 表缺省]、PriceBook、At 版本选价、PriceTag、LoadPrices）与 `internal/policy`（policy.py 1:1：Compute/TierFor/StrategyCosts + ErrTTLUnset/NoCachePriceError）。viewer 手抄的 policy 三函数（Derive/SimulateBeats/DoNothingCost）**并入 internal/policy 同包**，`internal/viewer/policy` 副本删除，viewer server/demo 改引新包——公式从此全仓唯一。

参照：spec §Implementation「包边界」「公式单源」；rev1 Task 3/4。

**验收标准**：
- [ ] tests/test_prices.py 全部用例 1:1 移植为 prices_test.go 且绿
- [ ] tests/test_policy.py → policy_test.go；viewer policy_test.go → policy_viewer_test.go 且重名函数加 `Viewer` 前缀（实锤冲突：TestNoCachePriceRefuses 两源都有）——全部绿
- [ ] 公式四式逐字：τ=safety·T；perBeat=S/per·pCache+beatOut/per·POut；expire=S/per·PIn；cap=τ·(PIn−PCache)/perBeat（perBeat≤0→+Inf）
- [ ] internal/viewer/policy 目录不存在；`grep -r "0.8.*TTL\|per_beat\|PerBeat" --include="*.go"` 只命中 internal/policy
- [ ] go test ./... 全绿

**Blocked by**：02
