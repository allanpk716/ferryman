# 票 02 · 基础件：mathx / clock / pathsx

**What to build**：三个基础包。mathx：`Round(x, n)` **必须**用 `strconv.FormatFloat(x,'f',n,64)+ParseFloat` 实现（half-even，与 CPython round 14 万组对照 0 失配；naive `RoundToEven(x*10ⁿ)/10ⁿ` 109 失配，禁用）+ `RuneLen`/`RuneTrunc`（码点语义，Python len/切片等价）。clock：包级 `Now` 变量（float64 epoch 秒，UnixNano/1e9），测试可整体替换注入。pathsx：`NormPath`（反斜杠→正斜杠+ToLower，lineage 唯一键形）。

参照：spec §Implementation「数值」；rev1 计划 Task 2。

**验收标准**：
- [ ] Round 为 FormatFloat 实现；边界电池含 2.675→2.67、3.175→3.17、6.335→6.33、0.0005→0.001、123.4565→123.457（期望值先 `uv run python -c "print(round(x,n))"` 实测钉死再写断言）+ half-even 常规例（2.5→2、3.5→4、0.5→0、-2.5→-2）
- [ ] RuneTrunc 中文按码点截断不出乱码、不超长返回原串；RuneLen=码点数
- [ ] NormPath(`C:\A\B.MD`)==`c:/a/b.md`
- [ ] clock.Now 可被测试替换且不影响其他测试

**Blocked by**：01
