# 票 06 · widget 自升级常驻通道(CI+端点)【paused】

## What to build
升级通道按默认选项 2"固定指针"落地(零新仓库):
1. 发版流水线在版本化发布完成后追加"常驻指针维护"步骤:移 `widget-latest` tag → `gh release delete widget-latest -y || true` → `gh release create widget-latest <dist-upload/latest.json> --prerelease --title ... --notes ...`。**必须 `--prerelease`**:否则该常驻 Release 会占据仓库"最新版"展示位,并劫持主程序升级通道的 releases/latest 语义。
2. 升级端点改指 `releases/download/widget-latest/latest.json`(由常驻 Release 提供该文件;其内 url 本就指向版本化 Release 的安装包资产)。
3. 发版手册「开始前」节注记,措辞**必须**是:"2026-09-25 按默认选项 2 实施(D2 待用户确认,不同意可改选),CI 已自动化——机制为移 tag+维护 widget-latest 常驻 Release 资产(预发布标记防 Latest 劫持)"。**不得出现"已拍板"字样**(评审 F8 红线:未决决策不得写进 durable 文档)。
4. 已知可忽略项:指针 Release 重建的秒级 404 窗口(周期轮询下无影响,维持现方案,评审 S2)。

## 验收标准
- [ ] CI 步骤含移 tag + delete/create + --prerelease,GH_TOKEN 就位
- [ ] 端点指向 widget-latest;首次发版前 404 属预期(与现状同)
- [ ] release.md 注记为"按默认实施(D2 待确认)"措辞,零"拍板"字样
- [ ] yaml 语法有效(actionlint 或 CI 自检)

## Blocked by
**paused(F3/D2)**:通道三选一待用户拍板;本票为选项 2 的就绪内容,拍板即执行(选项 1/3 则本票作废改写)。

## 涉及路径
- `.github/workflows/widget.yml`
- `widget/src-tauri/tauri.conf.json`
- `widget/docs/release.md`

## 副作用声明
无本地验证命令(yaml/JSON 静态自检;真实发版验证属人工四件套,不在本票)

decision_refs: D2(待拍板;F8 措辞红线、S1 prerelease 已烘焙)
review_blocks: F3(解除:D2 用户拍板), F8(解除凭据:票内文案无"拍板"字样——票级评审核对)
