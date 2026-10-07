// 票02 · 会话注册表——自 compact.ts 整段抽离（纯搬家,零行为变化;接口不扩不改,
// 给后续播种票的稳定落点）。五事件位维护（created/pre-step/session/event/
// status/disposed,handler 见 events.ts 各 handler 的 touch/setStatus/remove）;
// poll 体与执行前复查的唯一事实源。compact.ts 侧转发导出保既有 import 面
//（events.ts/test 从 compact.ts 取 SessionRegistry 不动）。

// ---- 会话注册表（五事件位维护;poll 体与复查的唯一事实源） ----

export interface RegistryEntry {
  sid: string;
  cwd: string;
  /** 宿主 agent 活引用——只活在本进程,绝不外泄出宿主面（blocked 仓同纪律） */
  agent: unknown;
  /** 闲置时钟起点（ms epoch;created/pre-step/session/event 触碰刷新） */
  lastActivityAt: number;
  /** agent/status 维护的运行位（true=running） */
  busy: boolean;
}

export type RegistrySnapshot = RegistryEntry & { idleS: number };

export class SessionRegistry {
  private readonly entries = new Map<string, RegistryEntry>();
  private readonly now: () => number;

  constructor(now: () => number = Date.now) {
    this.now = now;
  }

  /**
   * 登记/触碰：created（起点）、pre-step（用户步）、session/event（活动流）
   * 三处调用。agent 引用只增不覆盖（session/event 只有 SessionRef,不得抹掉
   * 既有 agent 活引用）;cwd 非空才覆盖（无 cwd 头的事件不得清掉既有值）。
   */
  touch(sid: string, cwd: string, agent?: unknown): void {
    const t = this.now();
    const e = this.entries.get(sid);
    if (e === undefined) {
      this.entries.set(sid, {
        sid,
        cwd: cwd || "",
        agent: agent ?? undefined,
        lastActivityAt: t,
        busy: false,
      });
      return;
    }
    if (cwd) e.cwd = cwd;
    if (agent !== undefined && agent !== null) e.agent = agent;
    e.lastActivityAt = t;
  }

  /** agent/status 忙位;未知 sid 忽略,非 idle/running 值忽略。 */
  setStatus(sid: string, status: unknown): void {
    const e = this.entries.get(sid);
    if (e === undefined) return;
    if (status === "running" || status === "idle") e.busy = status === "running";
  }

  /** 会话终局移除（agent/disposed;与 handoffPending/blocked 同纪律,不跨会话泄漏） */
  remove(sid: string): void {
    this.entries.delete(sid);
  }

  get(sid: string): RegistryEntry | undefined {
    return this.entries.get(sid);
  }

  /** 闲置秒数（向下取整,负值钳 0）;未知 sid → undefined。 */
  idleS(sid: string): number | undefined {
    const e = this.entries.get(sid);
    if (e === undefined) return undefined;
    return Math.max(0, Math.floor((this.now() - e.lastActivityAt) / 1000));
  }

  /** 快照（poll 体用）：含现算闲置秒。 */
  list(): RegistrySnapshot[] {
    const t = this.now();
    return [...this.entries.values()].map((e) => ({
      ...e,
      idleS: Math.max(0, Math.floor((t - e.lastActivityAt) / 1000)),
    }));
  }
}
