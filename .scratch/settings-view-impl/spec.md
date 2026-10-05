# 设置视图（工作台首片）· 实施 spec

> 来源：20261005-215408/220346 两环评审收敛稿（rev1）+ FINDINGS。术语遵守 CONTEXT.md；决策见 ADR-0021/0022。活动约束 F2/F7 仅影响安全重启端点（票 07/09 停靠，解除条件见 Further Notes）。

## Problem Statement

用户改 Ferryman 配置只能手编 config.toml 再重启守护；15900 面板只读难懂。需要一个完整的设置操作界面：日常改/增/删（风险操作除外）、密钥安全、改坏可还原。

## Solution

工作台（widget 桌面壳）新增**设置视图**：8 组配置（供应商/闸门与阈值/守望目录/心跳与保温/摆渡/价格表/通知/服务）+ 备份还原区，全部可改可增可删。后端在 daemon（15700，Bearer+回环继承）新增设置读写 API，写路径复用 config 包文本手术+校验+原子写纪律。除热缝（provider switch、tuning）外改动需安全重启生效；界面按操作级标注。

## User Stories

1. 作为用户，我想改闸门三态与阈值，不再手编 TOML。
2. 作为用户，我想新增/删除渡口上游条目（含模型映射六键）。
3. 作为用户，我想热切换活跃供应商（不重启不断流）。
4. 作为用户，我想填/改 API 密钥而界面永不回显明文（只显尾四位）。
5. 作为用户，我想每次保存前自动备份并能从列表一键还原。
6. 作为用户，我想知道每类改动是否需重启，并能在界面里安全重启。
7. 作为用户，我想高风险操作（删供应商/还原/降闸/换活跃供应商/改端口）有二次确认并明示后果。
8. 作为用户，我想改阈值/价格表/心跳参数时先看改动预览再确认。
9. 作为用户，我想界面文案说人话（无黑话、单位人话化）。

## Implementation Decisions

- **落点**：工作台设置视图（ADR-0021）；15900 维持只读至退役；mock 定稿 `widget/ui/mock/settings.html` 不偏离。
- **读面** `GET /settings`：全量配置 JSON；密钥字段 `{masked:"••••"+尾四位, has_key:bool}` 永不回明文；生效元数据**操作级**（provider switch 与 tuning 即时生效；其余一切改动——含新增上游后的首次切换——需重启）。
- **写面**：`PUT /settings/{section}` 节级整写（gate/thresholds/watch/notify/heartbeat/question_watch/wait_window/tuning/server），跨节校验（如 总结阈值<拦截阈值 且差≥120s）；实体集合 `PUT|DELETE /settings/dock/upstreams/{name}`、`/settings/providers/{name}`、`/settings/prices/{key}`；`POST /settings/dock/switch` 复用既有 provider_switch（只能切启动时已加载条目，UI 提示）。
- **单写者互斥**：daemon 全部设置写路径（自动快照→校验→写盘→还原→审计落账→switch 换绑）经同一互斥锁串行；并发写排队。〔F2/F7 约束：restart 编排入锁与回滚源语义待拍板，见 Further Notes〕
- **密钥写合并**：密钥字段**省略或等于当前掩码占位=保留现值**；仅显式非空新值覆盖；掩码串永不落盘；空串=保留现值（清钥走删除整条目）。〔F8 裁定随 spec 定死〕
- **引用完整性**：providers 条目被 `[ferry].chain`/`[ferry].provider` 引用拒删；dock 上游被 `[dock].active` 或 `[ferry.same_model].upstreams` 引用拒删；错误点名引用方（与 config.go:455 加载期校验双层）。
- **快照**：`<data_dir>/backups/config/` 滚动 20 份（含密钥，权限仅当前用户）；每次写前自动快照+`POST /settings/snapshots` 手动；`GET` 只回元数据（id/时间/原因 auto|manual/字节数），**无内容下载端点**；`{id}/restore` 还原前强制先快照当前态、还原后 needs_restart。
- **审计**：每次写一行日志（时间、入口 settings-ui|cli、节/实体、改前改后值——密钥除外），不进账本。
- **改端口**：保存响应带回新端口，UI 明示「重启后自动改连新端口」。
- **安全重启** `POST /settings/restart`：写后预检（端口可绑探测+config.Load 干跑）→静默门→停旧拉新→健康轮询；失败自动还原重启前快照再拉起一次；仍失败停现场+通知人工恢复指引。〔票 07 停靠：F2 回滚源、F7 入锁范围待拍板〕
- **UI**：vanilla 零构建零外部资源；新窗口 conf 声明常驻隐藏、关闭=隐藏（ADR-0018-settings 铁律）；入口=托盘菜单+现设置窗按钮；文案 renhua；风险三层交互按 mock 定稿；生效徽章操作级。

## Testing Decisions

- 行为测试（httptest，风格随 internal/daemon 既有测试）：掩码读不回明文；掩码回写/省略→真钥逐字节保留；并发两写（含还原重叠）=串行后值；跨节校验拒写；活跃/被引用条目拒删且点名引用方；还原=先快照当前+逐字节还原；审计行落盘；改端口响应带新端口；switch 只认启动时条目；快照 GET 载荷不含密钥明文；快照滚动 20。
- config 原语单测（先例=internal/config dock_edit 既有测试）：文本手术只动目标节、其余节与注释保真、原子写失败不留半文件、rename 自校验。
- UI：静态断言（零外部资源、密钥只显尾四位）+浏览器实开冒烟+数字对账（界面值=GET /settings 返回）。
- 测试输出落 `.scratch/settings-view-impl/logs/`，不裸跑全仓 go test。

## Out of Scope

其余六视图；新增热缝；15900 改动；MCP/agent 面写端点；widget 显示配置窗合并；provider apply 界面化；旧 upstream use；发版换装（本夜只交付分支，发布由人）。

## Further Notes

- **活动约束（票 07/09 停靠）**：F2=多次写后重启，回滚源「最近写前快照」可能已含坏配置——待拍板「上次健康运行配置」（daemon 每次成功启动盖戳的配置副本，**推荐**）或「未验证变更整体回退」；F7=restart 编排从预检到终局全程持设置互斥锁+在飞写排干（纯技术句，随 F2 一并落）。两者均不改任何已确认决策。
- 实施顺序与依赖见 issues/（01→03→{04,05}→06→08；02 并行先行）。
- 新增上游后须重启一次才能热切换到它（switchTo 查启动快照）——UI 文案已含。
