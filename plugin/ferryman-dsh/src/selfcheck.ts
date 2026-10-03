// 挂载自检——渡口健康检查＋daemon 版本对账（票03）＋dsh 三口连通性探针（票05）。
// 目标端点事实（Ferryman 仓内现行实现,只读钉点）：
//   - 渡口无专用健康路由：internal/dock/server.go:401 ServeHTTP 只分流 /responses* 与
//     /v1/messages,其余一切路径原样反代上游 ⇒ 健康检查＝TCP 可达性探测
//     （「端口可连＝活」,watchdog 同款语义 internal/installer/watchdog.go:62/80）。
//     缺省口 127.0.0.1:15722（internal/config/config.go:527 [dock].listen）。
//   - daemon 版本＝GET /stats（管理口缺省 15700,internal/daemon/httpapi.go:197-199）,
//     须 Authorization: Bearer <token>,token 在 <dataDir>/daemon.token
//     （httpapi.go:35-54/255-265,失败 401 {"error":"unauthorized"}）。
//     响应顶层 version 字段（internal/daemon/health.go:70-91,空串回落 "dev"）。
//   - 版本三态：v 前缀 semver / "dev" / commit 描述;dev 系无从比较 → 放行并注明
//     （internal/update/semver.go:19-52 + update.go:134 CheckDevBuild 语义）。
// 本模块纯逻辑：网络经 fetchImpl 注入,测试不打真端口（真连通留票 05 mock daemon 测试面）。

import { compareVersions } from "./version.ts";

export interface SelfcheckDeps {
  fetchImpl: typeof fetch;
  /** 渡口基址,如 http://127.0.0.1:15722（[dock].listen 缺省值） */
  dockURL: string;
  /** daemon 管理口基址,如 http://127.0.0.1:15700 */
  daemonURL: string;
  /** <dataDir>/daemon.token 内容（读取接线在票 05） */
  daemonToken: string;
  /** 插件要求的 daemon 最低版本 */
  minDaemonVersion: string;
}

export type CheckName = "dock" | "daemon-version" | "dsh-ports";

export interface CheckResult {
  name: CheckName;
  ok: boolean;
  /** 用户可见说明（自检失败时经 ctx.logger 呈现——协议事实：插件用户可见通道就是 logger） */
  detail: string;
}

export interface SelfcheckResult {
  ok: boolean;
  checks: CheckResult[];
  /** 全绿为 null;有失败时为合并后的用户可见消息 */
  userMessage: string | null;
}

export const DEFAULT_DOCK_URL = "http://127.0.0.1:15722";
export const DEFAULT_DAEMON_URL = "http://127.0.0.1:15700";
// 最低 daemon 版本（票03 占位的票05 定案,留痕）:三口（/dsh/gate、/dsh/event、
// /dsh/handoff,internal/daemon/dsh_receive.go）＋跨源去重（dsh_dedup.go）均落
// v0.5.2 之后的夜链,首个承载发版＝v0.5.3（spec Out of Scope 预告的发版号）。
// dev/commit 描述构建无从比较放行（既有三态语义）,由 dsh-ports 探针对账真实
// 可达性——双保险。如实际发版号变动,此处随发版复核。
export const MIN_DAEMON_VERSION = "0.5.3";

const SELF_CHECK_TIMEOUT_MS = 3_000;

async function checkDock(deps: SelfcheckDeps): Promise<CheckResult> {
  try {
    // 任一路径都会被反代——只要 fetch 有响应（任意状态码）即渡口活着
    await deps.fetchImpl(`${deps.dockURL}/`, { signal: AbortSignal.timeout(SELF_CHECK_TIMEOUT_MS) });
    return { name: "dock", ok: true, detail: `渡口可达（${deps.dockURL}）` };
  } catch {
    return {
      name: "dock",
      ok: false,
      detail: `渡口不可达（${deps.dockURL}）——Ferryman 渡口未安装或未启动`,
    };
  }
}

async function checkDaemonVersion(deps: SelfcheckDeps): Promise<CheckResult> {
  let res: Response;
  try {
    res = await deps.fetchImpl(`${deps.daemonURL}/stats`, {
      headers: { Authorization: `Bearer ${deps.daemonToken}` },
      signal: AbortSignal.timeout(SELF_CHECK_TIMEOUT_MS),
    });
  } catch {
    return {
      name: "daemon-version",
      ok: false,
      detail: `daemon 管理口不可达（${deps.daemonURL}）——Ferryman daemon 未运行`,
    };
  }
  if (res.status === 401) {
    return {
      name: "daemon-version",
      ok: false,
      detail: "daemon 鉴权失败——daemon.token 缺失或失效（须重开 dsh 会话刷新挂载）",
    };
  }
  if (res.status !== 200) {
    return { name: "daemon-version", ok: false, detail: `daemon /stats 返回 HTTP ${res.status}` };
  }
  let body: unknown;
  try {
    body = await res.json();
  } catch {
    return { name: "daemon-version", ok: false, detail: "daemon /stats 响应不是 JSON" };
  }
  const raw =
    typeof body === "object" && body !== null
      ? (body as Record<string, unknown>)["version"]
      : undefined;
  if (typeof raw !== "string") {
    return { name: "daemon-version", ok: false, detail: "daemon /stats 响应缺少 version 字段" };
  }
  const cmp = compareVersions(raw, deps.minDaemonVersion);
  if (cmp === null) {
    // dev/未打标构建：无从比较（同 CheckDevBuild 语义）——放行并注明
    return {
      name: "daemon-version",
      ok: true,
      detail: `daemon 版本「${raw === "" ? "dev" : raw}」无从比较——版本对账跳过`,
    };
  }
  if (cmp < 0) {
    return {
      name: "daemon-version",
      ok: false,
      detail: `daemon 版本 ${raw} 低于插件最低要求 v${deps.minDaemonVersion}——请升级 ferryman`,
    };
  }
  return {
    name: "daemon-version",
    ok: true,
    detail: `daemon 版本 ${raw} 满足最低要求 v${deps.minDaemonVersion}`,
  };
}

// checkDshPorts 票05 挂载自检扩展：dsh 三口连通性检查。探针走 POST
// /dsh/handoff 空 cwd（Restore 对空 cwd 静默收窄回 context:null,internal/
// daemon/restore.go 空收窄分支＋dsh_receive_test.go「空 cwd」钉子）——零副作用
// （不 Touch、不记账、不占闸门计数）,又完整穿通三口共用的守门拦截面
// （loopback→POST→Bearer→JSON 解码→dsh 分派,dsh_receive.go:59-91）。三口
// 同拦截面,一探即三口对账;404＝daemon 版本过旧无 /dsh/* 面（版本对账的
// 真值兜底——dev 构建无从比较时以此探针为准）。
async function checkDshPorts(deps: SelfcheckDeps): Promise<CheckResult> {
  let res: Response;
  try {
    res = await deps.fetchImpl(`${deps.daemonURL}/dsh/handoff`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${deps.daemonToken}`,
      },
      body: JSON.stringify({ cwd: "", session_id: "ferryman-dsh-selfcheck" }),
      signal: AbortSignal.timeout(SELF_CHECK_TIMEOUT_MS),
    });
  } catch {
    return {
      name: "dsh-ports",
      ok: false,
      detail: `dsh 接收面不可达（${deps.daemonURL}/dsh/*）——daemon 未运行`,
    };
  }
  await res.arrayBuffer().catch(() => {}); // 读光 body（不留悬挂流）
  if (res.status === 401) {
    return {
      name: "dsh-ports",
      ok: false,
      detail: "daemon 鉴权失败——daemon.token 缺失或失效（须重开 dsh 会话刷新挂载）",
    };
  }
  if (res.status === 404) {
    return {
      name: "dsh-ports",
      ok: false,
      detail: `daemon 无 /dsh/* 接收面（版本过旧,需 ≥ v${deps.minDaemonVersion}）——请升级 ferryman`,
    };
  }
  if (res.status !== 200) {
    return { name: "dsh-ports", ok: false, detail: `/dsh/handoff 探针返回 HTTP ${res.status}` };
  }
  return {
    name: "dsh-ports",
    ok: true,
    detail: "dsh 接收面三口可达（gate/event/handoff 同拦截面,探针零副作用）",
  };
}

export async function runSelfcheck(deps: SelfcheckDeps): Promise<SelfcheckResult> {
  const checks = await Promise.all([checkDock(deps), checkDaemonVersion(deps), checkDshPorts(deps)]);
  const failures = checks.filter((c) => !c.ok);
  return {
    ok: failures.length === 0,
    checks,
    userMessage: failures.length === 0 ? null : failures.map((c) => c.detail).join("；"),
  };
}
