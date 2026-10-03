// 票 03 · 挂载自检 TDD（先红后绿）
// 自检事实依据（Ferryman 仓内现行实现,只读钉点）：
//   - 渡口无专用健康路由：internal/dock/server.go:401 ServeHTTP 只分流 /responses* 与 /v1/messages,
//     其余路径原样反代上游 ⇒ 「端口可连＝活」（watchdog 同款语义,internal/installer/watchdog.go:62/80）。
//   - daemon 版本＝GET /stats（管理口缺省 15700,internal/daemon/httpapi.go:197-199）,
//     需 Bearer token（<dataDir>/daemon.token,httpapi.go:255-265）,响应顶层 version 字段
//     （internal/daemon/health.go:70-73,空串回落 "dev"）。
//   - 版本三态：v 前缀 semver / "dev" / commit 描述;dev 系无从比较
//     （internal/update/semver.go:19-52 解析,update.go:134 CheckDevBuild）。
import { test } from "node:test";
import assert from "node:assert/strict";
import { compareVersions } from "../src/version.ts";
import { runSelfcheck } from "../src/selfcheck.ts";
import type { SelfcheckDeps, SelfcheckResult } from "../src/selfcheck.ts";

// ---- compareVersions 表驱动（照 internal/update/semver.go 语义） ----
const cmpCases: Array<{ a: string; b: string; want: number | null; note: string }> = [
  { a: "v0.5.2", b: "0.5.2", want: 0, note: "v 前缀剥除后相等" },
  { a: "0.5.2", b: "0.5.3", want: -1, note: "小版本落后" },
  { a: "0.10.0", b: "0.9.1", want: 1, note: "逐段数值比,非字典序" },
  { a: "1.0.0+build.5", b: "1.0.0", want: 0, note: "构建元数据不参与比较" },
  { a: "1.0.0-alpha", b: "1.0.0", want: -1, note: "有预发布 < 同号无预发布" },
  { a: "1.0.0-alpha", b: "1.0.0-beta", want: -1, note: "预发布字母段按字典序" },
  { a: "1.0.0-1", b: "1.0.0-2", want: -1, note: "预发布数字段按数值" },
  { a: "1.0.0-2", b: "1.0.0-10", want: -1, note: "预发布数字段按数值,非字典序" },
  { a: "1.0.0-1", b: "1.0.0-alpha", want: -1, note: "数字标识 < 字母标识" },
  { a: "dev", b: "0.5.2", want: null, note: "dev 构建无从比较" },
  { a: "70e8431", b: "0.5.2", want: null, note: "commit 描述无从比较" },
  { a: "0.5", b: "0.5.2", want: null, note: "段数不足三段解析失败" },
  { a: "abc", b: "0.5.2", want: null, note: "非版本串解析失败" },
];

for (const c of cmpCases) {
  test(`compareVersions(${c.a}, ${c.b}) = ${c.want} — ${c.note}`, () => {
    assert.equal(compareVersions(c.a, c.b), c.want);
  });
}

// ---- runSelfcheck 表驱动 ----
const DOCK = "http://127.0.0.1:15722";
const DAEMON = "http://127.0.0.1:15700";

type Route = (url: string, init?: RequestInit) => Promise<Response>;

function fakeFetch(routes: Record<string, Route>): typeof fetch {
  return ((url: string | URL, init?: RequestInit) => {
    const key = String(url);
    const route = routes[key];
    if (!route) return Promise.reject(new Error(`fakeFetch: 未注册路由 ${key}`));
    return route(key, init);
  }) as typeof fetch;
}

function jsonRoute(status: number, body: unknown): Route {
  return () => Promise.resolve(new Response(JSON.stringify(body), { status }));
}

// 两侧默认都成功；场景只覆盖受测路由，避免未注册路由误伤另一侧检查项
const defaultRoutes: Record<string, Route> = {
  [`${DOCK}/`]: jsonRoute(200, {}),
  [`${DAEMON}/stats`]: jsonRoute(200, { version: "v0.5.4" }),
};

function baseDeps(fetchImpl: typeof fetch): SelfcheckDeps {
  return {
    fetchImpl,
    dockURL: DOCK,
    daemonURL: DAEMON,
    daemonToken: "tok-123",
    minDaemonVersion: "0.5.3",
  };
}

function checkOf(result: SelfcheckResult, name: string) {
  const found = result.checks.find((c) => c.name === name);
  assert.ok(found, `缺少名为 ${name} 的检查项`);
  return found;
}

interface Scenario {
  name: string;
  routes: Record<string, Route>;
  wantOk: boolean;
  dockOk: boolean;
  daemonOk: boolean;
  detailIncludes?: { name: string; fragments: string[] };
}

const scenarios: Scenario[] = [
  {
    name: "渡口任一响应(200)即活",
    routes: { [`${DOCK}/`]: jsonRoute(200, {}) },
    wantOk: true,
    dockOk: true,
    daemonOk: true,
  },
  {
    name: "渡口反代上游 404/502 也算渡口活着",
    routes: { [`${DOCK}/`]: jsonRoute(502, { error: "upstream" }) },
    wantOk: true,
    dockOk: true,
    daemonOk: true,
  },
  {
    name: "渡口连不上＝未装/未启动,自检失败",
    routes: {
      [`${DOCK}/`]: () => Promise.reject(new TypeError("fetch failed: ECONNREFUSED")),
    },
    wantOk: false,
    dockOk: false,
    daemonOk: true,
    detailIncludes: { name: "dock", fragments: ["不可达", DOCK] },
  },
  {
    name: "daemon 版本达标(v0.5.4 ≥ 0.5.3)",
    routes: { [`${DAEMON}/stats`]: jsonRoute(200, { version: "v0.5.4" }) },
    wantOk: true,
    dockOk: true,
    daemonOk: true,
  },
  {
    name: "daemon 版本低于插件要求(v0.5.2 < 0.5.3)＝对账失败",
    routes: { [`${DAEMON}/stats`]: jsonRoute(200, { version: "v0.5.2" }) },
    wantOk: false,
    dockOk: true,
    daemonOk: false,
    detailIncludes: { name: "daemon-version", fragments: ["v0.5.2", "0.5.3"] },
  },
  {
    name: "daemon dev 构建无从比较＝对账跳过(放行并注明)",
    routes: { [`${DAEMON}/stats`]: jsonRoute(200, { version: "dev" }) },
    wantOk: true,
    dockOk: true,
    daemonOk: true,
    detailIncludes: { name: "daemon-version", fragments: ["无从比较"] },
  },
  {
    name: "daemon 401＝token 缺失/失效,自检失败",
    routes: { [`${DAEMON}/stats`]: jsonRoute(401, { error: "unauthorized" }) },
    wantOk: false,
    dockOk: true,
    daemonOk: false,
    detailIncludes: { name: "daemon-version", fragments: ["鉴权"] },
  },
  {
    name: "daemon 响应缺 version 字段＝自检失败",
    routes: { [`${DAEMON}/stats`]: jsonRoute(200, { unexpected: true }) },
    wantOk: false,
    dockOk: true,
    daemonOk: false,
    detailIncludes: { name: "daemon-version", fragments: ["version"] },
  },
  {
    name: "daemon 响应非 JSON＝自检失败",
    routes: {
      [`${DAEMON}/stats`]: () => Promise.resolve(new Response("<html>not json</html>", { status: 200 })),
    },
    wantOk: false,
    dockOk: true,
    daemonOk: false,
  },
  {
    name: "daemon 非法版本串(空串回落 dev 之外的场景按无从比较放行)",
    routes: { [`${DAEMON}/stats`]: jsonRoute(200, { version: "" }) },
    wantOk: true,
    dockOk: true,
    daemonOk: true,
    detailIncludes: { name: "daemon-version", fragments: ["无从比较"] },
  },
  {
    name: "daemon 500＝自检失败",
    routes: { [`${DAEMON}/stats`]: jsonRoute(500, { error: "boom" }) },
    wantOk: false,
    dockOk: true,
    daemonOk: false,
  },
  {
    name: "daemon 管理口连不上＝自检失败",
    routes: {
      [`${DAEMON}/stats`]: () => Promise.reject(new TypeError("fetch failed: ECONNREFUSED")),
    },
    wantOk: false,
    dockOk: true,
    daemonOk: false,
    detailIncludes: { name: "daemon-version", fragments: ["不可达"] },
  },
  {
    name: "两处都挂＝失败原因合并为用户可见消息",
    routes: {
      [`${DOCK}/`]: () => Promise.reject(new TypeError("fetch failed")),
      [`${DAEMON}/stats`]: jsonRoute(401, { error: "unauthorized" }),
    },
    wantOk: false,
    dockOk: false,
    daemonOk: false,
  },
];

for (const s of scenarios) {
  test(`runSelfcheck: ${s.name}`, async () => {
    const deps = baseDeps(fakeFetch({ ...defaultRoutes, ...s.routes }));
    const result = await runSelfcheck(deps);
    assert.equal(result.ok, s.wantOk, `整体判定 want ${s.wantOk}`);
    assert.equal(checkOf(result, "dock")!.ok, s.dockOk, "dock 检查项");
    assert.equal(checkOf(result, "daemon-version")!.ok, s.daemonOk, "daemon-version 检查项");
    if (s.detailIncludes) {
      for (const frag of s.detailIncludes.fragments) {
        assert.ok(
          checkOf(result, s.detailIncludes.name)!.detail.includes(frag),
          `${s.detailIncludes.name} detail 应含「${frag}」`,
        );
      }
    }
    if (s.wantOk) {
      assert.equal(result.userMessage, null, "全绿时不应有用户可见消息");
    } else {
      assert.ok(result.userMessage && result.userMessage.length > 0, "失败时必须有用户可见消息");
    }
  });
}

test("runSelfcheck: /stats 请求带 Bearer token（daemon.token 对账前提）", async () => {
  let seenAuth: string | null = null;
  const deps = baseDeps(
    fakeFetch({
      ...defaultRoutes,
      [`${DAEMON}/stats`]: (_url, init) => {
        seenAuth = (init?.headers as Record<string, string>)?.["Authorization"] ?? null;
        return Promise.resolve(new Response(JSON.stringify({ version: "v0.5.4" }), { status: 200 }));
      },
    }),
  );
  const result = await runSelfcheck(deps);
  assert.equal(result.ok, true);
  assert.equal(seenAuth, "Bearer tok-123");
});
