# 票 05 评审(PASS)
- 全部验收过;评审员独立复跑 -quick 8/8 PASS 136s 复现;-all 8/8 174s 证据在盘且日志行逐一比对源码为真
- 生产隔离核实:五口黑名单+TestForbiddenPorts;FERRYMAN_CONFIG/USERPROFILE 重定向;internal 全部家目录读取走 os.UserHomeDir 无漏网;filterEnv 剥自拉起标记
- 三场景判据真实(S1 门真等满 10s 静默/版本双向翻;S2 如实记录;S3 sawStop 判零截断);五故障注入逐项恢复(F2 实测 lock-yield 正主完成)
- 口径裁定:S1"未观测到"诚实可用但注记说满("窗为 0"应改"该观测密度下 ≤12s 黑窗可能漏检");拒连窗首失败样本法系统性低估 2s 宽盲区
- 普通建议三条(记账不扩范围):①S1 注记措辞+可加 250ms TCP 拨测哨兵(先核裸连接不进记账口径);②-all 钉死 F2 要求 lock-yield 防唯一化兜底 masking;③S3 deltas 注释修正
- 收场核查 0 残留 0 孤儿;vet 净;单测层 5/5
