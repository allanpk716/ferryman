// 票06 · 压缩完成横幅——宿主半面状态仓（浏览器横幅的唯一事实源）。
//
// 触发源查证（dsh-research 克隆,只读）——「压缩完成」钉在轮询臂自己的成功点
// （执行道成功解析 + /dsh/compacted 上报送达）,不观察 session/event 流：
//   - 两执行道成功点同义（compact.ts 头注「执行」节钉点）：
//     · 命令道（首选,2026-10-07 返工）：commands.execute('/compact') 解析即
//       压缩收口——command-compact/src/index.ts:67 handler 直 await compactNow,
//       commands/src/index.ts:424-425 execute await handler settle;
//     · 服务面（次选）：compactNow(agent, signal) 成功解析：契约钉点
//       packages/compaction/compaction/src/index.ts:147（append standalone
//       `compaction/start` … 「That durable marker is the compaction lock until
//       one `compaction/end` attempt」——compaction/start 是锁标记,直到一次
//       compaction/end 尝试）+ :162-166（abstract compactNow 签名）;成功回值
//       CompactionResult（packages/compaction/compaction/src/types.ts:94）必带
//       endSeq（:104「The seq of the appended `compaction/end` event」）——解析时
//       compaction/end 已落事件流,其 error 字段缺省=成功收尾（types.ts:69-72）。
//   - 为什么不走 session/event 观察位：compaction 三事件 log-only、不进
//     surface（types.ts:3-4「…without entering the surface, so they are not
//     surface events」;surface 侧是紧随其后的替换 user/message 事件）,以事件流
//     为源还需 compactionId→会话配平并扩插件既有上报白名单（events.ts
//     buildEventBody）;轮询臂成功点与之等价且零新增事件依赖——票面授权的
//     最小触发源。
//   - 置位门=上报送达（reportCompacted true）：daemon 侧 compressed 标记未立时
//     「直接继续」是假承诺（闸门照拦）,不亮。
//
// 生命周期（一次性状态机,测试钉在 test/banner.test.ts）：
//   置位（compact.ts 成功路径）→ 浏览器经 ferrymanBlocked list 的信封 banner
//   布尔拉到即显示（client.js BlockedDock 横幅行,复用 conversation.composer.dock
//   槽+窄容器浮层先例）→ 用户下次发消息（events.ts onPreStep 用户步,允许/拦截
//   都算）清位 → 浏览器下轮拉到 absence 即消失;会话终局（onDisposed）同清
//   （blocked/handoffPending 同纪律,不跨会话泄漏）。

/** 每会话至多一条横幅（键=sid;Map 天然去重,重复置位=刷新时间戳） */
export class BannerStore {
  private readonly since = new Map<string, number>();

  /** 压缩成功+上报送达 → 亮（票06 触发源;compact.ts 成功路径调用） */
  set(sessionId: string): void {
    this.since.set(sessionId, Date.now());
  }

  /** 浏览器 wire 面:list 应答信封 banner 布尔（index.ts list 调用） */
  has(sessionId: string): boolean {
    return this.since.has(sessionId);
  }

  /** 用户步（下次发消息）或会话终局 → 撤（events.ts 调用） */
  clear(sessionId: string): void {
    this.since.delete(sessionId);
  }
}
