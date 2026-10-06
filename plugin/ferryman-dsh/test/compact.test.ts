// 票05 · DSH 热缓存压缩执行臂 TDD（先红后绿）。spec「架构与契约」钉点
//（.scratch/dsh-hot-compaction/spec.md,改契约=全部受影响票返工）：
//   - 轮询：POST /dsh/poll 体 {agent:"dsh", sessions:[{sid, idle_s}]};应答
//     {commands:[{action:"compact", session_id, cwd}]} + poll_hint_s 建议
//     （下限 10s）;轮询失败静默（下轮再试）。
//   - 双重复查（N1 插件侧）：①agent 仍空闲（agent/status 忙位）∧ ②闲置 < TTL
//     热窗（缺省 1800s,应答 ttl_s 可覆盖）。任一不过 → /dsh/compacted
//     {ok:false, reason:"expired-or-busy"},不执行。
//   - 执行：ctx.compaction.compactNow(agent, signal)（克隆钉点
//     packages/compaction/compaction/src/index.ts:162-166 签名带 AbortSignal;
//     忙=ManualCompactionError code 'busy',:35-64）;宿主服务方法永远带 receiver
//     整体调用（10-06 真机事故铁律,bind 后调）。
//   - 上报：{session_id, ok, reason?, prefix_tokens?, source?};前缀=投影尽力读
//     （token-meter projection.ts ContextPressureProjection.projectedTokens/
//     pressureTokens,不可得省略键）。
// 会话注册表=五事件位维护（created/pre-step/session/event/status/disposed）;
// interval 随 dispose 清理（cordis 约定:apply 返回函数=卸载 disposer）。
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  COMPACT_SOURCE,
  DEFAULT_POLL_INTERVAL_MS,
  SessionRegistry,
  startPollLoop,
  type PollDeps,
  type PollLoopHandle,
} from "../src/compact.ts";
import {
  BlockedStore,
  makeEventDeps,
  onCreated,
  onDisposed,
  onPreStep,
  onSessionEvent,
  onStatus,
  type EventDeps,
  type LoggerLike,
} from "../src/events.ts";
import { BannerStore } from "../src/banner.ts";
import { apply, inject as pluginInject, type PluginContext } from "../src/index.ts";
import {
  startMockDaemon,
  closedDaemonURL,
  waitUntil,
  type MockDaemon,
  type MockResponse,
} from "./mockdaemon.ts";

const SID = "session-55555555-5555-4555-8555-555555555555";
const CMD = { action: "compact", session_id: SID, cwd: "C:/proj" };

function makeLogger(): LoggerLike & { warns: string[]; errors: string[] } {
  const warns: string[] = [];
  const errors: string[] = [];
  return { warns, errors, warn: (m) => void warns.push(m), error: (m) => void errors.push(m) };
}

function epOf(mock: MockDaemon): PollDeps["ep"] {
  return { baseURL: mock.url, token: "tok-c5", fetchImpl: fetch };
}

async function mockFor(t: { after: (fn: () => void) => void }): Promise<MockDaemon> {
  const mock = await startMockDaemon();
  t.after(() => void mock.close());
  return mock;
}

/** 手工 deps（registry/now 可注入;EventDeps 为纯数据面,测试直构造合法） */
function makeDeps(mock: MockDaemon, logger: LoggerLike, registry: SessionRegistry, now: () => number = Date.now): EventDeps {
  return { ep: epOf(mock), logger, titles: new Map(), handoffPending: new Map(), blocked: new BlockedStore(), now, registry, banner: new BannerStore() };
}

/** compactNow 记录替身：抓 receiver/agent/signal,可编程回话 */
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
function ctxWithServices(logger: LoggerLike, services: Map<string, unknown>): PluginContext {
  return {
    logger,
    inject: (names: readonly string[], cb: (scoped: PluginContext) => void) => {
      const scoped: Record<string, unknown> = {};
      for (const n of names) scoped[n] = services.get(n);
      cb(scoped as never);
    },
  };
}

/** compact 指令派发开关（route 侧读旗标,测试里翻转） */
function dispatchRoute(mock: MockDaemon, extra: Record<string, unknown> = {}): { flag: { on: boolean } } {
  const flag = { on: false };
  mock.route("/dsh/poll", () => ({
    status: 200,
    json: flag.on ? { commands: [CMD], ...extra } : { commands: [] },
  }));
  return { flag };
}

/** 起循环并等立即首轮落定（请求落地≠轮次落定:再让 30ms 让应答处理完,
 *  否则手动 tick 撞单飞 guard 会被静默吞掉）。成功上报前的落盘稳定窗
 *  （REPORT_SETTLE_MS,生产 2s）在测试压到 5ms——除专测竞态闭窗的用例。 */
async function startSettled(t: { after: (fn: () => void) => void }, mock: MockDaemon, deps: Omit<PollDeps, "ep"> & { ep?: PollDeps["ep"] }): Promise<PollLoopHandle> {
  const handle = startPollLoop({ ep: epOf(mock), reportSettleMs: 5, ...deps } as PollDeps);
  t.after(() => handle.stop());
  await waitUntil(() => mock.requestsFor("/dsh/poll").length >= 1);
  await new Promise((r) => setTimeout(r, 30));
  return handle;
}

// ---- ⓪ 静态契约 ----

test("inject 数组不变：零宿主服务依赖声明保持空数组（验收红线）", () => {
  assert.deepEqual(pluginInject, []);
});

// ---- ① 轮询循环起停 ----

test("轮询循环起停（注入定时器）：默认 30000ms 挂表;stop 清表且幂等", () => {
  let fired: (() => void) | undefined;
  let armedMs = -1;
  const handle = startPollLoop({
    ep: { baseURL: "http://127.0.0.1:9", token: "t", fetchImpl: fetch },
    logger: makeLogger(),
    registry: new SessionRegistry(),
    setImpl: (fn, ms) => { fired = fn; armedMs = ms; return ms; },
    clearImpl: () => { fired = undefined; },
  });
  assert.equal(armedMs, DEFAULT_POLL_INTERVAL_MS, "默认间隔 30s（spec 钉点）");
  assert.equal(typeof fired, "function", "interval 已挂表");
  handle.stop();
  assert.equal(fired, undefined, "stop 清 interval");
  assert.doesNotThrow(() => handle.stop(), "stop 幂等");
});

test("轮询循环真节律：interval 到点真发 /dsh/poll;stop 后不再新增（≤1 条在途余量）", async (t) => {
  const mock = await mockFor(t);
  const handle = await startSettled(t, mock, { logger: makeLogger(), registry: new SessionRegistry(), intervalMs: 15, minIntervalMs: 15 });
  await waitUntil(() => mock.requestsFor("/dsh/poll").length >= 3, 3000);
  handle.stop();
  const n = mock.requestsFor("/dsh/poll").length;
  await new Promise((r) => setTimeout(r, 120));
  const growth = mock.requestsFor("/dsh/poll").length - n;
  assert.ok(growth <= 1, `停表后 120ms（≈8 拍）至多放行 1 条在途,实增 ${growth}`);
});

// ---- ② poll 请求形状 ----

test("poll 请求形状：{agent:'dsh', sessions:[{sid, idle_s}]} + Bearer 鉴权", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/poll", () => ({ status: 200, json: { commands: [] } }));
  let clock = 1_000_000_000_000;
  const registry = new SessionRegistry(() => clock);
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } } };
  registry.touch(SID, "C:/proj", agent);
  const handle = await startSettled(t, mock, { logger: makeLogger(), registry, intervalMs: 30000 });
  clock += 7_500; // 闲置 7.5s → floor 7
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/poll").length >= 2);
  const req = mock.requestsFor("/dsh/poll").at(-1)!; // 首轮是挂载即发（idle 0）,取手动轮
  assert.equal(req.path, "/dsh/poll");
  assert.equal(req.auth, "Bearer tok-c5");
  assert.deepEqual(req.body, { agent: "dsh", sessions: [{ sid: SID, idle_s: 7 }] });
});

// ---- ③ poll_hint_s 调整与 10s 下限 ----

test("poll_hint_s：应答建议改写间隔;0.5s 建议顶到缺省 10s 下限", async (t) => {
  const mock = await mockFor(t);
  let hintS: number | undefined = 60;
  mock.route("/dsh/poll", () => ({ status: 200, json: { commands: [], ...(hintS === undefined ? {} : { poll_hint_s: hintS }) } }));
  const handle = await startSettled(t, mock, { logger: makeLogger(), registry: new SessionRegistry(), intervalMs: 100000 });
  assert.equal(handle.intervalMs(), 60000, "首轮应答（挂载即发）已带 60s 建议并生效");
  await handle.tick();
  assert.equal(handle.intervalMs(), 60000, "建议 60s 生效");
  hintS = 0.5;
  await handle.tick();
  assert.equal(handle.intervalMs(), 10000, "0.5s 建议被 10s 下限托住（spec 钉点）");
  hintS = undefined;
  await handle.tick();
  assert.equal(handle.intervalMs(), 10000, "无建议时保持现值");
});

test("poll_hint_s 下限可注入（沙箱/E2E 压秒级用）:0.005s 建议 × min 25ms → 25ms", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/poll", () => ({ status: 200, json: { commands: [], poll_hint_s: 0.005 } }));
  const handle = await startSettled(t, mock, { logger: makeLogger(), registry: new SessionRegistry(), intervalMs: 100000, minIntervalMs: 25 });
  await handle.tick();
  assert.equal(handle.intervalMs(), 25);
});

// ---- ④ 双重复查（N1 插件侧） ----

test("双重复查·忙：agent running → 上报 expired-or-busy,不碰 compaction", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  let clock = 1_000_000_000_000;
  const registry = new SessionRegistry(() => clock);
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } } };
  registry.touch(SID, "C:/proj", agent);
  registry.setStatus(SID, "running"); // 用户回来了:agent 忙
  const { engine, rec } = makeEngine();
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ttlS: 1800, ctx: ctxWithServices(logger, new Map([["compaction", engine]])),
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  flag.on = false;
  const body = mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>;
  assert.equal(body["session_id"], SID);
  assert.equal(body["ok"], false);
  assert.equal(body["reason"], "expired-or-busy");
  assert.equal(body["source"], COMPACT_SOURCE);
  assert.equal(rec.calls.length, 0, "忙会话不执行压缩");
  assert.equal(logger.errors.length, 0);
});

test("双重复查·冷窗与未知会话：闲置 ≥ TTL / 注册表无此 sid → expired-or-busy", async (t) => {
  const mock = await mockFor(t);
  let cmdSid = SID;
  mock.route("/dsh/poll", () => ({ status: 200, json: { commands: [{ action: "compact", session_id: cmdSid, cwd: "C:/proj" }] } }));
  let clock = 1_000_000_000_000;
  const registry = new SessionRegistry(() => clock);
  registry.touch(SID, "C:/proj", { session: { header: { id: SID, cwd: "C:/proj" } } });
  clock += 1800_000; // 恰满 1800s:热窗为 < TTL,等于即出窗
  const logger = makeLogger();
  const handle = await startSettled(t, mock, { logger, registry });
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  const body = mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>;
  assert.equal(body["ok"], false);
  assert.equal(body["reason"], "expired-or-busy", "闲置恰满 TTL=出热窗");

  cmdSid = "session-does-not-exist";
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 2);
  const body2 = mock.requestsFor("/dsh/compacted")[1]!.body as Record<string, unknown>;
  assert.equal(body2["ok"], false);
  assert.equal(body2["reason"], "expired-or-busy", "宿主已不持有该会话=复查不过");
});

test("ttl_s 应答覆盖热窗：60s 窗杀超窗会话;3600s 窗放行同会话执行", async (t) => {
  const mock = await mockFor(t);
  let ttl = 60;
  const flag = { on: false };
  mock.route("/dsh/poll", () => ({
    status: 200,
    json: flag.on ? { commands: [CMD], ttl_s: ttl } : { commands: [] },
  }));
  let clock = 1_000_000_000_000;
  const registry = new SessionRegistry(() => clock);
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } } };
  registry.touch(SID, "C:/proj", agent);
  clock += 120_000; // 闲置 120s
  const { engine, rec } = makeEngine();
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ttlS: 1800, ctx: ctxWithServices(logger, new Map([["compaction", engine]])),
  });
  flag.on = true;
  await handle.tick(); // ttl_s=60 < 120 → 出窗
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  assert.equal((mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>)["reason"], "expired-or-busy");
  assert.equal(rec.calls.length, 0);

  ttl = 3600;
  await handle.tick(); // ttl_s=3600 > 120 → 热窗内,执行
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 2 && rec.calls.length >= 1);
  assert.equal(rec.calls.length, 1, "热窗覆盖后放行执行");
  assert.equal((mock.requestsFor("/dsh/compacted")[1]!.body as Record<string, unknown>)["ok"], true);
  flag.on = false;
});

// ---- ⑤ 执行臂：receiver / busy / 前缀 / 上报 ----

test("成功路径：compactNow 带 receiver+agent+AbortSignal;上报 {ok:true, prefix_tokens, source}", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  let clock = 1_000_000_000_000;
  const registry = new SessionRegistry(() => clock);
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } } };
  registry.touch(SID, "C:/proj", agent);
  const { engine, rec } = makeEngine();
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["compaction", engine]])),
    readPrefixTokens: () => 4321,
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  flag.on = false;
  assert.equal(rec.calls.length, 1);
  const call = rec.calls[0]!;
  assert.equal(call.receiver, engine, "receiver=compaction 服务本体（10-06 真机事故铁律:摘 receiver 裸调即崩）");
  assert.equal(call.agent, agent, "compactNow 收宿主 agent 活引用");
  assert.ok(call.signal instanceof AbortSignal, "compactNow(agent, signal) 克隆签名第二参（index.ts:162-166）");
  assert.deepEqual(mock.requestsFor("/dsh/compacted")[0]!.body, {
    session_id: SID, ok: true, prefix_tokens: 4321, source: COMPACT_SOURCE,
  });
});

test("前缀缺省读：snapshot wire 视图取 contextPressure.projectedTokens;抛错/缺数省键", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  const session = { header: { id: SID, cwd: "C:/proj" } };
  registry.touch(SID, "C:/proj", { session });
  const snapCalls: Array<{ session: unknown; keys: unknown }> = [];
  let snapImpl: (session: unknown, keys: readonly string[]) => unknown = (s, keys) => {
    snapCalls.push({ session: s, keys });
    return { asOfSeq: 9, values: { contextPressure: { projectedTokens: 777 } } };
  };
  const projections = {
    snapshot: (session: unknown, keys?: readonly string[]): unknown => snapImpl(session, keys ?? []),
  };
  const logger = makeLogger();
  const { engine } = makeEngine();
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["compaction", engine], ["sessionProjections", projections]])),
  });

  flag.on = true;
  await handle.tick(); // 投影给 projectedTokens
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  assert.deepEqual(snapCalls[0], { session, keys: ["contextPressure"] },
    "读 snapshot wire 视图 contextPressure（stateOf 原始态无 projectedTokens——2026-10-07 E2E 实锚返工钉点）");
  assert.equal((mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>)["prefix_tokens"], 777);

  snapImpl = () => { throw new Error("projection not ready"); };
  await handle.tick(); // 投影抛错 → 省键,ok 照报
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 2);
  const body2 = mock.requestsFor("/dsh/compacted")[1]!.body as Record<string, unknown>;
  assert.equal(body2["ok"], true);
  assert.equal("prefix_tokens" in body2, false, "不可得省略键（spec 钉点）");

  snapImpl = () => ({ asOfSeq: 9, values: { contextPressure: { surfaceTokens: 5 } } }); // 无 projected/pressure 数 → 省键
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 3);
  assert.equal("prefix_tokens" in (mock.requestsFor("/dsh/compacted")[2]!.body as Record<string, unknown>), false);
  flag.on = false;
});

test("busy catch：compactNow 抛 ManualCompactionError(busy) → 上报 {ok:false, reason:'busy'}", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", { session: { header: { id: SID, cwd: "C:/proj" } } });
  const busyErr = Object.assign(new Error("agent is busy"), { name: "ManualCompactionError", code: "busy" });
  const { engine, rec } = makeEngine(async () => { throw busyErr; });
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["compaction", engine]])),
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  flag.on = false;
  assert.equal(rec.calls.length, 1);
  const body = mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>;
  assert.equal(body["ok"], false);
  assert.equal(body["reason"], "busy");
  assert.equal(logger.errors.length, 0, "busy 是预期态:不走 error 通道");
});

test("非 busy 失败：抛普通错误 → 上报 {ok:false, reason:'error'} + warn 一行", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", { session: { header: { id: SID, cwd: "C:/proj" } } });
  const { engine } = makeEngine(async () => { throw new Error("summary backend boom"); });
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["compaction", engine]])),
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => logger.warns.length >= 1 && mock.requestsFor("/dsh/compacted").length >= 1);
  flag.on = false;
  const body = mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>;
  assert.equal(body["ok"], false);
  assert.equal(body["reason"], "error");
  assert.ok(logger.warns[0]!.includes("summary backend boom"), "失败详情进 logger（用户可见通道）");
});

test("无压缩通道：commands 与 compaction 面皆缺 → 上报 {ok:false, reason:'no-compaction-channel'} + warn 指因,不抛", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", { session: { header: { id: SID, cwd: "C:/proj" } } });
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map()), // 空 service 面（两道皆缺）
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1 && logger.warns.length >= 1);
  flag.on = false;
  const body = mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>;
  assert.equal(body["ok"], false);
  assert.equal(body["reason"], "no-compaction-channel", "通道缺席如实上报（返工票钉点,非笼统 error）");
  assert.ok(logger.warns.some((w) => w.includes("压缩通道")), "warn 指因（两服务面皆缺）");
});

// ---- ⑤b 命令道（首选执行道,2026-10-07 返工;克隆钉点见 src/compact.ts 头注） ----

/** 命令注册表替身：抓 receiver/agent/line/attachments/signal,可编程回话 */
function makeCommands(impl?: (agent: unknown, line: string) => Promise<unknown>) {
  const rec: {
    calls: Array<{ receiver: unknown; agent: unknown; line: string; attachments: unknown; signal: unknown }>;
    impl: (agent: unknown, line: string) => Promise<unknown>;
  } = {
    calls: [],
    // 缺省回话=真 CommandExecution 成功形（commands/src/types.ts:49-54 + command-compact :69-73）
    impl: impl ?? (async () => ({ commandId: "cmd-x1", result: { kind: "success", text: "Compacted 3 history items (~1234 tokens).", sourceEventSeq: 42 } })),
  };
  const commands = {
    execute(this: unknown, agent: unknown, line: string, attachments: readonly unknown[], signal: AbortSignal): Promise<unknown> {
      rec.calls.push({ receiver: this, agent, line, attachments, signal });
      return rec.impl(agent, line);
    },
  };
  return { commands, rec };
}

test("命令道成功：execute 带 receiver+agent+'/compact'+空附件+AbortSignal;上报 ok:true+prefix;横幅置位", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } } };
  registry.touch(SID, "C:/proj", agent);
  const { commands, rec } = makeCommands();
  const { engine: engineUnused, rec: engineRec } = makeEngine();
  const banner = new BannerStore();
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, banner,
    ctx: ctxWithServices(logger, new Map([["commands", commands], ["compaction", engineUnused]])),
    readPrefixTokens: () => 2345,
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  flag.on = false;
  assert.equal(rec.calls.length, 1);
  const call = rec.calls[0]!;
  assert.equal(call.receiver, commands, "receiver=commands 服务本体（宿主方法带 receiver 铁律）");
  assert.equal(call.agent, agent, "execute 收注册表存的 agent 活引用（view(agent) 按对象身份解析作用域层）");
  assert.equal(call.line, "/compact", "固定命令行 /compact");
  assert.deepEqual(call.attachments, [], "空附件（commands/src/index.ts:390 length>0 才走收件准入）");
  assert.ok(call.signal instanceof AbortSignal, "execute(agent, line, attachments, signal) 克隆签名第四参");
  assert.deepEqual(mock.requestsFor("/dsh/compacted")[0]!.body, {
    session_id: SID, ok: true, prefix_tokens: 2345, source: COMPACT_SOURCE,
  });
  assert.equal(banner.has(SID), true, "命令道成功+上报送达=横幅置位（票06 两道同义成功点）");
  assert.equal(engineRec.calls.length, 0, "命令道终局不回落服务面");
});

test("命令道 busy：error 文案命中 busy 分支（active compaction/not idle）→ {ok:false, reason:'busy'},零 warn", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", { session: { header: { id: SID, cwd: "C:/proj" } } });
  // command-compact/src/index.ts:26-31 busy 分支文案逐字
  const { commands } = makeCommands(async () => ({
    commandId: "cmd-x2",
    result: { kind: "error", text: "Compaction is unavailable because this process has an active compaction, or the agent is not idle." },
  }));
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["commands", commands]])),
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  flag.on = false;
  const body = mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>;
  assert.equal(body["ok"], false);
  assert.equal(body["reason"], "busy");
  assert.equal(logger.errors.length, 0);
  assert.equal(logger.warns.length, 0, "busy 是预期态:不走 warn/error 通道（服务面同款语义）");
});

test("命令道失败：error 文案（summary 失败）→ {ok:false, reason:'error'} + warn 带文案", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", { session: { header: { id: SID, cwd: "C:/proj" } } });
  const { commands } = makeCommands(async () => ({
    commandId: "cmd-x3",
    result: { kind: "error", text: "Compaction could not produce a useful summary. The attempt is recorded in the session log." },
  }));
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["commands", commands]])),
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1 && logger.warns.length >= 1);
  flag.on = false;
  const body = mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>;
  assert.equal(body["ok"], false);
  assert.equal(body["reason"], "error");
  assert.ok(logger.warns[0]!.includes("could not produce a useful summary"), "命令结果文案进 logger（用户可见通道）");
});

test("命令道缺 /compact（execute 回 undefined）或面无 execute 法 → 回落 compaction 服务面直调", async (t) => {
  const mock = await mockFor(t);
  const registry = new SessionRegistry();
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } } };
  registry.touch(SID, "C:/proj", agent);
  const { commands } = makeCommands(async () => undefined); // 命令未解析（commands/src/index.ts:367-370）
  const { engine, rec } = makeEngine();
  const logger = makeLogger();
  const flag = { on: false };
  mock.route("/dsh/poll", () => ({
    status: 200,
    json: flag.on ? { commands: [CMD] } : { commands: [] },
  }));
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["commands", commands], ["compaction", engine]])),
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1 && rec.calls.length >= 1);
  flag.on = false;
  assert.equal(rec.calls.length, 1, "undefined=该 agent 视图无 compact 命令 → 服务面接手");
  assert.equal((mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>)["ok"], true);

  // 面 2：commands 面在但缺 execute 法（鸭子面残缺）——同回落服务面
  const handle2 = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["commands", { registerOnly: true }], ["compaction", engine]])),
  });
  flag.on = true;
  await handle2.tick();
  await waitUntil(() => rec.calls.length >= 2);
  flag.on = false;
  assert.equal(rec.calls.length, 2, "无 execute 法的 commands 面=通道残缺 → 服务面接手");
});

test("命令道优先：两面皆在 → 只走 execute,compactNow 不被调", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", { session: { header: { id: SID, cwd: "C:/proj" } } });
  const { commands, rec } = makeCommands();
  const { engine, rec: engineRec } = makeEngine();
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["commands", commands], ["compaction", engine]])),
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  flag.on = false;
  assert.equal(rec.calls.length, 1, "命令道先行");
  assert.equal(engineRec.calls.length, 0, "服务面不被触碰（命令道已给出真结果）");
});

test("落盘稳定窗：成功上报晚于压缩收口 ≥ settle 窗（标记自杀竞态闭窗,REPORT_SETTLE_MS 头注）", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", { session: { header: { id: SID, cwd: "C:/proj" } } });
  const { commands, rec } = makeCommands();
  const logger = makeLogger();
  // 真生产缺省 2000ms 在此压 120ms——量的是「压缩收口→上报」间隔 ≥ 窗,不是绝对时延
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["commands", commands]])),
    reportSettleMs: 120,
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => rec.calls.length >= 1, 2000);
  const compactDoneAt = Date.now();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1, 4000);
  flag.on = false;
  const gap = Date.now() - compactDoneAt;
  assert.ok(gap >= 100, `上报须晚于压缩收口 ≥ settle 窗（实测 ${gap}ms）——dsh 批落盘（200ms 批窗）的
    mtime 晚于上报会令 daemon 守望 Touch 顶新 LastWrite、按 LastWrite>标记TS 判死自家标记（E2E 实锚
    2026-10-07:事件 42.598→上报 42.605→drain mtime 42.798→标记自杀,gate 放行链断）`);
});

test("命令道抛错（handler 非预期异常被 execute rethrow）→ {ok:false, reason:'error'} + warn", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", { session: { header: { id: SID, cwd: "C:/proj" } } });
  const { commands } = makeCommands(async () => { throw new Error("gateway boom"); });
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["commands", commands]])),
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1 && logger.warns.length >= 1);
  flag.on = false;
  const body = mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>;
  assert.equal(body["ok"], false);
  assert.equal(body["reason"], "error");
  assert.ok(logger.warns[0]!.includes("gateway boom"));
});

test("在途防双跑：同会话两指令一轮 → compactNow 恰一次、上报恰一次", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/poll", () => ({ status: 200, json: { commands: [CMD, { ...CMD }] } }));
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", { session: { header: { id: SID, cwd: "C:/proj" } } });
  let release!: () => void;
  const gate = new Promise<null>((r) => { release = () => r(null); });
  const { engine, rec } = makeEngine(() => gate);
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["compaction", engine]])),
  });
  await handle.tick();
  assert.equal(rec.calls.length, 1, "第二条指令撞在途 guard 静默跳过");
  release();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  await new Promise((r) => setTimeout(r, 40));
  assert.equal(mock.requestsFor("/dsh/compacted").length, 1, "恰一次上报（上一臂自会报）");
});

// ---- ⑥ 失败静默与上报重试 ----

test("轮询失败静默：daemon 不可达 → tick 不抛、零 warn（下轮再试）", async (t) => {
  const { url } = await closedDaemonURL();
  const logger = makeLogger();
  const handle = startPollLoop({
    ep: { baseURL: url, token: "t", fetchImpl: fetch, timeoutMs: 500 },
    logger,
    registry: new SessionRegistry(),
  });
  t.after(() => handle.stop());
  await handle.tick();
  assert.equal(logger.warns.length, 0);
  assert.equal(logger.errors.length, 0);
});

test("上报失败：静默重试一次,终败 warn 一行;成功路径零 warn", async (t) => {
  const mock = await mockFor(t);
  const { flag } = dispatchRoute(mock);
  const registry = new SessionRegistry();
  registry.touch(SID, "C:/proj", { session: { header: { id: SID, cwd: "C:/proj" } } });
  let fail = true;
  mock.route("/dsh/compacted", () => (fail ? { status: 500, json: { error: "down" } } : { status: 200, json: { ok: true } }));
  const { engine } = makeEngine();
  const logger = makeLogger();
  const handle = await startSettled(t, mock, {
    logger, registry, ctx: ctxWithServices(logger, new Map([["compaction", engine]])),
  });
  flag.on = true;
  await handle.tick();
  await waitUntil(() => logger.warns.length >= 1);
  flag.on = false;
  fail = false;
  assert.equal(mock.requestsFor("/dsh/compacted").length, 2, "恰一次静默重试（sendEvent 同款朴素策略）");
  assert.ok(logger.warns[0]!.includes("上报失败"));
});

// ---- ⑦ 会话注册表（五事件位维护） ----

test("会话注册表五事件位：created 登记 → session/event 刷闲置钟 → status 置忙 → dispose 移除", async (t) => {
  const mock = await mockFor(t);
  let clock = 0;
  const registry = new SessionRegistry(() => clock);
  const logger = makeLogger();
  const deps = makeDeps(mock, logger, registry, () => clock);
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } } };

  await onCreated(deps, { agent });
  let list = registry.list();
  assert.equal(list.length, 1);
  assert.equal(list[0]!.sid, SID);
  assert.equal(list[0]!.idleS, 0);
  assert.equal(list[0]!.busy, false, "created 时空闲");
  assert.equal(list[0]!.agent, agent, "agent 活引用留宿主侧（供 compactNow）");

  clock = 4_000;
  assert.equal(registry.list()[0]!.idleS, 4, "created 起点起算闲置钟");
  onSessionEvent(deps, { header: { id: SID } }, { type: "turn/start", time: 1, data: {} }); // 无 cwd 头:不覆盖 cwd
  list = registry.list();
  assert.equal(list[0]!.idleS, 0, "session/event 触碰=活动,闲置钟归零");
  assert.equal(list[0]!.cwd, "C:/proj", "空 cwd 不覆盖既有 cwd");
  assert.equal(list[0]!.agent, agent, "session/event 无 agent 引用,不得抹掉既有引用");

  onStatus(deps, { agent, status: "running" });
  assert.equal(registry.list()[0]!.busy, true, "status running → 忙位");
  onStatus(deps, { agent, status: "idle" });
  assert.equal(registry.list()[0]!.busy, false, "status idle → 空闲位");

  // pre-step 用户步也是活动（闲置钟归零）;顺带证明 handler 不因注册表改判
  clock = 9_000;
  const down = async () => ({ kind: "enter" as const, messages: [] });
  await onPreStep(deps, {
    agent,
    messages: [{ content: [{ type: "text", text: "hi" }] }],
    turn: 1, step: 1,
  }, down);
  assert.equal(registry.list()[0]!.idleS, 0, "pre-step 触碰归零闲置钟");

  // 未知 sid：session/event 兜底登记（agent 缺,复查必不过）;status/disposed 不炸
  onSessionEvent(deps, { header: { id: "session-ghost" } }, { type: "turn/start", time: 1, data: {} });
  assert.equal(registry.get("session-ghost")?.agent, undefined);
  assert.doesNotThrow(() => onStatus(deps, { agent: { session: { header: { id: "session-ghost" } } }, status: "running" }));
  assert.doesNotThrow(() => registry.setStatus("session-unknown-x", "running"));

  onDisposed(deps, { agent });
  assert.equal(registry.get(SID), undefined, "dispose 移除出注册表");
  assert.equal(registry.get("session-ghost")?.sid, "session-ghost", "他会话不受牵连");
});

// ---- ⑧ apply 接线 ----

test("apply 接线：起轮询循环（挂载即首轮 /dsh/poll）;返回卸载 disposer（幂等）", async (t) => {
  const mock = await mockFor(t);
  mock.route("/", () => ({ status: 200, json: {} }));
  mock.route("/stats", () => ({ status: 200, json: { version: "v0.5.4" } }));
  let release!: (v: MockResponse) => void;
  const gate = new Promise<MockResponse>((r) => (release = r));
  mock.route("/dsh/poll", () => gate); // 首轮应答挂起,隔离时序竞争
  const logger = makeLogger();
  const registered = new Map<string, (...args: never[]) => unknown>();
  const ctx: PluginContext = {
    logger,
    on: (event, handler) => void registered.set(event, handler),
  };
  const disposer = apply(ctx, { daemonURL: mock.url, daemonToken: "tok-c5", dockURL: mock.url, fetchImpl: fetch });
  assert.equal(typeof disposer, "function", "cordis 卸载 disposer（fiber 收集 apply 返回的函数）");
  await waitUntil(() => mock.requestsFor("/dsh/poll").length >= 1, 3000);
  const req = mock.requestsFor("/dsh/poll")[0]!;
  assert.equal((req.body as Record<string, unknown>)["agent"], "dsh", "挂载即首轮 poll");
  assert.ok(Array.isArray((req.body as Record<string, unknown>)["sessions"]), "体带会话清单（此刻为空注册表）");
  assert.equal(registered.has("agent/pre-step"), true, "五事件位照旧挂接");
  release({ status: 200, json: { commands: [] } });
  disposer!();
  assert.doesNotThrow(() => disposer!(), "disposer 幂等");
});
