// 票05 · 插件配置解析——daemonToken 读取接线（票03 遗留 TODO 落地）。
// 环境变量约定与 Ferryman 自家钩子脚本对齐（只读钉点,hooks/ferryman-gate-
// codex.ps1）：
//   - FERRYMAN_DISABLE=1 → 插件整体短路（:9 同款总开关）;
//   - FERRYMAN_PORT 覆盖管理口缺省 15700（:7/:18）;
//   - FERRYMAN_TOKEN_FILE 覆盖 token 文件缺省 <home>/ferryman/daemon.token
//     （:8/:19-20;Windows home=USERPROFILE,POSIX home=HOME）。
// 读取失败（未装 Ferryman/token 未生成）→ 空串,不抛——三口将以 401 失败,
// 挂载自检把「daemon.token 缺失或失效」呈现给用户（selfcheck 钉点）。

import { readFileSync } from "node:fs";
import { DEFAULT_DOCK_URL, MIN_DAEMON_VERSION } from "./selfcheck.ts";

export interface FerrymanPluginRawConfig {
  dockURL?: string;
  daemonURL?: string;
  daemonToken?: string;
  minDaemonVersion?: string;
  disable?: boolean;
}

export interface RawEnv {
  FERRYMAN_DISABLE?: string;
  FERRYMAN_PORT?: string;
  FERRYMAN_TOKEN_FILE?: string;
  USERPROFILE?: string;
  HOME?: string;
}

/** 读文件注入面：返回内容或 null（不可读）;生产= node:fs 只读尝试 */
export type ReadFileFn = (path: string) => string | null;

function defaultReadFile(path: string): string | null {
  try {
    return readFileSync(path, "utf8");
  } catch {
    return null;
  }
}

export interface ResolvedConfig {
  disabled: boolean;
  dockURL: string;
  daemonURL: string;
  daemonToken: string;
  minDaemonVersion: string;
  /** token 文件路径（自检/诊断提示用） */
  tokenFile: string;
}

export function resolveConfig(
  raw: FerrymanPluginRawConfig,
  env: RawEnv,
  readFile: ReadFileFn = defaultReadFile,
): ResolvedConfig {
  const disabled = env.FERRYMAN_DISABLE === "1" || raw.disable === true;
  const port = env.FERRYMAN_PORT && /^\d+$/.test(env.FERRYMAN_PORT) ? env.FERRYMAN_PORT : "15700";
  const home = env.HOME ?? env.USERPROFILE ?? "";
  // 缺省 token 路径恒用正斜杠模板（Windows node:fs 照收;避免 join 的平台分隔符
  // 让「配置键/日志呈现/注入读」三处口径漂移）。FERRYMAN_TOKEN_FILE 用户自填,
  // 原样尊重。
  const tokenFile = env.FERRYMAN_TOKEN_FILE ?? `${home}/ferryman/daemon.token`;
  let daemonToken = raw.daemonToken ?? "";
  if (!daemonToken) {
    const t = readFile(tokenFile);
    if (t !== null) daemonToken = t.trim();
  }
  return {
    disabled,
    dockURL: raw.dockURL ?? DEFAULT_DOCK_URL,
    daemonURL: raw.daemonURL ?? `http://127.0.0.1:${port}`,
    daemonToken,
    minDaemonVersion: raw.minDaemonVersion ?? MIN_DAEMON_VERSION,
    tokenFile,
  };
}
