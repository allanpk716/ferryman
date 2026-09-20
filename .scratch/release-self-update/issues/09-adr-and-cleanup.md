# 票 09 · ADR-0010 + 陈旧产物清理

**What to build**:
1. 新增 `docs/adr/0010-tag-release-self-update.md`:决策记录——发布通道唯一(tag→Actions→Release);纯手动自升级(CLI+托盘,agent 面不开放);监督者+锁+journal 要点;**原子替换论证**(停旧后单次 MoveFileEx,NTFS 元数据日志保证,双 rename 缺位窗口由此消除——取代早期"微秒窗口+手工恢复"草案);prerelease 标记保 latest 通道纯净;annotated tag 要求;windows-latest 选型(附 Linux 编译不可行实录:installer 无条件导入 x/sys/windows/registry);公开仓库信任模型(2FA;SHA256 防损坏不防恶意 release)。
2. 清理(工作区操作,D13 用户已确认精确清单,逐文件精确删除、禁通配):`viewer/ferryman-timeline.exe`(删后空 viewer/ 目录一并删)、`ferryman.exe.old-20260920`、`ferryman.exe.old-20260920-premerge`。
3. `.gitignore` 删除 `ferryman-timeline` 行(被跟踪文件,入本链提交)。

**验收标准**:
- [x] ADR 落盘,含上述全部要点,术语遵守 CONTEXT.md(update=自升级/cutover=切换)
- [x] 三个清单文件与空 viewer/ 目录已从工作区消失;其他脏区文件(CONTEXT.md、docs/*、experiments/* 等)原样未动
- [x] `.gitignore` 无 `ferryman-timeline` 行;该改动随本票提交
- [x] `git status --porcelain` 复核:除清单三文件消失外,脏区与 start 底账一致

**Blocked by**: 无,可立即开始
**涉及路径**: docs/adr/0010-tag-release-self-update.md(新), .gitignore, (工作区删除: viewer/ferryman-timeline.exe, ferryman.exe.old-20260920, ferryman.exe.old-20260920-premerge)
**副作用声明**: 工作区删除仅限清单三文件+空 viewer/ 目录;不碰其他任何脏区文件
**decision_refs**: D13, D14, D15
**review_blocks**: 无
