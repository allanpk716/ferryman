// 票 03 · 静态夹具测试——把插件协议事实与零依赖验收钉死。
// 协议事实出处见 src/manifest.ts、src/events.ts、src/index.ts 顶部钉点注释（dsh 调研克隆 file:line）。
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { PLUGIN_ID, PLUGIN_VERSION } from "../src/manifest.ts";
import { apply, registerHooks, selfcheckOnce } from "../src/index.ts";
import { makeEventDeps } from "../src/events.ts";
import type { DaemonEndpoint } from "../src/daemon.ts";

const here = dirname(fileURLToPath(import.meta.url));
const pkgRoot = join(here, "..");

function readPkg(): Record<string, unknown> {
  return JSON.parse(readFileSync(join(pkgRoot, "package.json"), "utf8")) as Record<string, unknown>;
}

test("manifest 形状钉夹具：package.json 满足 DshPackageManifest 必填与 dsh.bundle 形状", () => {
  // 协议事实：manifest 就是 package.json（packages/util/package-manifest/src/types.ts:8-27）,
  // dsh 键 = manifestVersion:1 + bundle.patch（同文件 :30-39）,官方桥同形
  // （packages/hooks/hooks-claude-code/package.json:13-23）。
  const pkg = readPkg();
  assert.equal(pkg["name"], PLUGIN_ID);
  assert.equal(pkg["version"], PLUGIN_VERSION);
  assert.equal(pkg["type"], "module");
  assert.ok(pkg["main"], "入口声明（main）");
  const dsh = pkg["dsh"] as Record<string, unknown>;
  assert.equal(dsh["manifestVersion"], 1);
  const bundle = dsh["bundle"] as Record<string, unknown>;
  assert.equal(bundle["patch"], "./cordis.patch.yml");
  const engines = pkg["engines"] as Record<string, unknown>;
  assert.ok(typeof engines["node"] === "string", "engines.node 声明（types.ts:57-66）");
});

test("零依赖钉夹具：无 dependencies/devDependencies 等任何依赖键,无 node_modules", () => {
  const pkg = readPkg();
  for (const key of ["dependencies", "devDependencies", "peerDependencies", "optionalDependencies"]) {
    assert.ok(!(key in pkg), `package.json 不得出现 ${key}（票验收：dev 亦无）`);
  }
  assert.ok(!existsSync(join(pkgRoot, "node_modules")), "不得出现 node_modules");
});

test("bundle patch 就位：cordis.patch.yml 含 - insert 与插件 id（publish.md:38-51 三件套）", () => {
  const yml = readFileSync(join(pkgRoot, "cordis.patch.yml"), "utf8");
  assert.ok(yml.includes("- insert"), "激活声明缺 - insert 行");
  assert.ok(yml.includes(PLUGIN_ID), "patch 未引用插件 id");
});

test("入口文件存在：main 指向的文件在盘上", () => {
  const pkg = readPkg();
  assert.ok(existsSync(join(pkgRoot, String(pkg["main"]))));
});

test("五事件位导出：events 模块导出五个钩子函数（票05 业务实现）", async () => {
  const events = (await import("../src/events.ts")) as Record<string, unknown>;
  for (const fn of ["onPreStep", "onCreated", "onSessionEvent", "onDisposed", "onStatus"]) {
    assert.equal(typeof events[fn], "function", `缺导出 ${fn}`);
  }
});

// 不可达 daemon 的 deps（fail-open 面专用;port 9 无监听,连接即拒）。
const deadDeps = () =>
  makeEventDeps(
    { baseURL: "http://127.0.0.1:9", token: "", fetchImpl: fetch, timeoutMs: 1000 } satisfies DaemonEndpoint,
    console,
  );

test("pre-step daemon 不可达 → fail-open 透传 next()（gate 脚本同纪律）", async () => {
  const { onPreStep } = await import("../src/events.ts");
  const decision = { kind: "enter", messages: [] } as const;
  const out = await onPreStep(deadDeps(), { agent: {}, messages: [{ content: [] }], turn: 0, step: 0 }, async () => decision);
  assert.equal(out, decision);
});

test("created 可 await 且 daemon 不可达不抛（serial awaited 位,creation 不得失败）", async () => {
  const { onCreated } = await import("../src/events.ts");
  await assert.doesNotReject(onCreated(deadDeps(), { agent: {} }));
});

test("registerHooks 挂接五事件位：事件名用 dsh 协议斜杠字符串", () => {
  // 协议事实：事件名 = 'agent/pre-step' 等斜杠串（packages/core/agent/src/runtime-types.ts:261/270/280/320;
  // session/event 见 packages/core/session/src/index.ts:77）;注册 API = ctx.on（hooks-claude-code/src/index.ts:209/225）。
  const registered: string[] = [];
  registerHooks({ on: (event) => void registered.push(event) }, deadDeps());
  assert.deepEqual(
    [...registered].sort(),
    ["agent/created", "agent/disposed", "agent/pre-step", "agent/status", "session/event"],
  );
});

test("apply 不因缺 on/logger 抛错（桥先例：logged and the agent continues）", () => {
  assert.doesNotThrow(() => apply({}, { daemonToken: "t", fetchImpl: (() => Promise.reject(new TypeError("x"))) as typeof fetch }));
});

test("selfcheckOnce 失败经 logger.error 用户可见,成功静默", async () => {
  const errors: string[] = [];
  const warns: string[] = [];
  const logger = {
    warn: (m: string) => void warns.push(m),
    error: (m: string) => void errors.push(m),
  };
  const failFetch = (() => Promise.reject(new TypeError("fetch failed"))) as typeof fetch;
  const ok = await selfcheckOnce({ logger }, { fetchImpl: failFetch });
  assert.equal(ok, false);
  assert.equal(errors.length, 1);
  assert.ok(errors[0]!.includes("挂载自检失败"));
  assert.ok(errors[0]!.includes("渡口"), "用户可见消息应点名渡口");

  const passFetch = (async (url: string | URL) => {
    const key = String(url);
    if (key.endsWith("/stats")) {
      return new Response(JSON.stringify({ version: "v9.9.9" }), { status: 200 });
    }
    return new Response("{}", { status: 200 });
  }) as typeof fetch;
  const ok2 = await selfcheckOnce({ logger }, { fetchImpl: passFetch, daemonToken: "t" });
  assert.equal(ok2, true);
  assert.equal(errors.length, 1, "成功不应再产生 error");
  assert.equal(warns.length, 0, "成功不应产生 warn（静默）");
});
