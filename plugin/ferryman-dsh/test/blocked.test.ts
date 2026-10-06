// 票08 · 对话区选择框 F1 双面包 TDD（先红后绿）。
// 两半面同测：
//   宿主半面（src/events.ts 拦截现场缓存 + src/index.ts Remote 服务）——协议事实
//   （dsh 调研克隆,只读）钉点：
//     - SRC Remote 面：网关对无 strict 描述符的端点走源标记回退——服务对象带
//       `typertRemote` 绑定（gateway/src/index.ts:1387-1427 readBinding 校验
//       service 自指）＋原型标记描述符 `'@deepseek-ai/dsh-typert-protocol/
//       remote-methods'`（typert-protocol/src/index.ts:140/265-287）；参数名由
//       Function.toString 解析（gateway/src/index.ts:1434-1468,纯标识符,类型
//       注解经 strip-types 变空白可 trim）;claims 缓存随 internal/service 失效
//       （gateway/src/index.ts:230-232）,插件 provide 后必被重扫。
//     - followup 代发：runtime-types.ts:218-222 followup(UserMessage)——入队即
//       唤醒,新 turn 首步过 pre-step;「强续」前缀命中 daemon bypass（gate.go:106）。
//     - 一键新会话：ctx.agents.create({sessionId, meta:{cwd}})
//       （core/agent/src/index.ts:391 CreateAgentOptions:sessionId 必填）;
//       工作区挂靠先例 session-controller/src/commands.ts:105-144/569-570
//       （workspaceRegistry.list() → sessionIds 含源会话 → attachSession）。
//   浏览器半面（client.js,零依赖手写）——模块系统钉点：
//     - 插件包声明 dsh.client{platform:'web'}＋exports["./client"] → 宿主扫描器
//       编入 roster（client/modules/src/index.ts:823-858 resolveMeta,:194-205
//       clientExportOf）,client.js 原文拼进 combo 脚本（:400-405 buildComboScript
//       纯拼接）,浏览器侧须自调 window.__ModuleLoader__.load({id, factory})
//       （client/manifest.ts:16-21 懒 CJS 模型）。
//     - factory 返回 {name, inject, apply}（vendor/cordis/src/registry.ts:222-228）;
//       react/ui-slots 走平台共享模块表（client/web/src/platform.ts:8-14）。
//     - 对话区卡面 = conversation.composer.dock 列表位（InputBar 渲染,真槽名见 client.js 头注
//       路;ui-goal/src/client/index.ts:92-144 注册先例,inject 回调按会话发面）。
//     - 浏览器侧拉取走 ctx.remote.$mount(手写 strict 描述符;api/gateway/src/
//       client/index.ts:202-210 $mount,:790-805 要求 strict 编解码器——手写
//       透传 parse 即合法）,调用面 ctx.remote.<ns>.<method>（:749-754 服务键）。
import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import {
  BlockedStore,
  makeEventDeps,
  onPreStep,
  type BlockedCard,
  type EventDeps,
  type LoggerLike,
} from "../src/events.ts";
import {
  BLOCKED_SERVICE_KEY,
  buildBlockedService,
  registerBlockedRemote,
} from "../src/index.ts";
import type { UserMessageLike } from "../src/usermessage.ts";
import { startMockDaemon, type MockDaemon } from "./mockdaemon.ts";

const SID = "session-55555555-5555-4555-8555-555555555555";
const SID_B = "session-bbbbbbbb-2222-4222-8222-222222222222";

function makeLogger(): LoggerLike & { warns: string[]; errors: string[] } {
  const warns: string[] = [];
  const errors: string[] = [];
  return { warns, errors, warn: (m) => void warns.push(m), error: (m) => void errors.push(m) };
}

function depsOver(mock: MockDaemon, logger: LoggerLike): EventDeps {
  return makeEventDeps({ baseURL: mock.url, token: "tok-t8", fetchImpl: fetch }, logger);
}

async function mockFor(t: { after: (fn: () => void) => void }): Promise<MockDaemon> {
  const mock = await startMockDaemon();
  t.after(() => void mock.close());
  return mock;
}

function blockRoute(mock: MockDaemon, reason = "会话已闲置 8.8 小时,缓存大概率已失效"): void {
  mock.route("/dsh/gate", () => ({ status: 200, json: { decision: "block", reason } }));
}

function allowRoute(mock: MockDaemon): void {
  mock.route("/dsh/gate", () => ({ status: 200, json: { decision: "allow" } }));
}

const agentOf = (sid: string) => ({ session: { header: { id: sid, cwd: "C:/proj" } } });
const down = async () => ({ kind: "enter", messages: [] }) as const;

async function intercept(deps: EventDeps, text: string, sid = SID): Promise<void> {
  const decision = await onPreStep(deps, {
    agent: agentOf(sid),
    messages: [{ content: [{ type: "text", text }] }],
    turn: 1, step: 1,
  }, down);
  assert.equal(decision.kind, "reject");
}

// ---- ① 拦截现场缓存（宿主半面数据源） ----

test("拦截→缓存：gate block 落账被拦事件（原话/原因/会话键同源 payload.messages）", async (t) => {
  const mock = await mockFor(t);
  blockRoute(mock, "会话已闲置 8.8 小时——直接继续会全量重付");
  const deps = depsOver(mock, makeLogger());
  await intercept(deps, "再按同样的分组算一遍方差");
  const cards = deps.blocked.list(SID);
  assert.equal(cards.length, 1, "恰一张卡");
  const card = cards[0]!;
  assert.equal(card.reason, "会话已闲置 8.8 小时——直接继续会全量重付");
  assert.equal(card.prompt, "再按同样的分组算一遍方差", "原话=blocksToText 同源");
  assert.equal(card.done, false);
  assert.ok(typeof card.id === "string" && card.id.length > 0, "稳定卡片 id");
  assert.ok(typeof card.time === "number");
});

test("list 返回 wire 形状：可 JSON 往返,不含 agent 引用（活引用不出宿主）", async (t) => {
  const mock = await mockFor(t);
  blockRoute(mock);
  const deps = depsOver(mock, makeLogger());
  await intercept(deps, "你好");
  const round = JSON.parse(JSON.stringify(deps.blocked.list(SID))) as BlockedCard[];
  assert.equal(round.length, 1);
  assert.ok(!("agent" in (round[0] as object)), "wire 面不得泄漏 agent 活引用");
  for (const key of Object.keys(round[0]!)) {
    assert.ok(["id", "reason", "prompt", "time", "done", "doneAction"].includes(key), `wire 键白名单外的键: ${key}`);
  }
});

test("多条被拦各自成卡：同会话按时间序排列,跨会话互不串", async (t) => {
  const mock = await mockFor(t);
  blockRoute(mock);
  const deps = depsOver(mock, makeLogger());
  await intercept(deps, "第一条被拦");
  await intercept(deps, "第二条被拦", SID_B);
  await intercept(deps, "第三条被拦");
  const cards = deps.blocked.list(SID);
  assert.deepEqual(cards.map((c) => c.prompt), ["第一条被拦", "第三条被拦"], "会话内按序");
  assert.equal(deps.blocked.list(SID_B).length, 1, "跨会话隔离");
});

test("会话内 FIFO 上限：超上限逐出最老（宿主内存有界）", () => {
  const store = new BlockedStore();
  const agent = agentOf(SID);
  for (let i = 0; i < 22; i++) {
    store.record({ sessionId: SID, cwd: "C:/proj", reason: "r", prompt: `p${i}`, time: i, agent });
  }
  const prompts = store.list(SID).map((c) => c.prompt);
  assert.equal(prompts.length, 20, "封顶 20");
  assert.equal(prompts[0], "p2", "最老被逐出");
});

test("wire prompt 截断：超 2000 码点截断（防御面,不随原话无限长）", () => {
  const store = new BlockedStore();
  store.record({ sessionId: SID, cwd: "C:/proj", reason: "r", prompt: "长".repeat(2001), time: 1, agent: agentOf(SID) });
  const card = store.list(SID)[0]!;
  assert.equal(Array.from(card.prompt).length, 2001, "2000 码点＋省略号");
  assert.ok(card.prompt.endsWith("…"));
});

test("手打强续对账：allow 路径「强续+原话」精确命中 → 待处理卡转 done(resend);未命中不动", async (t) => {
  const mock = await mockFor(t);
  blockRoute(mock);
  const deps = depsOver(mock, makeLogger());
  await intercept(deps, "这句话会被手打强续");
  await intercept(deps, "另一句不相关");
  assert.equal(deps.blocked.list(SID).filter((c) => !c.done).length, 2);

  allowRoute(mock);
  await onPreStep(deps, {
    agent: agentOf(SID),
    messages: [{ content: [{ type: "text", text: "强续 这句话会被手打强续" }] }],
    turn: 2, step: 1,
  }, down);
  const cards = deps.blocked.list(SID);
  const hit = cards.find((c) => c.prompt === "这句话会被手打强续")!;
  const miss = cards.find((c) => c.prompt === "另一句不相关")!;
  assert.equal(hit.done, true, "精确命中的卡塌缩");
  assert.equal(hit.doneAction, "resend");
  assert.equal(miss.done, false, "不匹配的卡不动");

  // 普通放行消息（无强续前缀）不对账
  await onPreStep(deps, {
    agent: agentOf(SID),
    messages: [{ content: [{ type: "text", text: "普通消息" }] }],
    turn: 3, step: 1,
  }, down);
  assert.equal(deps.blocked.list(SID).find((c) => c.prompt === "另一句不相关")!.done, false);
});

// ---- ② Remote 服务（SRC 面;浏览器卡片数据与按钮动作回落宿主） ----

/** 仿网关 SRC 参数解析（gateway/src/index.ts:1454-1467）：取函数源码括号内标识符 */
function srcParamNames(fn: (...args: never[]) => unknown): string[] {
  const source = Function.prototype.toString.call(fn);
  const open = source.indexOf("(");
  const close = source.indexOf(")", open + 1);
  const body = source.slice(open + 1, close).trim();
  if (body.length === 0) return [];
  return body.split(",").map((p) => p.trim());
}

test("SRC 面形状：typertRemote 绑定自指 + 原型标记描述符 version 1 与三方法齐", () => {
  const service = buildBlockedService({ store: new BlockedStore() }) as unknown as Record<string, unknown>;
  const binding = service["typertRemote"] as Record<string, unknown>;
  assert.ok(binding, "有 typertRemote 绑定（gateway SRC 发现面）");
  assert.equal(binding["service"], service, "binding.service 自指（readBinding 校验）");
  assert.equal(binding["serviceKey"], BLOCKED_SERVICE_KEY);
  assert.equal(binding["namespace"], BLOCKED_SERVICE_KEY);

  const proto = Object.getPrototypeOf(service) as Record<string, unknown>;
  const marker = proto["@deepseek-ai/dsh-typert-protocol/remote-methods"] as {
    version: number;
    methods: Array<{ method: string; invocation: { kind: string } }>;
  };
  assert.equal(marker.version, 1);
  assert.deepEqual(
    marker.methods.map((m) => m.method).sort(),
    ["list", "newSession", "resend"],
    "标记方法与实现齐（remoteMethods 读原型描述符）",
  );
  for (const m of marker.methods) assert.equal(m.invocation.kind, "direct");
});

test("SRC 参数名=纯标识符：网关 toString 解析可得 sessionId/id（strip-types 类型注解不碍事）", () => {
  const service = buildBlockedService({ store: new BlockedStore() }) as unknown as Record<string, (...args: never[]) => unknown>;
  const proto = Object.getPrototypeOf(service);
  assert.deepEqual(srcParamNames(proto["list"] as (...args: never[]) => unknown), ["sessionId"]);
  assert.deepEqual(srcParamNames(proto["resend"] as (...args: never[]) => unknown), ["id"]);
  assert.deepEqual(srcParamNames(proto["newSession"] as (...args: never[]) => unknown), ["id"]);
});

test("list：返回 wire 卡片（与 store 同形）;空会话空数组不抛", async () => {
  const store = new BlockedStore();
  store.record({ sessionId: SID, cwd: "C:/proj", reason: "原因", prompt: "原话", time: 5, agent: agentOf(SID) });
  const service = buildBlockedService({ store });
  const out = await service.list(SID);
  assert.equal(out.ok, true);
  assert.equal(out.cards.length, 1);
  assert.equal(out.cards[0]!.prompt, "原话");
  assert.deepEqual(await service.list("session-unknown"), { ok: true, sessionId: "session-unknown", cards: [], banner: false });
  // 票06：信封层扩 banner 布尔（压缩完成横幅数据通道,src/banner.ts）——上方
  // cards[] 元素键白名单（「wire 键白名单外的键」断言）不动,扩的是信封键;
  // banner 语义与状态机钉在 test/banner.test.ts。
});

test("resend：followup 代发「强续 +原话」UserMessage（ferryman-dsh source）,卡片转 done(resend)", async () => {
  const store = new BlockedStore();
  const sent: UserMessageLike[] = [];
  const agent = {
    session: { header: { id: SID, cwd: "C:/proj" } },
    followup: (m: UserMessageLike) => void sent.push(m),
  };
  store.record({ sessionId: SID, cwd: "C:/proj", reason: "r", prompt: "把表格再算一遍", time: 1, agent });
  const service = buildBlockedService({ store });

  const out = await service.resend(store.list(SID)[0]!.id);
  assert.equal(out.ok, true);
  assert.equal(sent.length, 1, "恰一次代发");
  const msg = sent[0]!;
  assert.equal(msg.role, "user");
  assert.equal(msg.source.kind, "ferryman-dsh");
  assert.equal((msg.content[0] as { text: string }).text, "强续 把表格再算一遍");
  assert.match(msg.id, /^[0-9a-f-]{36}$/i);
  const card = store.list(SID)[0]!;
  assert.equal(card.done, true, "动作后塌缩");
  assert.equal(card.doneAction, "resend");
});

test("resend 去重：原话本身以「强续」开头不叠前缀（避免「强续 强续 …」）", async () => {
  const store = new BlockedStore();
  const sent: UserMessageLike[] = [];
  const agent = { session: { header: { id: SID } }, followup: (m: UserMessageLike) => void sent.push(m) };
  store.record({ sessionId: SID, cwd: "C:/proj", reason: "r", prompt: "强续 已经带前缀的原话", time: 1, agent });
  const service = buildBlockedService({ store });
  await service.resend(store.list(SID)[0]!.id);
  assert.equal((sent[0]!.content[0] as { text: string }).text, "强续 已经带前缀的原话");
});

test("resend 降级：未知名（宿主重启丢缓存）与 agent 无 followup 面 → {ok:false} 不抛,文案指路手打", async () => {
  const store = new BlockedStore();
  const service = buildBlockedService({ store });
  const miss = await service.resend("b-nope");
  assert.equal(miss.ok, false);
  assert.ok(miss.error.includes("强续"), "降级文案指路手打「强续 <原话>」");

  store.record({ sessionId: SID, cwd: "C:/proj", reason: "r", prompt: "原话", time: 1, agent: agentOf(SID) });
  const noFollowup = await service.resend(store.list(SID)[0]!.id);
  assert.equal(noFollowup.ok, false, "agent 鸭子面无 followup → 失败不炸");
  assert.equal(store.list(SID)[0]!.done, false, "失败不塌缩（可重试）");
});

test("newSession：agents.create 同 cwd（session-<uuid>）+ 工作区挂靠,卡片 done(new-session)", async () => {
  const store = new BlockedStore();
  const created: Array<{ sessionId: string; meta?: { cwd?: string } }> = [];
  const attached: string[] = [];
  const agents = {
    create: async (options: { sessionId: string; meta?: { cwd?: string } }) => {
      created.push(options);
      return { session: { header: { id: options.sessionId } } };
    },
  };
  const workspaces = {
    list: () => [
      { sessionIds: ["session-other"], attachSession: async () => {} },
      { sessionIds: [SID], attachSession: async (id: string) => void attached.push(id) },
    ],
  };
  store.record({ sessionId: SID, cwd: "C:/proj", reason: "r", prompt: "原话", time: 1, agent: agentOf(SID) });
  const service = buildBlockedService({ store, agents, workspaces });

  const out = await service.newSession(store.list(SID)[0]!.id);
  assert.equal(out.ok, true);
  assert.ok(out.sessionId.startsWith("session-"), "session-<uuid> 同形（commands.ts:109/266 先例）");
  assert.equal(created.length, 1);
  assert.equal(created[0]!.sessionId, out.sessionId);
  assert.equal(created[0]!.meta?.cwd, "C:/proj", "同 cwd（daemon 归还链按 cwd 锚定）");
  assert.deepEqual(attached, [out.sessionId], "挂靠含源会话的工作区（侧栏成组可见）");
  const card = store.list(SID)[0]!;
  assert.equal(card.done, true);
  assert.equal(card.doneAction, "new-session");
});

test("newSession 降级：无 agents 服务 → {ok:false};无 workspaceRegistry → 仍成功不挂靠", async () => {
  const store = new BlockedStore();
  store.record({ sessionId: SID, cwd: "C:/proj", reason: "r", prompt: "原话", time: 1, agent: agentOf(SID) });
  const id = store.list(SID)[0]!.id;
  const noAgents = await buildBlockedService({ store }).newSession(id);
  assert.equal(noAgents.ok, false);
  const agents = { create: async () => ({}) };
  const bare = await buildBlockedService({ store, agents }).newSession(id);
  assert.equal(bare.ok, true, "无注册表仍创建,挂靠 best-effort 跳过");
});

test("动作宿主侧抛错 → {ok:false} 不抛（RPC 面错误即回话,卡片不塌缩）", async () => {
  const store = new BlockedStore();
  // followup 抛（会话已结束等）
  store.record({
    sessionId: SID, cwd: "C:/proj", reason: "r", prompt: "原话A", time: 1,
    agent: { session: { header: { id: SID } }, followup: () => { throw new Error("agent gone"); } },
  });
  // create 抛
  store.record({
    sessionId: SID, cwd: "C:/proj", reason: "r", prompt: "原话B", time: 2,
    agent: agentOf(SID),
  });
  const service = buildBlockedService({
    store,
    agents: { create: async () => { throw new Error("quota"); } },
  });
  const cards = store.list(SID);
  const r1 = await service.resend(cards.find((c) => c.prompt === "原话A")!.id);
  assert.equal(r1.ok, false);
  assert.ok(r1.error!.includes("请手动输入"), "失败文案只留动作指引（裸异常不面向用户,10-06 真机反馈改钉）");
  const r2 = await service.newSession(cards.find((c) => c.prompt === "原话B")!.id);
  assert.equal(r2.ok, false);
  assert.ok(r2.error!.includes("quota"));
  assert.equal(store.list(SID).every((c) => !c.done), true, "失败两卡都保持待处理（可重试）");
});

test("registerBlockedRemote：ctx.provide 注册 ferrymanBlocked 服务;无 provide 面静默跳过", () => {
  const provided = new Map<string, unknown>();
  const ctx = {
    provide: (name: string, value: unknown) => void provided.set(name, value),
  };
  registerBlockedRemote(ctx, { blocked: new BlockedStore() });
  assert.ok(provided.has(BLOCKED_SERVICE_KEY), "以服务键注册进 cordis（网关 SRC 发现走 reflect.props）");
  const svc = provided.get(BLOCKED_SERVICE_KEY) as { typertRemote: unknown };
  assert.ok(svc.typertRemote, "注册的就是带 SRC 面的服务对象");
  assert.doesNotThrow(() => registerBlockedRemote({}, { blocked: new BlockedStore() }), "裸 ctx 不炸（非 cordis 宿主）");
});

test("registerBlockedRemote：cordis 代理对未声明 inject 的服务属性读取抛错时容错——服务照常注册（真机 web 实例激活事故回归钉）", () => {
  const provided = new Map<string, unknown>();
  const ctx = {
    provide: (name: string, value: unknown) => void provided.set(name, value),
    get agents(): never {
      // cordis Context.handler 执法形态：Reflect.has 不中且不在 inject 清单 → 读即抛
      throw new Error('cannot get property "agents" without inject');
    },
    get workspaceRegistry(): never {
      throw new Error('cannot get property "workspaceRegistry" without inject');
    },
  };
  assert.doesNotThrow(() => registerBlockedRemote(ctx, { blocked: new BlockedStore() }), "注入执法抛错不再炸激活");
  assert.ok(provided.has(BLOCKED_SERVICE_KEY), "服务仍注册（list/resend/拦截不受影响）");
  const svc = provided.get(BLOCKED_SERVICE_KEY) as { typertRemote: unknown };
  assert.ok(svc.typertRemote, "注册的是带 SRC 面的服务对象");
});

test("registerBlockedRemote 懒注入:宿主运行期递 agents 面 →「新会话」真建会话（fork-session.ts:60-64 正道,真机 web 宿主改钉）", async (t) => {
  const mock = await mockFor(t);
  blockRoute(mock);
  const deps = depsOver(mock, makeLogger());
  await intercept(deps, "懒注入后新会话可点", SID);
  const provided = new Map<string, unknown>();
  const created: Array<Record<string, unknown>> = [];
  const fakeAgents = { create: async (opts: Record<string, unknown>) => { created.push(opts); } };
  const ctx = {
    provide: (name: string, value: unknown) => void provided.set(name, value),
    get agents(): never { throw new Error('cannot get property "agents" without inject'); },
    inject: (services: readonly string[], cb: (scoped: unknown) => void) => {
      if (services.includes("agents")) cb({ agents: fakeAgents });
    },
  };
  registerBlockedRemote(ctx as never, deps);
  const svc = provided.get(BLOCKED_SERVICE_KEY) as { newSession(id: string): Promise<{ ok: boolean; sessionId?: string }> };
  const card = deps.blocked.list(SID)[0]!;
  const r = await svc.newSession(card.id);
  assert.equal(r.ok, true, "懒注入的 agents 面被用上——新会话真建了");
  assert.match(r.sessionId ?? "", /^session-/, "回话带新会话键");
  assert.equal(created.length, 1, "agents.create 恰调一次");
  assert.equal((created[0]!.meta as Record<string, unknown>).cwd, "C:/proj", "同目录建会话（交接按 cwd 锚定带回）");
});

test("apply 集成：挂接的 pre-step 拦下后,经 provide 出的 Remote 服务可拉到卡片", async (t) => {
  const mock = await mockFor(t);
  mock.route("/", () => ({ status: 200, json: {} }));
  mock.route("/stats", () => ({ status: 200, json: { version: "v0.5.4" } }));
  blockRoute(mock, "闲置拦截");
  const provided = new Map<string, unknown>();
  const registered = new Map<string, (...args: never[]) => unknown>();
  const ctx = {
    logger: makeLogger(),
    on: (event: string, handler: (...args: never[]) => unknown) => void registered.set(event, handler),
    provide: (name: string, value: unknown) => void provided.set(name, value),
  };
  const { apply } = await import("../src/index.ts");
  apply(ctx, { daemonURL: mock.url, daemonToken: "tok-t8", fetchImpl: fetch });
  const handler = registered.get("agent/pre-step")!;
  await handler({
    agent: agentOf(SID),
    messages: [{ content: [{ type: "text", text: "端到端被拦原话" }] }],
    turn: 1, step: 1,
  } as never, (() => Promise.resolve({ kind: "enter", messages: [] })) as never);
  const svc = provided.get(BLOCKED_SERVICE_KEY) as { list(sid: string): Promise<{ ok: boolean; cards: BlockedCard[] }> };
  assert.ok(svc, "apply 连带把 Remote 面挂上");
  const out = await svc.list(SID);
  assert.equal(out.cards.length, 1);
  assert.equal(out.cards[0]!.prompt, "端到端被拦原话");
});

// ---- ③ 浏览器半面（client.js;零浏览器依赖——伪模块加载器+伪 React 驱动） ----

/** 伪 React：记录 createElement 树,useState/useEffect 可驱动 */
function makeFakeReact() {
  const hooks: { states: Map<number, unknown>; effects: Array<() => (void | (() => void))>; cursor: number } = {
    states: new Map(), effects: [], cursor: 0,
  };
  const react = {
    createElement: (type: unknown, props: unknown, ...children: unknown[]) => ({
      type,
      props: (props ?? {}) as Record<string, unknown>,
      children: children.flat(Infinity),
    }),
    useState: (initial: unknown) => {
      const id = hooks.cursor++;
      if (!hooks.states.has(id)) hooks.states.set(id, typeof initial === "function" ? (initial as () => unknown)() : initial);
      const set = (v: unknown) => void hooks.states.set(
        id, typeof v === "function" ? (v as (prev: unknown) => unknown)(hooks.states.get(id)) : v,
      );
      return [hooks.states.get(id), set] as const;
    },
    useEffect: (fn: () => void | (() => void)) => void hooks.effects.push(fn),
  };
  return {
    react,
    hooks: {
      states: hooks.states,
      effects: hooks.effects,
      /** 每次组件渲染前清零 hook 游标（组件内 useState 序列从 0 重数） */
      reset: () => { hooks.cursor = 0; },
    },
  };
}

/** 树内全文收集（文本节点） */
function textsOf(node: unknown, acc: string[] = []): string[] {
  if (typeof node === "string" || typeof node === "number") acc.push(String(node));
  else if (Array.isArray(node)) for (const n of node) textsOf(n, acc);
  else if (node !== null && typeof node === "object" && "children" in (node as object)) {
    textsOf((node as { children: unknown }).children, acc);
  }
  return acc;
}

function findAll(node: unknown, pred: (n: Record<string, unknown>) => boolean, acc: Record<string, unknown>[] = []): Record<string, unknown>[] {
  if (Array.isArray(node)) { for (const n of node) findAll(n, pred, acc); return acc; }
  if (node !== null && typeof node === "object" && "type" in (node as object)) {
    const rec = node as Record<string, unknown>;
    if (pred(rec)) acc.push(rec);
    findAll(rec["children"], pred, acc);
  }
  return acc;
}

const asString = (v: unknown): string => (typeof v === "string" ? v : "");

// client.js 在导入求值时向 __ModuleLoader__ 注册（懒 CJS 模型）——本文件进程内只求值一次,
// 故在顶层装伪 loader、导入一次,后续各用例复用 factory 并按需换 react 替身。
interface FakeRegistration { id: string; factory: (require: (spec: string) => unknown) => Record<string, unknown> }
const fakeRegistrations: FakeRegistration[] = [];
const reactHolder: { current: unknown } = { current: undefined };
(globalThis as Record<string, unknown>)["__ModuleLoader__"] = {
  load: (reg: FakeRegistration) => void fakeRegistrations.push(reg),
};
await import("../client.js");

/** 载入 client.js 模块面（须先装好伪 loader） */
async function loadClientModule(reactStub: unknown): Promise<Record<string, unknown>> {
  assert.equal(fakeRegistrations.length, 1, "client.js 恰一次注册");
  const reg = fakeRegistrations[0]!;
  assert.equal(reg.id, "ferryman-dsh", "注册 id=包名（与 roster 行 id 对齐）");
  reactHolder.current = reactStub;
  return reg.factory((spec: string) => {
    if (spec === "react") return reactHolder.current;
    throw new Error(`fake require: unexpected specifier ${spec}`);
  });
}

test("client 注册与导出：factory 返回 {name, inject[slots], apply}（10-06 事故三后:零宿主服务依赖）", async () => {
  const { react } = makeFakeReact();
  const mod = (await loadClientModule(react)) as unknown as { name: string; inject: string[]; apply: unknown };
  assert.equal(mod.name, "ferryman-dsh");
  assert.deepEqual(mod.inject, ["slots"], "只依赖 slots（remote.<ns> 命名空间经 inject 执法不可达,调用走裸 RPC——真机事故三改钉）");
  assert.equal(typeof mod.apply, "function");
});

test("client apply：dock 注册进 conversation.composer.dock（无 remote 面也不炸）", async () => {
  const { react } = makeFakeReact();
  const mod = (await loadClientModule(react)) as unknown as { apply: (ctx: unknown) => void };
  const registered: Array<{ options: Record<string, unknown>; component: unknown }> = [];
  const slots = {
    inject: (_slot: string, factory: () => unknown) => factory(),
    register: (options: Record<string, unknown>, component: unknown) => {
      registered.push({ options, component });
      return () => {};
    },
  };
  assert.doesNotThrow(() => mod.apply({}), "无 slots 面静默降级");
  assert.equal(registered.length, 0, "无 slots 不注册");
  mod.apply({ slots });

  assert.equal(registered.length, 1, "恰一个 dock 条目");
  assert.equal(registered[0]!.options["name"], "conversation.composer.dock", "真槽名（装机版 InputBar 渲染锚点;调研克隆无此键但装机版双锚并存,composer.dock 为 stats 同位先例;10-06 真机事故改钉）");
  assert.equal(registered[0]!.options["id"], "ferryman-blocked");
  assert.equal(typeof registered[0]!.options["inject"], "function", "按会话发业务面");
  assert.equal(typeof registered[0]!.component, "function", "组件为函数组件");
});

test("client face：动作走裸网关 RPC（client-request 信封,方法/参数名与宿主端点一致）", async () => {
  const { react } = makeFakeReact();
  const mod = (await loadClientModule(react)) as unknown as { apply: (ctx: unknown) => void };
  const posts: Array<{ url: string; body: Record<string, unknown> }> = [];
  const realFetch = globalThis.fetch;
  globalThis.fetch = (((url: RequestInfo | URL, init?: RequestInit) => {
    posts.push({ url: String(url), body: JSON.parse(String(init?.body)) as Record<string, unknown> });
    const body = posts.at(-1)!.body;
    const method = String(body.method);
    const value = method.endsWith("/list")
      ? { ok: true, sessionId: SID, cards: [] }
      : { ok: true };
    return Promise.resolve(new Response(JSON.stringify({ type: "server-response", rpcId: String(body.rpcId), result: { ok: true, value } }), { status: 200 }));
  }) as typeof fetch);
  try {
    const registered: Array<{ options: Record<string, unknown> }> = [];
    const slots = {
      inject: (_s: string, f: () => unknown) => { f(); return () => {}; },
      register: (options: Record<string, unknown>) => { registered.push({ options }); return () => {}; },
    };
    mod.apply({ slots });
    const injected = registered[0]!.options["inject"] as (sessionId: string) => { ferrymanBlocked: Record<string, unknown> };
    const f = injected(SID).ferrymanBlocked;
    assert.equal(f["sessionId"], SID);
    const l = await (f["list"] as () => Promise<unknown>)() as { ok: boolean; cards: unknown[] };
    assert.equal(l.ok, true, "list 解包 result.value");
    await (f["resend"] as (id: string) => Promise<unknown>)("b1");
    await (f["newSession"] as (id: string) => Promise<unknown>)("b2");
    assert.deepEqual(posts.map((p) => p.url.split("/api/")[1]), ["ferrymanBlocked/list", "ferrymanBlocked/resend", "ferrymanBlocked/newSession"], "三方法打到网关端点");
    assert.deepEqual(posts[0]!.body.method, "ferrymanBlocked/list", "信封 method=ns/method");
    assert.deepEqual(posts[0]!.body.payload, { args: { sessionId: SID } }, "list 带会话键");
    assert.deepEqual(posts[1]!.body.payload, { args: { id: "b1" } });
    assert.deepEqual(posts[2]!.body.payload, { args: { id: "b2" } });
    assert.equal(posts.every((p) => p.body.type === "client-request" && typeof p.body.rpcId === "string"), true, "client-request 信封齐");
  } finally {
    globalThis.fetch = realFetch;
  }
});

function installDock(react: unknown): Array<{ options: Record<string, unknown>; component: (props: unknown) => unknown }> {
  // apply 在此替身 ctx 上只做 dock 注册（remote 无 $mount 面 → mount 失败静默,卡面自降级）
  const registered: Array<{ options: Record<string, unknown>; component: (props: unknown) => unknown }> = [];
  const slots = {
    inject: (_s: string, f: () => unknown) => { f(); return () => {}; },
    register: (options: Record<string, unknown>, component: (props: unknown) => unknown) => {
      registered.push({ options, component });
      return () => {};
    },
  };
  // 复用顶层已注册的 factory：给一个可成功的 remote 替身
  const remoteOK = {
    $mount: async () => async () => {},
    ferrymanBlocked: {},
  };
  const mod = fakeRegistrations[0]!.factory((spec: string) => {
    if (spec === "react") return react;
    throw new Error(`fake require: unexpected specifier ${spec}`);
  }) as unknown as { apply: (ctx: unknown) => void };
  mod.apply({ remote: remoteOK, slots });
  assert.equal(registered.length, 1, "dock 恰一注册");
  return registered;
}

/**
 * 替身渲染：首渲（挂载态）→ 显式跑 useEffect（真实 React 挂载后自动跑,替身不跑;
 * effect 会起真 setInterval,收尾必须跑 cleanup,否则进程被轮询吊死）→ 冲微任务
 * （首拉 list 落定）→ 复渲取带数据的树。
 */
async function renderMounted(
  Component: (props: unknown) => unknown,
  hooks: { effects: Array<() => (void | (() => void))>; reset: () => void },
  face: unknown,
  flushMs = 0,
): Promise<unknown> {
  hooks.reset();
  Component({ ferrymanBlocked: face }); // 首渲：挂载态,记录 effect
  const cleanups = hooks.effects.splice(0).map((effect) => effect());
  if (flushMs > 0) await new Promise((r) => setTimeout(r, flushMs));
  else await new Promise((r) => setTimeout(r, 0));
  hooks.reset();
  const tree = Component({ ferrymanBlocked: face });
  for (const cleanup of cleanups) if (typeof cleanup === "function") cleanup();
  return tree;
}

test("卡片待处理态：标题+原因+原话引用+两按钮+底部小字（mock 信息结构）;超长原话截断", async () => {
  const { react, hooks } = makeFakeReact();
  await loadClientModule(react); // 确保注册在位（前序用例已载,幂等）
  const registered = installDock(react);
  const Component = registered[0]!.component;

  const longPrompt = "前".repeat(300);
  const card = { id: "b1", reason: "会话已闲置 8.8 小时,缓存大概率已失效——直接继续会全量重付(约 6.8 万 token)。", prompt: longPrompt, time: 1, done: false, doneAction: null };
  const face = {
    sessionId: SID,
    list: async () => ({ ok: true, cards: [card] }),
    resend: async () => ({ ok: true }),
    newSession: async () => ({ ok: true, sessionId: "session-new" }),
  };
  const tree = (await renderMounted(Component, hooks, face)) as Record<string, unknown>;
  const all = textsOf(tree).join("\n");
  assert.ok(all.includes("这条消息被 Ferryman 闸门拦下"), "标题（mock 头行）");
  assert.ok(all.includes("8.8 小时"), "原因一行");
  assert.ok(all.includes("你刚发的原话"), "引用标签（没有丢）");
  assert.ok(all.includes("前".repeat(120) + "…"), "超长原话截断预览（120 码点）");
  assert.ok(!all.includes("前".repeat(200)), "不整段塞长文");
  const buttons = findAll(tree, (n) => n["type"] === "button");
  assert.equal(buttons.length, 2, "恰两按钮");
  const btnTexts = buttons.map((b) => textsOf(b["children"]).join("")).join("|");
  assert.ok(btnTexts.includes("强续重发"), "按钮①文案");
  assert.ok(btnTexts.includes("新会话继续"), "按钮②文案");
  assert.ok(btnTexts.includes("留在本会话"), "按钮①副行");
  assert.ok(btnTexts.includes("开场自动收到"), "按钮②副行");
  assert.ok(all.includes("无视此卡"), "底部小字");
  for (const b of buttons) assert.equal((b["props"] as Record<string, unknown>)["type"], "button");
});

test("塌缩态与动作闭环：done 卡渲染一行记录;点「强续重发」→ resend 落宿主 → 刷新转塌缩", async () => {
  const { react, hooks } = makeFakeReact();
  await loadClientModule(react);
  const registered = installDock(react);
  const Component = registered[0]!.component;

  const card = { id: "b1", reason: "r", prompt: "再算一遍方差", time: 1, done: false, doneAction: null };
  const doneCard = { ...card, done: true, doneAction: "resend" };
  const doneNew = { ...card, done: true, doneAction: "new-session" };

  // 已处理态：一行记录（两动作两种文案）
  const collapsed = textsOf(await renderMounted(Component, hooks, { list: async () => ({ ok: true, cards: [doneCard] }) }));
  const line = collapsed.join("");
  assert.ok(line.includes("已按「强续」重发") && line.includes("再算一遍方差"), "塌缩一行=动作+原话（mock 状态二）");
  assert.ok(!line.includes("留在本会话"), "塌缩态不渲染按钮区");
  const collapsedNew = textsOf(await renderMounted(Component, hooks, { list: async () => ({ ok: true, cards: [doneNew] }) })).join("");
  assert.ok(collapsedNew.includes("已按「新会话」继续"), "新会话动作另有文案");

  // 动作闭环：pending 态点按钮 → face.resend → ok 后重拉 → 塌缩
  const calls: string[] = [];
  let current: BlockedCard[] = [card];
  const face = {
    sessionId: SID,
    list: async () => ({ ok: true, cards: current }),
    resend: async (id: string) => { calls.push(`resend:${id}`); current = [doneCard]; return { ok: true }; },
    newSession: async (id: string) => { calls.push(`newSession:${id}`); current = [doneNew]; return { ok: true, sessionId: "session-new" }; },
  };
  const tree = (await renderMounted(Component, hooks, face)) as Record<string, unknown>;
  const buttons = findAll(tree, (n) => n["type"] === "button");
  assert.equal(buttons.length, 2);
  const resendBtn = buttons.find((b) => asString(textsOf(b["children"]).join("")).includes("强续重发"))!;
  await ((resendBtn["props"] as { onClick: () => Promise<void> }).onClick)();
  assert.deepEqual(calls, ["resend:b1"], "点击落宿主 resend,带卡片 id");
  // 组件内部态已更新（动作后重拉列表）→ 复渲同组件即塌缩
  hooks.reset();
  const after = textsOf(Component({ ferrymanBlocked: face })).join("");
  assert.ok(after.includes("已按「强续」重发"), "动作后卡片塌缩为记录");
});

test("package.json 双面包声明：dsh.client{platform:'web'} + exports['./client'] 指向 client.js", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const pkg = JSON.parse(readFileSync(join(here, "..", "package.json"), "utf8")) as Record<string, unknown>;
  const dsh = pkg["dsh"] as Record<string, unknown>;
  const client = dsh["client"] as Record<string, unknown>;
  assert.equal(client["platform"], "web", "parseDshClient 认 web 平台（manifest.ts:161-181）");
  const exports = pkg["exports"] as Record<string, unknown>;
  assert.equal(asString(exports["./client"]), "./client.js", "clientExportOf 解析出 client.js（modules/src/index.ts:194-205）");
  assert.ok(readFileSync(join(here, "..", "client.js"), "utf8").includes("__ModuleLoader__"), "client.js 自注册（懒 CJS 模型）");
});

test("exports 门契约：'.' 与 './package.json' 必须在列（文件路径 import 永远撞不上的门）", () => {
  // 返工钉点（2026-10-06 评审）：exports 字段一旦存在,Node 只放行声明路径——
  // 宿主以裸包名 import 插件（repo patch insert 行 name: ferryman-dsh）走 ".",
  // 模块扫描器 locatePkgJson 走 createRequire.resolve('<pkg>/package.json')
  // （client/modules/src/index.ts:885-886）——缺任一条即 ERR_PACKAGE_PATH_NOT_EXPORTED,
  // 插件整体失活/客户端行被静默丢弃。生态四个双面包包（ui-goal/ui-conversation/
  // ui-approval/ui-chat）exports 均含 "."＋"./package.json"。本包测试全走相对
  // 路径 import,实测不到这扇门,故以静态契约断言钉死。
  const here = dirname(fileURLToPath(import.meta.url));
  const pkgRoot = join(here, "..");
  const pkg = JSON.parse(readFileSync(join(pkgRoot, "package.json"), "utf8")) as {
    main?: string;
    exports?: Record<string, unknown>;
  };
  assert.ok(pkg.exports, "exports 字段在（双面包声明）");
  const dotEntry = pkg.exports["."];
  assert.ok(dotEntry !== undefined, "exports['.'] 在列——裸包名 import 的唯一放行门");
  // "." 指向既有真实入口,与 main 一致（宿主两条加载路径在此汇合;别臆造）
  const dotTarget = typeof dotEntry === "string"
    ? dotEntry
    : asString((dotEntry as Record<string, unknown>)["default"]);
  assert.ok(dotTarget, "exports['.'] 形态可解析（字符串或 {default}）");
  // exports 目标强制 "./" 前缀、main 惯用裸相对——比解析后的落盘路径,不比字串
  assert.ok(pkg.main, "main 声明在（老解析路径与裸名解析的汇合入口）");
  assert.equal(join(pkgRoot, dotTarget), join(pkgRoot, pkg.main!), "exports['.'] 与 main 指向同一入口（宿主裸名解析=main）");
  assert.ok(existsSync(join(pkgRoot, dotTarget)), `exports['.'] 目标真实在盘: ${dotTarget}`);
  // 扫描器的 package.json 解析门
  assert.equal(asString(pkg.exports["./package.json"]), "./package.json", "exports['./package.json'] 在列（locatePkgJson 解析门）");
});

test("dispose 清 blocked：会话终局清被拦缓存,不跨会话泄漏（与 handoffPending 同纪律）", async (t) => {
  const mock = await mockFor(t);
  blockRoute(mock);
  const deps = depsOver(mock, makeLogger());
  await intercept(deps, "死会话的被拦原话");
  await intercept(deps, "别家会话的原话", SID_B);
  assert.equal(deps.blocked.list(SID).length, 1);

  const { onDisposed } = await import("../src/events.ts");
  onDisposed(deps, { agent: agentOf(SID) });

  assert.equal(deps.blocked.list(SID).length, 0, "死会话条目清空（≤20 条原话全文不缓跑累积）");
  assert.equal(deps.blocked.list(SID_B).length, 1, "他 会话不受牵连");
});
