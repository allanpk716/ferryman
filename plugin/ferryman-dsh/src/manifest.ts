// 插件 manifest 形状——协议事实（dsh 调研克隆,只读）钉点：
//   - manifest 就是 package.json：packages/util/package-manifest/src/types.ts:8-27
//     DshPackageManifest——必填 name/version；可选 description/icon/private/dependencies/
//     peerDependencies/engines/dsh。
//   - dsh 键（同文件 :30-39）：manifestVersion?: 1 ＋ bundle/profile/client 三选,插件包用 bundle；
//     bundle.patch 指向 cordis patch 文件（激活声明,见仓内 cordis.patch.yml）。
//   - engines（同文件 :57-66）：{ dsh?, node?, npm? },SemVer range 声明式兼容声明。
//   - 官方桥实包同形佐证：packages/hooks/hooks-claude-code/package.json:13-23
//     （"type": "module" + "main" + exports;宿主共享类型走 peerDependencies :32-41——本插件为守住
//     零依赖验收不声明 peerDependencies,票 05 若需宿主共享类型再议）。
//   - 最小可装插件包三件套：docs/user/develop/basic/publish.md:38-51
//     （package.json 带 dsh.bundle.patch ＋ 入口导出 name/apply ＋ cordis.patch.yml 的 - insert 行）。
//   - 加载器接受形状：vendor/cordis/src/registry.ts:222-228（function/class/{ apply } 对象三选）。
//   - 本地分发 = `dsh plugin --profile <name> add <路径>` → profile 的 package.json 长出
//     "link:/path" 依赖（publish.md:82-98）——票面「分发先 file:./」对应此 link 形态。

/** 插件注册 id（与 package.json name、cordis.patch.yml insert.id 三处一致） */
export const PLUGIN_ID = "ferryman-dsh";
/** 与 package.json version 保持一致（DshPackageManifest 必填字段,types.ts:8-27;票05 业务对接＝0.2.0） */
export const PLUGIN_VERSION = "0.2.0";

/** package.json 里 dsh 键的形状（DshPackageManifest["dsh"] 子集,types.ts:30-39） */
export interface DshKeyShape {
  manifestVersion?: 1;
  bundle?: { patch: string };
}
