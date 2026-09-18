# 票 05 · config 移植

**What to build**：`internal/config`（config.py 1:1）：各 dataclass 结构与默认值逐字（thresholds 1500/2100/20000/720、port 7311、poll 3.0、question_watch off/5/420/2/480、harvest true 等）；Load 路径优先级（显式 > $FERRYMAN_CONFIG > ~/ferryman/config.toml，不存在=全默认）；Validate 全分支中文文案逐字 + relaxMinGap + **lead 下限夹取副作用与两类告警打印保留**（config.py:164-205）；DataDir/ThresholdFor。

参照：rev1 Task 6。

**验收标准**：
- [ ] tests/test_config.py 全部用例 1:1 移植且绿（默认值/各节覆盖/全部校验失败分支文案/relax/环境变量/lead 夹取）
- [ ] FerryWallTimeoutS=480 常量导出
- [ ] TOML 用 BurntSushi（部分字段容忍语义=Python .get）

**Blocked by**：02
