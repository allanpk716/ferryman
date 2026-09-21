# 票 01 · 项目骨架与构建隔离

## What to build

`widget/` 子项目落地：`src-tauri/`（Cargo.toml / build.rs / tauri.conf.json / src/main.rs+lib.rs / capabilities）+ `ui/`（静态前端；本票先放 `docs/research/20260921_悬浮窗mock.html` 的副本作占位，票 03 做正式移植）+ `package.json`（仅 `@tauri-apps/cli` devDependency）+ CI `.github/workflows/widget.yml`（paths 过滤 `widget/**` 与 workflow 自身）。根 `.gitignore` 增 `widget/src-tauri/target/`、`widget/node_modules/`。

窗口起步配置：`visible:false`（零闪窗铁律），setup 回调内 show；置顶/无边框/skipTaskbar/不可缩放先写入 conf。

## 验收标准

- [ ] `widget/src-tauri` 内 `cargo check` 绿；首次生成的 `Cargo.lock` 入库（应用crate 锁依赖）
- [ ] 主构建零变化：仓库 Go 侧无任何新增/改动文件，`go build ./...` 行为不变
- [ ] CI workflow 仅在 `widget/**` 或自身变更时触发（paths 过滤正确）
- [ ] `git subtree split -P widget/ -b widget-split-test` 可跑通（拆分出口验证，验证后删分支）
- [ ] Rust/pnpm 工具链不出现在任何主构建路径（根目录无 workspace Cargo.toml）

## Blocked by

无（壳阶段首票）

## 涉及路径

- widget/**（新增）
- .github/workflows/widget.yml（新增）
- .gitignore（追加两行）

## 副作用声明

- 独占验证命令：`cargo check`（widget/src-tauri 内执行）
- 首次编译拉取 Tauri 依赖树（仅 widget 目录，网络+分钟级时长）
- 不运行 `cargo tauri dev`（图标未生成前 dev 可用但 bundle 不行；图标在票 02）

decision_refs: ADR-0012
review_blocks: 无
