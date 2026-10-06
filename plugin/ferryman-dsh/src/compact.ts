// 票05 · DSH 会话热缓存压缩——宿主半面执行臂。spec「架构与契约」逐字钉点
//（.scratch/dsh-hot-compaction/spec.md,改契约=全部受影响票返工）：
//   - 轮询循环：apply() 起 setInterval（默认 30000ms;daemon 应答 poll_hint_s
//     建议可调,取 max(建议, 10000ms) 下限）;每轮 POST {daemon}/dsh/poll,体=
//     本宿主会话清单 {agent:"dsh", sessions:[{sid, idle_s}]}（会话注册表由
//     五事件位维护,见 events.ts 各 handler 的 touch/setStatus/remove）。
//   - 指令执行前双重复查（N1 插件侧）：①agent 仍空闲（agent/status 维护的
//     忙位）∧ ②会话闲置时钟仍 < TTL（热窗内;缺省 1800s=ADR-0016 同款,应答
//     ttl_s 可覆盖）。任一不过 → POST /dsh/compacted {ok:false,
//     reason:"expired-or-busy"},不执行。
//   - 执行（两道,先命令后服务面——2026-10-07 E2E 实锚返工）：
//     ①命令道（首选）：真机 web 宿主把 compaction 服务隔离在 preset 组内
//      （packages/bundle/web-app/cordis.patch.yml「The token METER stays on the
//      host plane; only the compaction backend that reads it moves」——host 面
//      compaction-basic/command-compact 双 disabled;presets/cordis.patch.yml
//      cordis:group isolate:{compaction:true}）,插件域 ctx.inject('compaction')
//      恒不递面;而命令注册表 commands 服务留在 host 面（packages/bundle/base/
//      cordis.patch.yml:307-308,未被 disable/isolate）——宿主 UI 的 /compact
//      即走它（client/ui-commands/src/client/service.ts:406 remote 调用,网关
//      解析 agent 后同样落到 execute）,插件同走。懒注入 ctx.commands
//      （services 键=commands,interaction/commands/src/index.ts:30 name+:113-117
//      Context 声明合并）取 CommandRuntime,带 receiver 调
//      execute(agent, '/compact', [], signal)（:360-366 签名;@Remote 装饰只挂
//      原型标记不换方法体——typert-protocol/src/index.ts:198-225,进程内直调
//      即真执行;view(agent) 按对象身份解析 agent 作用域层——core/scope/src/
//      index.ts:15 ScopeKey=object,agent-loop/src/agent.ts:130 createScope
//      (loopCtx,this) 以 agent 本体为键,注册表存的活引用=同一对象）。
//      完成信号=execute 的 promise 本身:handler 直 await compactNow
//      （command-compact/src/index.ts:67）,execute await handler settle
//      （commands/src/index.ts:424-425）——解析即 compaction/end 已落,与直调
//      compactNow 等价,无需另观察事件流。结果面:undefined=该 agent 视图无
//      compact 命令（:367-370 未解析）→回落服务面;{kind:'success'}=压缩真
//      完成;{kind:'error',text}=预期失败（ManualCompactionError 已被
//      command-compact/src/index.ts:24-56 转译,code 丢失——busy 分支文案钉点
//      :26-31 'active compaction'/'not idle',匹配即 busy;文案改版退化成
//      error,仅账本 reason 标签之差,daemon 对 reason 无行为分支——
//      internal/daemon/compact.go:156-166 只记账）;handler 非预期抛错被
//      execute 原样 rethrow（commands/src/index.ts:426-429）→落 catch 带。
//     ②服务面（次选:命令道缺席/未注册时的合成宿主与 base 组成）：
//      compactNow(agent, signal)（packages/compaction/compaction/src/index.ts:
//      162-166 签名;忙=ManualCompactionError code 'busy',:35-64,catch 判
//      code 含 'busy' 或 message 形态匹配）→ 上报 {ok:false, reason:"busy"};
//      其余失败 → {ok:false, reason:"error"}＋logger.warn 一行。
//     两道皆缺 → {ok:false, reason:"no-compaction-channel"}＋warn 指因。
//   - 成功：尽力读新前缀（ctx.sessionProjections 投影 contextPressure 的
//     projectedTokens ?? pressureTokens,packages/llm/token-meter/src/
//     projection.ts:30-48;不可得省略键）→ {ok:true, prefix_tokens?,
//     source:"host-plugin"}。
// 纪律：轮询失败静默（下轮再试）;上报失败静默重试一次,终败 warn 一行;
// dispose 清 interval——cordis 约定插件 apply 返回函数=卸载 disposer
//（vendor/cordis/src/fiber.ts:359-362 typeof function → collect）;cordis 对
// 未声明 inject 的服务属性读取即抛,一律 optionalFace 容错取用（index.ts 的
// optionalHostFace 同款,本地私有副本避免运行时环）。单飞 guard：一轮未落定
// 下一轮跳过;同会话压缩在途再遇同指令静默跳过（上一臂自会上报）。

import {
  askPoll,
  reportCompacted,
  type DaemonEndpoint,
  type PollCommand,
} from "./daemon.ts";
import type { LoggerLike } from "./events.ts";
import type { BannerStore } from "./banner.ts";
import type { PluginContext } from "./index.ts";

// ---- 常量（spec 钉点） ----

/** 默认轮询间隔 ms */
export const DEFAULT_POLL_INTERVAL_MS = 30_000;
/** 间隔下限 ms（poll_hint_s 建议托底,spec:下限 10s） */
export const MIN_POLL_INTERVAL_MS = 10_000;
/** 热窗 TTL 缺省秒（复查用;应答 ttl_s 可覆盖。ADR-0016 同模型摆渡 ttl_s=1800 同款） */
export const DEFAULT_HOT_TTL_S = 1800;
/** compacted 上报的 source 值（daemon 落账本 kind=compacted 随行） */
export const COMPACT_SOURCE = "host-plugin";
/**
 * 成功上报前的落盘稳定窗 ms（2026-10-07 E2E 实锚返工第三修）。竞态链:压缩
 * 事件 append（如 03:16:42.598）→ 本插件 ~10ms 内上报 → daemon 立 compressed
 * 标记（TS=42.605）;而 dsh 会话日志是批量落盘——批窗 200ms
 * （session-persistence-jsonl/src/storage.ts:36 LIVE_WRITE_BATCH_MAX_DELAY_MS）
 * ,drain 后文件 mtime=42.798 → daemon 守望（poll_interval_s=1）检测到
 * compaction/* 机器产出增量 → TouchFull 把 LastWrite 顶到 42.798
 * （watcher_dsh.go:247-258）→ 标记作废规则 `LastWrite > 标记TS` 判死自家标记
 * （compact.go:196）——压缩自身的落盘写晚于上报即自杀,±0.2s 掷硬币（真机
 * 两轮:一轮活一轮死,死者 gate 侧 idle=111.1 ⇒ LastWrite=42.8 实证）。稳定窗
 * 2s=批窗 10 倍,让 TS 落在 drain mtime 之后,竞态闭死（drainPaused 只在写
 * 失败时置位——失败写不动 mtime,不引入新竞态）。横幅与标记同窗延后,代价
 * 不可感。
 */
export const REPORT_SETTLE_MS = 2_000;

// ---- 宿主服务鸭子面（未声明 inject 的属性读取即抛,一律 optionalFace 取用） ----

/** ctx.compaction（compaction/src/index.ts:88-91 Context 声明合并;119 Service） */
export interface CompactionLike {
  compactNow(agent: unknown, signal: AbortSignal, sourceCommandId?: string): Promise<unknown>;
}

/**
 * ctx.commands（interaction/commands/src/index.ts:30 name='commands' + :113-117
 * Context 声明合并;base bundle host 面 cordis.patch.yml:307-308）。execute 契约
 * :360-366——@Remote async execute(agent, line, submittedAttachments, signal)
 * : Promise<CommandExecution | undefined>;完成信号=promise 本身（头注「执行」节钉点）。
 */
export interface CommandsLike {
  execute(
    agent: unknown,
    line: string,
    submittedAttachments: readonly unknown[],
    signal: AbortSignal,
  ): Promise<unknown>;
}

/**
 * 命令道结果面解析（commands/src/types.ts:49-54 CommandExecution={commandId,
 * result};result 联合 :34-41 success{text?,sourceEventSeq?}|error{text}）：
 *   - success → 压缩真完成（含「No compactable history yet.」——compactNow 回
 *     null 的合法成功,command-compact/src/index.ts:68;前缀未变,daemon 侧照
 *     prefix_tokens 实值判定,不为此时伪造利好）;
 *   - error 文本匹配 busy 分支文案（command-compact/src/index.ts:26-31 唯一
 *     含 'active compaction'/'not idle' 的分支）→ busy;其余 → error。
 */
export function parseCommandOutcome(
  outcome: unknown,
): { ok: true } | { ok: false; reason: "busy" | "error"; text: string } {
  const result = outcome !== null && typeof outcome === "object"
    ? (outcome as { result?: unknown }).result
    : undefined;
  if (result !== null && typeof result === "object") {
    const r = result as { kind?: unknown; text?: unknown };
    if (r.kind === "success") return { ok: true };
    const text = typeof r.text === "string" ? r.text : "";
    if (/active compaction|not idle/i.test(text)) return { ok: false, reason: "busy", text };
    return { ok: false, reason: "error", text };
  }
  // 非对象回话=宿主契约外的畸形面,按 error 收口（不拦降级链）
  return { ok: false, reason: "error", text: typeof outcome === "string" ? outcome : "" };
}

/** ctx.sessionProjections（session-projection/src/index.ts:196+ Service;读面=
 *  snapshot——stateOf 回的是单元原始态,无 projectedTokens（该键只活在 wire
 *  视图,:208-216 view 计算;2026-10-07 E2E 实锚:stateOf 落 pressureTokens
 *  兜底=压缩盲的旧请求压值,prefix_tokens 恒报旧数） */
export interface SessionProjectionsLike {
  /** wire 视图读：snapshot(session, keys) → {asOfSeq, values}（:336-357 一致切面） */
  snapshot(session: unknown, keys?: readonly string[]): { values?: Record<string, unknown> } | undefined;
}

/** 宿主服务面 best-effort 取用:读抛错（inject 执法）或缺面都归 undefined,不炸执行臂。 */
function optionalFace<T>(get: () => T | undefined): T | undefined {
  try {
    return get();
  } catch {
    return undefined;
  }
}

// ---- 会话注册表（五事件位维护;poll 体与复查的唯一事实源） ----

export interface RegistryEntry {
  sid: string;
  cwd: string;
  /** 宿主 agent 活引用——只活在本进程,绝不外泄出宿主面（blocked 仓同纪律） */
  agent: unknown;
  /** 闲置时钟起点（ms epoch;created/pre-step/session/event 触碰刷新） */
  lastActivityAt: number;
  /** agent/status 维护的运行位（true=running） */
  busy: boolean;
}

export type RegistrySnapshot = RegistryEntry & { idleS: number };

export class SessionRegistry {
  private readonly entries = new Map<string, RegistryEntry>();
  private readonly now: () => number;

  constructor(now: () => number = Date.now) {
    this.now = now;
  }

  /**
   * 登记/触碰：created（起点）、pre-step（用户步）、session/event（活动流）
   * 三处调用。agent 引用只增不覆盖（session/event 只有 SessionRef,不得抹掉
   * 既有 agent 活引用）;cwd 非空才覆盖（无 cwd 头的事件不得清掉既有值）。
   */
  touch(sid: string, cwd: string, agent?: unknown): void {
    const t = this.now();
    const e = this.entries.get(sid);
    if (e === undefined) {
      this.entries.set(sid, {
        sid,
        cwd: cwd || "",
        agent: agent ?? undefined,
        lastActivityAt: t,
        busy: false,
      });
      return;
    }
    if (cwd) e.cwd = cwd;
    if (agent !== undefined && agent !== null) e.agent = agent;
    e.lastActivityAt = t;
  }

  /** agent/status 忙位;未知 sid 忽略,非 idle/running 值忽略。 */
  setStatus(sid: string, status: unknown): void {
    const e = this.entries.get(sid);
    if (e === undefined) return;
    if (status === "running" || status === "idle") e.busy = status === "running";
  }

  /** 会话终局移除（agent/disposed;与 handoffPending/blocked 同纪律,不跨会话泄漏） */
  remove(sid: string): void {
    this.entries.delete(sid);
  }

  get(sid: string): RegistryEntry | undefined {
    return this.entries.get(sid);
  }

  /** 闲置秒数（向下取整,负值钳 0）;未知 sid → undefined。 */
  idleS(sid: string): number | undefined {
    const e = this.entries.get(sid);
    if (e === undefined) return undefined;
    return Math.max(0, Math.floor((this.now() - e.lastActivityAt) / 1000));
  }

  /** 快照（poll 体用）：含现算闲置秒。 */
  list(): RegistrySnapshot[] {
    const t = this.now();
    return [...this.entries.values()].map((e) => ({
      ...e,
      idleS: Math.max(0, Math.floor((t - e.lastActivityAt) / 1000)),
    }));
  }
}

// ---- 轮询循环 ----

export interface PollDeps {
  ep: DaemonEndpoint;
  logger: LoggerLike;
  registry: SessionRegistry;
  /** 宿主 ctx（懒注入 compaction/sessionProjections 面）;缺省=纯测试驱动 */
  ctx?: PluginContext;
  /** 热窗复查 TTL 缺省（秒）;应答 ttl_s 可覆盖。缺省 DEFAULT_HOT_TTL_S */
  ttlS?: number;
  /** 初始轮询间隔 ms;缺省 DEFAULT_POLL_INTERVAL_MS */
  intervalMs?: number;
  /** 间隔下限 ms;缺省 MIN_POLL_INTERVAL_MS（沙箱/E2E 压秒级可注入小值） */
  minIntervalMs?: number;
  /** 定时器注入面;缺省全局 setInterval/clearInterval */
  setImpl?: (fn: () => void, ms: number) => unknown;
  clearImpl?: (handle: unknown) => void;
  /** 新前缀读取注入面;缺省经 sessionProjections 投影尽力读 */
  readPrefixTokens?: (agent: unknown) => number | undefined;
  /** 成功上报前落盘稳定窗 ms;缺省 REPORT_SETTLE_MS（常量头注:标记自杀竞态
   *  的闭窗;沙箱/测试压秒级可注入小值） */
  reportSettleMs?: number;
  /** 压缩成功横幅仓（票06,src/banner.ts）:ok:true 上报送达即置位,浏览器经
   *  ferrymanBlocked list 信封 banner 布尔拉取;缺省=不置位（纯测试驱动/老接线） */
  banner?: BannerStore;
}

export interface PollLoopHandle {
  /** 停表（dispose 面;幂等）。在途的一轮自然收尾（含上报),不再有下一轮。 */
  stop(): void;
  /** 手动跑一轮（单飞 guard 与自动节律共用;测试/诊断面）。 */
  tick(): Promise<void>;
  /** 当前生效间隔 ms（poll_hint_s 调整后的观测面）。 */
  intervalMs(): number;
}

function normalizePositive(v: unknown): number | undefined {
  return typeof v === "number" && Number.isFinite(v) && v > 0 ? v : undefined;
}

const defaultSetInterval = (fn: () => void, ms: number): unknown => setInterval(fn, ms);
const defaultClearInterval = (handle: unknown): void =>
  clearInterval(handle as ReturnType<typeof setInterval>);

/**
 * ManualCompactionError 形态匹配（compaction/src/index.ts:48-64:name=
 * 'ManualCompactionError'、code 为稳定失败类,'busy' 可来自任何压缩入口,
 * 含 durable-lock 入口断言）;兜底再认 message 含 busy
 *（票面钉点:「判错误码含 'busy' 或形态匹配即可」）。
 */
function isBusyError(e: unknown): boolean {
  if (typeof e === "string") return e.toLowerCase().includes("busy");
  if (e === null || typeof e !== "object") return false;
  const rec = e as Record<string, unknown>;
  if (typeof rec["code"] === "string" && rec["code"].toLowerCase().includes("busy")) return true;
  return typeof rec["message"] === "string" && rec["message"].toLowerCase().includes("busy");
}

/**
 * 懒注入挂接（apply 时一次;回调由宿主在服务就绪时递面——fork-session.ts:60-64
 * 先例,registerBlockedRemote 同款形态）。回调里再 optionalFace:scoped ctx 的
 * 服务属性同样受 inject 执法,但已在清单内应可读——防御带照吞。
 */
function hookLazyInjection(
  ctx: PluginContext | undefined,
  lazy: { compaction?: CompactionLike; projections?: SessionProjectionsLike; commands?: CommandsLike },
): void {
  if (ctx === undefined) return;
  try {
    ctx.inject?.(["compaction"], (scoped) => {
      const e = optionalFace(() => (scoped as PluginContext).compaction);
      if (e !== undefined) lazy.compaction = e;
    });
  } catch { /* 老宿主无懒注入 API */ }
  try {
    ctx.inject?.(["sessionProjections"], (scoped) => {
      const p = optionalFace(() => (scoped as PluginContext).sessionProjections);
      if (p !== undefined) lazy.projections = p;
    });
  } catch { /* 老宿主无懒注入 API */ }
  try {
    // 命令道（首选执行道,头注「执行」节钉点）;真机 web 宿主实证递面
    ctx.inject?.(["commands"], (scoped) => {
      const c = optionalFace(() => (scoped as PluginContext).commands);
      if (c !== undefined) lazy.commands = c;
    });
  } catch { /* 老宿主无懒注入 API */ }
}

/** 取 compaction 引擎：懒注入缓存 → 直接读（执法环境抛错吞掉）→ 再挂一次懒注入。 */
function resolveCompaction(
  ctx: PluginContext | undefined,
  lazy: { compaction?: CompactionLike },
): CompactionLike | undefined {
  if (lazy.compaction !== undefined) return lazy.compaction;
  if (ctx === undefined) return undefined;
  const direct = optionalFace(() => ctx.compaction);
  if (direct !== undefined) {
    lazy.compaction = direct;
    return direct;
  }
  try {
    ctx.inject?.(["compaction"], (scoped) => {
      const e = optionalFace(() => (scoped as PluginContext).compaction);
      if (e !== undefined) lazy.compaction = e;
    });
  } catch { /* 老宿主无懒注入 API */ }
  return lazy.compaction;
}

/** 取命令注册表服务面：三段式同 resolveCompaction（懒注入缓存→直接读→再挂）。 */
function resolveCommands(
  ctx: PluginContext | undefined,
  lazy: { commands?: CommandsLike },
): CommandsLike | undefined {
  if (lazy.commands !== undefined) return lazy.commands;
  if (ctx === undefined) return undefined;
  const direct = optionalFace(() => ctx.commands);
  if (direct !== undefined) {
    lazy.commands = direct;
    return direct;
  }
  try {
    ctx.inject?.(["commands"], (scoped) => {
      const c = optionalFace(() => (scoped as PluginContext).commands);
      if (c !== undefined) lazy.commands = c;
    });
  } catch { /* 老宿主无懒注入 API */ }
  return lazy.commands;
}

/**
 * 尽力读新前缀（spec:会话投影 contextPressure/tokenUsage 可得则得,不可得省略）。
 * 读面=snapshot wire 视图（非 stateOf 原始态——projectedTokens 只在 view 里,
 * session-projection/src/index.ts:208-216）:projectedTokens=「下一个请求的前缀价」
 * （压缩影子化立即反应——token-meter/src/projection.ts:36-44「reacting the
 * moment a compaction shadows a span」;压力口 pressureTokens 对压缩盲,只作
 * 缺档兜底）;无投影数→undefined（上报省键）。
 */
function readPrefixFromProjections(
  projections: SessionProjectionsLike | undefined,
  agent: unknown,
): number | undefined {
  if (projections === undefined) return undefined;
  return optionalFace(() => {
    const session = (agent as { session?: unknown } | null | undefined)?.session;
    if (session === undefined || session === null) return undefined;
    const snap = projections.snapshot(session, ["contextPressure"]);
    const view = snap?.values?.["contextPressure"];
    if (view === null || typeof view !== "object") return undefined;
    for (const v of [(view as Record<string, unknown>)["projectedTokens"], (view as Record<string, unknown>)["pressureTokens"]]) {
      if (typeof v === "number" && Number.isFinite(v) && v >= 0) return v;
    }
    return undefined;
  });
}

/**
 * 起轮询执行臂：挂表→立即首轮→（应答带 poll_hint_s/ttl_s 时就地调参）→
 * 逐条派发 compact 指令（fire-and-forget,单条执行不阻塞下一轮节律）。
 */
export function startPollLoop(deps: PollDeps): PollLoopHandle {
  const logger = deps.logger;
  const setImpl = deps.setImpl ?? defaultSetInterval;
  const clearImpl = deps.clearImpl ?? defaultClearInterval;
  const minIntervalMs = normalizePositive(deps.minIntervalMs) ?? MIN_POLL_INTERVAL_MS;
  let intervalMs = Math.max(normalizePositive(deps.intervalMs) ?? DEFAULT_POLL_INTERVAL_MS, minIntervalMs);
  let ttlS = normalizePositive(deps.ttlS) ?? DEFAULT_HOT_TTL_S;
  let timer: unknown;
  let stopped = false;
  let ticking = false;
  const inFlight = new Set<string>();
  const lazy: { compaction?: CompactionLike; projections?: SessionProjectionsLike; commands?: CommandsLike } = {};

  hookLazyInjection(deps.ctx, lazy);

  function arm(): void {
    if (stopped) return;
    timer = setImpl(() => void safeTick(), intervalMs);
    try {
      // Node 定时器脱钩事件循环:宿主进程生命周期另有把手,测试进程不被 30s 表吊死
      (timer as { unref?: () => void } | undefined)?.unref?.();
    } catch { /* 非 Node 定时器替身 */ }
  }

  function disarm(): void {
    if (timer === undefined) return;
    try {
      clearImpl(timer);
    } catch { /* 替身清除失败不碍停机语义 */ }
    timer = undefined;
  }

  /** 执行单条 compact 指令：复查→命令道/服务面压缩→上报。fire-and-forget,内吞一切异常。 */
  async function executeCommand(cmd: PollCommand): Promise<void> {
    const sid = cmd.session_id as string;
    const report = async (body: { ok: boolean; reason?: string; prefix_tokens?: number }): Promise<boolean> => {
      const delivered = await reportCompacted(deps.ep, { session_id: sid, source: COMPACT_SOURCE, ...body });
      if (!delivered) logger.warn(`[ferryman-dsh] 压缩结果上报失败（daemon 不可达?）: ${sid}`);
      return delivered;
    };
    /** 成功收口共道（两执行道共用）：落盘稳定窗（REPORT_SETTLE_MS 头注:标记
     *  自杀竞态闭窗——等压缩自身的批落盘写落定再立标记）→尽力读新前缀→上报
     *  →横幅置位（票06:置位门=上报送达）。 */
    const reportSuccess = async (entry: RegistryEntry): Promise<void> => {
      const settleMs = normalizePositive(deps.reportSettleMs) ?? REPORT_SETTLE_MS;
      await new Promise<void>((r) => setTimeout(r, settleMs));
      const prefix = deps.readPrefixTokens !== undefined
        ? deps.readPrefixTokens(entry.agent)
        : readPrefixFromProjections(lazy.projections, entry.agent);
      const delivered = await report(prefix === undefined ? { ok: true } : { ok: true, prefix_tokens: prefix });
      // 票06 横幅触发源（src/banner.ts 头注克隆钉点）：两执行道的成功点同义——
      // 命令道 execute 解析=handler 已 await compactNow（command-compact/src/
      // index.ts:67;commands/src/index.ts:424-425 await handler settle）,服务面
      // compactNow 解析即 compaction/end 已落（compaction/src/index.ts:147 锁语义
      // + types.ts:104 endSeq）;上报送达=daemon 侧 compressed 标记已立——「直接
      // 继续」承诺成立才亮;ok:false/上报终败一律不亮
      if (delivered && deps.banner !== undefined) deps.banner.set(sid);
    };
    try {
      // N1 双重复查：agent 空闲 ∧ 闲置 < TTL 热窗;任一不过 → expired-or-busy 不执行。
      // 无登记/无 agent 引用（会话已终局或仅剩事件流残影）同归此支——宿主拿不出的会话不硬压。
      const idle = deps.registry.idleS(sid);
      const entry = idle === undefined ? undefined : deps.registry.get(sid);
      if (entry === undefined || entry.agent === undefined || entry.agent === null || entry.busy || idle >= ttlS) {
        await report({ ok: false, reason: "expired-or-busy" });
        return;
      }
      if (inFlight.has(sid)) return; // 同会话压缩在途——上一臂自会上报,不双跑
      inFlight.add(sid);
      try {
        // ①命令道（首选,头注「执行」节克隆钉点）：宿主 UI /compact 同款入口
        const commandsFace = resolveCommands(deps.ctx, lazy);
        const runCommand = commandsFace !== undefined
          ? commandsFace.execute?.bind(commandsFace) // receiver 铁律:bind 整体调用
          : undefined;
        if (typeof runCommand === "function") {
          // 空附件跳过收件准入（commands/src/index.ts:390 length>0 才走）;signal
          // 永不 abort=命令跑到底
          const outcome = await runCommand(entry.agent, "/compact", [], new AbortController().signal);
          if (outcome !== undefined) {
            // 命令道终局:成功/错误都是已执行的真结果,不回落服务面
            const parsed = parseCommandOutcome(outcome);
            if (parsed.ok) {
              await reportSuccess(entry);
            } else if (parsed.reason === "busy") {
              await report({ ok: false, reason: "busy" });
            } else {
              logger.warn(`[ferryman-dsh] 压缩命令失败: ${sid} ${parsed.text}`);
              await report({ ok: false, reason: "error" });
            }
            return;
          }
          // undefined=该 agent 视图无 compact 命令（commands/src/index.ts:367-370
          // 语法/名字未解析）→ 落服务面
        }
        // ②服务面（次选）
        const engine = resolveCompaction(deps.ctx, lazy);
        const compactNow = engine?.compactNow?.bind(engine); // receiver 铁律:bind 整体调用
        if (typeof compactNow !== "function") {
          logger.warn(`[ferryman-dsh] 压缩指令无法执行：宿主无压缩通道（commands 服务面与 compaction 服务面皆缺）: ${sid}`);
          await report({ ok: false, reason: "no-compaction-channel" });
          return;
        }
        await compactNow(entry.agent, new AbortController().signal);
        await reportSuccess(entry);
      } catch (e) {
        if (isBusyError(e)) {
          await report({ ok: false, reason: "busy" });
        } else {
          logger.warn(`[ferryman-dsh] 压缩执行失败: ${sid} ${e instanceof Error ? e.message : String(e)}`);
          await report({ ok: false, reason: "error" });
        }
      } finally {
        inFlight.delete(sid);
      }
    } catch {
      // 兜底带：上报链路意外也不得冒 unhandled rejection（daemon 侧指令槽已在
      // poll 应答时清空,漏报只损失 compressed 标记,闸门照旧拦截兜底）
    }
  }

  async function tick(): Promise<void> {
    if (ticking) return; // 单飞:上一轮未落定（慢 HTTP/大清单）本轮跳过
    ticking = true;
    try {
      const sessions = deps.registry.list().map((e) => ({ sid: e.sid, idle_s: e.idleS }));
      const res = await askPoll(deps.ep, { agent: "dsh", sessions });
      if (res === null) return; // 轮询失败静默——下轮再试
      const hintS = normalizePositive(res.poll_hint_s);
      if (hintS !== undefined) {
        const next = Math.max(Math.round(hintS * 1000), minIntervalMs); // 下限 10s（spec 钉点）
        if (next !== intervalMs) {
          intervalMs = next;
          disarm();
          arm();
        }
      }
      const resTtl = normalizePositive(res.ttl_s);
      if (resTtl !== undefined) ttlS = resTtl;
      const commands = Array.isArray(res.commands) ? res.commands : [];
      for (const cmd of commands) {
        if (cmd === null || typeof cmd !== "object") continue;
        if (cmd.action !== "compact") continue; // 未知动作忽略（协议向前兼容）
        if (typeof cmd.session_id !== "string" || cmd.session_id.length === 0) continue;
        void executeCommand(cmd);
      }
    } finally {
      ticking = false;
    }
  }

  async function safeTick(): Promise<void> {
    try {
      await tick();
    } catch { /* 单轮任何意外静默——轮询臂永不冒烟,下轮再试 */ }
  }

  arm();
  void safeTick(); // 立即首轮:挂载即报一次会话清单（节律照旧由 interval 掌管）

  return {
    stop(): void {
      stopped = true;
      disarm();
    },
    tick,
    intervalMs: () => intervalMs,
  };
}
