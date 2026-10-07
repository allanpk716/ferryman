// 票02 · 会话注册表纯单测——registry.ts 自 compact.ts 整段抽离后的稳定落点
//（纯搬家零行为变化;给后续播种票在此加用例）。compact.test.ts ⑦ 节留的是
// 五事件位集成面（经 events.ts handler）;这里只测注册表本体的纯行为,时钟注入。
import { test } from "node:test";
import assert from "node:assert/strict";
import { SessionRegistry } from "../src/registry.ts";
import { SessionRegistry as SessionRegistryViaCompact } from "../src/compact.ts";

test("compact.ts 转发导出与 registry.ts 同一实现（票02 搬家兼容红线）", () => {
  assert.equal(SessionRegistryViaCompact, SessionRegistry, "既有 import 面（events.ts/banner.test.ts/compact.test.ts）不断");
});

test("touch：新建登记起点;agent 引用只增不覆盖;空 cwd 不覆盖既有值;触碰刷闲置钟", () => {
  let clock = 1_000_000_000_000;
  const r = new SessionRegistry(() => clock);
  const agent = { session: { header: { id: "s1", cwd: "C:/proj" } } };
  r.touch("s1", "C:/proj", agent);
  clock += 5_000;
  assert.equal(r.idleS("s1"), 5, "created 起点起算闲置钟");
  r.touch("s1", "", undefined); // 空 cwd/无 agent 的触碰（session/event 残影形态）
  assert.equal(r.idleS("s1"), 0, "触碰=活动,闲置钟归零");
  const e = r.get("s1")!;
  assert.equal(e.cwd, "C:/proj", "空 cwd 不覆盖既有 cwd");
  assert.equal(e.agent, agent, "无 agent 引用的触碰不得抹掉既有活引用");
});

test("touch：既有条目遇 null agent 不覆盖;新条目缺省 agent=undefined、cwd 空串", () => {
  const r = new SessionRegistry();
  r.touch("s1", "", null);
  assert.equal(r.get("s1")!.agent, undefined, "新建时 agent ?? undefined");
  const agent = {};
  r.touch("s1", "C:/p", agent);
  r.touch("s1", "C:/p", null); // null 不得清掉活引用
  assert.equal(r.get("s1")!.agent, agent);
  assert.equal(r.get("s1")!.cwd, "C:/p");
});

test("setStatus：未知 sid 忽略;非 idle/running 值忽略;idle/running 置位", () => {
  const r = new SessionRegistry();
  assert.doesNotThrow(() => r.setStatus("ghost", "running"), "未知 sid 不炸");
  r.touch("s1", "C:/proj");
  r.setStatus("s1", "paused"); // 协议外值
  assert.equal(r.get("s1")!.busy, false, "非 idle/running 值忽略");
  r.setStatus("s1", "running");
  assert.equal(r.get("s1")!.busy, true);
  r.setStatus("s1", "idle");
  assert.equal(r.get("s1")!.busy, false);
});

test("idleS/list：注入时钟现算闲置秒;负值钳 0;未知 sid undefined;快照含 idleS", () => {
  let clock = 0;
  const r = new SessionRegistry(() => clock);
  assert.equal(r.idleS("ghost"), undefined, "未知 sid → undefined");
  r.touch("s1", "C:/proj");
  clock -= 9_500; // 时钟倒挂（不该发生,防御语义）
  assert.equal(r.idleS("s1"), 0, "负闲置钳 0");
  clock = 7_300;
  const snap = r.list();
  assert.equal(snap.length, 1);
  assert.equal(snap[0]!.sid, "s1");
  assert.equal(snap[0]!.idleS, 7, "list 快照现算闲置秒（向下取整）");
  assert.equal(snap[0]!.cwd, "C:/proj");
  assert.equal(snap[0]!.busy, false);
});

test("remove：终局移除;他会话不牵连;remove 后 get/idleS 归 undefined", () => {
  const r = new SessionRegistry();
  r.touch("s1", "C:/a");
  r.touch("s2", "C:/b");
  r.remove("s1");
  assert.equal(r.get("s1"), undefined);
  assert.equal(r.idleS("s1"), undefined);
  assert.equal(r.get("s2")!.sid, "s2", "他会话不受牵连");
  assert.doesNotThrow(() => r.remove("s1"), "重复移除幂等");
});
