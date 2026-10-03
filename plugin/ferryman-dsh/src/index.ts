// ferryman-dsh 入口——票03 骨架＋票05 业务接线。
// 协议事实（dsh 调研克隆,只读）钉点：
//   - 插件入口 = 包导出 name ＋ apply(ctx, config)（docs/user/develop/basic/publish.md:38-51;
//     加载器收 function/{ apply } 对象,vendor/cordis/src/registry.ts:222-228）。
//   - 服务声明 = export const inject = [...]（官方桥 packages/hooks/hooks-claude-code/src/index.ts:48,
//     缺服务时加载失败;本插件零宿主服务依赖,空数组）。
//   - 用户可见错误通道 = ctx.logger.warn/error（宿主侧插件运行错误统一走 context.logger.error,
//     vendor/cordis/src/fiber.ts:125-134;官方桥 config 读不了 → ctx.logger.warn 后照常返回,
//     「logged, and the agent continues」hooks-claude-code/src/index.ts:119-122 + README.md:72-73
//     ——没有插件级 toast API）。故：自检失败走 logger.error,不阻断会话启动。
//   - 插件 Config schema 校验在加载时进行（fiber.ts:50-53,standard-schema validate）;
//     本插件零依赖不引 schemastery,Config 为宽松对象＋运行时防御解析
//     （config.ts resolveConfig,坏值回缺省——官方桥 assertPositiveInteger+
//     「could not load → warn and return」同款防御形态）。
// 分发/构建注记：本包以 TS 源直接维护（node --experimental-strip-types 可跑）,main 指 src/index.ts;
// 打包成 JS 入口（如 lib/index.js,官方桥形态）属分发票决策。
import { DEFAULT_DAEMON_URL, DEFAULT_DOCK_URL, MIN_DAEMON_VERSION, runSelfcheck } from "./selfcheck.ts";
import { makeEventDeps, onCreated, onDisposed, onPreStep, onSessionEvent, onStatus, type EventDeps } from "./events.ts";
import type { DaemonEndpoint } from "./daemon.ts";
import { resolveConfig, type FerrymanPluginRawConfig } from "./config.ts";
import type { UserMessageLike } from "./usermessage.ts";

export const name = "ferryman-dsh";
/** 宿主服务依赖清单（hooks-claude-code/src/index.ts:48 同位;零依赖 = 空数组） */
export const inject: string[] = [];

export interface FerrymanPluginConfig extends FerrymanPluginRawConfig {
  /** 测试注入面——生产留空用全局 fetch */
  fetchImpl?: typeof fetch;
}

/** 结构化的宿主 context 子集（cordis Context 真型的鸭子面;logger/on 为本插件实际用到面） */
export interface PluginContext {
  logger?: { warn(msg: string): void; error(msg: string): void };
  /** 事件挂接（ctx.on,官方桥 hooks-claude-code/src/index.ts:209/225 同用） */
  on?(event: string, handler: (...args: never[]) => unknown): void;
}

/** 五事件位接线（票05 业务实现在 events.ts;deps 注入 daemon 端点/logger/标题跟踪） */
export function registerHooks(ctx: PluginContext, deps: EventDeps): void {
  if (typeof ctx.on !== "function") return;
  ctx.on("agent/pre-step", ((payload: never, next: never) =>
    onPreStep(deps, payload, next)) as (...args: never[]) => unknown);
  ctx.on("agent/created", ((payload: never) =>
    onCreated(deps, payload)) as (...args: never[]) => unknown);
  ctx.on("session/event", ((session: never, event: never) =>
    onSessionEvent(deps, session, event)) as (...args: never[]) => unknown);
  ctx.on("agent/disposed", ((payload: never) =>
    onDisposed(deps, payload)) as (...args: never[]) => unknown);
  ctx.on("agent/status", ((payload: never) =>
    onStatus(deps, payload)) as (...args: never[]) => unknown);
}

/**
 * 挂载自检一次（渡口可达＋daemon 版本对账＋dsh 三口探针）。
 * 失败经 ctx.logger.error 用户可见（不抛出、不阻断加载）;成功静默。
 * 独立导出以便测试复用。
 */
export function selfcheckOnce(
  ctx: PluginContext,
  config: {
    dockURL?: string;
    daemonURL?: string;
    daemonToken?: string;
    minDaemonVersion?: string;
    fetchImpl?: typeof fetch;
  } = {},
): Promise<boolean> {
  return runSelfcheck({
    fetchImpl: config.fetchImpl ?? fetch,
    dockURL: config.dockURL ?? DEFAULT_DOCK_URL,
    daemonURL: config.daemonURL ?? DEFAULT_DAEMON_URL,
    daemonToken: config.daemonToken ?? "",
    minDaemonVersion: config.minDaemonVersion ?? MIN_DAEMON_VERSION,
  }).then((result) => {
    if (!result.ok) {
      (ctx.logger ?? console).error(`[ferryman-dsh] 挂载自检失败：${result.userMessage}`);
    }
    return result.ok;
  });
}

/**
 * 插件入口：解析配置（token 读取接线,FERRYMAN_* 环境约定与自家钩子脚本对齐
 * ——config.ts 钉点）→ FERRYMAN_DISABLE 短路 → 挂五事件位 → 触发挂载自检
 * （非阻塞,自检失败仅 logger 可见）。
 */
export function apply(ctx: PluginContext, config: FerrymanPluginConfig = {}): void {
  const resolved = resolveConfig(config, process.env);
  if (resolved.disabled) return; // FERRYMAN_DISABLE=1 / config.disable（ferryman-gate-codex.ps1:9 同款总开关）
  const ep: DaemonEndpoint = {
    baseURL: resolved.daemonURL,
    token: resolved.daemonToken,
    fetchImpl: config.fetchImpl,
  };
  registerHooks(ctx, makeEventDeps(ep, ctx.logger ?? console));
  void selfcheckOnce(ctx, { ...resolved, fetchImpl: config.fetchImpl });
}

// 类型再导出（下游/测试引用面）。
export type { EventDeps, UserMessageLike };
