// dsh-host-guard 票02 · 卸载下线上报 TDD（先红后绿）。spec E/F9 钉点：
//   - dispose 尽力发一次 POST /dsh/poll {agent:"dsh", poller, offline:true,
//     sessions:[]}(poll 体 offline 变体——daemon 侧置 offline 基线标记,
//     下一轮真实心跳即消除);
//   - 失败静默不重试（F9 显式取舍:恰逢 daemon 不可达则丢标记,最长 24h fail
//     误报窗——显式接受）;卸载路径绝不抛、绝不冒 unhandled rejection。
import { test } from "node:test";
import assert from "node:assert/strict";
import { reportOffline, type DaemonEndpoint } from "../src/daemon.ts";
import { apply, type PluginContext } from "../src/index.ts";
import { startMockDaemon, closedDaemonURL, waitUntil, type MockDaemon } from "./mockdaemon.ts";

function makeLogger() {
  const warns: string[] = [];
  const errors: string[] = [];
  return { warns, errors, warn: (m: string) => void warns.push(m), error: (m: string) => void errors.push(m) };
}

function epOf(mock: MockDaemon): DaemonEndpoint {
  return { baseURL: mock.url, token: "tok-t2", fetchImpl: fetch };
}

async function mockFor(t: { after: (fn: () => void) => void }): Promise<MockDaemon> {
  const mock = await startMockDaemon();
  t.after(() => void mock.close());
  return mock;
}

test("reportOffline 形状：POST /dsh/poll {agent, poller, offline:true, sessions:[]} + Bearer;单次尝试不重试", async (t) => {
  const mock = await mockFor(t);
  await reportOffline(epOf(mock), { agent: "dsh", poller: "desktop" });
  const reqs = mock.requestsFor("/dsh/poll");
  assert.equal(reqs.length, 1, "单次尝试（失败也不重试——F9）");
  assert.equal(reqs[0]!.auth, "Bearer tok-t2");
  assert.deepEqual(
    reqs[0]!.body,
    { agent: "dsh", poller: "desktop", offline: true, sessions: [] },
    "poll 体 offline 变体（daemon 置 offline 基线标记）",
  );
});

test("reportOffline 失败静默：daemon 不可达 → 不抛、回值丢弃", async (t) => {
  const { url } = await closedDaemonURL();
  await assert.doesNotReject(() =>
    reportOffline({ baseURL: url, token: "t", fetchImpl: fetch }, { agent: "dsh", poller: "desktop" }),
  );
});

test("dispose 触发下线上报（apply 卸载 disposer）：offline 变体带 poller 名;disposer 幂等不加新上报", async (t) => {
  const mock = await mockFor(t);
  const logger = makeLogger();
  const ctx: PluginContext = {
    logger,
    on: () => {},
  };
  const disposer = apply(ctx, { daemonURL: mock.url, daemonToken: "tok-t2", dockURL: mock.url, fetchImpl: fetch });
  assert.equal(typeof disposer, "function", "cordis 卸载 disposer");
  await waitUntil(() => mock.requestsFor("/dsh/poll").length >= 1, 3000); // 挂载首轮 poll
  disposer!();
  await waitUntil(() => mock.requestsFor("/dsh/poll").length >= 2, 3000); // 下线上报
  const body = mock.requestsFor("/dsh/poll").at(-1)!.body as Record<string, unknown>;
  assert.equal(body["offline"], true, "下线标记");
  assert.deepEqual(body["sessions"], [], "空会话清单（下线体形状）");
  assert.equal(body["agent"], "dsh");
  assert.equal(typeof body["poller"], "string", "带宿主身份名（compact.ts 推导）");
  assert.equal((body["poller"] as string).length > 0, true, "poller 非空");
  // 幂等：二次调用 disposer 不再加新上报。
  disposer!();
  const n = mock.requestsFor("/dsh/poll").length;
  await new Promise((r) => setTimeout(r, 50));
  assert.ok(mock.requestsFor("/dsh/poll").length <= n, "幂等调用不得加新下线上报");
});

test("dispose 下线上报失败静默：daemon 不可达时卸载路径不抛（F9 丢标记显式接受）", async (t) => {
  const { url } = await closedDaemonURL();
  const logger = makeLogger();
  const ctx: PluginContext = { logger, on: () => {} };
  const disposer = apply(ctx, { daemonURL: url, daemonToken: "t", dockURL: url, fetchImpl: fetch });
  assert.doesNotThrow(() => disposer!(), "卸载路径绝不抛");
  await new Promise((r) => setTimeout(r, 30)); // 给在途 fire-and-forget 一个落地窗
});
