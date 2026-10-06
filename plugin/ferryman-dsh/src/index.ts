// ferryman-dsh 入口——票03 骨架＋票05 业务接线＋票08 被拦反馈 Remote 面。
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
import { randomUUID } from "node:crypto";
import { DEFAULT_DAEMON_URL, DEFAULT_DOCK_URL, MIN_DAEMON_VERSION, runSelfcheck } from "./selfcheck.ts";
import {
  makeEventDeps,
  onCreated,
  onDisposed,
  onPreStep,
  onSessionEvent,
  onStatus,
  withBypassPrefix,
  type BlockedCard,
  type BlockedStore,
  type EventDeps,
} from "./events.ts";
import type { DaemonEndpoint } from "./daemon.ts";
import { resolveConfig, type FerrymanPluginRawConfig } from "./config.ts";
import { injectedMessage, type UserMessageLike } from "./usermessage.ts";

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
  /** cordis 服务注册面（ctx.provide,reflect.ts:277-305）——票08 Remote 暴露用;
   *  缺省=非 cordis 宿主/裸测试 ctx,静默跳过（票10 logger 文案仍兜底） */
  provide?(name: string, value: unknown): unknown;
  /** 宿主 agents 服务鸭子面（ctx.agents,core/agent/src/index.ts:29-31 服务声明）——「新会话继续」用 */
  agents?: AgentsLike;
  /** 宿主工作区注册表鸭子面（ctx.workspaceRegistry）——新会话挂靠 best-effort
   *  （先例 session-controller/src/commands.ts:569-570 forkWorkspace） */
  workspaceRegistry?: WorkspaceRegistryLike;
  /** cordis 懒注入面:宿主运行期把服务面递进来（dsh-ios-control fork-session.ts:60-64
   *  真机 web 宿主实证可用的先例）;老宿主无此 API → 调用方须 try/catch 兜底 */
  inject?(services: readonly string[], callback: (scoped: PluginContext) => void): unknown;
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

// ---- 票08 · F1 双面包：被拦反馈 Remote 面（typert SRC 路,先例 plugin-manager） ----

/** Remote 命名空间与服务键（合一;网关 SRC 发现按服务键扫描 reflect.props） */
export const BLOCKED_SERVICE_KEY = "ferrymanBlocked";
/** 原型标记描述符键（typert-protocol/src/index.ts:140 REMOTE_METHOD_DESCRIPTOR 原样字串） */
const REMOTE_METHODS_DESCRIPTOR = "@deepseek-ai/dsh-typert-protocol/remote-methods";

/** 宿主 agents 服务鸭子面（core/agent/src/index.ts:391 create(CreateAgentOptions)） */
export interface AgentsLike {
  create(options: { sessionId: string; meta?: { cwd?: string } }): Promise<unknown>;
}

/** 工作区鸭子面（attachSession 同 session-controller/src/commands.ts:131-141 用法） */
export interface WorkspaceLike {
  sessionIds: readonly string[];
  attachSession(sessionId: string): Promise<unknown>;
}

export interface WorkspaceRegistryLike {
  list(): WorkspaceLike[];
}

export interface BlockedRemoteDeps {
  store: BlockedStore;
  agents?: AgentsLike;
  workspaces?: WorkspaceRegistryLike;
}

/** list 回话（wire 卡片数组;浏览器卡片数据源） */
export interface BlockedListResult {
  ok: boolean;
  sessionId: string;
  cards: BlockedCard[];
}

/** 动作回话（失败不抛——错误走 ok:false + 中文指路文案,浏览器就地提示） */
export interface BlockedActionResult {
  ok: boolean;
  sessionId?: string;
  error?: string;
}

/** Remote 服务对象（SRC 面;方法签名=网关 Function.toString 解析的参数名契约） */
export interface BlockedRemoteService {
  list(sessionId: string): Promise<BlockedListResult>;
  resend(id: string): Promise<BlockedActionResult>;
  newSession(id: string): Promise<BlockedActionResult>;
  /** 网关 SRC 发现面读的可见绑定（gateway/src/index.ts:1387-1427 readBinding 校验自指） */
  readonly typertRemote: unknown;
}

/**
 * 构造被拦反馈 Remote 服务（纯函数,零 cordis 依赖——测试直调方法面）。
 * SRC 契约钉点（dsh 调研克隆）：
 *   - 服务对象挂 `typertRemote` 自指绑定 + 原型 `'@deepseek-ai/dsh-typert-protocol/
 *     remote-methods'` 标记描述符（{version:1, methods:[{method, invocation:{kind:'direct'}}]}）
 *     ——网关对无 strict 描述符的端点按源标记合成（gateway/src/index.ts:769-890）,
 *     claims 缓存随 internal/service 注册事件失效（:230-232）,provide 后必被重扫。
 *   - 参数名由宿主方法 Function.toString 解析（:1434-1468）——只许纯标识符
 *     （TS 类型注解经 strip-types 变空白,可 trim）;与 client.js 手写描述符的
 *     wire 名一一对应,断了即 RPC 参数错位。
 *   - 动作全程不抛（代理端按 RemoteResult 收错;文案指路手打降级——票10 兜底）。
 */
export function buildBlockedService(deps: BlockedRemoteDeps): BlockedRemoteService {
  // eslint 姿态说明：三方法刻意收窄为纯标识符参数,勿加默认值/解构/剩余参数。
  const proto = {
    async list(sessionId: string): Promise<BlockedListResult> {
      return { ok: true, sessionId, cards: deps.store.list(sessionId) };
    },
    async resend(id: string): Promise<BlockedActionResult> {
      const ev = deps.store.get(id);
      if (ev === undefined) {
        // 宿主重启丢缓存（ADR-0023 已知取舍）——降级指路手打
        return { ok: false, error: "被拦记录已不在（宿主重启会丢缓存）。请手动输入「强续 <原话>」重发。" };
      }
      const followup = ev.agent.followup;
      if (typeof followup !== "function") {
        return { ok: false, error: "当前宿主会话没有代发通道（followup 面缺失）。请手动输入「强续 <原话>」重发。" };
      }
      // 「强续 」前缀命中 daemon bypass（gate.go:106）,新 turn 首步过 pre-step 放行
      try {
        followup(injectedMessage(withBypassPrefix(ev.prompt)));
      } catch (e) {
        // 10-06 真机反馈:裸异常文案（Cannot read properties of undefined…）不面向用户——只留动作指引
        return { ok: false, error: "代发失败（会话可能已结束，原话就保存在卡片上）。请手动输入「强续 + 原话」重发。" };
      }
      deps.store.markDone(id, "resend");
      return { ok: true };
    },
    async newSession(id: string): Promise<BlockedActionResult> {
      const ev = deps.store.get(id);
      if (ev === undefined) {
        return { ok: false, error: "被拦记录已不在（宿主重启会丢缓存）。请在侧栏新建会话（同目录）,开场会自动收到交接与原话。" };
      }
      // 方法必须带着 agents receiver 调（10-06 真机:摘下来裸调 this=undefined →
      // create 内 this.ctx 崩 "reading 'ctx'"——fork-session.ts:97 是整体调用）
      const create = deps.agents?.create?.bind(deps.agents);
      if (typeof create !== "function") {
        return { ok: false, error: "宿主无 agents 服务。请在侧栏新建会话（同目录）,开场会自动收到交接与原话。" };
      }
      // session-<uuid> 同形（session-controller commands.ts:109/266 先例）;同 cwd
      // ——交接+原话由 daemon 归还链按 cwd 锚定带回（restore.go:35-95,零自带）。
      // meta 形状对齐 fork-session.ts:88-92 可运行先例（parentSession/agentPreset）:
      // 10-06 真机裸 {cwd} 在 agents.create 内部崩（reading 'ctx'）,补齐即愈。
      const sessionId = `session-${randomUUID()}`;
      const header = (ev.agent as { session?: { header?: Record<string, unknown> } } | undefined)
        ?.session?.header;
      const meta: Record<string, unknown> = { parentSession: ev.sessionId, cwd: ev.cwd };
      if (typeof header?.["agentPreset"] === "string") meta["agentPreset"] = header["agentPreset"];
      try {
        await create({ sessionId, meta });
      } catch (e) {
        return { ok: false, error: `新建会话失败：${e instanceof Error ? e.message : String(e)}。请在侧栏手动新建（同目录）,开场自动收到交接与原话。` };
      }
      // 工作区挂靠 best-effort（失败不回滚创建;会话已在,侧栏按注册表可见）
      const ws = deps.workspaces?.list?.bind(deps.workspaces)().find((w) => w.sessionIds.includes(ev.sessionId));
      if (ws !== undefined) {
        try {
          await ws.attachSession(sessionId);
        } catch {
          // 挂靠失败容忍——会话本体已建,不影响归还链
        }
      }
      deps.store.markDone(id, "new-session");
      return { ok: true, sessionId };
    },
  };
  Object.defineProperty(proto, REMOTE_METHODS_DESCRIPTOR, {
    configurable: true,
    value: Object.freeze({
      version: 1,
      methods: Object.freeze([
        Object.freeze({ method: "list", invocation: Object.freeze({ kind: "direct" }) }),
        Object.freeze({ method: "resend", invocation: Object.freeze({ kind: "direct" }) }),
        Object.freeze({ method: "newSession", invocation: Object.freeze({ kind: "direct" }) }),
      ]),
    }),
  });
  const service = Object.create(proto) as BlockedRemoteService;
  Object.defineProperty(service, "typertRemote", {
    configurable: true,
    enumerable: true,
    value: Object.freeze({ service, serviceKey: BLOCKED_SERVICE_KEY, namespace: BLOCKED_SERVICE_KEY }),
  });
  return service;
}

/**
 * 把被拦反馈 Remote 服务 provide 进 cordis（网关经 reflect.props SRC 扫描发现,
 * claims 缓存随服务注册失效——时序天然安全）。无 provide 面（裸 ctx/非 cordis
 * 宿主）静默跳过:拦截与票10 文案不受影响,只是无浏览器卡面。
 *
 * ctx.agents/ctx.workspaceRegistry 是宿主服务属性——cordis Context 代理对未声明
 * 进 inject 的服务属性**读取即抛**（"cannot get property without inject"）,而声明
 * 进 inject 在缺服务的宿主上会整插件加载失败（官方桥语义）,与零宿主依赖设计冲突。
 * 故经 optionalHostFace 容错取用:取不到时服务照常注册,「新会话继续」走
 * events.ts 既有的手工指引兜底（真机 web 实例 10-06 激活事故根因）。
 */
export function registerBlockedRemote(ctx: PluginContext, deps: { blocked: BlockedStore }): void {
  if (typeof ctx.provide !== "function") return;
  const serviceDeps: BlockedRemoteDeps = {
    store: deps.blocked,
    // 惰性初取（inject 执法环境取不到=undefined）,懒注入到位后覆盖
    agents: optionalHostFace(() => ctx.agents),
    workspaces: optionalHostFace(() => ctx.workspaceRegistry),
  };
  ctx.provide(BLOCKED_SERVICE_KEY, buildBlockedService(serviceDeps));
  // 懒注入正道（fork-session.ts:60-64 同款,真机 web 宿主实证可用）:「新会话继续」
  // 要 agents.create 真建会话;老宿主无此 API/服务缺席 → 保持初取结果,走手工指引兜底
  try {
    ctx.inject?.(["agents"], (scoped) => {
      if (scoped.agents !== undefined) serviceDeps.agents = scoped.agents;
    });
  } catch { /* 老宿主无懒注入 API */ }
  try {
    ctx.inject?.(["workspaceRegistry"], (scoped) => {
      if (scoped.workspaceRegistry !== undefined) serviceDeps.workspaces = scoped.workspaceRegistry;
    });
  } catch { /* 老宿主无懒注入 API */ }
}

/** 宿主服务面 best-effort 取用:读抛错（inject 执法）或缺面都归 undefined,不炸激活。 */
function optionalHostFace<T>(get: () => T | undefined): T | undefined {
  try {
    return get();
  } catch {
    return undefined;
  }
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
 * ——config.ts 钉点）→ FERRYMAN_DISABLE 短路 → 挂五事件位 → 提供被拦反馈
 * Remote 面（票08;provide 由宿主重启加载）→ 触发挂载自检（非阻塞,自检失败
 * 仅 logger 可见）。
 */
export function apply(ctx: PluginContext, config: FerrymanPluginConfig = {}): void {
  const resolved = resolveConfig(config, process.env);
  if (resolved.disabled) return; // FERRYMAN_DISABLE=1 / config.disable（ferryman-gate-codex.ps1:9 同款总开关）
  const ep: DaemonEndpoint = {
    baseURL: resolved.daemonURL,
    token: resolved.daemonToken,
    fetchImpl: config.fetchImpl,
  };
  const deps = makeEventDeps(ep, ctx.logger ?? console);
  registerHooks(ctx, deps);
  registerBlockedRemote(ctx, deps);
  void selfcheckOnce(ctx, { ...resolved, fetchImpl: config.fetchImpl });
}

// 类型再导出（下游/测试引用面）。
export type { EventDeps, UserMessageLike };
