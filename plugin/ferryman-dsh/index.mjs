// index.mjs — TS 源装载垫片。
// 缘由：cordis loader 的入口解析不吃 ".ts" 的 main，且以目录 URL 导入会失败
//（须指到本文件）；而 Node 24 能直接导入 node_modules 之外的 TS（类型剥离）。
// 本垫片以纯 JS 作包入口，再动态导入同目录 TS 源。junction 安装形态下本目录
// 实体在仓库 plugin/ferryman-dsh/（node_modules 之外），类型剥离放行。
// 挂载方式（三 profile 同款）：profiles/<p>/ferryman-dsh 为指向本目录的
// junction，cordis.patch.yml 加 `insert: [{id: ferryman-dsh,
// name: ./ferryman-dsh/index.mjs}]`（相对路径锚定在 patch 旁）。
const mod = await import('./src/index.ts')

export const name = mod.name
export const apply = mod.apply
export const inject = mod.inject
export const registerHooks = mod.registerHooks
export const selfcheckOnce = mod.selfcheckOnce
