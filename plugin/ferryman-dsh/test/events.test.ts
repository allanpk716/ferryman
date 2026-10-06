// 票05 · 五事件位业务对接 TDD（先红后绿）。三口交互全部走 mock daemon
//（localhost 随机端口,自起自灭）;票04 口形状契约 = internal/daemon/
// dsh_receive.go 及其表驱动测试 dsh_receive_test.go。
//
// 覆盖面（票验收）：
//   - 闸门 reject 理由可见（协议无理由字段——理由走 logger,runtime-types.ts:112-119）
//   - created 播种赶首请求时序（serial awaited 位,runtime-types.ts:261;
//     inject 语义 runtime-types.ts:241）
//   - 事件上报字段与票 04 口形状咬合（usage 四列 snake 映射/标题/子会话随父）
//   - 上报失败不阻塞（朴素策略：静默重试一次,两败 warn 一行不抛）
//   - 判活信号挂接（agent/disposed、agent/status 转发;daemon 今日回
//     skipped=unknown-event,P2-5 定消费）
//
// 测试卫生：每个 mock daemon 经 t.after 注册清理（断言失败也不漏句柄——
// 漏关的 listener 会把 node --test 子进程吊死）;fire-and-forget 上报是并发
// 到达,凡多条断言一律序不敏感。
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  buildEventBody,
  makeEventDeps,
  onCreated,
  onDisposed,
  onPreStep,
  onSessionEvent,
  onStatus,
  usageToDaemon,
  type EventDeps,
  type LoggerLike,
  type PreStepDecision,
} from "../src/events.ts";
import { sendEvent } from "../src/daemon.ts";
import { injectedMessage, type UserMessageLike } from "../src/usermessage.ts";
import { startMockDaemon, closedDaemonURL, waitUntil, type MockDaemon } from "./mockdaemon.ts";

const SID = "session-55555555-5555-4555-8555-555555555555";

function makeLogger(): LoggerLike & { warns: string[]; errors: string[] } {
  const warns: string[] = [];
  const errors: string[] = [];
  return { warns, errors, warn: (m) => void warns.push(m), error: (m) => void errors.push(m) };
}

function depsOver(mock: MockDaemon, logger: LoggerLike): EventDeps {
  return makeEventDeps({ baseURL: mock.url, token: "tok-t5", fetchImpl: fetch }, logger);
}

/** 起 mock 并把清理挂到测试生命周期（失败也不漏句柄） */
async function mockFor(t: { after: (fn: () => void) => void }): Promise<MockDaemon> {
  const mock = await startMockDaemon();
  t.after(() => void mock.close());
  return mock;
}

// ---- ① agent/pre-step：闸门问询 ----

test("pre-step：daemon block → reject,理由经 logger.warn 用户可见（协议无理由字段）", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/gate", () => ({
    status: 200,
    json: { decision: "block", reason: "此会话已闲置 40 分钟（缓存已失效）,请开新会话或用「强续」", suppressOriginalPrompt: true },
  }));
  const logger = makeLogger();
  const deps = depsOver(mock, logger);
  let nextCalled = false;

  const decision = await onPreStep(deps, {
    agent: { session: { header: { id: SID, cwd: "C:/proj" } } },
    messages: [{ content: [{ type: "text", text: "继续干活" }] }],
    turn: 1,
    step: 1,
  }, async () => {
    nextCalled = true;
    return { kind: "enter", messages: [] };
  });

  assert.equal(decision.kind, "reject");
  assert.equal(nextCalled, false, "reject 后不得再委托下游 next()");
  assert.equal(logger.warns.length, 1, "理由恰 warn 一行");
  assert.ok(logger.warns[0]!.includes("闲置 40 分钟"), `理由应含 daemon 原话: ${logger.warns[0]}`);
  // 请求形状：键=头行 id（P2-2 定案零换算）,Bearer 鉴权（票04 守门序）。
  const req = mock.requestsFor("/dsh/gate")[0]!;
  assert.equal(req.auth, "Bearer tok-t5");
  const body = req.body as Record<string, unknown>;
  assert.equal(body["session_id"], SID);
  assert.equal(body["cwd"], "C:/proj");
  assert.equal(body["prompt"], "继续干活");
  assert.equal(body["transcript_path"], "");
});

test("pre-step：allow → 透传 next() 决策（不拦截）", async (t) => {
  const mock = await mockFor(t);
  const deps = depsOver(mock, makeLogger());
  const downstream = { kind: "enter", messages: [] } as const;
  const out = await onPreStep(deps, {
    agent: { session: { header: { id: SID, cwd: "C:/proj" } } },
    messages: [{ content: [{ type: "text", text: "hi" }] }],
    turn: 1, step: 1,
  }, async () => downstream);
  assert.equal(out, downstream, "allow 时原样透传下游决策");
});

test("pre-step：allow 带 additional_context（observe 警告）→ 追加注入上下文消息（官方桥先例）", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/gate", () => ({
    status: 200,
    json: { decision: "allow", additional_context: "[Ferryman] 此会话已闲置 12 分钟…" },
  }));
  const deps = depsOver(mock, makeLogger());
  const userMsg = { role: "user", content: [], source: { kind: "user" } } as never;
  const out = await onPreStep(deps, {
    agent: { session: { header: { id: SID, cwd: "C:/proj" } } },
    messages: [{ content: [{ type: "text", text: "hi" }] }],
    turn: 1, step: 1,
  }, async () => ({ kind: "enter", messages: [userMsg] }));
  assert.equal(out.kind, "enter");
  if (out.kind !== "enter") return;
  assert.equal(out.messages.length, 2, "原消息保留＋警告上下文追加在末尾");
  const appended = out.messages[1] as UserMessageLike;
  assert.equal(appended.role, "user");
  assert.equal((appended.content[0] as { text: string }).text, "[Ferryman] 此会话已闲置 12 分钟…");
});

test("pre-step：daemon 不可达 → fail-open 透传（gate 脚本同纪律,不拦截不打扰）", async () => {
  const { url } = await closedDaemonURL();
  const logger = makeLogger();
  const deps = makeEventDeps({ baseURL: url, token: "t", fetchImpl: fetch, timeoutMs: 1000 }, logger);
  const downstream = { kind: "enter", messages: [] } as const;
  const out = await onPreStep(deps, {
    agent: { session: { header: { id: SID, cwd: "C:/proj" } } },
    messages: [{ content: [{ type: "text", text: "hi" }] }],
    turn: 1, step: 1,
  }, async () => downstream);
  assert.equal(out, downstream);
  assert.equal(logger.warns.length, 0, "fail-open 静默（可观测性留给挂载自检）");
});

test("pre-step：messages 空（合成上下文步）→ 不问闸门直接透传（官方桥同位短路）", async (t) => {
  const mock = await mockFor(t);
  const deps = depsOver(mock, makeLogger());
  const downstream = { kind: "enter", messages: [] } as const;
  const out = await onPreStep(deps, { agent: { session: { header: { id: SID } } }, messages: [], turn: 1, step: 1 }, async () => downstream);
  assert.equal(out, downstream);
  assert.equal(mock.requests.length, 0, "空步不应发 HTTP");
});

test("pre-step：step>1（工具循环步）→ 不问闸门直接透传（2026-10-05 案：只审用户步）", async (t) => {
  const mock = await mockFor(t);
  const deps = depsOver(mock, makeLogger());
  const downstream = { kind: "enter", messages: [] } as const;
  const out = await onPreStep(deps, {
    agent: { session: { header: { id: SID, cwd: "C:/proj" } } },
    messages: [{ content: [{ type: "text", text: "循环步" }] }],
    turn: 3, step: 2,
  }, async () => downstream);
  assert.equal(out, downstream, "循环步原样透传下游决策");
  assert.equal(mock.requests.length, 0, "循环步不应发 HTTP（闸门只审 step=1 用户步）");
});

// ---- ② agent/created（awaited）：交接播种 ----

test("created：交接 MD 存在 → agent.inject 播种,消息形状=宿主 UserMessage", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/handoff", () => ({ status: 200, json: { context: "# 交接\n上一会话精华…" } }));
  const injected: UserMessageLike[] = [];
  const agent = {
    session: { header: { id: SID, cwd: "C:/proj" } },
    inject: (m: UserMessageLike) => void injected.push(m),
  };
  await onCreated(depsOver(mock, makeLogger()), { agent, source: "startup" });

  assert.equal(injected.length, 1);
  const msg = injected[0]!;
  assert.equal(msg.role, "user");
  assert.equal((msg.content[0] as { type: string; text: string }).type, "text");
  assert.ok((msg.content[0] as { text: string }).text.includes("上一会话精华"));
  assert.equal(msg.source.kind, "ferryman-dsh");
  assert.match(msg.id, /^[0-9a-f-]{36}$/i, "id=UUID（createUserMessage 同形）");
  // 请求形状：agent 钉 daemon 侧,同 cwd+session_id 查询（票04 契约）。
  const req = mock.requestsFor("/dsh/handoff")[0]!;
  assert.deepEqual(req.body, { cwd: "C:/proj", session_id: SID });
});

test("created 播种赶首请求时序：inject 在 HTTP 响应后、handler 决议前（serial awaited 保证赶首请求）", async (t) => {
  const mock = await mockFor(t);
  let release!: (v: { status: number; json: unknown }) => void;
  const gate = new Promise<{ status: number; json: unknown }>((r) => (release = r));
  mock.route("/dsh/handoff", () => gate);
  const injected: UserMessageLike[] = [];
  const agent = {
    session: { header: { id: SID, cwd: "C:/proj" } },
    inject: (m: UserMessageLike) => void injected.push(m),
  };
  try {
    const pending = onCreated(depsOver(mock, makeLogger()), { agent, source: "startup" });
    await new Promise((r) => setTimeout(r, 30));
    assert.equal(injected.length, 0, "HTTP 在途时不得提前注入");
    release({ status: 200, json: { context: "# 交接" } });
    await pending;
    assert.equal(injected.length, 1, "决议前完成注入——@mode serial（runtime-types.ts:261）下 AgentLoop 扣住排队输入等 listeners 完成,注入必赶首请求");
  } finally {
    release({ status: 200, json: { context: null } }); // 断言失败也放行在途请求
  }
});

test("created：无交接（context null）/daemon 不可达 → 不注入、不抛（creation 不得失败）", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/handoff", () => ({ status: 200, json: { context: null } }));
  const injected: UserMessageLike[] = [];
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } }, inject: (m) => void injected.push(m) };
  await assert.doesNotReject(onCreated(depsOver(mock, makeLogger()), { agent }));
  assert.equal(injected.length, 0);

  const { url } = await closedDaemonURL();
  const deps = makeEventDeps({ baseURL: url, token: "t", fetchImpl: fetch, timeoutMs: 1000 }, makeLogger());
  await assert.doesNotReject(onCreated(deps, { agent }));
  assert.equal(injected.length, 0);
});

// ---- ③ session/event：上报（字段与票 04 口形状咬合） ----

test("事件上报：turn/start 形状咬合（session_id/event/time/cwd/title,无 usage 键）", async (t) => {
  const mock = await mockFor(t);
  const deps = depsOver(mock, makeLogger());
  const session = { header: { id: SID, cwd: "C:/proj" } };
  onSessionEvent(deps, session, { type: "turn/start", seq: 2, time: 1790905227000, data: { turn: 1 } });
  await waitUntil(() => mock.requestsFor("/dsh/event").length >= 1);
  const body = mock.requestsFor("/dsh/event")[0]!.body as Record<string, unknown>;
  assert.equal(body["session_id"], SID);
  assert.equal(body["event"], "turn/start");
  assert.equal(body["time"], 1790905227000);
  assert.equal(body["cwd"], "C:/proj");
  assert.equal(body["title"], "");
  assert.ok(!("usage" in body), "turn/start 无 usage 键");
  assert.equal(mock.requestsFor("/dsh/event")[0]!.auth, "Bearer tok-t5");
});

test("事件上报：session/title 跟踪标题,后续 turn/start 携带", async (t) => {
  const mock = await mockFor(t);
  const deps = depsOver(mock, makeLogger());
  const session = { header: { id: SID, cwd: "C:/proj" } };
  // session/title 不在票04 白名单——插件侧消费,不上报
  onSessionEvent(deps, session, {
    type: "session/title", seq: 1, time: 1790905230000,
    data: { title: "接线会话", messageSeqs: [1], source: "user" },
  });
  assert.equal(mock.requests.length, 0, "session/title 本身不上报");
  onSessionEvent(deps, session, { type: "turn/start", seq: 2, time: 1790905227000, data: { turn: 1 } });
  await waitUntil(() => mock.requestsFor("/dsh/event").length >= 1);
  const body = mock.requestsFor("/dsh/event")[0]!.body as Record<string, unknown>;
  assert.equal(body["title"], "接线会话");
});

test("事件上报：assistant/message usage 四列 camel→snake 映射＋model 提取", async (t) => {
  const mock = await mockFor(t);
  const deps = depsOver(mock, makeLogger());
  const session = { header: { id: SID, cwd: "C:/proj" } };
  onSessionEvent(deps, session, {
    type: "assistant/message", seq: 3, time: 1790905227524,
    data: {
      turn: 1, step: 1,
      message: { role: "assistant", content: [], source: { kind: "model", provider: "ferryman-dock", model: "glm-5.3" } },
      usage: { inputTokens: 120, outputTokens: 80, cacheReadTokens: 30000, cacheWriteTokens: 5000 },
      stream: [],
    },
  });
  await waitUntil(() => mock.requestsFor("/dsh/event").length >= 1);
  const body = mock.requestsFor("/dsh/event")[0]!.body as Record<string, unknown>;
  assert.equal(body["event"], "assistant/message");
  assert.equal(body["model"], "glm-5.3");
  assert.deepEqual(body["usage"], {
    input_tokens: 120, cache_read_tokens: 30000, cache_creation_tokens: 5000, output_tokens: 80,
  });
});

test("事件上报：assistant/message 无 usage → 无 usage 键；user/message 等白名单外类型零上报", async (t) => {
  const mock = await mockFor(t);
  const deps = depsOver(mock, makeLogger());
  const session = { header: { id: SID, cwd: "C:/proj" } };
  onSessionEvent(deps, session, {
    type: "assistant/message", seq: 4, time: 1790905250000,
    data: { turn: 1, step: 2, message: { role: "assistant", content: [], source: { kind: "model", model: "glm-5.3" } }, stream: [] },
  });
  onSessionEvent(deps, session, { type: "user/message", seq: 5, time: 1790905260000, data: {} });
  onSessionEvent(deps, session, { type: "turn/end", seq: 6, time: 1790905270000, data: {} });
  await new Promise((r) => setTimeout(r, 50));
  const evReqs = mock.requestsFor("/dsh/event");
  assert.equal(evReqs.length, 1, "仅 assistant/message 上报");
  assert.ok(!("usage" in (evReqs[0]!.body as Record<string, unknown>)), "无 usage 事件不带 usage 键");
});

test("事件上报：compaction/* 全转发（并发到达,序不敏感）", async (t) => {
  const mock = await mockFor(t);
  const deps = depsOver(mock, makeLogger());
  const session = { header: { id: SID, cwd: "C:/proj" } };
  for (const [i, type] of ["compaction/start", "compaction/summary", "compaction/end"].entries()) {
    onSessionEvent(deps, session, { type, seq: 10 + i, time: 1790905280000 + i, data: {} });
  }
  await waitUntil(() => mock.requestsFor("/dsh/event").length >= 3);
  const events = mock.requestsFor("/dsh/event")
    .map((r) => (r.body as Record<string, unknown>)["event"])
    .sort();
  assert.deepEqual(events, ["compaction/end", "compaction/start", "compaction/summary"]);
});

test("事件上报：子会话（origin=subagent）带 parent_session_id,session_id=子键（随父入账键）", async (t) => {
  const mock = await mockFor(t);
  const deps = depsOver(mock, makeLogger());
  const child = { header: { id: "session-child-1", cwd: "C:/proj", origin: "subagent", parentSession: SID } };
  onSessionEvent(deps, child, {
    type: "assistant/message", seq: 1, time: 1790905300000,
    data: { message: { source: { kind: "model", model: "glm-5.3" } }, usage: { inputTokens: 1, outputTokens: 1 } },
  });
  await waitUntil(() => mock.requestsFor("/dsh/event").length >= 1);
  const body = mock.requestsFor("/dsh/event")[0]!.body as Record<string, unknown>;
  assert.equal(body["session_id"], "session-child-1");
  assert.equal(body["parent_session_id"], SID);
});

// ---- ③ 上报失败策略：不阻塞、静默重试一次 ----

test("上报失败不阻塞：HTTP 500 → 静默重试一次,第二次成功送达,零 warn", async (t) => {
  const mock = await mockFor(t);
  let n = 0;
  mock.route("/dsh/event", () => {
    n++;
    return n === 1 ? { status: 500, json: { error: "boom" } } : { status: 200, json: { ok: true } };
  });
  const logger = makeLogger();
  const deps = depsOver(mock, logger);
  const session = { header: { id: SID, cwd: "C:/proj" } };
  onSessionEvent(deps, session, { type: "turn/start", seq: 2, time: 1790905227000, data: {} });
  await waitUntil(() => mock.requestsFor("/dsh/event").length >= 2);
  assert.equal(mock.requestsFor("/dsh/event").length, 2, "恰一次重试");
  await new Promise((r) => setTimeout(r, 30));
  assert.equal(logger.warns.length, 0, "重试成功不打扰用户");
});

test("上报失败不阻塞：两败俱败 → warn 一行,handler 不抛", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/event", () => ({ status: 500, json: { error: "down" } }));
  const logger = makeLogger();
  const deps = depsOver(mock, logger);
  const session = { header: { id: SID, cwd: "C:/proj" } };
  assert.doesNotThrow(() =>
    onSessionEvent(deps, session, { type: "turn/start", seq: 2, time: 1790905227000, data: {} }));
  await waitUntil(() => mock.requestsFor("/dsh/event").length >= 2);
  await waitUntil(() => logger.warns.length >= 1);
  assert.equal(logger.warns.length, 1, "终败恰 warn 一行");
  assert.ok(logger.warns[0]!.includes("事件上报失败"));
});

test("sendEvent 客户端钉：网络失败静默重试一次（两调用）,终败不抛", async () => {
  let calls = 0;
  const fetchImpl = (async () => {
    calls++;
    throw new TypeError("fetch failed: ECONNREFUSED");
  }) as typeof fetch;
  const ep = { baseURL: "http://127.0.0.1:9", token: "t", fetchImpl, timeoutMs: 500 };
  const r = await sendEvent(ep, { session_id: "s", event: "turn/start" });
  assert.equal(r.delivered, false);
  assert.equal(calls, 2, "恰一次静默重试（朴素策略）");
});

test("上报回话 skipped（白名单外事件被 daemon 静默收窄）＝送达,不重试", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/event", () => ({ status: 200, json: { ok: true, skipped: "unknown-event" } }));
  const logger = makeLogger();
  const deps = depsOver(mock, logger);
  onStatus(deps, { agent: { session: { header: { id: SID, cwd: "C:/proj" } } }, status: "idle" });
  await waitUntil(() => mock.requestsFor("/dsh/event").length >= 1);
  await new Promise((r) => setTimeout(r, 30));
  assert.equal(mock.requestsFor("/dsh/event").length, 1, "skipped 不触发重试");
  assert.equal(logger.warns.length, 0);
});

// ---- ④ 判活信号挂接（agent/disposed、agent/status 转发;P2-5 消费） ----

test("判活转发：agent/status 带 status;agent/disposed 同款（并发到达,按事件型断言）", async (t) => {
  const mock = await mockFor(t);
  const deps = depsOver(mock, makeLogger());
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } } };
  onStatus(deps, { agent, status: "idle" });
  onStatus(deps, { agent, status: "running" });
  onDisposed(deps, { agent });
  await waitUntil(() => mock.requestsFor("/dsh/event").length >= 3);
  const bodies = mock.requestsFor("/dsh/event").map((r) => r.body as Record<string, unknown>);
  const byEvent = (name: string) => bodies.filter((b) => b["event"] === name);
  assert.equal(byEvent("agent/status").length, 2, "两次 status 转发");
  const statuses = byEvent("agent/status").map((b) => b["status"]).sort();
  assert.deepEqual(statuses, ["idle", "running"]);
  assert.equal(byEvent("agent/disposed").length, 1, "disposed 转发");
  for (const b of bodies) {
    assert.equal(b["session_id"], SID);
    assert.ok(typeof b["time"] === "number");
  }
});

// ---- ⑤ 晚到交接注入（A4②：created 空交接 → 记欠账 → 用户步持续重问） ----

test("晚到交接：created 空交接 → 记欠账;首个用户步重问拿到 → 注入当前步并清账", async (t) => {
  const mock = await mockFor(t);
  let handoffCalls = 0;
  mock.route("/dsh/handoff", () => {
    handoffCalls++;
    return handoffCalls === 1
      ? { status: 200, json: { context: null } } // created 时交接还没铸好
      : { status: 200, json: { context: "# 交接（晚到）\n上一会话精华…" } };
  });
  const logger = makeLogger();
  const deps = depsOver(mock, logger);
  const injected: UserMessageLike[] = [];
  const agent = {
    session: { header: { id: SID, cwd: "C:/proj" } },
    inject: (m: UserMessageLike) => void injected.push(m),
  };
  await onCreated(deps, { agent });
  assert.equal(injected.length, 0, "created 时无交接不注入");
  assert.equal(handoffCalls, 1);
  assert.equal(logger.warns.length, 0, "空交接置欠账静默,不 warn");

  const userMsg = { role: "user", content: [], source: { kind: "user" } } as never;
  const out = await onPreStep(deps, {
    agent,
    messages: [{ content: [{ type: "text", text: "第一条消息" }] }],
    turn: 1, step: 1,
  }, async () => ({ kind: "enter", messages: [userMsg] }));

  assert.equal(out.kind, "enter", "重问不阻塞用户消息");
  assert.equal(handoffCalls, 2, "首个用户步恰好重问一次");
  if (out.kind !== "enter") return;
  assert.equal(out.messages.length, 2, "用户消息保留＋晚到交接追加在末尾");
  const appended = out.messages[1] as UserMessageLike;
  assert.equal(appended.role, "user");
  assert.equal((appended.content[0] as { type: string }).type, "text");
  assert.ok((appended.content[0] as { text: string }).text.includes("晚到"));
  assert.equal(appended.source.kind, "ferryman-dsh");
  assert.equal(injected.length, 0, "重问走下游注入,不走 agent.inject");

  // 清账：拿到后后续用户步不再重问、不再追加
  const out2 = await onPreStep(deps, {
    agent,
    messages: [{ content: [{ type: "text", text: "第二条消息" }] }],
    turn: 2, step: 1,
  }, async () => ({ kind: "enter", messages: [userMsg] }));
  assert.equal(handoffCalls, 2, "拿到交接即清账,不再重问");
  if (out2.kind === "enter") assert.equal(out2.messages.length, 1, "清账后不再追加");
});

test("晚到交接：连续空 → 保持欠账持续重问（每个用户步都问）;step>1 循环步不问", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/handoff", () => ({ status: 200, json: { context: null } }));
  const logger = makeLogger();
  const deps = depsOver(mock, logger);
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } }, inject: () => {} };
  await onCreated(deps, { agent });
  const userMsg = { role: "user", content: [], source: { kind: "user" } } as never;
  const down = async (): Promise<PreStepDecision> => ({ kind: "enter", messages: [userMsg] });
  for (const [i, text] of ["m1", "m2", "m3"].entries()) {
    const out = await onPreStep(deps, {
      agent, messages: [{ content: [{ type: "text", text }] }], turn: i + 1, step: 1,
    }, down);
    assert.equal(out.kind, "enter", `第 ${i + 1} 条消息不因欠账被阻`);
    if (out.kind === "enter") assert.equal(out.messages.length, 1, "没拿到不注入");
  }
  assert.equal(mock.requestsFor("/dsh/handoff").length, 4, "created 1 次＋每用户步重问 1 次×3");
  await onPreStep(deps, {
    agent, messages: [{ content: [{ type: "text", text: "循环步" }] }], turn: 3, step: 2,
  }, down);
  assert.equal(mock.requestsFor("/dsh/handoff").length, 4, "step>1 工具循环步不重问");
  assert.equal(logger.warns.length, 0, "持续欠账静默,不刷 warn");
});

test("晚到交接：欠账按会话键隔离,多会话互不串", async (t) => {
  const mock = await mockFor(t);
  const SID_A = "session-aaaaaaaa-1111-4111-8111-111111111111";
  const SID_B = "session-bbbbbbbb-2222-4222-8222-222222222222";
  const callsBy = new Map<string, number>();
  mock.route("/dsh/handoff", (rec) => {
    const sid = (rec.body as Record<string, unknown>)["session_id"] as string;
    const n = (callsBy.get(sid) ?? 0) + 1;
    callsBy.set(sid, n);
    if (sid === SID_A && n === 1) return { status: 200, json: { context: null } }; // A 首问空
    if (sid === SID_A) return { status: 200, json: { context: "# 只欠 A 的交接" } };
    return { status: 200, json: { context: "# B 自带交接" } };
  });
  const deps = depsOver(mock, makeLogger());
  const agentA = { session: { header: { id: SID_A, cwd: "C:/proj" } }, inject: () => {} };
  const agentB = { session: { header: { id: SID_B, cwd: "C:/proj" } }, inject: () => {} };
  await onCreated(deps, { agent: agentA }); // A 欠账
  await onCreated(deps, { agent: agentB }); // B 拿到,不欠
  const userMsg = { role: "user", content: [], source: { kind: "user" } } as never;
  const down = async (): Promise<PreStepDecision> => ({ kind: "enter", messages: [userMsg] });

  const outB = await onPreStep(deps, {
    agent: agentB, messages: [{ content: [{ type: "text", text: "B 的消息" }] }], turn: 1, step: 1,
  }, down);
  assert.equal(callsBy.get(SID_B), 1, "B 无欠账,用户步不重问");
  if (outB.kind === "enter") assert.equal(outB.messages.length, 1, "B 对话不被 A 的欠账污染");

  const outA = await onPreStep(deps, {
    agent: agentA, messages: [{ content: [{ type: "text", text: "A 的消息" }] }], turn: 1, step: 1,
  }, down);
  assert.equal(callsBy.get(SID_A), 2, "A 欠账,用户步重问");
  const retry = mock.requestsFor("/dsh/handoff").find((r) => (r.body as Record<string, unknown>)["session_id"] === SID_A && callsBy.get(SID_A) === 2);
  assert.ok(retry, "A 的重问请求在");
  if (outA.kind === "enter") {
    assert.equal(outA.messages.length, 2, "A 的对话收到注入");
    assert.ok(((outA.messages[1] as UserMessageLike).content[0] as { text: string }).text.includes("只欠 A"));
  }
});

test("晚到交接：dispose 清欠账,不跨会话泄漏", async (t) => {
  const mock = await mockFor(t);
  mock.route("/dsh/handoff", () => ({ status: 200, json: { context: null } }));
  const deps = depsOver(mock, makeLogger());
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } }, inject: () => {} };
  await onCreated(deps, { agent });
  assert.equal(mock.requestsFor("/dsh/handoff").length, 1);
  onDisposed(deps, { agent });
  const userMsg = { role: "user", content: [], source: { kind: "user" } } as never;
  await onPreStep(deps, {
    agent, messages: [{ content: [{ type: "text", text: "dispose 之后" }] }], turn: 1, step: 1,
  }, async () => ({ kind: "enter", messages: [userMsg] }));
  assert.equal(mock.requestsFor("/dsh/handoff").length, 1, "欠账已随 dispose 清除,不再重问");
});

test("晚到交接：重问遇 daemon 故障 → 静默放行不阻塞,欠账保留待下步再试", async (t) => {
  const mock = await mockFor(t);
  let daemonDown = true;
  mock.route("/dsh/handoff", () =>
    daemonDown ? { status: 500, json: { error: "down" } } : { status: 200, json: { context: "# 恢复后的交接" } });
  const logger = makeLogger();
  const deps = depsOver(mock, logger);
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } }, inject: () => {} };
  await onCreated(deps, { agent }); // 故障→空→欠账成立
  const userMsg = { role: "user", content: [], source: { kind: "user" } } as never;
  const down = async (): Promise<PreStepDecision> => ({ kind: "enter", messages: [userMsg] });
  const out = await onPreStep(deps, {
    agent, messages: [{ content: [{ type: "text", text: "m1" }] }], turn: 1, step: 1,
  }, down);
  assert.equal(out.kind, "enter", "重问失败不阻塞用户消息");
  if (out.kind === "enter") assert.equal(out.messages.length, 1, "失败不注入");
  assert.equal(logger.warns.length, 0, "重问失败静默（fail-open）");
  daemonDown = false;
  const out2 = await onPreStep(deps, {
    agent, messages: [{ content: [{ type: "text", text: "m2" }] }], turn: 2, step: 1,
  }, down);
  assert.equal(out2.kind, "enter");
  if (out2.kind === "enter") {
    assert.equal(out2.messages.length, 2, "daemon 恢复后下一用户步注入");
    assert.ok(((out2.messages[1] as UserMessageLike).content[0] as { text: string }).text.includes("恢复后"));
  }
});

test("晚到交接：gate block 步不重问（reject 无下游可注入）,欠账保留待放行步", async (t) => {
  const mock = await mockFor(t);
  let handoffReady = false;
  mock.route("/dsh/handoff", () => ({ status: 200, json: { context: handoffReady ? "# 交接已铸好" : null } }));
  const deps = depsOver(mock, makeLogger());
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } }, inject: () => {} };
  await onCreated(deps, { agent }); // 交接未铸好 → 欠账
  mock.route("/dsh/gate", () => ({ status: 200, json: { decision: "block", reason: "拦截" } }));
  const d1 = await onPreStep(deps, {
    agent, messages: [{ content: [{ type: "text", text: "被拦" }] }], turn: 1, step: 1,
  }, async () => ({ kind: "enter", messages: [] }));
  assert.equal(d1.kind, "reject");
  assert.equal(mock.requestsFor("/dsh/handoff").length, 1, "block 步无下游,不重问");
  handoffReady = true;
  mock.unroute("/dsh/gate");
  const userMsg = { role: "user", content: [], source: { kind: "user" } } as never;
  const d2 = await onPreStep(deps, {
    agent, messages: [{ content: [{ type: "text", text: "放行" }] }], turn: 2, step: 1,
  }, async () => ({ kind: "enter", messages: [userMsg] }));
  assert.equal(d2.kind, "enter");
  if (d2.kind === "enter") {
    assert.equal(d2.messages.length, 2, "欠账跨 block 步保留,放行步重问并注入");
  }
});

// ---- 入口接线闭环（index.apply → events → daemon） ----

test("apply 端到端：挂接的 session/event 处理器真发 HTTP（五事件位接线闭环）", async (t) => {
  const mock = await mockFor(t);
  // 自检面路由（apply 会触发 selfcheckOnce——让它静默通过,不污染输出）
  mock.route("/", () => ({ status: 200, json: {} }));
  mock.route("/stats", () => ({ status: 200, json: { version: "v0.5.4" } }));
  const registered = new Map<string, (...args: never[]) => unknown>();
  const ctx = {
    logger: makeLogger(),
    on: (event: string, handler: (...args: never[]) => unknown) => void registered.set(event, handler),
  };
  const { apply } = await import("../src/index.ts");
  apply(ctx, { daemonURL: mock.url, daemonToken: "tok-t5", fetchImpl: fetch });
  const handler = registered.get("session/event");
  assert.ok(handler, "session/event 已挂接");
  handler({ header: { id: SID, cwd: "C:/proj" } } as never,
    { type: "turn/start", seq: 1, time: 1790905227000, data: {} } as never);
  await waitUntil(() => mock.requestsFor("/dsh/event").length >= 1);
  const body = mock.requestsFor("/dsh/event")[0]!.body as Record<string, unknown>;
  assert.equal(body["session_id"], SID);
  assert.equal(mock.requestsFor("/dsh/event")[0]!.auth, "Bearer tok-t5");
});

// ---- 纯函数面 ----

test("usageToDaemon：camel→snake 四列,cacheWriteTokens→cache_creation_tokens,缺省 0", () => {
  assert.deepEqual(usageToDaemon({ inputTokens: 9169, outputTokens: 21, totalTokens: 9190 }), {
    input_tokens: 9169, cache_read_tokens: 0, cache_creation_tokens: 0, output_tokens: 21,
  });
  assert.deepEqual(
    usageToDaemon({ inputTokens: 120, outputTokens: 80, cacheReadTokens: 30000, cacheWriteTokens: 5000 }),
    { input_tokens: 120, cache_read_tokens: 30000, cache_creation_tokens: 5000, output_tokens: 80 },
  );
  assert.equal(usageToDaemon(undefined), undefined);
  assert.equal(usageToDaemon("oops"), undefined);
});

test("buildEventBody：非对象事件/无 session_id → null（坏形静默收窄不炸）", () => {
  assert.equal(buildEventBody({ header: { id: SID } }, { type: "assistant/message" }, new Map()) !== null, true);
  assert.equal(buildEventBody({ header: {} }, { type: "turn/start", data: {} }, new Map()), null);
  assert.equal(buildEventBody({ header: { id: SID } }, null, new Map()), null);
  assert.equal(buildEventBody({ header: { id: SID } }, { type: "agent/status" }, new Map()), null);
});

test("injectedMessage：宿主 UserMessage 同形（role/content/source/id）,两次调用 id 不同", () => {
  const a = injectedMessage("# 交接 A");
  const b = injectedMessage("# 交接 B");
  assert.equal(a.role, "user");
  assert.equal((a.content[0] as { text: string }).text, "# 交接 A");
  assert.equal(a.source.kind, "ferryman-dsh");
  assert.notEqual(a.id, b.id, "每消息独立身份（createMessage randomUUID 同语义）");
});
