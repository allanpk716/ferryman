// 票06 · 压缩完成横幅 TDD（先红后绿）。两半面同测：
//   宿主半面（src/banner.ts 状态仓 + src/compact.ts 触发 + src/events.ts 清位 +
//   src/index.ts list wire）——触发源克隆查证（只读 dsh-research 克隆）钉点：
//     - 「压缩完成」= 执行道成功点（两道同义,2026-10-07 返工扩命令道,src/
//       compact.ts 头注「执行」节全锚点）：命令道 commands.execute('/compact')
//       解析（command-compact/src/index.ts:67 handler 直 await compactNow +
//       commands/src/index.ts:424-425 execute await settle）,或服务面
//       compactNow(agent, signal) 成功解析：契约钉点
//       packages/compaction/compaction/src/index.ts:147（append standalone
//       `compaction/start` … 「That durable marker is the compaction lock until
//       one `compaction/end` attempt」——锁标记直到一次 compaction/end 尝试）+
//       :162-166（abstract compactNow 签名）;成功回值 CompactionResult
//       （packages/compaction/compaction/src/types.ts:94）必带 endSeq（:104
//       「The seq of the appended `compaction/end` event」）——解析即
//       compaction/end 已落事件流,其 error 字段缺省=成功收尾（types.ts:69-72）。
//     - 不走 session/event 观察位的取舍：compaction 三事件 log-only、不进
//       surface（types.ts:3-4「…without entering the surface, so they are not
//       surface events」）,以事件流为源还需 compactionId→会话配平并扩插件
//       上报白名单;轮询臂成功点（compactNow 解析+/dsh/compacted 送达）与之
//       等价且零新增事件依赖（票面授权的最小触发源）。
//     - 置位门=上报送达：daemon 侧 compressed 标记未立时「直接继续」是假承诺
//       （闸门照拦）,不亮。
//   浏览器半面（client.js BlockedDock 内横幅行）——复用 conversation.composer.dock
//   槽+窄容器浮层先例（client.js 头注钉点）;通道=既有 ferrymanBlocked list 应答
//   扩信封层 banner 布尔——卡片键白名单（blocked.test.ts 钉的 cards[] 元素键）
//   不动,信封扩键在本文件「Remote list wire」用例钉住。
//   状态机：置位（压缩成功+上报送达）→ 浏览器拉到即显示（无按钮,D3 轻横幅）→
//   用户下次发消息（events.ts onPreStep 用户步,允许/拦截都算）宿主清位 →
//   浏览器下轮拉到 absence 即消失;会话终局（onDisposed）同清
//   （blocked/handoffPending 同纪律,不跨会话泄漏）。
import { test } from "node:test";
import assert from "node:assert/strict";
import { BannerStore } from "../src/banner.ts";
import {
  SessionRegistry,
  startPollLoop,
  type PollDeps,
  type PollLoopHandle,
} from "../src/compact.ts";
import {
  BlockedStore,
  makeEventDeps,
  onDisposed,
  onPreStep,
  type EventDeps,
  type LoggerLike,
} from "../src/events.ts";
import { BLOCKED_SERVICE_KEY, buildBlockedService } from "../src/index.ts";
import { startMockDaemon, waitUntil, type MockDaemon } from "./mockdaemon.ts";

const SID = "session-55555555-5555-4555-8555-555555555555";
const SID_B = "session-bbbbbbbb-2222-4222-8222-222222222222";
const CMD = { action: "compact", session_id: SID, cwd: "C:/proj" };
const BANNER_TEXT = "本会话已压缩归档（交接已存档），直接继续";

function makeLogger(): LoggerLike & { warns: string[]; errors: string[] } {
  const warns: string[] = [];
  const errors: string[] = [];
  return { warns, errors, warn: (m) => void warns.push(m), error: (m) => void errors.push(m) };
}

async function mockFor(t: { after: (fn: () => void) => void }): Promise<MockDaemon> {
  const mock = await startMockDaemon();
  t.after(() => void mock.close());
  return mock;
}

const agentOf = (sid: string) => ({ session: { header: { id: sid, cwd: "C:/proj" } } });

/** EventDeps（banner 仓由 makeEventDeps 自带;测试直接取 deps.banner 断言） */
function depsOver(mock: MockDaemon, logger: LoggerLike): EventDeps {
  return makeEventDeps({ baseURL: mock.url, token: "tok-b6", fetchImpl: fetch }, logger);
}

/** compactNow 记录替身（compact.test.ts 同款）：抓 receiver/agent/signal,可编程回话 */
function makeEngine(impl?: () => Promise<unknown>) {
  const rec: {
    calls: Array<{ receiver: unknown; agent: unknown; signal: unknown }>;
    impl: () => Promise<unknown>;
  } = { calls: [], impl: impl ?? (async () => null) };
  const engine = {
    compactNow(this: unknown, agent: unknown, signal: AbortSignal): Promise<unknown> {
      rec.calls.push({ receiver: this, agent, signal });
      return rec.impl();
    },
  };
  return { engine, rec };
}

/** 宿主 ctx 替身：inject 同步回调（cordis inject 就绪即递面的同步形态） */
function ctxWithServices(logger: LoggerLike, services: Map<string, unknown>) {
  return {
    logger,
    inject: (names: readonly string[], cb: (scoped: unknown) => void) => {
      const scoped: Record<string, unknown> = {};
      for (const n of names) scoped[n] = services.get(n);
      cb(scoped);
    },
  };
}

/** compact 指令派发开关（route 侧读旗标,测试里翻转） */
function dispatchRoute(mock: MockDaemon): { flag: { on: boolean } } {
  const flag = { on: false };
  mock.route("/dsh/poll", () => ({
    status: 200,
    json: flag.on ? { commands: [CMD] } : { commands: [] },
  }));
  return { flag };
}

/** 起循环并等立即首轮落定（compact.test.ts 同款;再让 30ms 让应答处理完）。
 *  成功上报前的落盘稳定窗（REPORT_SETTLE_MS,生产 2s）压到 5ms。 */
async function startSettled(
  t: { after: (fn: () => void) => void },
  mock: MockDaemon,
  deps: Omit<PollDeps, "ep"> & { ep?: PollDeps["ep"] },
): Promise<PollLoopHandle> {
  const handle = startPollLoop({ ep: { baseURL: mock.url, token: "tok-b6", fetchImpl: fetch }, reportSettleMs: 5, ...deps } as PollDeps);
  t.after(() => handle.stop());
  await waitUntil(() => mock.requestsFor("/dsh/poll").length >= 1);
  await new Promise((r) => setTimeout(r, 30));
  return handle;
}

// ---- ① 宿主半面：状态仓与触发源 ----

test("BannerStore 单元：置位/撤/按会话隔离/重复置位幂等", () => {
  const banner = new BannerStore();
  assert.equal(banner.has(SID), false, "初始无横幅");
  banner.set(SID);
  assert.equal(banner.has(SID), true);
  banner.set(SID);
  assert.equal(banner.has(SID), true, "重复置位幂等（Map 覆盖）");
  banner.set(SID_B);
  banner.clear(SID);
  assert.equal(banner.has(SID), false, "撤=下次拉到 absence");
  assert.equal(banner.has(SID_B), true, "他会话不受牵连");
  assert.doesNotThrow(() => banner.clear("session-unknown"), "清未知会话不炸");
});

test("触发源：compactNow 成功+上报送达 → banner 置位（ok:true 路径）", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  const agent = agentOf(SID);
  registry.touch(SID, "C:/proj", agent);
  const { engine, rec } = makeEngine();
  const banner = new BannerStore();
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, banner, ctx: ctxWithServices(logger, new Map([["compaction", engine]])) as never,
  });
  assert.equal(banner.has(SID), false, "未执行压缩不亮横幅");
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  await waitUntil(() => banner.has(SID), 1000);
  flag.on = false;
  assert.equal(rec.calls.length, 1);
  assert.equal(banner.has(SID), true, "压缩成功+上报送达=横幅亮（banner.ts 头注克隆钉点）");
});

test("触发源·命令道：execute('/compact') success+上报送达 → banner 置位（两执行道同义成功点,返工票钉点）", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", agentOf(SID));
  // 命令注册表替身：execute 解析即压缩收口（command-compact/src/index.ts:67
  // handler 直 await compactNow;commands/src/index.ts:424-425 await settle）
  const commands = {
    execute: async () => ({ commandId: "cmd-b7", result: { kind: "success", text: "Compacted 2 history items (~800 tokens)." } }),
  };
  const banner = new BannerStore();
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, banner, ctx: ctxWithServices(logger, new Map([["commands", commands]])) as never,
  });
  assert.equal(banner.has(SID), false, "未执行压缩不亮横幅");
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  await waitUntil(() => banner.has(SID), 1000);
  flag.on = false;
  const body = mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>;
  assert.equal(body["ok"], true);
  assert.equal(banner.has(SID), true, "命令道成功+上报送达=横幅亮（与 compactNow 路径同一置位门）");
});

test("上报终败不置位：/dsh/compacted 500 两败 → warn 但横幅不亮（承诺不立）", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  mock.route("/dsh/compacted", () => ({ status: 500, json: { error: "down" } }));
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", agentOf(SID));
  const { engine } = makeEngine();
  const banner = new BannerStore();
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, banner, ctx: ctxWithServices(logger, new Map([["compaction", engine]])) as never,
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => logger.warns.length >= 1);
  await new Promise((r) => setTimeout(r, 30));
  flag.on = false;
  assert.equal(mock.requestsFor("/dsh/compacted").length, 2, "静默重试一次（sendEvent 同款朴素策略）");
  assert.equal(banner.has(SID), false, "上报不达 → daemon compressed 标记未立 → 不亮");
});

test("ok:false 不置位：busy 上报送达也不亮横幅（压缩没做成不假承诺）", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", agentOf(SID));
  const busyErr = Object.assign(new Error("agent is busy"), { name: "ManualCompactionError", code: "busy" });
  const { engine } = makeEngine(async () => { throw busyErr; });
  const banner = new BannerStore();
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, banner, ctx: ctxWithServices(logger, new Map([["compaction", engine]])) as never,
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  await new Promise((r) => setTimeout(r, 30));
  flag.on = false;
  const body = mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>;
  assert.equal(body["ok"], false);
  assert.equal(body["reason"], "busy");
  assert.equal(banner.has(SID), false, "ok:false 不置位");
});

// ---- ② 宿主半面：清位（用户步/终局） ----

test("pre-step 用户步清位：横幅展示一次随下次发消息消失;循环步（step>1）不清", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/gate", () => ({ status: 200, json: { decision: "allow" } }));
  const deps = depsOver(mock, makeLogger());
  deps.banner.set(SID);
  const down = async () => ({ kind: "enter" as const, messages: [] });
  await onPreStep(deps, {
    agent: agentOf(SID),
    messages: [{ content: [{ type: "text", text: "继续" }] }],
    turn: 1, step: 1,
  }, down);
  assert.equal(deps.banner.has(SID), false, "用户步=「下次发消息」→ 撤（允许路径同样撤）");

  deps.banner.set(SID);
  await onPreStep(deps, {
    agent: agentOf(SID),
    messages: [{ content: [{ type: "text", text: "工具循环后续步" }] }],
    turn: 1, step: 2,
  }, down);
  assert.equal(deps.banner.has(SID), true, "step>1 是模型循环步,不算用户回流");
});

test("block 路径同样清位：用户已回流,横幅滞留反成假承诺", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/gate", () => ({ status: 200, json: { decision: "block", reason: "又闲置过线了" } }));
  const deps = depsOver(mock, makeLogger());
  deps.banner.set(SID);
  const down = async () => ({ kind: "enter" as const, messages: [] });
  const decision = await onPreStep(deps, {
    agent: agentOf(SID),
    messages: [{ content: [{ type: "text", text: "又来一条" }] }],
    turn: 2, step: 1,
  }, down);
  assert.equal(decision.kind, "reject");
  assert.equal(deps.banner.has(SID), false, "拦截也撤（横幅使命已随用户回流结束）");
});

test("dispose 清位：会话终局横幅清空,他会话不受牵连（blocked/handoffPending 同纪律）", async (t) => {
  const mock = await mockFor(t);
  const deps = depsOver(mock, makeLogger());
  deps.banner.set(SID);
  deps.banner.set(SID_B);
  onDisposed(deps, { agent: agentOf(SID) });
  assert.equal(deps.banner.has(SID), false, "终局清横幅");
  assert.equal(deps.banner.has(SID_B), true, "他会话不受牵连");
});

// ---- ③ 宿主半面：Remote list wire（信封层扩键,卡片白名单不动） ----

test("Remote list wire：信封扩 banner 布尔随仓状态;无 banner 仓回 false（兼容）", async () => {
  const store = new BlockedStore();
  store.record({ sessionId: SID, cwd: "C:/proj", reason: "r", prompt: "原话", time: 1, agent: agentOf(SID) });
  const banner = new BannerStore();
  const service = buildBlockedService({ store, banner });

  banner.set(SID);
  const out = await service.list(SID);
  assert.equal(out.banner, true, "置位 → list 带 banner:true（浏览器拉到即显示）");
  assert.deepEqual(
    Object.keys(out).sort(),
    ["banner", "cards", "ok", "sessionId"],
    "信封四键（票06 扩 banner;cards[] 元素键白名单不动——blocked.test.ts 白名单断言照旧）",
  );

  banner.clear(SID);
  assert.equal((await service.list(SID)).banner, false, "清位 → 下轮拉到 absence 即隐藏");
  assert.equal((await service.list(SID_B)).banner, false, "他会话不串");
  const bare = buildBlockedService({ store: new BlockedStore() });
  assert.equal((await bare.list(SID)).banner, false, "无 banner 仓（老构造面/直调测试）回 false 不炸");
});

// ---- ④ apply 端到端：触发→wire→清位全链 ----

test("apply 端到端：压缩成功 → list banner:true → 用户步清位 → banner:false", async (t) => {
  const mock = await mockFor(t);
  mock.route("/", () => ({ status: 200, json: {} }));
  mock.route("/stats", () => ({ status: 200, json: { version: "v0.5.4" } }));
  mock.route("/dsh/gate", () => ({ status: 200, json: { decision: "allow" } }));
  mock.route("/dsh/poll", () => ({ status: 200, json: { commands: [CMD] } }));
  const logger = makeLogger();
  const { engine, rec } = makeEngine();
  const provided = new Map<string, unknown>();
  const registered = new Map<string, (...args: never[]) => unknown>();
  const eagerAgent = agentOf(SID);
  const ctx = {
    logger,
    // 急切宿主替身：注册 agent/created 位时同步重放一次 created 事件——
    // 让 apply 内部 startPollLoop 的立即首轮就能看到已登记会话（否则下一拍
    // 在 30s 缺省间隔之外）。onCreated 在 askHandoff await 前同步 touch 注册表。
    on: (event: string, handler: (...args: never[]) => unknown) => {
      registered.set(event, handler);
      if (event === "agent/created") void handler({ agent: eagerAgent } as never);
    },
    provide: (name: string, value: unknown) => void provided.set(name, value),
    inject: (names: readonly string[], cb: (scoped: unknown) => void) => {
      const scoped: Record<string, unknown> = {};
      for (const n of names) scoped[n] = n === "compaction" ? engine : undefined;
      cb(scoped);
    },
  };
  const { apply } = await import("../src/index.ts");
  const disposer = apply(ctx as never, { daemonURL: mock.url, daemonToken: "tok-b6", dockURL: mock.url, fetchImpl: fetch, reportSettleMs: 5 });
  const stop = () => { if (typeof disposer === "function") disposer(); };
  t.after(stop);
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1 && rec.calls.length >= 1);
  await new Promise((r) => setTimeout(r, 30)); // 上报落地→置位的微任务间隙
  stop(); // 停表后再断言,免下一拍重入搅局
  const svc = provided.get(BLOCKED_SERVICE_KEY) as
    { list(sid: string): Promise<{ ok: boolean; banner: boolean; cards: unknown[] }> };
  assert.ok(svc, "apply 提供 ferrymanBlocked 服务");
  const shown = await svc.list(SID);
  assert.equal(shown.ok, true);
  assert.equal(shown.banner, true, "压缩成功+上报送达 → 信封 banner:true");

  await (registered.get("agent/pre-step") as (p: never, n: never) => Promise<unknown>)(
    {
      agent: agentOf(SID),
      messages: [{ content: [{ type: "text", text: "继续" }] }],
      turn: 1, step: 1,
    } as never,
    (async () => ({ kind: "enter", messages: [] })) as never,
  );
  assert.equal((await svc.list(SID)).banner, false, "用户步清位 → 横幅消失");
});

// ---- ⑤ 浏览器半面（client.js;零浏览器依赖——伪模块加载器+伪 React 驱动,
//         blocked.test.ts 先例原样复用） ----

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

// client.js 在导入求值时向 __ModuleLoader__ 注册（懒 CJS 模型）——本文件进程内
// 只求值一次,顶层装伪 loader、导入一次,各用例复用 factory 按需换 react 替身。
interface FakeRegistration { id: string; factory: (require: (spec: string) => unknown) => Record<string, unknown> }
const fakeRegistrations: FakeRegistration[] = [];
(globalThis as Record<string, unknown>)["__ModuleLoader__"] = {
  load: (reg: FakeRegistration) => void fakeRegistrations.push(reg),
};
await import("../client.js");

function loadClientModule(reactStub: unknown): Record<string, unknown> {
  assert.equal(fakeRegistrations.length, 1, "client.js 恰一次注册（懒 CJS 模型,每测试进程一次）");
  const reg = fakeRegistrations[0]!;
  assert.equal(reg.id, "ferryman-dsh", "注册 id=包名（与 roster 行 id 对齐）");
  return reg.factory((spec: string) => {
    if (spec === "react") return reactStub;
    throw new Error(`fake require: unexpected specifier ${spec}`);
  });
}

function installDock(react: unknown): Array<{ options: Record<string, unknown>; component: (props: unknown) => unknown }> {
  const registered: Array<{ options: Record<string, unknown>; component: (props: unknown) => unknown }> = [];
  const slots = {
    inject: (_s: string, f: () => unknown) => { f(); return () => {}; },
    register: (options: Record<string, unknown>, component: (props: unknown) => unknown) => {
      registered.push({ options, component });
      return () => {};
    },
  };
  const mod = loadClientModule(react) as unknown as { apply: (ctx: unknown) => void };
  mod.apply({ slots });
  assert.equal(registered.length, 1, "dock 恰一注册（横幅复用同一 dock 槽,不另开槽位）");
  return registered;
}

/**
 * 替身渲染（blocked.test.ts 同款）：首渲（挂载态）→ 显式跑 useEffect（替身不自动
 * 跑;effect 会起真 setInterval,收尾必须跑 cleanup,否则进程被轮询吊死）→ 冲
 * 微任务（首拉 list 落定）→ 复渲取带数据的树。
 */
async function renderMounted(
  Component: (props: unknown) => unknown,
  hooks: { effects: Array<() => (void | (() => void))>; reset: () => void },
  face: unknown,
): Promise<unknown> {
  hooks.reset();
  Component({ ferrymanBlocked: face }); // 首渲：挂载态,记录 effect
  const cleanups = hooks.effects.splice(0).map((effect) => effect());
  await new Promise((r) => setTimeout(r, 0));
  hooks.reset();
  const tree = Component({ ferrymanBlocked: face });
  for (const cleanup of cleanups) if (typeof cleanup === "function") cleanup();
  return tree;
}

const bannerOf = (tree: unknown): Record<string, unknown>[] =>
  findAll(tree, (n) => (n["props"] as Record<string, unknown>)["data-ferryman-banner"] === "compacted");

const CARD = { id: "b1", reason: "r", prompt: "再算一遍方差", time: 1, done: false, doneAction: null };

test("横幅渲染：banner 行置顶（✓ 图标+票面文案+data 标记+role status）,无按钮;与卡片同屏先横幅后卡片", async () => {
  const { react, hooks } = makeFakeReact();
  const registered = installDock(react);
  const Component = registered[0]!.component;
  const face = { sessionId: SID, list: async () => ({ ok: true, cards: [CARD], banner: true }) };
  const tree = (await renderMounted(Component, hooks, face)) as Record<string, unknown>;
  const all = textsOf(tree).join("");
  assert.ok(all.includes(BANNER_TEXT), "文案逐字（票面钉死）");
  const banners = bannerOf(tree);
  assert.equal(banners.length, 1, "恰一横幅节点");
  assert.equal((banners[0]!["props"] as Record<string, unknown>)["role"], "status", "状态语义（非操作件,无障碍面）");
  assert.equal(findAll(banners[0], (n) => n["type"] === "button").length, 0, "横幅行内无按钮（D3 轻横幅定案;卡片自身按钮不在此断言域）");
  const kids = tree["children"] as Record<string, unknown>[];
  assert.equal((kids[0]!["props"] as Record<string, unknown>)["data-ferryman-banner"], "compacted", "横幅在卡片之前（先安心再看事）");
  assert.equal((kids[1]!["props"] as Record<string, unknown>)["data-ferryman-blocked"], "b1", "卡片照常渲染,互不挤占");
});

test("零卡片仅横幅：dock 仍渲染（不回落整体 null）", async () => {
  const { react, hooks } = makeFakeReact();
  const Component = installDock(react)[0]!.component;
  const face = { sessionId: SID, list: async () => ({ ok: true, cards: [], banner: true }) };
  const tree = await renderMounted(Component, hooks, face);
  assert.ok(tree !== null && typeof tree === "object", "横幅在场时 dock 不整体 null（既有早退只看卡片,已扩）");
  assert.equal(bannerOf(tree).length, 1);
  assert.ok(textsOf(tree).join("").includes(BANNER_TEXT));
  assert.equal(findAll(tree, (n) => n["type"] === "button").length, 0, "仅横幅时全树无按钮（D3 轻横幅定案）");
});

test("横幅缺席：banner:false 无横幅节点;零卡且无横幅 dock 整体 null（既有行为不变）", async () => {
  const { react, hooks } = makeFakeReact();
  const Component = installDock(react)[0]!.component;
  const withCard = { sessionId: SID, list: async () => ({ ok: true, cards: [CARD], banner: false }) };
  const tree = await renderMounted(Component, hooks, withCard);
  assert.equal(bannerOf(tree).length, 0, "有卡无横幅:无横幅节点");
  assert.ok(textsOf(tree).join("").includes("这条消息被 Ferryman 闸门拦下"), "卡面照旧");
  const empty = { sessionId: SID, list: async () => ({ ok: true, cards: [], banner: false }) };
  assert.equal(await renderMounted(Component, hooks, empty), null, "无卡无横幅=不渲染（既有早退行为保持）");
});

test("显示/消失状态机：banner true→宿主清位→false 复渲染横幅消失（单次展示语义）", async () => {
  const { react, hooks } = makeFakeReact();
  const Component = installDock(react)[0]!.component;
  let payload: { ok: boolean; cards: unknown[]; banner: boolean } = { ok: true, cards: [], banner: true };
  const face = { sessionId: SID, list: async () => payload };
  const shown = textsOf(await renderMounted(Component, hooks, face)).join("");
  assert.ok(shown.includes(BANNER_TEXT), "置位时显示");

  payload = { ok: true, cards: [], banner: false }; // 用户下次发消息,宿主 pre-step 清位
  const gone = textsOf(await renderMounted(Component, hooks, face)).join("");
  assert.ok(!gone.includes(BANNER_TEXT), "清位后消失（浏览器不自行续命,以宿主位为准）");
});
