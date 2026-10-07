// 票05 · daemon 管理口 HTTP 客户端——五路（/dsh/gate、/dsh/event、/dsh/handoff、
// /dsh/poll、/dsh/compacted,后两路=票05 热压缩执行臂）的统一发送面。对面契约钉点（Ferryman 仓内,只读）：
//   - 端点与守门序：POST + Bearer <daemon.token> + loopback（internal/daemon/
//     dsh_receive.go:59-91;token 文件 <dataDir>/daemon.token,internal/daemon/
//     httpapi.go:35-54）;管理口缺省 15700。
//   - /dsh/gate 回话：{decision:allow|block,reason?,additional_context?,
//     suppressOriginalPrompt?,handoff_path?}（internal/daemon/gate.go:120-259,
//     表驱动契约 dsh_receive_test.go:70-141）——block 映射 reject（桥同款）
//     reason 即用户可见理由。
//   - /dsh/handoff 回话：{context:string|null, continuation?:boolean}
//     （internal/daemon/restore.go:80-111,
//     单候选注入截 6000 码点;多候选列清单;无候选 null;continuation=true=
//     续用档（restoreContinue 三分支恒带）——插件据此清欠账止问;旧 daemon
//     无此键=false=保守置账,旧行为）。
//   - /dsh/event 回话：{ok:true[,skipped:<因>]}——坏形静默收窄不 5xx
//     （dsh_receive.go:100-183）;skipped 语义=daemon 收下但不动作（如白名单外
//     事件）,非错误,不重试。
// 纪律：本模块永不抛错——一切故障折叠为 {ok:false} 回值,调用方据此 fail-open
//（hooks/ferryman-gate-codex.ps1:73 「连接拒绝/超时/任何非 200 → 放行」同款）。

export interface DaemonEndpoint {
  /** 管理口基址,如 http://127.0.0.1:15700 */
  baseURL: string;
  /** <dataDir>/daemon.token 内容（config.ts 读取接线） */
  token: string;
  /** 测试注入面;缺省全局 fetch */
  fetchImpl?: typeof fetch;
  /** 单请求超时;缺省 3000ms */
  timeoutMs?: number;
}

export type PostOutcome =
  | { ok: true; status: number; data: unknown }
  | { ok: false; status?: number; error: string };

async function postJSON(
  ep: DaemonEndpoint,
  path: string,
  body: unknown,
): Promise<PostOutcome> {
  const fetchImpl = ep.fetchImpl ?? fetch;
  try {
    const res = await fetchImpl(`${ep.baseURL}${path}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${ep.token}`,
      },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(ep.timeoutMs ?? 3000),
    });
    let data: unknown = null;
    try {
      data = await res.json();
    } catch {
      data = null; // 空/坏 JSON 回话按无体处理（对面 200 无体不视为故障）
    }
    if (!res.ok) {
      return { ok: false, status: res.status, error: `HTTP ${res.status}` };
    }
    return { ok: true, status: res.status, data };
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : String(e) };
  }
}

// ---- /dsh/gate 闸门问询 ----

/** Gate 决策回话（gate.go 全分支形状并集;additional_context=observe 警告） */
export interface GateDecision {
  decision?: string;
  reason?: string;
  additional_context?: string;
  suppressOriginalPrompt?: boolean;
  handoff_path?: string;
}

/**
 * 问闸门。任何故障（网络/非 200/坏形）回 null＝fail-open 放行——闸门绝不
 * 因 daemon 侧问题卡用户输入。
 */
export async function askGate(
  ep: DaemonEndpoint,
  body: { session_id: string; cwd: string; prompt: string },
): Promise<GateDecision | null> {
  const r = await postJSON(ep, "/dsh/gate", { ...body, transcript_path: "" });
  if (!r.ok || typeof r.data !== "object" || r.data === null) return null;
  return r.data as GateDecision;
}

// ---- /dsh/handoff 交接查询 ----

/**
 * /dsh/handoff 答话形状（夜链终局评审小修）：
 *   - md 非空＝交接 MD（Restore 侧已截 6000 码点,调用方注入）;
 *   - md=null 且 continuation=true＝续用档（daemon 判同会话续用,上下文已在
 *     本会话内零注入）——调用方据此清欠账止问,不再与"材料未到稍后重试"
 *     混为一谈;
 *   - md=null 且 continuation=false＝材料未到（含 daemon 不可达/故障/旧
 *     daemon 无键）——保守置账,既有重试行为零变化。
 */
export interface HandoffAnswer {
  md: string | null;
  continuation: boolean;
}

export async function askHandoff(
  ep: DaemonEndpoint,
  body: { cwd: string; session_id: string },
): Promise<HandoffAnswer> {
  const r = await postJSON(ep, "/dsh/handoff", body);
  // daemon 不可达/非 200/坏形：continuation 钉 false——不清账,保留重试。
  if (!r.ok || typeof r.data !== "object" || r.data === null) {
    return { md: null, continuation: false };
  }
  const d = r.data as Record<string, unknown>;
  const ctx = d["context"];
  return {
    md: typeof ctx === "string" && ctx.length > 0 ? ctx : null,
    continuation: d["continuation"] === true, // 无键（旧 daemon）/非 true → false
  };
}

// ---- /dsh/poll 指令轮询与 /dsh/compacted 压缩上报（票05 热压缩执行臂） ----

/** poll 应答里的单条指令（spec「架构与契约」:{action:"compact", session_id, cwd}） */
export interface PollCommand {
  action?: string;
  session_id?: string;
  cwd?: string;
}

/** poll 应答（commands=未过期指令,过期即丢弃在 daemon 侧;poll_hint_s=建议轮询
 *  间隔秒,下限 10s 插件侧托底;ttl_s=热窗复查 TTL 秒,插件侧可选覆盖） */
export interface PollResponse {
  commands?: PollCommand[];
  poll_hint_s?: number;
  ttl_s?: number;
}

/**
 * 取压缩指令。单次尝试,失败回 null＝轮询失败静默（spec 钉点:下轮再试）。
 */
export async function askPoll(
  ep: DaemonEndpoint,
  body: { agent: string; sessions: Array<{ sid: string; idle_s: number }> },
): Promise<PollResponse | null> {
  const r = await postJSON(ep, "/dsh/poll", body);
  if (!r.ok || typeof r.data !== "object" || r.data === null) return null;
  return r.data as PollResponse;
}

/** compacted 上报体（daemon 落账本 kind=compacted 全字段;ok=true 且 prefix_tokens
 *  非空时更新 gate 会话 compressed 标记——spec「架构与契约」） */
export interface CompactedReport {
  session_id: string;
  ok: boolean;
  reason?: string;
  prefix_tokens?: number;
  source?: string;
}

/**
 * 上报压缩结果。朴素失败策略（sendEvent 同款）：失败不阻塞、静默重试一次;
 * 两败俱败回 false（调用方 warn 一行）,永不抛。
 */
export async function reportCompacted(ep: DaemonEndpoint, body: CompactedReport): Promise<boolean> {
  let r = await postJSON(ep, "/dsh/compacted", body);
  if (!r.ok) {
    r = await postJSON(ep, "/dsh/compacted", body); // 静默重试一次
    if (!r.ok) return false;
  }
  return true;
}

// ---- /dsh/event 事件上报 ----

export interface EventSendResult {
  /** 200 回话（含 skipped 静默收窄）＝送达 */
  delivered: boolean;
  /** daemon 回话的 skipped 因（白名单外事件等;非错误） */
  skipped?: string;
  attempts: number;
}

/**
 * 上报事件。朴素失败策略（票面钉死）：失败不阻塞、静默重试一次;两败俱败
 * 回 delivered:false（调用方决定是否 warn）,永不抛。
 */
export async function sendEvent(
  ep: DaemonEndpoint,
  body: Record<string, unknown>,
): Promise<EventSendResult> {
  let r = await postJSON(ep, "/dsh/event", body);
  if (!r.ok) {
    r = await postJSON(ep, "/dsh/event", body); // 静默重试一次
    if (!r.ok) return { delivered: false, attempts: 2 };
  }
  let skipped: string | undefined;
  if (r.data !== null && typeof r.data === "object") {
    const s = (r.data as Record<string, unknown>)["skipped"];
    if (typeof s === "string") skipped = s;
  }
  return { delivered: true, skipped, attempts: 1 };
}
