// 票05 · 五事件位业务对接（票03 骨架的实现层）。
// 协议事实（dsh 调研克隆,只读）——事件名与形状钉点（runtime-types.ts =
// packages/core/agent/src/runtime-types.ts）：
//   - agent/created（runtime-types.ts:261）payload { agent; source; signal? },@mode serial：
//     监听器按序 await,AgentLoop 扣住排队输入直到 listeners 全部完成（emit 位
//     core/agent/src/index.ts:550 await this.ctx.serial(...)）——归还播种赶得上首请求;
//     throw/rejection 会弄失败 creation（:253-254）——本 handler 永不抛。
//   - agent/disposed（:270）payload { agent },@mode emit（fire-and-forget,监听器
//     reject 只记日志,core/agent/src/index.ts:513-525）。
//   - agent/status（:280）payload { agent; status },@mode emit;状态 idle ⇄ running（:272-274）。
//   - agent/pre-step（:320）payload { agent; messages; turn; step; signal },next waterfall 中间件;
//     PreStepDecision（:112-119）= { kind:'reject' } | { kind:'enter'; messages; startsRequestSeries? }
//     ——协议不携带「用户可见理由」字段,理由呈现走 logger（宿主插件可见通道唯一,
//     vendor/cordis/src/fiber.ts:125-134;票03 钉死）。
//   - session/event（packages/core/session/src/index.ts:77）(session, event),@mode emit,
//     post-commit 追加流（观察者失败只记日志不使 append 失败,:66-76）;事件形状
//     packages/core/session/src/types.ts:281 起——'turn/start'(:288)、'assistant/message'
//     (:341-349,usage?: TokenUsage 内嵌于事件 data)、'session/title'（标题源,
//     dsh_wiring_test.go 夹具同形）、compaction 三事件 packages/compaction/
//     compaction/src/types.ts:24 'compaction/start' / :34 'compaction/summary' /
//     :72 'compaction/end'。
//   - 会话键=头行 id session-<uuid>（P2-2 定案,官方桥同款取法
//     hooks-claude-code/src/index.ts:329 session_id: agent?.session.header.id,
//     空串回退边界同款）;cwd 取 header.cwd ?? process.cwd()（:331-334）。
//   - agent.inject 语义（runtime-types.ts:241）：排队赶下一个 pre-step、不唤醒
//     driver;可能错过「pre-step 已领取批次」的那次请求（:233-240 文档）。
//     用法先例 hooks-claude-code/src/index.ts:213-214。
//   - TokenUsage（packages/llm/llm/src/types.ts:175-196）：camelCase 不相交口径
//     （inputTokens=仅未缓存输入;计费输入=三输入列之和）——四列 snake 映射与
//     守望/事件口同源（internal/dshtrans/dshtrans.go:48-51:
//     cacheWriteTokens→账面 cache_creation_tokens）。
//   - pre-step 空步短路、additionalContext 追加在 enter 尾部——官方桥同位先例
//     （hooks-claude-code/src/index.ts:225-241）。
//
// 跨源去重（票05 链内修订）不在本文件：daemon 侧策略 (a) 事件接管单源化
//（internal/daemon/dsh_dedup.go）——插件只管把事件如实直报,接管/让位由
// daemon 收口。

import { askGate, askHandoff, sendEvent, type DaemonEndpoint } from "./daemon.ts";
import { injectedMessage, type UserMessageLike } from "./usermessage.ts";

export interface LoggerLike {
  warn(msg: string): void;
  error(msg: string): void;
}

// ---- 结构化宿主引用（鸭子形;字段全部可选,勿臆造必填） ----

export interface SessionHeaderRef {
  id?: string;
  cwd?: string;
  parentSession?: string;
  origin?: string;
}

export interface SessionRef {
  header?: SessionHeaderRef;
}

export interface AgentRef {
  session?: SessionRef;
  inject?: (message: UserMessageLike) => void;
}

// ---- agent/pre-step（waterfall） ----

/** PreStepDecision（runtime-types.ts:112-119）——协议无理由字段 */
export type PreStepDecision =
  | { kind: "reject" }
  | { kind: "enter"; messages: unknown[]; startsRequestSeries?: true };

export interface MessageInput {
  content?: Array<{ type?: string; text?: string }>;
}

export interface PreStepPayload {
  agent: AgentRef;
  messages: MessageInput[];
  turn: number;
  step: number;
  signal?: AbortSignal;
}

export type PreStepNext = () => Promise<PreStepDecision>;

// ---- agent/created（serial,可 awaited） ----

export interface CreatedPayload {
  agent: AgentRef;
  source?: string;
  signal?: AbortSignal;
}

// ---- agent/disposed / agent/status（emit） ----

export interface DisposedPayload {
  agent: AgentRef;
}

export interface StatusPayload {
  agent: AgentRef;
  status: "idle" | "running";
}

// ---- session/event（emit,post-commit） ----

export interface SessionEventRef {
  /** 'turn/start' | 'assistant/message' | 'compaction/*' | 'session/title' | … */
  type: string;
  seq?: number;
  time?: number;
  data?: unknown;
  [key: string]: unknown;
}

// ---- deps 束 ----

export interface EventDeps {
  /** daemon 管理口端点（三口共用） */
  ep: DaemonEndpoint;
  logger: LoggerLike;
  /** session/title 跟踪的最近标题（键=会话头行 id;随后续上报带出） */
  titles: Map<string, string>;
  /** 时钟注入面（判活转发的时间戳）;缺省 Date.now */
  now?: () => number;
}

export function makeEventDeps(ep: DaemonEndpoint, logger: LoggerLike): EventDeps {
  return { ep, logger, titles: new Map(), now: () => Date.now() };
}

// ---- 共用小件 ----

function sessionIdOf(agent: AgentRef | undefined): string {
  return agent?.session?.header?.id ?? "";
}

function cwdOf(agent: AgentRef | undefined): string {
  return agent?.session?.header?.cwd ?? process.cwd();
}

/** 文本块拍平（官方桥 blocksToText 同位先例,hooks-claude-code/src/index.ts:323-326） */
export function blocksToText(messages: MessageInput[]): string {
  return messages
    .flatMap((m) => m?.content ?? [])
    .filter((b) => b?.type === "text" && typeof b.text === "string")
    .map((b) => b.text as string)
    .join("");
}

// ---- ① agent/pre-step：闸门问询 ----

/**
 * 问 daemon 闸门：block → reject（用户可见理由经 logger——协议无理由字段,
 * 票03 钉死的设计约束）;allow 带 additional_context（observe 警告）→ 追加注入
 * 上下文消息后放行（官方桥 :225-241 同款）;一切故障 fail-open 放行（
 * ferryman-gate-codex.ps1:73 同纪律）。空步（合成上下文步）不问——官方桥
 * :226 同位短路。
 */
export async function onPreStep(
  deps: EventDeps,
  payload: PreStepPayload,
  next: PreStepNext,
): Promise<PreStepDecision> {
  if (!payload?.messages?.length) return next();
  const gate = await askGate(deps.ep, {
    session_id: sessionIdOf(payload.agent),
    cwd: cwdOf(payload.agent),
    prompt: blocksToText(payload.messages),
  });
  if (gate?.decision === "block") {
    const reason = gate.reason ?? "会话闲置被闸门拦截";
    deps.logger.warn(`[ferryman-dsh] 本条输入被 Ferryman 闸门拦截：${reason}`);
    return { kind: "reject" };
  }
  const downstream = await next();
  if (gate?.additional_context && downstream.kind === "enter") {
    return {
      ...downstream,
      messages: [...downstream.messages, injectedMessage(gate.additional_context)],
    };
  }
  return downstream;
}

// ---- ② agent/created（awaited）：交接播种 ----

/**
 * 同 Agent＋cwd 的交接 MD 经 agent.inject() 播种（赶首请求——serial awaited
 * 位保证,见文件头钉点）。无交接/无键/daemon 故障 → 静默返回;永不抛
 *（throw 会弄失败 agent creation,runtime-types.ts:253-254）。
 */
export async function onCreated(deps: EventDeps, payload: CreatedPayload): Promise<void> {
  try {
    const agent = payload?.agent;
    const sid = sessionIdOf(agent);
    if (!sid || typeof agent?.inject !== "function") return;
    const md = await askHandoff(deps.ep, { cwd: cwdOf(agent), session_id: sid });
    if (md) agent.inject(injectedMessage(md));
  } catch (e) {
    // 防御带：inject 抛错等宿主侧意外——creation 不因插件失败
    deps.logger.warn(`[ferryman-dsh] 交接播种失败（忽略继续）: ${e instanceof Error ? e.message : String(e)}`);
  }
}

// ---- ③ session/event：上报（含标题跟踪与形状构造） ----

/** session/title 事件的标题提取（事件日志本体形,dsh_wiring_test.go 夹具同源） */
function titleOf(event: SessionEventRef): string {
  const data = event?.data;
  if (data !== null && typeof data === "object") {
    const t = (data as Record<string, unknown>)["title"];
    if (typeof t === "string" && t.length > 0) return t;
  }
  return "";
}

/**
 * dsh TokenUsage（camel,不相交口径）→ 票04 口四列（snake）。
 * cacheWriteTokens → cache_creation_tokens（账面列名,dshtrans.go:48-51 同映射）;
 * 缺省 0（daemon dshIntOr 同宽收形）。非对象 usage → undefined（键省略）。
 */
export function usageToDaemon(usage: unknown): Record<string, number> | undefined {
  if (usage === null || typeof usage !== "object" || Array.isArray(usage)) return undefined;
  const u = usage as Record<string, unknown>;
  const n = (v: unknown): number => (typeof v === "number" && Number.isFinite(v) ? v : 0);
  return {
    input_tokens: n(u.inputTokens),
    cache_read_tokens: n(u.cacheReadTokens),
    cache_creation_tokens: n(u.cacheWriteTokens),
    output_tokens: n(u.outputTokens),
  };
}

/**
 * session/event → /dsh/event body。白名单=turn/start、assistant/message、
 * compaction/*（票04 契约;dsh_receive.go:108-110）;session/title 只作标题
 * 跟踪不上报;子会话（origin=subagent 且 parentSession）带 parent_session_id
 *（daemon 侧随父入账,dsh_receive.go:121 分流同款）。null=不上报。
 */
export function buildEventBody(
  session: SessionRef,
  event: SessionEventRef | null,
  titles: Map<string, string>,
): Record<string, unknown> | null {
  const h = session?.header;
  const sid = h?.id ?? "";
  if (!sid || event === null || typeof event !== "object") return null;
  const type = typeof event.type === "string" ? event.type : "";
  if (type !== "turn/start" && type !== "assistant/message" && !type.startsWith("compaction/")) {
    return null;
  }
  const body: Record<string, unknown> = {
    session_id: sid,
    event: type,
    time: typeof event.time === "number" && Number.isFinite(event.time) ? event.time : 0,
    cwd: h?.cwd ?? "",
    title: titles.get(sid) ?? "",
  };
  if (h?.origin === "subagent" && h.parentSession) {
    body.parent_session_id = h.parentSession;
  }
  if (type === "assistant/message") {
    const data = (event.data ?? {}) as Record<string, unknown>;
    const message = (data.message ?? {}) as Record<string, unknown>;
    const source = (message.source ?? {}) as Record<string, unknown>;
    if (typeof source.model === "string" && source.model) body.model = source.model;
    const usage = usageToDaemon(data.usage);
    if (usage) body.usage = usage;
  }
  return body;
}

/**
 * session/event 处理器（fire-and-forget）：post-commit 追加流不因上报阻塞;
 * 失败静默重试一次（sendEvent 内策略）,终败 warn 一行。
 */
export function onSessionEvent(deps: EventDeps, session: SessionRef, event: SessionEventRef): void {
  const sid = session?.header?.id ?? "";
  if (!sid) return;
  if (event?.type === "session/title") {
    const t = titleOf(event);
    if (t) deps.titles.set(sid, t);
  }
  const body = buildEventBody(session, event, deps.titles);
  if (!body) return;
  void sendEvent(deps.ep, body).then((r) => {
    if (!r.delivered) {
      deps.logger.warn(`[ferryman-dsh] 事件上报失败（已静默重试一次,放弃）: ${String(event.type)} ${sid}`);
    }
  });
}

// ---- ④ agent/disposed 与 agent/status：判活信号挂接 ----

/**
 * 判活信号转发（fire-and-forget、尽力而为、失败静默——今日 daemon 对
 * agent/status、agent/disposed 按白名单外事件回 ok+skipped=unknown-event
 * 静默收窄,dsh_receive.go:108-110;消费方=P2-5 判活设计,届时 daemon 侧
 * 白名单/语义随设计落票——本侧先把信号送达事件口,通道不再改）。
 */
function forwardLifecycle(
  deps: EventDeps,
  agent: AgentRef | undefined,
  type: "agent/status" | "agent/disposed",
  extra: Record<string, unknown>,
): void {
  const sid = sessionIdOf(agent);
  if (!sid) return;
  const body: Record<string, unknown> = {
    session_id: sid,
    event: type,
    time: (deps.now ?? Date.now)(),
    ...extra,
  };
  void sendEvent(deps.ep, body).catch(() => {});
}

export function onStatus(deps: EventDeps, payload: StatusPayload): void {
  forwardLifecycle(deps, payload?.agent, "agent/status",
    payload?.status ? { status: payload.status } : {});
}

export function onDisposed(deps: EventDeps, payload: DisposedPayload): void {
  forwardLifecycle(deps, payload?.agent, "agent/disposed", {});
}
