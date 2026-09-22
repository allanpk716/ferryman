# 票 05 · 独立升级线与首发

## What to build

widget 自升级闭环：tauri-plugin-updater（endpoints 指向本仓 Releases 的 widget 产物 latest.json，签名密钥对生成与私钥保管流程照 AntFeedingLog `docs/release.md`）；**检查更新只由托盘菜单手动触发**（无后台自升级，与主程序自升级纪律同款）；tag 规范 `widget-v*` 与主程序 tag 互不干扰；CI 发版：widget 路径过滤的 workflow 在 `widget-v*` tag 上构建 nsis 安装包 + updater artifacts（签名）并附 Release。

## 验收标准
代码侧（夜链可验）：
- [ ] updater 插件接线 + tauri.conf.json plugins.updater 结构（pubkey 占位与填入步骤注释化——密钥生成本身属发布侧）
- [ ] CI 发版段：`widget-v*` tag 触发构建 nsis + createUpdaterArtifacts 签名产物并附 Release（workflow 配置就绪；实际触发 tag 属发布侧）
- [ ] `widget/docs/release.md`：密钥对生成、latest.json、发版与回滚流程文档（照 AntFeedingLog 样板）
- [ ] 主程序发布通道（ADR-0010 tag→Release）零牵动（互不触发）
- [ ] 契约防御随行：widget 解析对未知字段向前兼容（喂超集 JSON 不崩，自动化测试）

发布侧（人工；待用户晨间确认 D8b 后执行，夜链永不代行）：
- [ ] 生成 updater 密钥对；公钥入 tauri.conf.json，私钥与密码记录在 AntFeedingLog 同款私有位置（不入库）
- [ ] `widget-v0.1.0-rc1` tag → CI 产出 Release（nsis + latest.json 签名）
- [ ] 另一目录安装 rc1 → 托盘"检查更新"发现 rc2 → 下载校验安装重启走通
- [ ] 签名不符/网络失败 → 如实报错不安装
## Blocked by

02, 04

## 涉及路径

- widget/src-tauri/**（updater 插件、tauri.conf.json plugins 段）
- .github/workflows/widget.yml（tag 触发发版）
- widget/docs/release.md（密钥与发版流程文档，新建）

## 副作用声明

- 独占验证命令：CI 产物 + 双版本手工升级实测
- 生成密钥对属敏感操作：私钥永不入库，过程记录不含密钥内容

decision_refs: ADR-0014"独立升级线"；AntFeedingLog ADR-0001/docs/release.md
review_blocks: 无
