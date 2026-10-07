// 票02 · 会话注册表——自 compact.ts 整段抽离(纯搬家,接口不扩不改,
// 给后续播种票的稳定落点)。五事件位维护(created/pre-step/session/event/
// status/disposed,handler 见 events.ts 各 handler 的 touch/setStatus/remove);
// poll 体与执行前复查的唯一事实源。compact.ts 侧转发导出保既有 import 面
// (events.ts/test 从 compact.ts 取 SessionRegistry 不动)。
//
// 票03 · 三钟分工(spec「注册表全量播种(三钟分工终版)」节逐字,.scratch/
// dsh-registry-seed/spec.md):
//   钟① 守护文件面闲置钟(daemon 台账)——结构性独立,不在本文件。
//   钟② lastEventAt(新字段,事件面专属时钟):仅由事件面五事件位推进(touch/
//     setStatus 内推进);播种永不可触碰(seed* 原语一律不碰它);条目创建时
//     初始化为创建时刻——创建初始化不算触碰(round2 措辞澄清)。消费者=
//     busyLive 衰减唯一输入(busyLive := busy ∧ (now−lastEventAt)<300s)。
//   钟③ lastActivityAt(原闲置钟):事件 + 播种(seedAdvanceActivity,仅快照
//     updatedAt 实际推进时单调);消费者=纯信息性 idle_s 上报,无执行语义依赖。

// ---- 会话注册表(五事件位维护;poll 体与复查的唯一事实源) ----

/**
 * 事件钟新鲜窗 ms(票03 钟②:busyLive := busy ∧ (now−lastEventAt)<此窗;
 * 原先挂 idleS 的 300s 衰减语义随三钟分工迁到本常量,compact.ts/seed.ts 共用)。
 */
export const EVENT_FRESH_WINDOW_MS = 300_000;

export interface RegistryEntry {
  sid: string;
  cwd: string;
  /** 宿主 agent 活引用——只活在本进程,绝不外泄出宿主面（blocked 仓同纪律） */
  agent: unknown;
  /** 钟③·闲置时钟起点（ms epoch;created/pre-step/session/event 触碰刷新 +
   *  播种 seedAdvanceActivity 单调推进——票03） */
  lastActivityAt: number;
  /** 钟②·事件面专属时钟（ms epoch;仅 touch/setStatus 推进,播种永不可触碰,
   *  创建初始化不算触碰——票03 spec 逐字;busyLive 衰减唯一输入） */
  lastEventAt: number;
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
   * 票03:本方法属事件面——钟② lastEventAt 随之推进;播种路径绝不走此法。
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
        lastEventAt: t, // 钟②创建初始化(创建初始化不算触碰——票03 措辞)
        busy: false,
      });
      return;
    }
    if (cwd) e.cwd = cwd;
    if (agent !== undefined && agent !== null) e.agent = agent;
    e.lastActivityAt = t;
    e.lastEventAt = t; // 钟②:事件面推进
  }

  /**
   * agent/status 忙位;未知 sid 忽略,非 idle/running 值忽略。
   * 票03:合法置位同时推进钟②(事件面路径;协议外值连钟②也不动——onStatus
   * 的 touch 已代表事件本身,此处不重复计)。
   */
  setStatus(sid: string, status: unknown): void {
    const e = this.entries.get(sid);
    if (e === undefined) return;
    if (status === "running" || status === "idle") {
      e.busy = status === "running";
      e.lastEventAt = this.now(); // 钟②:事件面推进
    }
  }

  /** 会话终局移除（agent/disposed;与 handoffPending/blocked 同纪律,不跨会话泄漏） */
  remove(sid: string): void {
    this.entries.delete(sid);
  }

  get(sid: string): RegistryEntry | undefined {
    return this.entries.get(sid);
  }

  /** 闲置秒数（向下取整,负值钳 0）;未知 sid → undefined。钟③消费面(纯信息性 idle_s 上报)。 */
  idleS(sid: string): number | undefined {
    const e = this.entries.get(sid);
    if (e === undefined) return undefined;
    return Math.max(0, Math.floor((this.now() - e.lastActivityAt) / 1000));
  }

  /** 钟②·事件钟龄 ms（now−lastEventAt,负值钳 0）;未知 sid → undefined。
   *  busyLive 衰减窗唯一输入(票03;compact.ts 执行前复查与 seed.ts 护栏共用)。 */
  eventAgeMs(sid: string): number | undefined {
    const e = this.entries.get(sid);
    if (e === undefined) return undefined;
    return Math.max(0, this.now() - e.lastEventAt);
  }

  // ---- 播种原语(票03;seed.ts 专用,一律不碰钟② lastEventAt) ----

  /**
   * 播种新建:条目缺席时由 seed.ts 调用(已存在请走合并原语,勿覆盖重建)。
   * 钟③=activityAt ?? now(主路=快照 updatedAt/Plan B=播种时刻);钟②=创建
   * 时刻(创建初始化不算触碰);busy=显式给值(主路=快照 running,Plan B 不设
   * 即缺省 false);cwd/agent 直落。覆盖同名旧条目(dispose 后重播重建形态)。
   */
  seedRegister(
    sid: string,
    opts: { cwd?: string; agent?: unknown; busy?: boolean; activityAt?: number } = {},
  ): RegistryEntry {
    const t = this.now();
    const at = typeof opts.activityAt === "number" && Number.isFinite(opts.activityAt)
      ? opts.activityAt
      : t;
    const e: RegistryEntry = {
      sid,
      cwd: opts.cwd ?? "",
      agent: opts.agent ?? undefined,
      lastActivityAt: at,
      lastEventAt: t, // 钟②创建初始化(不算触碰)
      busy: opts.busy === true,
    };
    this.entries.set(sid, e);
    return e;
  }

  /** 播种合并·agent 只补空位(null/undefined 才设;活引用只增纪律同 touch)。 */
  seedFillAgent(sid: string, agent: unknown): void {
    if (agent === undefined || agent === null) return;
    const e = this.entries.get(sid);
    if (e === undefined) return;
    if (e.agent === undefined || e.agent === null) e.agent = agent;
  }

  /**
   * 播种合并·busy 覆盖(快照 running 校真陈旧);guard=true 表事件面 busy 且
   * 钟②新鲜——拒 false 覆盖(票03 护栏:新鲜事件不被快照 stale 误杀)。
   * seed.ts 负责算 guard(条目存在性+eventAgeMs),本原语只执行。
   */
  seedOverwriteBusy(sid: string, busy: boolean, guard: boolean): void {
    const e = this.entries.get(sid);
    if (e === undefined) return;
    if (busy === false && guard) return;
    e.busy = busy;
  }

  /** 播种合并·钟③单调推进:at 有限且 > 现值才设(不回拨——宁 stale 勿清零)。 */
  seedAdvanceActivity(sid: string, at: number): void {
    if (typeof at !== "number" || !Number.isFinite(at)) return;
    const e = this.entries.get(sid);
    if (e === undefined) return;
    if (at > e.lastActivityAt) e.lastActivityAt = at;
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
