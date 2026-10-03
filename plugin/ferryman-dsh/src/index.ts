// ferryman-dsh 入口骨架。
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
//     本骨架不声明 Config schema,票 05 按需补。
// 分发/构建注记：本包以 TS 源直接维护（node --experimental-strip-types 可跑）,main 暂指 src/index.ts;
// 打包成 JS 入口（如 lib/index.js,官方桥形态）属票 05/分发票决策。
import {
  DEFAULT_DAEMON_URL,
  DEFAULT_DOCK_URL,
  MIN_DAEMON_VERSION,
  runSelfcheck,
} from "./selfcheck.ts";
import {
  onCreated,
  onDisposed,
  onPreStep,
  onSessionEvent,
  onStatus,
} from "./events.ts";

export const name = "ferryman-dsh";
/** 宿主服务依赖清单（hooks-claude-code/src/index.ts:48 同位;零依赖 = 空数组） */
export const inject: string[] = [];

export interface FerrymanPluginConfig {
  /** 渡口基址,缺省 127.0.0.1:15722（internal/config/config.go:527 [dock].listen） */
  dockURL?: string;
  /** daemon 管理口基址,缺省 127.0.0.1:15700 */
  daemonURL?: string;
  /** <dataDir>/daemon.token 内容（internal/daemon/httpapi.go:35-54;文件读取接线在票 05） */
  daemonToken?: string;
  /** 版本对账最低要求,缺省 selfcheck.MIN_DAEMON_VERSION */
  minDaemonVersion?: string;
  /** 测试注入面——生产留空用全局 fetch */
  fetchImpl?: typeof fetch;
}

/** 结构化的宿主 context 子集（cordis Context 真型票 05 对齐;logger/on 为本插件实际用到面） */
export interface PluginContext {
  logger?: { warn(msg: string): void; error(msg: string): void };
  /** 事件挂接（ctx.on,官方桥 hooks-claude-code/src/index.ts:209/225 同用） */
  on?(event: string, handler: (...args: never[]) => unknown): void;
}

/** 五事件位接线（票 05 实现业务逻辑,本票只挂桩） */
export function registerHooks(ctx: PluginContext): void {
  if (typeof ctx.on !== "function") return;
  ctx.on("agent/pre-step", onPreStep as (...args: never[]) => unknown);
  ctx.on("agent/created", onCreated as (...args: never[]) => unknown);
  ctx.on("session/event", onSessionEvent as (...args: never[]) => unknown);
  ctx.on("agent/disposed", onDisposed as (...args: never[]) => unknown);
  ctx.on("agent/status", onStatus as (...args: never[]) => unknown);
}

/**
 * 挂载自检一次（渡口可达 ＋ daemon /stats 版本对账）。
 * 失败经 ctx.logger.error 用户可见（不抛出、不阻断加载）;成功静默。
 * 独立导出以便测试与票 05 复用。
 */
export function selfcheckOnce(ctx: PluginContext, config: FerrymanPluginConfig = {}): Promise<boolean> {
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

/** 插件入口：挂五事件位桩 ＋ 触发挂载自检（非阻塞,自检失败仅 logger 可见） */
export function apply(ctx: PluginContext, config: FerrymanPluginConfig = {}): void {
  registerHooks(ctx);
  void selfcheckOnce(ctx, config);
}
