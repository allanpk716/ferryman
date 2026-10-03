// 五事件位接口桩——本票只搭骨不实现业务（业务对接=票 05）。
// 协议事实（dsh 调研克隆,只读）——事件名与形状钉点（runtime-types.ts =
// packages/core/agent/src/runtime-types.ts）：
//   - agent/created（runtime-types.ts:261）payload { agent; source; signal? },@mode serial：
//     监听器按序 await,AgentLoop 扣住排队输入直到 listeners 全部完成（emit 位
//     core/agent/src/index.ts:550 await this.ctx.serial(...)）——票 05 的归还播种赶得上首请求。
//   - agent/disposed（:270）payload { agent },@mode emit（fire-and-forget,监听器 reject 只记日志,
//     core/agent/src/index.ts:513-525）。
//   - agent/status（:280）payload { agent; status },@mode emit;状态 idle ⇄ running（:272-274）。
//   - agent/pre-step（:320）payload { agent; messages; turn; step; signal },next waterfall 中间件;
//     PreStepDecision（:112-119）= { kind:'reject' } | { kind:'enter'; messages; startsRequestSeries? }
//     ——协议不携带「用户可见理由」字段,理由呈现须经 logger/context 注入（票 05 设计约束）。
//   - session/event（packages/core/session/src/index.ts:77）(session, event),@mode emit,
//     post-commit 追加流（观察者失败只记日志不使 append 失败,:66-76）;事件形状
//     packages/core/session/src/types.ts:281 起——'turn/start'(:288)、'assistant/message'(:341-349,
//     usage?: TokenUsage 内嵌于事件)、compaction 三事件经插件 declare-module 并入同表：
//     packages/compaction/compaction/src/types.ts:24 'compaction/start' / :34 'compaction/summary' /
//     :72 'compaction/end'。
//   - 注册 API：插件导出 apply(ctx, config),事件用 ctx.on(event, handler) 挂接;pre-step 处理器
//     可 await next() 委托下游（官方桥 packages/hooks/hooks-claude-code/src/index.ts:102/209/225）。
//   - agent.inject 语义（runtime-types.ts:241）：inject(message: UserMessage): void——排队赶下一个
//     pre-step、不唤醒 driver;运行中的 driver 在最近的 step 边界领取,idle 的挂着直到下次唤醒;
//     可能错过「pre-step 已领取批次」的那次请求（:233-240 文档）。用法先例
//     hooks-claude-code/src/index.ts:213-214（createUserMessage 后 agent.inject(context)）。

/** 宿主 agent 的结构化引用——票 05 对齐宿主类型后收敛字段（勿臆造必填） */
export interface AgentRef {
  id?: string;
  cwd?: string;
}

/** session/event 的 session 侧引用（dsh 会话键=会话头行 id,P2-2 定案） */
export interface SessionRef {
  id?: string;
  cwd?: string;
}

// ---- agent/pre-step（waterfall）----

/** PreStepDecision（runtime-types.ts:112-119）——协议无理由字段 */
export type PreStepDecision =
  | { kind: "reject" }
  | { kind: "enter"; messages: unknown[]; startsRequestSeries?: true };

export interface PreStepPayload {
  agent: AgentRef;
  messages: unknown[];
  turn: number;
  step: number;
  signal?: AbortSignal;
}

export type PreStepNext = () => Promise<PreStepDecision>;

// ---- agent/created（serial,可 awaited）----

export interface CreatedPayload {
  agent: AgentRef;
  source?: string;
  signal?: AbortSignal;
}

// ---- agent/disposed / agent/status（emit）----

export interface DisposedPayload {
  agent: AgentRef;
}

export interface StatusPayload {
  agent: AgentRef;
  status: "idle" | "running";
}

// ---- session/event（emit,post-commit）----

export interface SessionEventRef {
  /** 'turn/start' | 'assistant/message' | 'compaction/start' | 'compaction/summary' | 'compaction/end' 等 */
  type: string;
  /** 其余字段按事件型别透传（usage 内嵌于 assistant/message,types.ts:341-349） */
  [key: string]: unknown;
}

/**
 * agent/pre-step 桩——TODO(票05): 向 daemon 管理口问闸门,reject（用户可见理由走 logger）
 * 或 enter。骨架默认透传 next(),不拦截。
 */
export async function onPreStep(payload: PreStepPayload, next: PreStepNext): Promise<PreStepDecision> {
  void payload;
  return next();
}

/**
 * agent/created 桩（awaited）——TODO(票05): 同 Agent＋同 cwd 的最新交接 MD 经 agent.inject()
 * 播种（UserMessage 构造后注入,先例 hooks-claude-code/src/index.ts:213-214;注意 inject 可能错过
 * 已领取批次的请求）。
 */
export async function onCreated(payload: CreatedPayload): Promise<void> {
  void payload;
}

/**
 * session/event 桩——TODO(票05): turn/start、assistant/message（带 usage）、compaction/*
 * 上报 daemon（渡口捕获的替代/补充,字段与 P2-1 pollDsh 同构）。
 */
export function onSessionEvent(session: SessionRef, event: SessionEventRef): void {
  void session;
  void event;
}

/** agent/disposed 桩——TODO(票05): 判活信号（P2-5 消费） */
export function onDisposed(payload: DisposedPayload): void {
  void payload;
}

/** agent/status 桩——TODO(票05): 判活信号（P2-5 消费,status=idle 即空闲判据候选） */
export function onStatus(payload: StatusPayload): void {
  void payload;
}
