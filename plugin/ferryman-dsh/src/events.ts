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

import { askGate, askHandoff, sendEvent, type DaemonEndpoint, type HandoffAnswer } from "./daemon.ts";
import { SessionRegistry } from "./compact.ts";
import { BannerStore } from "./banner.ts";
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
  /** followup 代发面（runtime-types.ts:218-222,入队即唤醒开新 turn）——票08
   *  「强续重发」用;可选鸭子面,缺省走降级文案（spike 验②:拦截现场可直接捕获） */
  followup?: (message: UserMessageLike) => void;
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
  /** 欠交接账本（键=会话头行 id,多会话互不串）：created 问空记一笔,
   *  用户步重问拿到即清;dispose 终局清（A4② 持续重试,2026-10-06 票05;
   *  票02 补:created 拿到也清——15:41 陈欠账根因,拿到即债务清偿） */
  handoffPending: Map<string, boolean>;
  /** 同轮交接注入去重账本（票02 dsh-first-live-followups,键=会话头行 id）：
   *  值=轮游标（lastTurn）+本轮已注入材料的首句模板键集;同会话同轮内同族
   *  材料只注入一份（15:41 双清单案——全文比对拦不住内容不同的重复）。
   *  用户步 turn 前进=新轮（清键集）;created 不清（重铸 agent 连发两问正是
   *  双清单形态的活路径）;dispose 终局清。可选字段：makeEventDeps 恒置;
   *  手构测试替身可缺省＝该面去重关闭（helpers 判缺直放,不炸） */
  handoffDedup?: Map<string, HandoffDedupRec>;
  /** 被拦事件仓（票08：拦截现场缓存,浏览器卡片数据源;宿主进程生命周期） */
  blocked: BlockedStore;
  /** 会话注册表（票05 热压缩）：五事件位维护 sid→{agent 活引用,闲置时钟,忙位};
   *  轮询执行臂（compact.ts）据此报宿主会话清单并做执行前双重复查 */
  registry: SessionRegistry;
  /** 压缩完成横幅仓（票06,src/banner.ts）：compact.ts 成功路径置位,此处用户步
   *  （下次发消息）与 dispose 清位;浏览器经 ferrymanBlocked list 信封 banner
   *  布尔拉取显示 */
  banner: BannerStore;
  /** 时钟注入面（判活转发与注册表闲置钟的时间戳）;缺省 Date.now */
  now?: () => number;
}

export function makeEventDeps(ep: DaemonEndpoint, logger: LoggerLike): EventDeps {
  const now = () => Date.now();
  return {
    ep,
    logger,
    titles: new Map(),
    handoffPending: new Map(),
    handoffDedup: new Map(),
    blocked: new BlockedStore(),
    now,
    registry: new SessionRegistry(now),
    banner: new BannerStore(),
  };
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

// ---- 票02（dsh-first-live-followups）：同轮交接注入去重（首句模板判重） ----

/** 同轮去重账本的单会话记录：lastTurn=已见最大用户轮号;keys=本轮已注入
 * 材料的首句模板键集 */
export interface HandoffDedupRec {
  lastTurn: number;
  keys: Set<string>;
}

/**
 * 交接材料的首句模板键：首行（到首个换行）中数字段折叠为 "#" 后比对。
 * daemon 注入材料的可变部都在首行数字位——"本项目有 N 份可用交接"（N 与
 * 清单内容可变）与"[Ferryman 交接 · 日期 时间 · 会话 X]"（正文可变）。
 * 2026-10-07 15:41 双清单案：全文比对拦不住内容不同的重复,模板键不同 N/
 * 不同清单也同键;不同首句（清单族 vs 文档族 vs 不同标题文档）不同键。
 */
export function handoffTemplateKey(md: string): string {
  const firstLine = (md.split("\n")[0] ?? "").trim();
  return firstLine.replace(/[0-9]+/g, "#");
}

/**
 * 记账式判重：同会话本轮已注入同模板键 → true（拦下）;否则记账返回 false。
 * 空键（首行为空——非 [Ferryman] 形态的防御面）与缺账本（手构替身）不判重
 * 直放。created 不清账（重铸 agent 连发两问正是双清单形态的活路径）——轮
 * 推进只在 handoffRoundAdvance（用户步）。
 */
function handoffDedupMark(deps: EventDeps, sid: string, md: string): boolean {
  if (deps.handoffDedup === undefined) return false;
  let rec = deps.handoffDedup.get(sid);
  if (rec === undefined) {
    rec = { lastTurn: 0, keys: new Set() };
    deps.handoffDedup.set(sid, rec);
  }
  const key = handoffTemplateKey(md);
  if (key === "") return false;
  if (rec.keys.has(key)) return true;
  rec.keys.add(key);
  return false;
}

/** 用户步轮推进（step===1 处调用）：turn 前进＝新轮——清该会话模板账
 * （同轮判重,不跨轮）;turn 未前进（同轮重试/工具循环）不动账;缺账本
 * （手构替身）直过 */
function handoffRoundAdvance(deps: EventDeps, sid: string, turn: number): void {
  if (deps.handoffDedup === undefined) return;
  const rec = deps.handoffDedup.get(sid);
  if (rec === undefined) {
    deps.handoffDedup.set(sid, { lastTurn: turn, keys: new Set() });
    return;
  }
  if (turn > rec.lastTurn) {
    rec.lastTurn = turn;
    rec.keys.clear();
  }
}

// ---- block 文案（票10 · F2 止血） ----

/**
 * block 文案里被拦原话的截断上限：120 码点（Code Point,增补平面字符按 1 计,
 * 不按 UTF-16 单元——emoji 等不会被腰斩）。插件侧此前无截断工具,本 helper 为
 * 本票新增;daemon 侧先例是 token 估算 cap=500（store.go pendingPromptCap）,
 * 日志面板一行摘要用码点上限更直观,不照搬。已知取舍：宿主日志面板手机端
 * 不可见的根治在票08（F1 对话区选择框）,本票是过渡止血。
 */
const blockPromptCap = 120;

/** 按码点截断：超 cap 取头 cap 个码点加省略号;不超原样返回 */
export function truncateCodePoints(text: string, cap: number): string {
  const cps = Array.from(text);
  if (cps.length <= cap) return text;
  return cps.slice(0, cap).join("") + "…";
}

// ---- 被拦事件缓存（票08 · F1 双面包,宿主半面数据源） ----

/** daemon bypass 前缀（internal/daemon/gate.go:106 strings.HasPrefix(prompt,"强续")） */
export const BYPASS_PREFIX = "强续";

/**
 * 宿主侧完整被拦事件（含 agent 活引用——只活在本进程,绝不外泄出宿主面;
 * 被拒消息本身从 durable inbox 消失不进会话日志,runtime-types.ts:291,
 * 拦截时缓存是原话唯一主径——spike 验②）。
 */
export interface BlockedEvent {
  id: string;
  sessionId: string;
  cwd: string;
  reason: string;
  prompt: string;
  time: number;
  agent: AgentRef;
  done: boolean;
  doneAction: BlockedAction | null;
}

/** 动作记录（塌缩行文案的判别键） */
export type BlockedAction = "resend" | "new-session";

/** 浏览器拉取面 wire 形状（JSON 安全;键白名单,无 agent） */
export interface BlockedCard {
  id: string;
  reason: string;
  prompt: string;
  time: number;
  done: boolean;
  doneAction: BlockedAction | null;
}

export interface BlockedRecordInput {
  sessionId: string;
  cwd: string;
  reason: string;
  prompt: string;
  time: number;
  agent: AgentRef;
}

/** 会话内缓存上限（FIFO 逐出最老;宿主内存有界,丢失面=降级手打,票10 文案兜底） */
const BLOCKED_SESSION_CAP = 20;
/** wire 面原话截断（码点;防御面——原话可能极长,浏览器只需预览与身份比对） */
const BLOCKED_PROMPT_WIRE_CAP = 2000;

/**
 * 被拦事件仓：拦截现场 record,Remote 面拉取/动作落账。宿主重启即失
 * （ADR-0023 取舍）——已知且接受:强续降级为用户手打,新会话本来就不依赖缓存。
 */
export class BlockedStore {
  private seq = 0;
  private readonly bySession = new Map<string, BlockedEvent[]>();

  /** 拦截现场落账（同会话 FIFO 封顶;返回完整事件供调用方续用） */
  record(input: BlockedRecordInput): BlockedEvent {
    const events = this.bySession.get(input.sessionId) ?? [];
    if (events.length >= BLOCKED_SESSION_CAP) events.shift();
    const ev: BlockedEvent = {
      id: `b${++this.seq}`,
      sessionId: input.sessionId,
      cwd: input.cwd,
      reason: input.reason,
      prompt: input.prompt,
      time: input.time,
      agent: input.agent,
      done: false,
      doneAction: null,
    };
    events.push(ev);
    this.bySession.set(input.sessionId, events);
    return ev;
  }

  /** wire 面（prompt 截断;不含 agent）;空会话空数组 */
  list(sessionId: string): BlockedCard[] {
    return (this.bySession.get(sessionId) ?? []).map((ev) => ({
      id: ev.id,
      reason: ev.reason,
      prompt: truncateCodePoints(ev.prompt, BLOCKED_PROMPT_WIRE_CAP),
      time: ev.time,
      done: ev.done,
      doneAction: ev.doneAction,
    }));
  }

  get(id: string): BlockedEvent | undefined {
    for (const events of this.bySession.values()) {
      const hit = events.find((ev) => ev.id === id);
      if (hit !== undefined) return hit;
    }
    return undefined;
  }

  markDone(id: string, action: BlockedAction): BlockedEvent | undefined {
    const ev = this.get(id);
    if (ev === undefined || ev.done) return ev;
    ev.done = true;
    ev.doneAction = action;
    return ev;
  }

  /**
   * 会话终局清空该会话的全部被拦条目（与 handoffPending 同纪律:dispose 即清,
   * 不跨会话泄漏）。死会话的卡片无处渲染也无从动作——原话全文（≤20 条）不再
   * 长跑缓占内存;清后 Remote 面对该会话回空数组。
   */
  clearSession(sessionId: string): void {
    this.bySession.delete(sessionId);
  }

  /**
   * 手打强续对账：用户绕过卡片手敲「强续 <原话>」放行时,把精确同文的待处理卡
   * 转 done(resend)——卡片不留假待办。精确匹配（去前缀+trim 后全等）,不做模糊
   * 归并;未命中不动（另一句被拦的原话没被重发,卡片保持真状态）。
   */
  markDoneByPrompt(sessionId: string, prompt: string, action: BlockedAction): boolean {
    const events = this.bySession.get(sessionId);
    if (events === undefined) return false;
    let hit = false;
    for (const ev of events) {
      if (!ev.done && ev.prompt === prompt) {
        ev.done = true;
        ev.doneAction = action;
        hit = true;
      }
    }
    return hit;
  }
}

/** 代发文本：「强续 」+原话;原话本身以强续开头时去重避免「强续 强续 …」 */
export function withBypassPrefix(prompt: string): string {
  return prompt.startsWith(BYPASS_PREFIX) ? prompt : `${BYPASS_PREFIX} ${prompt}`;
}

/** 手打对账的比对键：强续前缀剥掉+两端 trim;无前缀返回 null（普通消息不对账） */
export function stripBypassPrefix(text: string): string | null {
  if (!text.startsWith(BYPASS_PREFIX)) return null;
  return text.slice(BYPASS_PREFIX.length).trim();
}


// ---- ① agent/pre-step：闸门问询 ----

/**
 * 问 daemon 闸门：block → reject（用户可见理由经 logger——协议无理由字段,
 * 票03 钉死的设计约束）;allow 带 additional_context（observe 警告）→ 追加注入
 * 上下文消息后放行（官方桥 :225-241 同款）;一切故障 fail-open 放行（
 * ferryman-gate-codex.ps1:73 同纪律）。空步（合成上下文步）不问——官方桥
 * :226 同位短路。
 *
 * 晚到交接重问（A4②）：created 记了欠账的会话,每个用户步（step===1）在此
 * 独立补问一次（不依赖闸门结果,allow/fail-open 都问）;拿到 → injectedMessage
 * 追加进当前步 downstream.messages（additional_context 同位先例）并清账;
 * 没拿到 → 账留着静默放行,绝不阻塞用户消息;答零注入终态（continuation=
 * true,夜链终局评审小修;票02 dsh-cross-inject 扩注＝续用档或新会话无料,
 * daemon 侧零注入终态）→ 清账止问（上下文本就在会话内/新会话本无料,零
 * 注入）。block 步无下游可注入,不问
 * 不清账;循环步（step>1）照旧短路。票02 补两道：用户轮前进即推进去重轮
 * 游标（新轮清模板账）;补注注入过同轮同族材料（首句模板同键）→ 拦下不
 * 重复注入（欠账照清——材料已在会话内）。
 *
 * 票08（F1）：block 落账被拦事件仓（浏览器选择框卡片数据源）;allow 的
 * 「强续 <原话>」做手打对账（精确同文 → 待处理卡转 done）。
 */
export async function onPreStep(
  deps: EventDeps,
  payload: PreStepPayload,
  next: PreStepNext,
): Promise<PreStepDecision> {
  if (!payload?.messages?.length) return next();
  // 只审用户步（step===1）：turn 内第 2+ 步是工具循环的后续模型请求，不是
  // 用户输入——闸门语义是「审用户的回流」，循环步不问不注入（2026-10-05 案：
  // 活跃对话每步都过闸门吃 machineWaiting 豁免＋注入兜底文案，污染上下文）。
  if (payload.step > 1) return next();
  const sid = sessionIdOf(payload.agent);
  // 会话注册表（票05）：用户步=活动,闲置钟归零（agent 活引用同步登记/刷新）
  if (sid) deps.registry.touch(sid, cwdOf(payload.agent), payload.agent);
  // 票06：用户步=横幅的「下次发消息」——压缩横幅展示一次即撤（允许/拦截都撤:
  // 用户已回流,横幅使命结束,滞留反成假承诺）;循环步（step>1）不算用户回流
  if (sid) deps.banner.clear(sid);
  // 票02 同轮注入去重：用户轮前进＝新轮——清该会话本轮已注入模板账（在
  // 补注重问之前推进,本轮 created/重试已记的键随上一轮作废）
  if (sid) handoffRoundAdvance(deps, sid, payload.turn);
  const text = blocksToText(payload.messages);
  const gate = await askGate(deps.ep, {
    session_id: sid,
    cwd: cwdOf(payload.agent),
    prompt: text,
  });
  if (gate?.decision === "block") {
    const reason = gate.reason ?? "会话闲置被闸门拦截";
    // 票10（F2 止血）：reason 之后追加被拦原话与两行指路,不替换 reason。
    // 原话来源=拦截现场 payload.messages,与闸门 prompt 同走 blocksToText,
    // 零新增 daemon 依赖;无文本块时省略原话行（无话可示,不给空行）。
    const promptLine = text ? `被拦原话：${truncateCodePoints(text, blockPromptCap)}\n` : "";
    deps.logger.warn(
      `[ferryman-dsh] 本条输入被 Ferryman 闸门拦截：${reason}\n` +
      `${promptLine}` +
      `留在本会话：发送「强续 重发你的内容」可强制继续；\n` +
      `新建会话（同目录）：开场自动收到交接与本条原话，无需重打。`,
    );
    // 票08（F1）：被拒消息从 durable inbox 消失不进会话日志（runtime-types.ts:291）
    // ——拦截时缓存是原话唯一主径;agent 活引用只留宿主侧供「强续重发」代发。
    deps.blocked.record({
      sessionId: sid,
      cwd: cwdOf(payload.agent),
      reason,
      prompt: text,
      time: (deps.now ?? Date.now)(),
      agent: payload.agent,
    });
    return { kind: "reject" };
  }
  // 手打强续对账（票08）：用户绕过卡片手敲「强续 <原话>」过闸——把精确同文的
  // 待处理卡转 done(resend),卡片不留假待办;普通放行消息不参与对账。
  const stripped = stripBypassPrefix(text);
  if (sid && stripped !== null) {
    deps.blocked.markDoneByPrompt(sid, stripped, "resend");
  }
  const downstream = await next();
  if (downstream.kind !== "enter") return downstream;
  const extras: UserMessageLike[] = [];
  if (sid && deps.handoffPending.get(sid)) {
    let ans: HandoffAnswer = { md: null, continuation: false };
    try {
      ans = await askHandoff(deps.ep, { cwd: cwdOf(payload.agent), session_id: sid });
    } catch {
      // 防御带：askHandoff 契约不抛;真抛等同没拿到,放行不阻不噪
    }
    if (ans.md) {
      deps.handoffPending.delete(sid);
      // 票02 同轮去重：同族材料本轮已注入（首句模板同键）→ 拦下不补注
      //（材料已在会话内,欠账照清）;warn 一行留可观测痕
      if (handoffDedupMark(deps, sid, ans.md)) {
        deps.logger.warn(`[ferryman-dsh] 同轮交接注入去重（首句模板相同,已拦）: ${sid}`);
      } else {
        extras.push(injectedMessage(ans.md));
      }
    } else if (ans.continuation) {
      // 零注入终态（夜链终局评审小修;票02 dsh-cross-inject 扩注）：续用档
      // 或新会话无料（daemon 侧零注入终态）——上下文本就在会话内或新会话
      // 本无料,零注入;与"材料未到稍后重试"就此可区分,清账止问（否则每条
      // 用户消息重问＋daemon 每问全量读解转录）。
      deps.handoffPending.delete(sid);
    }
  }
  if (gate?.additional_context) {
    extras.push(injectedMessage(gate.additional_context));
  }
  if (extras.length === 0) return downstream;
  return { ...downstream, messages: [...downstream.messages, ...extras] };
}

// ---- ② agent/created（awaited）：交接播种 ----

/**
 * 同 Agent＋cwd 的交接 MD 经 agent.inject() 播种（赶首请求——serial awaited
 * 位保证,见文件头钉点）。askHandoff 空（交接还没铸好,含 daemon 暂不可达）→
 * 记欠账 handoffPending,此后每个用户步重问补注（A4② 持续重试）;置账静默。
 * 空但 continuation=true（夜链终局评审小修;票02 dsh-cross-inject 扩注）＝
 * 零注入终态（续用档或新会话无料,daemon 侧零注入终态）——清欠账不置账
 *（止问）;无键/抛错 → 不置账（fail-open:欠账只在「确实问过且确实空」时记）;
 * 永不抛（throw 会弄失败 agent creation,runtime-types.ts:253-254）。
 *
 * 票02（dsh-first-live-followups）两道补：①拿到即清欠账（15:41 陈欠账根因
 * ——此前拿到不清,用户步又重问,同轮双注入）;②同轮去重——同族材料（首句
 * 模板同键）本轮已注入（重铸 agent 连发两问的活路径）拦下不重复播种,warn
 * 一行留可观测痕。
 */
export async function onCreated(deps: EventDeps, payload: CreatedPayload): Promise<void> {
  try {
    const agent = payload?.agent;
    const sid = sessionIdOf(agent);
    if (!sid) return;
    // 会话注册表（票05）：created 即登记（闲置时钟起点）,agent 活引用留给执行臂
    deps.registry.touch(sid, cwdOf(agent), agent);
    if (typeof agent?.inject !== "function") return;
    const ans = await askHandoff(deps.ep, { cwd: cwdOf(agent), session_id: sid });
    if (ans.md) {
      deps.handoffPending.delete(sid); // 拿到即清欠账（票02:15:41 陈欠账根因）
      if (handoffDedupMark(deps, sid, ans.md)) {
        deps.logger.warn(`[ferryman-dsh] 同轮交接注入去重（首句模板相同,已拦）: ${sid}`);
        return;
      }
      agent.inject(injectedMessage(ans.md));
    } else if (ans.continuation) {
      // 零注入终态（夜链终局评审小修;票02 dsh-cross-inject 扩注）：续用档
      // 或新会话无料（daemon 侧零注入终态）——零注入;清欠账止问,不置账
      //（与"材料未到稍后重试"就此可区分）。
      deps.handoffPending.delete(sid);
    } else {
      deps.handoffPending.set(sid, true);
    }
  } catch (e) {
    // 防御带：inject 抛错等宿主侧意外——creation 不因插件失败;
    // askHandoff 若真抛同落此处,不置欠账。
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
  // 会话注册表（票05）：会话活动流刷新闲置钟（无 agent 引用,不覆盖既有引用）
  deps.registry.touch(sid, session?.header?.cwd ?? "");
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
  const sid = sessionIdOf(payload?.agent);
  if (sid) {
    // 会话注册表（票05）：先登记（status 可能先于 created 到）,再置忙位
    deps.registry.touch(sid, cwdOf(payload?.agent), payload?.agent);
    deps.registry.setStatus(sid, payload?.status);
  }
  forwardLifecycle(deps, payload?.agent, "agent/status",
    payload?.status ? { status: payload.status } : {});
}

export function onDisposed(deps: EventDeps, payload: DisposedPayload): void {
  const sid = sessionIdOf(payload?.agent);
  if (sid) {
    deps.handoffPending.delete(sid); // 会话终局清欠账,不跨会话泄漏
    deps.handoffDedup?.delete(sid); // 票02：终局清同轮去重账（同纪律）
    deps.blocked.clearSession(sid); // 会话终局清被拦缓存（票08 返工:原话全文不缓跑累积）
    deps.banner.clear(sid); // 会话终局清横幅（票06;与 blocked/handoffPending 同纪律）
    deps.registry.remove(sid); // 会话终局出注册表（票05:不再进 poll 会话清单）
  }
  forwardLifecycle(deps, payload?.agent, "agent/disposed", {});
}
