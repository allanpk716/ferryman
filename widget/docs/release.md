# Widget 发版操作手册（独立升级线 · 票 05）

> 面向：仓库主人自己。目标：打一个 `widget-vX.Y.Z` tag 推上去，GitHub Actions（`.github/workflows/widget.yml` 的 release 作业）自动构建 nsis 安装包 + updater 签名产物并发布 Release，悬浮窗从此可用托盘菜单「检查更新 → 下载安装」自升级。
>
> 与主程序发版（`docs/` 外的 `.github/workflows/release.yml`，tag 规范 `v*`）完全隔离：`v*` 只匹配 v 开头，`widget-v*` 只匹配 widget- 开头，两个模式无交集——推 widget tag 不会触发主程序发版，反之亦然。
>
> **状态（2026-09-21，票 05 收尾时）**：代码侧全部就绪（updater 插件接线 / CI 发版作业 / 本手册）。**尚未做**：密钥对生成、pubkey 占位串替换、Secrets 配置、首次 tag——这四件属发布侧人工操作，见文末「发布侧人工验收单」。

---

## 开始前：已知风险（必须先读，晨间拍板）

**本仓主程序与 widget 共用同一个 GitHub Releases 空间。** tauri.conf.json 里 updater 的 endpoint 是：

```
https://github.com/allanpk716/ferryman/releases/latest/download/latest.json
```

`releases/latest` 指向的是**全仓最新的那个 Release**（按创建时间，不含 prerelease/draft）。问题：主程序发一版（比如 `v0.4.0`），它就变成 latest——这个 URL 会 404（主程序 Release 里没有 latest.json）或指到旧版 widget 的清单。也就是说：**主程序发版穿插时，widget 的自动更新会被打断**。

三个缓解选项（互斥，选一个；**今晚不定，晨间人工拍板后改 endpoint 并重发**）：

1. **独立 widget 仓库**：widget 用 subtree 拆出去（ADR-0012 本来就留了这出口），Releases 完全分开，endpoint 永远指向自己的 latest。最干净，成本是仓库管理。
2. **固定 tag 指针**：发版后把一个常驻 tag（如 `widget-latest`）强制移到最新 widget Release 上，endpoint 改成 `.../releases/download/widget-latest/latest.json`。零新仓库，成本是发版流程多一步 `git tag -f`（可塞进 CI 自动做）。
3. **GitHub Pages 托 latest.json**：CI 发版时把 latest.json 推到 gh-pages（路径如 `/ferryman/widget/latest.json`），安装包仍放 Release。endpoint 指 Pages，永不 404；成本是多维护一个分支。

## 第 1 步：生成 minisign 密钥对（独立于主程序密钥）

更新包靠 minisign 密钥对签名：私钥进 GitHub Secrets 供 CI 签名，公钥写进 tauri.conf.json 供客户端验签。**widget 必须用自己的一对密钥**，不与主程序共用——独立升级线=独立信任域，换锁不串门。

在仓库根目录（有 `package.json` 的地方没有也没关系，widget 目录里就有 cli）跑：

```bash
cd widget
npx tauri signer generate -w ~/.tauri/ferryman-widget.key
```

说明：

- 建议 **Git Bash**（`~` 自动展开）；PowerShell/CMD 下写全路径 `-w C:\Users\allan716\.tauri\ferryman-widget.key`。
- 会让你设一个密码（passphrase），CI 签名时要用。**忘了=私钥报废**，只能重新生成并同步换公钥+Secrets。
- 产出两个文件：
  - `~/.tauri/ferryman-widget.key` — **私钥**，绝不给任何人、绝不进 git；
  - `~/.tauri/ferryman-widget.key.pub` — **公钥**，下一步填进配置，公开无妨。

## 第 2 步：把公钥填进 tauri.conf.json（替换占位串）

打开 `widget/src-tauri/tauri.conf.json`，找 `plugins` → `updater` → `pubkey`。当前是一串**自带说明的占位符**（base64 解开第一行是 `untrusted comment: PLACEHOLDER - run: npx tauri signer generate ...`，照着做即可）：

```json
"plugins": {
  "updater": {
    "pubkey": "<整串替换成 ferryman-widget.key.pub 的内容>",
    "endpoints": [
      "https://github.com/allanpk716/ferryman/releases/latest/download/latest.json"
    ]
  }
}
```

用文本编辑器打开 `~/.tauri/ferryman-widget.key.pub`——**一行** base64（`dW50cnVzdGVkIGNvbW1lbnQ6` 开头），整行复制替换占位串。**原样粘贴，不要加工**（tauri-cli 已把「注释+密钥」整体编码过一次，客户端验签时自己解回来；手工拆行/转义会坏）。

自查：`echo "<pubkey那一行>" | base64 -d`，第一行应是 `untrusted comment: minisign public key: <十六进制>`。

顺手核对 `endpoints` 是否要按「开始前」拍板结果调整。

## 第 3 步：GitHub 仓库配两个 Secrets（widget 专用名）

仓库页面 → Settings → Secrets and variables → Actions → New repository secret：

| Name | Secret 值 |
| --- | --- |
| `TAURI_WIDGET_SIGNING_PRIVATE_KEY` | 私钥**文件内容**：`~/.tauri/ferryman-widget.key` 里那一行 base64，原样粘（CLI 的环境变量名固定为 `TAURI_SIGNING_PRIVATE_KEY`，workflow 已做好映射；Secret 名带 WIDGET 是刻意的——与主程序的 `TAURI_SIGNING_PRIVATE_KEY` 隔离，两对密钥不串） |
| `TAURI_WIDGET_SIGNING_PRIVATE_KEY_PASSWORD` | 第 1 步设的密码 |

两条都加完，CI 才能签出 `.sig`。配一条漏一条：签名步会失败，`widget.yml` 里的预检 warning 会提示。

## 第 4 步：私钥灾备 + 红线

- **灾备**：`~/.tauri/ferryman-widget.key` + 密码另存一份到密码管理器/离线介质（参照主程序密钥放群晖 Drive 的做法，建一个 `FerrymanWidget` 文件夹放 `.key` / `.key.passphrase` / `.key.pub` + 恢复 README）。机器坏了密钥就没了，所有已装用户验不了新包。
- **红线：私钥绝不入 git。** 生成路径 `~/.tauri/` 在仓库外；若另存到仓库目录内，提交前 `git status` 确认没出现它。

## 第 5 步：发版（速查）

1. 改**三处**版本号（必须一致）：
   - `widget/package.json`
   - `widget/src-tauri/tauri.conf.json`
   - `widget/src-tauri/Cargo.toml`
   （CI 有硬校验：tag 与三处不一致直接拒绝发版。）
2. 提交进 main，然后：

```bash
git tag -a widget-v0.2.0 -m "widget 0.2.0：……"   # 务必 annotated tag，注释会成为 Release body
git push origin widget-v0.2.0
```

3. 等 Actions 的 **widget** 流水线 release 作业（Windows 构建，约 10–20 分钟）。
4. **验收**：Releases 页出现 `widget-v0.2.0`，资产三件套齐全：
   - `Ferryman-Widget_0.2.0_x64-setup.exe`（nsis 安装包，应用内更新走它）
   - `Ferryman-Widget_0.2.0_x64-setup.exe.sig`（签名，latest.json 里内联其内容）
   - `latest.json`（更新清单：已装客户端轮询 endpoint 发现新版本）

### latest.json 形状（CI 自动生成，供核对）

```json
{
  "version": "0.2.0",
  "notes": "Ferryman Widget 0.2.0",
  "pub_date": "2026-09-22T00:00:00Z",
  "platforms": {
    "windows-x86_64": {
      "signature": "<.sig 文件内容，一行>",
      "url": "https://github.com/allanpk716/ferryman/releases/download/widget-v0.2.0/Ferryman-Widget_0.2.0_x64-setup.exe"
    }
  }
}
```

## 回滚

- **发版流水线失败**：直接在 Actions 页 Re-run failed jobs。release 作业是 **upsert 语义**（Release 已存在则覆盖上传资产+刷新 notes），绝不手动删 Release 重来。
- **发错了版本内容**：updater 只会升不会降（客户端比对版本号）。撤回=把该 Release 资产删掉或标记 prerelease（latest 通道即刻不再指它），修复后发**更高**版本号。
- **签名坏/密钥丢**：客户端验签失败、更新被拒——这是防线在工作，不是 bug。重新生成密钥对→换公钥→换 Secrets→发更高版本。
- 本仓最新 Release 被主程序穿插顶掉导致 endpoint 404：见「开始前」三选项，拍板后落地即根治。

## 发布侧人工验收单（票面四项，对应「状态」里的未做项）

- [ ] **密钥生成**：第 1–4 步走完（密钥对、pubkey 替换、双 Secrets、灾备副本）。
- [ ] **tag→Release**：打一个真实 `widget-vX.Y.Z`，CI 出三件套资产（第 5 步）。
- [ ] **双版本升级实测**：本机装旧版（如 0.1.0）→ 托盘「检查更新」→ 通知条出「有新版本 … 可下载」→ 点「下载安装」→ 安装器重启后版本号变新。再点一次「检查更新」应显示「已是最新版本」。
- [ ] **签名失败实测**：篡改 Release 上的 setup.exe（或换一对密钥重签 latest.json）→ 客户端「下载安装」应失败并如实报错，绝不装上坏包。

## 附：本地开发注意

- `npx tauri dev` 不受影响（不打包不签名）。
- `npx tauri build`（本地打包）现在需要签名环境，否则失败：`TAURI_SIGNING_PRIVATE_KEY` / `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` 指向第 1 步的私钥与密码。CI 之外的本地验包不必要的话，本地仍用 `dev` 调试即可。
- pubkey 占位期间，托盘「检查更新」会在通知条如实报错（端点 404 或密钥无效）——**这是预期行为**，不是坏。
