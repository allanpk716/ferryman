// 票03 · 注册表全量播种 TDD(先红后绿)——spec「注册表全量播种(三钟分工终版)」
// 节逐字钉点(.scratch/dsh-registry-seed/spec.md,权威设计)。六断言适用面标注见
// 用例名([两路]/[主路]/[仅主路]/[仅 Plan B]);时钟注入先例 new
// SessionRegistry(() => clock);mock 形状按生产 asar 实锚(sessions 服务
// getListSnapshot() → {items:[{sessionId,cwd,running,updatedAt,origin,
// parentSessionId,...}]},spec D7.5「回写为沙箱 mock 形状断言,防沙箱绿真机红」)。
//
// 三钟分工(终版表,spec 逐字):
//   钟② lastEventAt:仅事件面五事件位推进(touch/setStatus);播种永不可触碰
//     (条目创建时初始化为创建时刻,创建初始化不算触碰);busyLive 唯一输入。
//   钟③ lastActivityAt:事件 + 播种(仅快照 updatedAt 实际推进时,单调)。
//   busy 合并:播种以快照 running 覆盖;护栏=事件面 busy=true 且
//     (now−lastEventAt)<300s 不被快照 false 覆盖。
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  DEFAULT_SEED_INTERVAL_MS,
  D7_STALE_RUNNING_MS,
  RegistrySeeder,
  startSeedScheduler,
  type SeedSources,
} from "../src/seed.ts";
import { EVENT_FRESH_WINDOW_MS, SessionRegistry } from "../src/registry.ts";
import { startPollLoop } from "../src/compact.ts";
import { startMockDaemon, waitUntil } from "./mockdaemon.ts";

const SID = "session-55555555-5555-4555-8555-555555555555";
const SID16 = SID.slice(0, 16); // sid 截断 16 位(v0.9.3 票4 纪律)

interface TestLogger {
  warns: string[];
  errors: string[];
  warn(msg: string): void;
  error(msg: string): void;
}

function makeLogger(): TestLogger {
  const warns: string[] = [];
  const errors: string[] = [];
  return { warns, errors, warn: (m) => void warns.push(m), error: (m) => void errors.push(m) };
}

/** 生产 getListSnapshot 形状的单条 item(实锚字段;title 等多余字段实现不得依赖) */
function item(sid: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    sessionId: sid,
    cwd: "C:/proj",
    running: false,
    updatedAt: 0,
    origin: "user",
    parentSessionId: undefined,
    ...extra,
  };
}

const primaryOf = (items: unknown[]): SeedSources => ({ getListSnapshot: () => ({ items }) });
const planbOf = (sids: string[], agent?: unknown): SeedSources => ({
  listWorkspaces: () => sids.map((s) => ({ sessionIds: [s] })),
  ...(agent === undefined ? {} : { getAgent: () => agent }),
});

// ---- ⓪ 静态契约与钟②机制底座 ----

test("播种常量(票03):重播缺省 5min;D7 探针阈值 600s;事件钟新鲜窗 300s", () => {
  assert.equal(DEFAULT_SEED_INTERVAL_MS, 300_000, "每 5min 重播(票面钉点,可注入)");
  assert.equal(D7_STALE_RUNNING_MS, 600_000, "D7 探针:running=true 且 updatedAt 落后 >600s 注记");
  assert.equal(EVENT_FRESH_WINDOW_MS, 300_000, "busyLive 衰减窗 300s(spec 钟②行)");
});

test("钟②机制底座:touch/setStatus 推进 lastEventAt;播种原语四件全不碰;eventAgeMs 现算", () => {
  let clock = 1_000_000_000_000;
  const r = new SessionRegistry(() => clock);
  r.touch(SID, "C:/proj");
  assert.equal(r.get(SID)!.lastEventAt, clock, "创建初始化=创建时刻(不算触碰——round2 措辞澄清)");
  clock += 1_000;
  r.touch(SID, "C:/proj");
  assert.equal(r.get(SID)!.lastEventAt, clock, "touch 推进钟②(事件面)");
  clock += 1_000;
  r.setStatus(SID, "running");
  assert.equal(r.get(SID)!.lastEventAt, clock, "setStatus 推进钟②(事件面)");
  clock += 1_000;
  const before = r.get(SID)!.lastEventAt;
  r.setStatus(SID, "paused"); // 协议外值:置位忽略,钟②也不动(合法值才推进)
  assert.equal(r.get(SID)!.lastEventAt, before, "非法 status 不推进钟②");
  assert.equal(r.eventAgeMs(SID), 1_000, "eventAgeMs=now−lastEventAt");
  clock -= 5_000; // 时钟倒挂防御
  assert.equal(r.eventAgeMs(SID), 0, "负值钳 0");
  clock += 5_000;
  assert.equal(r.eventAgeMs("session-ghost"), undefined, "未知 sid → undefined");
  // 播种原语四件(合并纪律的机械面):一律不推钟②
  clock += 60_000;
  const t0 = r.get(SID)!.lastEventAt;
  r.seedOverwriteBusy(SID, true, false);
  assert.equal(r.get(SID)!.lastEventAt, t0, "seedOverwriteBusy 不碰钟②");
  r.seedFillAgent(SID, {});
  assert.equal(r.get(SID)!.lastEventAt, t0, "seedFillAgent 不碰钟②");
  r.seedAdvanceActivity(SID, clock + 5_000);
  assert.equal(r.get(SID)!.lastEventAt, t0, "seedAdvanceActivity 不碰钟②");
});

// ---- 六断言①:噪声推 updatedAt → 钟②不受影响 → busyLive 照常成熟[两路通用] ----

test("六断言①:噪声推 updatedAt 不触碰钟②,busyLive 照钟②照常成熟(表驱动:主路+Plan B)", async () => {
  const cases: Array<{ name: string; mode: "primary" | "planb"; sources: (now: number) => SeedSources; expectBusy: boolean }> = [
    {
      name: "主路",
      mode: "primary",
      sources: (now) => primaryOf([item(SID, { running: false, updatedAt: now })]), // updatedAt 噪声推进到 now
      expectBusy: false, // 快照 running=false 校真(事件已 stale,护栏不挡)
    },
    {
      name: "PlanB",
      mode: "planb",
      sources: () => planbOf([SID]), // 无 running/updatedAt 源
      expectBusy: true, // 不做 busy 校真,靠钟②衰减兜底
    },
  ];
  for (const c of cases) {
    let clock = 1_000_000_000_000;
    const registry = new SessionRegistry(() => clock);
    const logger = makeLogger();
    registry.touch(SID, "C:/proj");
    registry.setStatus(SID, "running"); // 事件面置忙(T0):钟②=T0
    const eventAt = clock;
    clock += 400_000; // 事件钟出 300s 窗
    const seeder = new RegistrySeeder({ registry, logger, sources: c.sources(clock), now: () => clock });
    assert.equal(await seeder.seedOnce(), c.mode);
    const e = registry.get(SID)!;
    assert.equal(e.lastEventAt, eventAt, `[${c.name}] 钟②纹丝不动(播种永不可触碰)`);
    assert.equal(e.busy, c.expectBusy, `[${c.name}] busy 合并语义`);
    assert.ok(
      (registry.eventAgeMs(SID) ?? 0) >= EVENT_FRESH_WINDOW_MS,
      `[${c.name}] busyLive 唯一输入(钟②)已出窗=照常成熟(busy ∧ fresh 双灭其一)`,
    );
  }
});

// ---- 六断言②③:busy 合并(快照 running 覆盖 + 护栏)[主路] ----

test("六断言②③:快照 running 覆盖 busy 校正陈旧;事件面新鲜 busy 不被快照 false 覆盖(表驱动,主路)", async () => {
  const cases = [
    { name: "②stale 事件 busy 被快照校真", preStatus: "running" as const, ageMs: 400_000, snapRunning: false, expect: false },
    { name: "③事件新鲜 busy 不被快照 false 覆盖", preStatus: "running" as const, ageMs: 100_000, snapRunning: false, expect: true },
    { name: "合并·快照 true 置忙(事件 stale)", preStatus: "running" as const, ageMs: 400_000, snapRunning: true, expect: true },
    { name: "合并·快照 true 置忙(事件 fresh)", preStatus: "running" as const, ageMs: 100_000, snapRunning: true, expect: true },
    { name: "合并·事件空闲+快照 false 维持空", preStatus: "idle" as const, ageMs: 50_000, snapRunning: false, expect: false },
  ];
  for (const c of cases) {
    let clock = 2_000_000_000_000;
    const registry = new SessionRegistry(() => clock);
    const logger = makeLogger();
    registry.touch(SID, "C:/proj");
    registry.setStatus(SID, c.preStatus); // 事件面置位(钟②=T0)
    clock += c.ageMs;
    const seeder = new RegistrySeeder({
      registry,
      logger,
      sources: primaryOf([item(SID, { running: c.snapRunning, updatedAt: clock })]),
      now: () => clock,
    });
    await seeder.seedOnce();
    assert.equal(registry.get(SID)!.busy, c.expect, c.name);
  }
});

// ---- 六断言④:主路重播钟③仅 updatedAt 实际推进时单调推进;钟②恒定[仅主路] ----

test("六断言④:重播推进钟③仅当 updatedAt 实际推进(单调不回拨),钟②恒为创建时刻(仅主路)", async () => {
  let clock = 3_000_000_000_000;
  const T0 = clock;
  const registry = new SessionRegistry(() => clock);
  const logger = makeLogger();
  let snapUpdatedAt = T0;
  const seeder = new RegistrySeeder({
    registry,
    logger,
    now: () => clock,
    sources: { getListSnapshot: () => ({ items: [item(SID, { updatedAt: snapUpdatedAt })] }) }, // 闭包读可变量
  });
  await seeder.seedOnce(); // 新建:钟③=快照 updatedAt;钟②=创建时刻
  let e = registry.get(SID)!;
  assert.equal(e.lastActivityAt, T0, "新建钟③=快照 updatedAt(非播种时刻)");
  assert.equal(e.lastEventAt, T0, "新建钟②=创建时刻(=播种时刻,创建初始化)");

  clock += 50_000;
  await seeder.seedOnce(); // 重播:same updatedAt → 不推进
  e = registry.get(SID)!;
  assert.equal(e.lastActivityAt, T0, "同 updatedAt 重播不推进钟③(水位未动)");
  assert.equal(e.lastEventAt, T0, "重播不碰钟②");

  clock += 50_000;
  snapUpdatedAt = clock; // updatedAt 实际推进
  await seeder.seedOnce();
  e = registry.get(SID)!;
  assert.equal(e.lastActivityAt, clock, "updatedAt 实际推进 → 钟③单调推进");
  assert.equal(e.lastEventAt, T0, "钟②恒定(任何情况不被播种推进)");

  snapUpdatedAt = clock - 30_000; // 宿主 updatedAt 倒退(回拨形态)
  await seeder.seedOnce();
  e = registry.get(SID)!;
  assert.equal(e.lastActivityAt, clock, "倒退不回拨钟③(单调)");
  assert.equal(e.lastEventAt, T0, "钟②仍恒定");

  // 倒退后再次正常推进:水位未被倒退污染,推进照常
  clock += 60_000;
  snapUpdatedAt = clock;
  await seeder.seedOnce();
  assert.equal(registry.get(SID)!.lastActivityAt, clock, "倒退后正常推进照常");
});

// ---- 六断言⑤:Plan B 重播不推进已播种条目钟②/③(宁 stale 勿清零)[仅 Plan B] ----

test("六断言⑤:Plan B 重播不推进已播种条目两钟;主路播种条目遇 Plan B 重播同样不动(仅 Plan B)", async () => {
  let clock = 4_000_000_000_000;
  const T0 = clock;
  const registry = new SessionRegistry(() => clock);
  const logger = makeLogger();
  const planb = new RegistrySeeder({ registry, logger, now: () => clock, sources: planbOf([SID]) });
  await planb.seedOnce(); // Plan B 新建
  const e0 = registry.get(SID)!;
  assert.equal(e0.lastActivityAt, T0, "Plan B 新建钟③=播种时刻(创建初始化)");
  assert.equal(e0.lastEventAt, T0, "Plan B 新建钟②=创建时刻");
  assert.equal(e0.busy, false, "Plan B 新建不设 busy(无 running 源)");

  clock += 60_000;
  await planb.seedOnce(); // 重播:不推任何已播种条目两钟
  assert.equal(registry.get(SID)!.lastActivityAt, T0, "重播不推钟③(宁 stale 勿清零)");
  assert.equal(registry.get(SID)!.lastEventAt, T0, "重播不推钟②");

  // 变体:主路播种(updatedAt 实际推进)→ Plan B 重播 → 两钟同样不动
  clock += 60_000;
  const T3 = clock;
  const primary = new RegistrySeeder({
    registry,
    logger,
    now: () => clock,
    sources: primaryOf([item(SID, { updatedAt: T3 })]),
  });
  await primary.seedOnce();
  assert.equal(registry.get(SID)!.lastActivityAt, T3, "主路合并:updatedAt 推进 → 钟③=T3");
  clock += 60_000;
  await planb.seedOnce();
  assert.equal(registry.get(SID)!.lastActivityAt, T3, "主路播种条目不被 Plan B 重播推进");
  assert.equal(registry.get(SID)!.lastEventAt, T0, "钟②仍旧(自创建起恒定)");
});

// ---- 六断言⑥:新建时钟来源 + agent 只补 null + subagent 照登 ----

test("六断言⑥:主路新建钟③=快照 updatedAt(≠播种时刻)/钟②=创建时刻;agent 只补空位不覆盖;subagent 照登", async () => {
  let clock = 5_000_000_000_000;
  const T0 = clock;
  const registry = new SessionRegistry(() => clock);
  const logger = makeLogger();
  const agentB = { session: { header: { id: SID, cwd: "C:/proj" } } };
  const agentA = { session: { header: { id: SID, cwd: "C:/proj" } }, tag: "A" };
  const childSid = "session-66666666-6666-4666-8666-666666666666";
  const S3 = "session-77777777-7777-4777-8777-777777777777";
  const ghost = "session-88888888-8888-4888-8888-888888888888";
  registry.touch(S3, "C:/proj", agentA); // 事件面登记:已有活引用
  registry.touch(ghost, "C:/proj"); // 事件面残影:无 agent
  const seeder = new RegistrySeeder({
    registry,
    logger,
    now: () => clock,
    sources: {
      getListSnapshot: () => ({
        items: [
          item(SID, { updatedAt: T0 - 60_000 }), // 快照时间戳落后播种时刻 60s(宿主常态)
          item(childSid, { updatedAt: T0 - 60_000, origin: "subagent", parentSessionId: SID }),
          item(S3, { updatedAt: T0 - 60_000 }),
          item(ghost, { updatedAt: T0 - 60_000 }),
        ],
      }),
      getAgent: () => agentB,
    },
  });
  await seeder.seedOnce();
  const e = registry.get(SID)!;
  assert.equal(e.lastActivityAt, T0 - 60_000, "钟③=快照 updatedAt(非播种时刻——六断言⑥时钟来源)");
  assert.equal(e.lastEventAt, T0, "钟②=创建时刻(播种时刻)");
  assert.ok(registry.get(childSid) !== undefined, "subagent 条目照登(origin=subagent 不跳过)");
  assert.equal(registry.get(S3)!.agent, agentA, "agent 只补 null:既有活引用不被播种覆盖");
  assert.equal(registry.get(ghost)!.agent, agentB, "agent 空位由 agents.get 补(供执行臂)");
});

// ---- agents.get 形态容错(thenable/抛错/缺面) ----

test("agents.get 形态:thenable 回话接住;抛错容错不炸;取不到不补(条目照建)", async () => {
  let clock = 6_000_000_000_000;
  const registry = new SessionRegistry(() => clock);
  const logger = makeLogger();
  const agent = { session: { header: { id: "session-a1" } } };
  const seeder = new RegistrySeeder({
    registry,
    logger,
    now: () => clock,
    sources: {
      getListSnapshot: () => ({
        items: [item("session-a1", { updatedAt: clock }), item("session-a2", { updatedAt: clock }), item("session-a3", { updatedAt: clock })],
      }),
      getAgent: (sid: string) => {
        if (sid === "session-a1") return Promise.resolve(agent); // async 形态
        if (sid === "session-a2") throw new Error("inject 执法形态"); // 抛错
        return undefined; // a3 取不到
      },
    },
  });
  assert.equal(await seeder.seedOnce(), "primary");
  assert.equal(registry.get("session-a1")!.agent, agent, "thenable 归一(await)");
  assert.equal(registry.get("session-a2")!.agent, undefined, "getAgent 抛错容错(optionalFace 纪律)");
  assert.equal(registry.get("session-a3")!.agent, undefined, "取不到不补");
  assert.ok(registry.get("session-a3") !== undefined, "agent 缺席不挡条目登记");
});

// ---- mock 形状契约(主路形状不符 → Plan B;畸形元素跳过;spec D7.5) ----

test("mock 形状契约:getListSnapshot 回非 {items:[...]} 形状 → 落 Plan B;items 内畸形元素跳过不炸", async () => {
  let clock = 7_000_000_000_000;
  const registry = new SessionRegistry(() => clock);
  const logger = makeLogger();
  let primary: unknown = { nope: 1 }; // 形状不符(无 items)
  const seeder = new RegistrySeeder({
    registry,
    logger,
    now: () => clock,
    sources: {
      getListSnapshot: () => primary,
      listWorkspaces: () => [{ sessionIds: [SID] }],
    },
  });
  assert.equal(await seeder.seedOnce(), "planb", "主路形状不符 → Plan B 备胎(spec 语义)");
  assert.ok(registry.get(SID) !== undefined, "Plan B 接住登记(sid+cwd)");

  primary = { items: [null, 42, "junk", item(SID, { updatedAt: clock })] }; // 混入畸形元素
  assert.equal(await seeder.seedOnce(), "primary");
  assert.equal(registry.get(SID)!.lastActivityAt, clock, "畸形元素跳过,合法 item 照常合并");
});

// ---- 降级(缺面/抛错 → 静默回事件喂养,warn 一行) ----

test("降级:主路+Plan B 皆缺面 → failed + warn 恰一行,registry 不动、不抛(加载与轮询不受影响)", async () => {
  const registry = new SessionRegistry();
  const logger = makeLogger();
  const seeder = new RegistrySeeder({ registry, logger, sources: {} });
  assert.equal(await seeder.seedOnce(), "failed");
  assert.equal(registry.list().length, 0, "空降级:事件喂养不受影响");
  assert.equal(logger.warns.length, 1, "warn 一行(票面钉点)");
  assert.ok(logger.warns[0]!.includes("播种"));
  assert.equal(logger.errors.length, 0, "静默降级:不走 error 通道");
});

test("降级:主路抛错+Plan B 抛错 → failed + warn 一行;主路抛错+Plan B 可用 → planb(切换静默)", async () => {
  let clock = 8_000_000_000_000;
  const registry = new SessionRegistry(() => clock);
  const logger = makeLogger();
  const boom = new RegistrySeeder({
    registry,
    logger,
    now: () => clock,
    sources: {
      getListSnapshot: () => {
        throw new Error("sessions boom");
      },
      listWorkspaces: () => {
        throw new Error("ws boom");
      },
    },
  });
  assert.equal(await boom.seedOnce(), "failed", "双抛=失败");
  assert.equal(logger.warns.length, 1, "失败 warn 一行");
  assert.equal(logger.errors.length, 0);

  const logger2 = makeLogger();
  const fallback = new RegistrySeeder({
    registry,
    logger: logger2,
    now: () => clock,
    sources: {
      getListSnapshot: () => {
        throw new Error("sessions boom");
      },
      listWorkspaces: () => [{ sessionIds: [SID] }],
    },
  });
  assert.equal(await fallback.seedOnce(), "planb", "主路抛错落 Plan B=设计内备胎,非失败");
  assert.equal(logger2.warns.filter((w) => w.includes("不可用")).length, 0, "Plan B 接住:不 warn 失败(切换静默)");
  assert.ok(registry.get(SID) !== undefined);
});

// ---- 调度(可注入不真等) ----

test("调度:startSeedScheduler 挂重播表(缺省 300_000ms);首轮由 seedOnce 驱动;stop 清表幂等;重播真触发", async () => {
  let fired: (() => void) | undefined;
  let armedMs = -1;
  let cleared = false;
  let calls = 0;
  const handle = startSeedScheduler({
    logger: makeLogger(),
    registry: new SessionRegistry(),
    sources: {
      getListSnapshot: () => {
        calls++;
        return { items: [] };
      },
    },
    setImpl: (fn, ms) => {
      fired = fn;
      armedMs = ms;
      return ms;
    },
    clearImpl: () => {
      cleared = true;
      fired = undefined;
    },
  });
  assert.equal(armedMs, DEFAULT_SEED_INTERVAL_MS, "缺省重播间隔 5min");
  assert.equal(typeof fired, "function", "重播表已挂");
  assert.equal(calls, 0, "构造不自动播种(首轮由 index.ts 在 poll 前 await seedOnce)");
  await handle.seedOnce();
  assert.equal(calls, 1, "首轮播种");
  fired!(); // 重播表到点
  await waitUntil(() => calls >= 2, 1000);
  assert.equal(calls, 2, "重播真触发");
  handle.stop();
  assert.equal(cleared, true, "stop 清重播表");
  assert.doesNotThrow(() => handle.stop(), "stop 幂等");
});

test("调度:重播间隔可注入(沙箱/E2E 压秒级,不真等 5min)", () => {
  let armedMs = -1;
  const handle = startSeedScheduler({
    logger: makeLogger(),
    registry: new SessionRegistry(),
    intervalMs: 15,
    setImpl: (_fn, ms) => {
      armedMs = ms;
      return ms;
    },
    clearImpl: () => {},
  });
  handle.stop();
  assert.equal(armedMs, 15, "间隔可注入(票面钉点)");
});

test("调度:播种在途重入共享同一 promise(guard 防重播表与首轮撞车)", async () => {
  let calls = 0;
  let release!: () => void;
  const gate = new Promise<void>((r) => {
    release = () => r();
  });
  const handle = startSeedScheduler({
    logger: makeLogger(),
    registry: new SessionRegistry(),
    sources: {
      getListSnapshot: () => {
        calls++;
        return { items: [] };
      },
      getAgent: () => gate, // 挡住播种在途
    },
    setImpl: () => 0,
    clearImpl: () => {},
  });
  const p1 = handle.seedOnce();
  const p2 = handle.seedOnce(); // 在途:共享 p1
  release();
  assert.equal(await p1, "primary");
  assert.equal(await p2, "primary");
  assert.equal(calls, 1, "重入不重复播种");
  handle.stop();
});

// ---- D7 探针(实现内置,只注记,不做任何判定) ----

test("D7 探针:主路 running=true 且 now−updatedAt>600s → warn 一行(含 sid16);未超/Plan B 不注记", async () => {
  let clock = 9_000_000_000_000;
  const registry = new SessionRegistry(() => clock);
  const logger = makeLogger();
  const seeder = new RegistrySeeder({
    registry,
    logger,
    now: () => clock,
    sources: primaryOf([item(SID, { running: true, updatedAt: clock - D7_STALE_RUNNING_MS - 1_000 })]),
  });
  await seeder.seedOnce();
  const d7 = logger.warns.filter((w) => w.includes("D7"));
  assert.equal(d7.length, 1, "超 600s:恰一行(可信度观察)");
  assert.ok(d7[0]!.includes(SID16), "sid16 截断(v0.9.3 票4 纪律)");

  const logger2 = makeLogger();
  const seeder2 = new RegistrySeeder({
    registry,
    logger: logger2,
    now: () => clock,
    sources: primaryOf([item("session-b1", { running: true, updatedAt: clock - 100_000 })]),
  });
  await seeder2.seedOnce();
  assert.equal(logger2.warns.filter((w) => w.includes("D7")).length, 0, "未超 600s:不注记");

  const logger3 = makeLogger();
  const seeder3 = new RegistrySeeder({ registry, logger: logger3, now: () => clock, sources: planbOf([SID]) });
  await seeder3.seedOnce();
  assert.equal(logger3.warns.filter((w) => w.includes("D7")).length, 0, "Plan B 无 updatedAt 源:不探");
});

// ---- 端到端:播种静置会话进轮询名单;busyLive 走钟②(播种重播不救活挂死 busy) ----

// (makeEngine/ctxWithServices 克隆自 compact.test.ts——测试文件独立,不互 import)

/** compactNow 记录替身:抓 receiver/agent/signal,可编程回话(克隆自 compact.test.ts) */
function makeEngine(impl?: () => Promise<unknown>) {
  const rec: { calls: Array<{ receiver: unknown; agent: unknown; signal: unknown }>; impl: () => Promise<unknown> } = {
    calls: [],
    impl: impl ?? (async () => null),
  };
  const engine = {
    compactNow(this: unknown, agent: unknown, signal: AbortSignal): Promise<unknown> {
      rec.calls.push({ receiver: this, agent, signal });
      return rec.impl();
    },
  };
  return { engine, rec };
}

/** 宿主 ctx 替身:inject 同步回调(克隆自 compact.test.ts) */
function ctxWithServices(logger: TestLogger, services: Map<string, unknown>) {
  return {
    logger,
    inject: (names: readonly string[], cb: (scoped: unknown) => void) => {
      const scoped: Record<string, unknown> = {};
      for (const n of names) scoped[n] = services.get(n);
      cb(scoped);
    },
  };
}

test("端到端:播种的静置会话进首轮 poll 名单;播种 running 条目窗内拒/钟②出窗后(重播不救活)执行", async (t) => {
  const mock = await startMockDaemon();
  t.after(() => void mock.close());
  let clock = 10_000_000_000_000;
  const registry = new SessionRegistry(() => clock);
  const logger = makeLogger();
  const agent = { session: { header: { id: SID, cwd: "C:/proj" } } };
  const seeder = new RegistrySeeder({
    registry,
    logger,
    now: () => clock,
    sources: {
      getListSnapshot: () => ({ items: [item(SID, { running: true, updatedAt: clock })] }),
      getAgent: () => agent,
    },
  });
  await seeder.seedOnce(); // 播种新建:busy=true(快照),钟②=创建时刻
  const { engine, rec } = makeEngine();
  const ctx = ctxWithServices(logger, new Map([["compaction", engine]]));
  const CMD = { action: "compact", session_id: SID, cwd: "C:/proj" };
  const flag = { on: false };
  mock.route("/dsh/poll", () => ({ status: 200, json: { commands: flag.on ? [CMD] : [] } }));
  const loop = startPollLoop({
    ep: { baseURL: mock.url, token: "tok", fetchImpl: fetch },
    logger,
    registry,
    ctx: ctx as never,
    reportSettleMs: 5,
  });
  t.after(() => loop.stop());

  await waitUntil(() => mock.requestsFor("/dsh/poll").length >= 1); // 挂载即首轮(空 commands)
  // 请求落地≠轮次落定:让 30ms 让应答处理完,否则手动 tick 撞单飞 guard 被静默吞
  // (compact.test.ts startSettled 同款纪律)
  await new Promise((r) => setTimeout(r, 30));
  const req = mock.requestsFor("/dsh/poll")[0]!;
  assert.deepEqual((req.body as { sessions: unknown[] })["sessions"], [{ sid: SID, idle_s: 0 }], "播种的静置会话进轮询名单");

  flag.on = true;
  await loop.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 1);
  assert.equal(
    (mock.requestsFor("/dsh/compacted")[0]!.body as Record<string, unknown>)["reason"],
    "expired-or-busy",
    "播种 busy 新鲜条目:窗内拒(保守——钟②=创建时刻起算)",
  );

  clock += 400_000; // 钟②出窗
  await seeder.seedOnce(); // 重播:快照仍 running=true(busy 保持),钟②不被播种推进
  await loop.tick();
  await waitUntil(() => mock.requestsFor("/dsh/compacted").length >= 2, 3000);
  assert.equal(
    (mock.requestsFor("/dsh/compacted")[1]!.body as Record<string, unknown>)["ok"],
    true,
    "钟②出窗=busyLive 成熟,执行(播种重播未救活挂死 busy——若播种错误推进钟②,此处将永拒)",
  );
  assert.equal(rec.calls.length, 1);
  assert.equal(rec.calls[0]!.agent, agent, "播种补的活引用进执行臂");
  flag.on = false;
});
