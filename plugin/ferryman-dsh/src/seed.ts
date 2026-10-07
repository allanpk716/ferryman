// 票03 · 注册表全量播种(三钟分工终版)——spec「注册表全量播种」节逐字钉点
// (.scratch/dsh-registry-seed/spec.md,权威设计):
//   - 调度:启动首轮 poll 前播种一次(index.ts await 首轮播种落定后再起轮询
//     执行臂)+ 每 5min 重播(DEFAULT_SEED_INTERVAL_MS=300_000,可注入)。
//   - 主路:懒注入宿主 sessions 服务 getListSnapshot()(生产 asar 实锚:
//     {items:[{sessionId,cwd,running,updatedAt,origin,parentSessionId,...}]});
//     再经 agents 服务 get(sessionId) 解析活引用(optionalFace 容错)。
//   - Plan B:sessions 缺面/抛错 → workspaceRegistry.list()(含 sessionIds)+
//     agents.get 拼(sid+cwd,无 running/updatedAt)。
//   - 三钟分工(终版):钟② lastEventAt 播种永不可触碰;钟③ lastActivityAt
//     播种仅当快照 updatedAt 较上次播种水位实际推进时单调推进(lastSeededAt
//     水位记在本模块);busy 合并=快照 running 覆盖,护栏=事件面 busy=true 且
//     (now−lastEventAt)<300s 不被快照 false 覆盖(护栏在 seedOverwriteBusy)。
//   - 合并规则(spec 终版逐字):条目缺席→新建(钟③主路=快照 updatedAt/
//     PlanB=播种时刻,钟②=创建时刻,busy=快照 running/PlanB 不设);agent 仅
//     条目空位时补;Plan B 重播不推进任何已播种条目两钟(宁 stale 勿清零);
//     钟②任何情况不被播种推进。origin=subagent 条目照登。
//   - 失败(缺面/抛错)静默降级回事件喂养,warn 一行日志。
//   - D7 探针(只注记):主路可用时,对「快照 running=true 但 (now−updatedAt)>
//     600s」的会话 warn 一行(可信度观察,不用来做任何判定)。
import type { LoggerLike } from "./events.ts";
import type { AgentsLike, PluginContext, WorkspaceRegistryLike } from "./index.ts";
import {
  EVENT_FRESH_WINDOW_MS,
  SessionRegistry,
} from "./registry.ts";

// ---- 常量 ----

/** 重播间隔缺省 ms(票面:每 5min 重播;可注入,沙箱/E2E 压毫秒级) */
export const DEFAULT_SEED_INTERVAL_MS = 300_000;
/**
 * D7 探针阈值 ms(票面:快照 running=true 但 (now−updatedAt)>600s 的会话
 * 打一行 warn——可信度观察;不用来做任何判定)。
 */
export const D7_STALE_RUNNING_MS = 600_000;

// ---- 宿主服务鸭子面 ----

/**
 * 宿主 sessions 服务鸭子面(票03 播种主路)。生产 asar 实锚字段清单:
 * items[{sessionId, cwd, running, updatedAt, origin, parentSessionId, title, …}]
 * ——updatedAt 为 ms epoch(number);title 等未列字段实现不得依赖(spec D7.5
 * 「真实字段清单回写为 mock 形状断言」见 test/seed.test.ts 形状契约用例)。
 * 回话 sync/async 皆容(toValue 归一)。
 */
export interface SessionsLike {
  getListSnapshot(): unknown;
}

/** agents 活引用解析面(窄化鸭子面:本模块只要 get;宿主 AgentsLike 的
 *  create/followup 等面归 index.ts/调用方)——receiver 由调用方 bind 整体调。 */
type AgentsGetFace = { get?(sessionId: string): unknown };

// ---- 源面注入(测试/沙箱直驱,绕过 ctx 鸭子面;生产走 ctx 解析) ----

export interface SeedSources {
  /** 主路:宿主 sessions 服务.getListSnapshot()(sync/async 皆容) */
  getListSnapshot?: () => unknown;
  /** Plan B:workspaceRegistry.list()(工作区数组,元素含 sessionIds) */
  listWorkspaces?: () => unknown;
  /** agents.get(sid) 活引用解析(sync/async 皆容);缺=不补 agent */
  getAgent?: (sid: string) => unknown;
}

export interface SeedDeps {
  logger: LoggerLike;
  registry: SessionRegistry;
  /** 宿主 ctx(懒注入 sessions/agents/workspaceRegistry);与 sources 可并存,
   *  sources 优先(测试直驱) */
  ctx?: PluginContext;
  /** 直接注入源面;缺省从 ctx 三段式解析(懒注入缓存→直接读→再挂懒注入) */
  sources?: SeedSources;
  /** 重播间隔 ms(startSeedScheduler 用);缺省 DEFAULT_SEED_INTERVAL_MS */
  intervalMs?: number;
  /** 定时器注入面;缺省全局 setInterval/clearInterval(compact.ts 同款) */
  setImpl?: (fn: () => void, ms: number) => unknown;
  clearImpl?: (handle: unknown) => void;
  /** 时钟注入面;缺省 Date.now */
  now?: () => number;
}

export type SeedMode = "primary" | "planb" | "failed";

// ---- 小件 ----

function normalizePositive(v: unknown): number | undefined {
  return typeof v === "number" && Number.isFinite(v) && v > 0 ? v : undefined;
}

function isThenable(v: unknown): v is PromiseLike<unknown> {
  return v !== null
    && (typeof v === "object" || typeof v === "function")
    && typeof (v as PromiseLike<unknown>).then === "function";
}

/** sync/async 回话归一(getListSnapshot/agents.get 的两种宿主形态皆容)。 */
async function toValue(v: unknown): Promise<unknown> {
  return isThenable(v) ? await v : v;
}

/** 主路回话形状提取:{items:[...]} 之外(null/缺 items/非数组)归 null → Plan B。 */
function extractItems(raw: unknown): unknown[] | null {
  if (raw === null || typeof raw !== "object") return null;
  const items = (raw as Record<string, unknown>)["items"];
  return Array.isArray(items) ? items : null;
}

/** agent 引用上的 cwd(header.cwd ?? "";Plan B 新建的 cwd 源) */
function agentCwd(agent: unknown): string {
  const cwd = (agent as { session?: { header?: { cwd?: unknown } } } | null | undefined)
    ?.session?.header?.cwd;
  return typeof cwd === "string" ? cwd : "";
}

/** 宿主服务面 best-effort 取用:读抛错(inject 执法)或缺面都归 undefined(compact.ts 同款)。 */
function optionalFace<T>(get: () => T | undefined): T | undefined {
  try {
    return get();
  } catch {
    return undefined;
  }
}

/**
 * 宿主服务面三段式取用(compact.ts resolveCompaction 同款先例):懒注入缓存 →
 * 直接读(执法抛错吞)→ 再挂一次懒注入。cordis 对未声明 inject 的服务属性读取
 * 即抛,一律经此容错。
 */
function resolveFace<F>(
  cache: { face?: F },
  ctx: PluginContext | undefined,
  service: string,
  read: (c: PluginContext) => F | undefined,
): F | undefined {
  if (cache.face !== undefined) return cache.face;
  if (ctx === undefined) return undefined;
  const direct = optionalFace(() => read(ctx));
  if (direct !== undefined) {
    cache.face = direct;
    return direct;
  }
  try {
    ctx.inject?.([service], (scoped) => {
      const f = optionalFace(() => read(scoped as PluginContext));
      if (f !== undefined) cache.face = f;
    });
  } catch { /* 老宿主无懒注入 API */ }
  return cache.face;
}

// ---- 播种器 ----

/**
 * 会话注册表播种器:seedOnce() 按主路→Plan B→失败三级执行合并(幂等,可重播)。
 * lastSeededAt 水位表(票面:「需在条目或 seed 模块记 lastSeededUpdatedAt」——
 * 记本模块,注册表不加字段):键=sid,值=上次播种写入/见到的快照 updatedAt;
 * 钟③推进仅当本次 updatedAt 实际高于水位(单调,倒退不回拨)。
 */
export class RegistrySeeder {
  private readonly registry: SessionRegistry;
  private readonly logger: LoggerLike;
  private readonly ctx: PluginContext | undefined;
  private readonly sources: SeedSources | undefined;
  private readonly now: () => number;
  /** 快照 updatedAt 播种水位(spec:上次播种记录的 updatedAt;钟③推进判据) */
  private readonly lastSeededAt = new Map<string, number>();
  /** 宿主服务面懒注入缓存(三段式) */
  private readonly lazy = {
    sessions: {} as { face?: SessionsLike },
    agents: {} as { face?: AgentsLike },
    workspaces: {} as { face?: WorkspaceRegistryLike },
  };

  constructor(deps: SeedDeps) {
    this.registry = deps.registry;
    this.logger = deps.logger;
    this.ctx = deps.ctx;
    this.sources = deps.sources;
    this.now = deps.now ?? Date.now;
  }

  /**
   * 播种一次(幂等)。返回落点:primary(主路快照)/planb(Plan B)/failed
   * (两者皆缺面或抛错——静默降级回事件喂养,warn 一行)。绝不抛。
   */
  async seedOnce(): Promise<SeedMode> {
    // 主路:sessions 服务 getListSnapshot()
    try {
      const sessions = this.resolveSessions();
      if (sessions !== undefined) {
        const raw = await toValue(sessions.getListSnapshot());
        const items = extractItems(raw);
        if (items !== null) {
          await this.mergePrimary(items, this.now());
          return "primary";
        }
      }
    } catch { /* 主路抛错 → Plan B(spec 备胎语义;切换静默不 warn) */ }
    // Plan B:workspaceRegistry.list() + agents.get
    try {
      const workspaces = this.resolveWorkspaces();
      if (workspaces !== undefined) {
        const list = await toValue(workspaces.list());
        if (Array.isArray(list)) {
          await this.mergePlanB(list, this.now());
          return "planb";
        }
      }
    } catch { /* Plan B 也抛 → 落失败 */ }
    // 失败(缺面/抛错):静默降级回事件喂养,warn 一行(票面钉点)
    this.logger.warn(
      "[ferryman-dsh] 会话注册表播种不可用（sessions/workspaceRegistry 服务面皆缺或抛错）,降级回事件喂养",
    );
    return "failed";
  }

  /** 主路合并(合并规则 spec 终版逐字;每条目独立防御,畸形跳过不炸整轮)。 */
  private async mergePrimary(items: unknown[], now: number): Promise<void> {
    const getAgent = this.resolveAgentGet();
    for (const raw of items) {
      if (raw === null || typeof raw !== "object") continue;
      const it = raw as Record<string, unknown>;
      const sid = typeof it["sessionId"] === "string" ? it["sessionId"] : "";
      if (!sid) continue;
      const cwd = typeof it["cwd"] === "string" ? it["cwd"] : "";
      const running = it["running"] === true;
      const updatedAt = typeof it["updatedAt"] === "number" && Number.isFinite(it["updatedAt"])
        ? (it["updatedAt"] as number)
        : undefined;
      const entry = this.registry.get(sid);
      // agent 仅条目空位(新建/既有 null/undefined)时经 agents.get 补
      const agent = entry === undefined || entry.agent === undefined || entry.agent === null
        ? await resolveAgent(sid, getAgent)
        : undefined;
      if (entry === undefined) {
        // 新建:钟③=快照 updatedAt(缺=播种时刻)/钟②=创建时刻(seedRegister 内)/
        // busy=快照 running;水位=实际写入值(重播同值不误判推进)
        const at = updatedAt ?? now;
        this.registry.seedRegister(sid, { cwd, agent, busy: running, activityAt: at });
        this.lastSeededAt.set(sid, at);
      } else {
        this.registry.seedFillAgent(sid, agent);
        // busy 合并:快照 running 覆盖(校正陈旧);护栏=事件面 busy=true 且
        // (now−lastEventAt)<300s(钟②新鲜)时不被快照 false 覆盖
        const fresh = (this.registry.eventAgeMs(sid) ?? Infinity) < EVENT_FRESH_WINDOW_MS;
        this.registry.seedOverwriteBusy(sid, running, entry.busy === true && fresh);
        // 钟③:仅当 updatedAt 较上次播种水位实际推进(单调;倒退不回拨不降水位)
        if (updatedAt !== undefined) {
          const seen = this.lastSeededAt.get(sid);
          if (seen === undefined || updatedAt > seen) {
            this.registry.seedAdvanceActivity(sid, updatedAt);
            this.lastSeededAt.set(sid, updatedAt);
          }
        }
        // 钟②任何情况不被播种推进(seed* 原语结构性不碰 lastEventAt)
      }
      // D7 探针(只注记,不做任何判定):running=true 但 updatedAt 落后 >600s
      if (running && updatedAt !== undefined && now - updatedAt > D7_STALE_RUNNING_MS) {
        this.logger.warn(
          `[ferryman-dsh] D7 探针:快照 running=true 但 updatedAt 落后 ${Math.round((now - updatedAt) / 1000)}s` +
          `（可信度观察,不做任何判定）: ${sid.slice(0, 16)}`,
        );
      }
    }
  }

  /** Plan B 合并:拼 sid+cwd(无 running/updatedAt 源);重播不推任何已播种条目两钟。 */
  private async mergePlanB(workspaces: unknown[], now: number): Promise<void> {
    const getAgent = this.resolveAgentGet();
    for (const raw of workspaces) {
      if (raw === null || typeof raw !== "object") continue;
      const sids = (raw as { sessionIds?: unknown }).sessionIds;
      if (!Array.isArray(sids)) continue;
      for (const sid of sids) {
        if (typeof sid !== "string" || !sid) continue;
        const entry = this.registry.get(sid);
        const agent = entry === undefined || entry.agent === undefined || entry.agent === null
          ? await resolveAgent(sid, getAgent)
          : undefined;
        if (entry === undefined) {
          // Plan B 新建:钟②/③=播种时刻(创建初始化);不设 busy(无 running 源);
          // cwd 从 agent 头 best-effort 取
          this.registry.seedRegister(sid, { cwd: agentCwd(agent), agent, busy: false, activityAt: now });
          this.lastSeededAt.delete(sid); // 无 updatedAt 源:清水位,主路恢复后首次快照可比
        } else {
          // 重播:不推进任何已播种条目两钟(宁 stale 勿清零);不做 busy 校真
          // (无 running 字段,靠事件面+钟②衰减兜底);agent 只补空位
          this.registry.seedFillAgent(sid, agent);
        }
      }
    }
  }

  private resolveSessions(): SessionsLike | undefined {
    if (this.sources?.getListSnapshot !== undefined) {
      return { getListSnapshot: this.sources.getListSnapshot };
    }
    return resolveFace(this.lazy.sessions, this.ctx, "sessions", (c) => c.sessions);
  }

  private resolveWorkspaces(): WorkspaceRegistryLike | undefined {
    if (this.sources?.listWorkspaces !== undefined) {
      return { list: this.sources.listWorkspaces as WorkspaceRegistryLike["list"] };
    }
    return resolveFace(this.lazy.workspaces, this.ctx, "workspaceRegistry", (c) => c.workspaceRegistry);
  }

  /** agents.get 解析面(receiver 铁律:bind 整体调用);sources 优先,缺=undefined。 */
  private resolveAgentGet(): ((sid: string) => unknown) | undefined {
    if (this.sources?.getAgent !== undefined) return this.sources.getAgent;
    const agents = resolveFace(this.lazy.agents, this.ctx, "agents", (c) => c.agents);
    const get = agents?.get;
    return typeof get === "function" ? get.bind(agents) : undefined;
  }
}

/** agents.get 单条解析:thenable 归一 + 抛错容错(optionalFace 纪律,不炸播种)。 */
async function resolveAgent(sid: string, get: ((sid: string) => unknown) | undefined): Promise<unknown | undefined> {
  if (get === undefined) return undefined;
  try {
    const v = await toValue(get(sid));
    return v ?? undefined;
  } catch {
    return undefined;
  }
}

// ---- 调度 ----

export interface SeedHandle {
  /** 停重播表(幂等);在途的播种自然收尾(合并幂等,不产生重复登记)。 */
  stop(): void;
  /**
   * 手动跑一次播种(测试/诊断面)。首轮由 index.ts 在起轮询前 await 调用
   * (票面:「启动首轮 poll 前播种一次」);此后由重播表按 intervalMs 驱动。
   * 在途重入共享同一 promise(首轮与重播表撞车不双跑)。
   */
  seedOnce(): Promise<SeedMode>;
}

const defaultSetInterval = (fn: () => void, ms: number): unknown => setInterval(fn, ms);
const defaultClearInterval = (handle: unknown): void =>
  clearInterval(handle as ReturnType<typeof setInterval>);

/** 起播种调度:挂重播表(立即首轮由调用方 seedOnce 驱动,构造不自动播种)。 */
export function startSeedScheduler(deps: SeedDeps): SeedHandle {
  const seeder = new RegistrySeeder(deps);
  const setImpl = deps.setImpl ?? defaultSetInterval;
  const clearImpl = deps.clearImpl ?? defaultClearInterval;
  const intervalMs = normalizePositive(deps.intervalMs) ?? DEFAULT_SEED_INTERVAL_MS;
  let timer: unknown;
  let stopped = false;
  let inflight: Promise<SeedMode> | undefined;

  const once = (): Promise<SeedMode> => {
    if (inflight !== undefined) return inflight; // 单飞:在途共享(重播表 vs 首轮撞车)
    inflight = seeder.seedOnce().finally(() => {
      inflight = undefined;
    });
    return inflight;
  };

  function arm(): void {
    if (stopped) return;
    timer = setImpl(() => void once().catch(() => {}), intervalMs);
    try {
      // 同轮询表:脱钩事件循环,宿主进程生命周期另有把手
      (timer as { unref?: () => void } | undefined)?.unref?.();
    } catch { /* 非 Node 定时器替身 */ }
  }
  arm();

  return {
    stop(): void {
      stopped = true;
      if (timer !== undefined) {
        try {
          clearImpl(timer);
        } catch { /* 替身清除失败不碍停机语义 */ }
        timer = undefined;
      }
    },
    seedOnce: once,
  };
}
