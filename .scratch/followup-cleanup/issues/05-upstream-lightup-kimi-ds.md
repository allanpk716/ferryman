# 票 05 · 点亮 Kimi/DeepSeek 两家圆控件【paused】

## What to build
渡口配置追加 kimi 与 deepseek 两条上游条目(端点/模型映射按 dock_migrate 预置同形),钥匙由用户在场提供(cc-switch DB 读取需明示授权或直接粘贴);优雅重启守护后,悬浮窗 30s 内自动出 Kimi(双环+倒计时)与 DeepSeek(¥余额)圆控件。月度 token 估算会因单上游兜底消失而下降(降幅≈09-23 直连前的旧模型名行用量)——诚实归属口径,非缺陷,需在交付时披露。

## 验收标准
- [ ] upstream list 三条目、kimi/deepseek 钥匙掩码非空、active 仍=智谱
- [ ] /widget/summary upstreams 三条(查询真出数或如实错误类别)
- [ ] GLM month_tokens 下降幅度与预期口径吻合
- [ ] 悬浮窗出新圆控件,无需重启 widget

## Blocked by
**paused(F3/D3)**:钥匙必须用户在场提供;夜里不动 cc-switch DB。

## 涉及路径
- `C:\Users\allan716\ferryman\config.toml`(本机配置,非仓内文件——部署票,经用户在场执行)

## 副作用声明
守护进程优雅重启一次(observe 周纪律记账);不触仓内文件

decision_refs: D3(待拍板)
review_blocks: F3(解除:D3 用户给钥匙并授权)
