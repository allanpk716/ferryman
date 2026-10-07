#!/usr/bin/env node
// driver.mjs — 票08 · DSH 热缓存压缩 E2E 全链剧本（tools/e2e_dsh/suite）。
//
// 驱动票07 沙箱栈（setup.sh 已整备：夜链工作树插件 + 秒级压缩参数）走完
// spec「E2E 验收栈」剧本并落断言（票面清单①-⑦，映射见文件尾 SUMMARY）：
//   登录 → 幂等新建会话 → 多轮灌上下文至 peak≥min_peak → 闲置等触发 →
//   断言 kind=compacted 账本行（字段齐）+ poll/compacted HTTP 往返（推证）→
//   横幅 DOM 出现（且随下次发消息消失）→ 拦窗内发消息不被拦（无选择卡 +
//   gate.log mode=compacted-short-prefix）→ 交接文件落盘 → 降级链回归
//   （压缩红利失效后照旧拦=选择卡出现；停 daemon 后消息照发=fail-open）。
//
// 零人工介入；断言失败时输出可定位诊断（账本行+gate.log+DOM 状态+截图）。
// 依赖：全局 playwright（本仓不装依赖）；env 契约见 run.sh（E2E_SUITE_*）。
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { setTimeout as sleep } from 'node:timers/promises';

// ---- env 契约 ----
const E = (k, d) => {
  const v = process.env[k];
  if (v === undefined || v === '') {
    if (d === undefined) throw new Error(`缺环境变量 ${k}（应由 run.sh 经 setup.sh 的 env.driver 导出）`);
    return d;
  }
  return v;
};
const ROOT = E('E2E_SUITE_ROOT');
const DATA = E('E2E_SUITE_DATA');
const SESS = E('E2E_SUITE_SESSIONS');
const LOGS = E('E2E_SUITE_LOGS');
const DAEMON_LOG = E('E2E_SUITE_DAEMON_LOG');
const DAEMON_PORT = Number(E('E2E_SUITE_DAEMON_PORT'));
const WEB_PORT = Number(E('E2E_SUITE_WEB_PORT'));
const WEB_TOKEN = E('E2E_SUITE_WEB_TOKEN');
const TTL_S = Number(E('E2E_SUITE_TTL_S'));
const TRIGGER_RATIO = Number(E('E2E_SUITE_TRIGGER_RATIO'));
const MIN_PEAK = Number(E('E2E_SUITE_MIN_PEAK'));
const COMMAND_TTL_RATIO = Number(E('E2E_SUITE_COMMAND_TTL_RATIO'));
const MARK_RATIO = Number(E('E2E_SUITE_MARK_RATIO'));
const BLOCK_S = Number(E('E2E_SUITE_BLOCK_S'));
const RUNSTAMP = E('E2E_SUITE_RUNSTAMP');

const ART = path.join(LOGS, `e2e-driver-${RUNSTAMP}`);
fs.mkdirSync(ART, { recursive: true });

const nowS = () => Date.now() / 1000;
const t0 = nowS();
function log(msg) {
  const el = Math.round(nowS() - t0);
  console.log(`[+${String(el).padStart(4)}s] ${msg}`);
}

// ---- 全局 playwright 解析（本仓零依赖；候选链：显式 env → 本地 → 全局 npm） ----
function loadPlaywright() {
  const require_ = createRequire(import.meta.url);
  const candidates = [
    process.env.PLAYWRIGHT_MODULE,
    'playwright',
    `${process.env.APPDATA || ''}/npm/node_modules/playwright`,
  ].filter(Boolean);
  for (const c of candidates) {
    try {
      const pw = require_(c);
      if (pw && pw.chromium) return pw;
    } catch { /* 试下一个 */ }
  }
  throw new Error(`playwright 不可用（试过: ${candidates.join(', ')}）——设 PLAYWRIGHT_MODULE 指向 playwright 包目录`);
}
const { chromium } = loadPlaywright();

// ms-playwright 缓存里的 chromium 可执行文件（默认解析失败时的兜底链）
function chromiumExeFallbacks() {
  const cache = path.join(process.env.LOCALAPPDATA || '', 'ms-playwright');
  if (!fs.existsSync(cache)) return [];
  const vers = fs.readdirSync(cache)
    .filter((d) => /^chromium-\d+$/.test(d))
    .sort((a, b) => Number(b.slice(9)) - Number(a.slice(9)));
  const exes = [];
  for (const v of vers) {
    for (const rel of ['chrome-win/chrome.exe', 'chrome-win64/chrome.exe']) {
      const p = path.join(cache, v, rel);
      if (fs.existsSync(p)) exes.push(p);
    }
  }
  return exes;
}
async function launchBrowser() {
  const opts = { headless: true, viewport: { width: 1440, height: 900 } };
  let lastErr;
  const attempts = [() => chromium.launch(opts)];
  for (const exe of chromiumExeFallbacks()) attempts.push(() => chromium.launch({ ...opts, executablePath: exe }));
  for (const a of attempts) {
    try { return await a(); } catch (e) { lastErr = e; }
  }
  throw lastErr;
}

// ---- 账本读取（沙箱数据目录只读；断言面=事实源） ----
function ledgerFiles() {
  const dir = path.join(DATA, 'accounts');
  if (!fs.existsSync(dir)) return [];
  return fs.readdirSync(dir).filter((f) => f.endsWith('.jsonl')).map((f) => path.join(dir, f));
}
function readRows() {
  const rows = [];
  for (const f of ledgerFiles()) {
    for (const line of fs.readFileSync(f, 'utf8').split('\n')) {
      const s = line.trim();
      if (!s) continue;
      try { rows.push(JSON.parse(s)); } catch { /* 坏行跳过 */ }
    }
  }
  return rows;
}
function rowsFor(sid, kind) {
  return readRows().filter((r) => r.session_id === sid && (kind === undefined || r.kind === kind));
}
/** 会话最新 usage 行的计费输入（三输入列之和）＝当前前缀规模（daemon 口径同源） */
function latestBilled(sid) {
  const rs = rowsFor(sid, 'usage').filter((r) => typeof r.ts === 'number').sort((a, b) => a.ts - b.ts);
  for (let i = rs.length - 1; i >= 0; i--) {
    const r = rs[i];
    const b = (r.input_tokens || 0) + (r.cache_read_tokens || 0) + (r.cache_creation_tokens || 0);
    if (b > 0) return b;
  }
  return 0;
}

// ---- 会话文件面（发现/统计；zstd 不解析，只 stat） ----
function listSessionDirs() {
  const out = [];
  if (!fs.existsSync(SESS)) return out;
  for (const proj of fs.readdirSync(SESS)) {
    const pd = path.join(SESS, proj);
    let st;
    try { st = fs.statSync(pd); } catch { continue; }
    if (!st.isDirectory()) continue;
    for (const s of fs.readdirSync(pd)) {
      if (!s.startsWith('session-')) continue;
      const sd = path.join(pd, s);
      try { if (fs.statSync(sd).isDirectory()) out.push(sd); } catch { /* 并发删除 */ }
    }
  }
  return out;
}
function sessionFileOf(dir) {
  let best = null;
  for (const f of fs.readdirSync(dir)) {
    if (!/^session(\.v\d+)?\.jsonl(\.zstd)?$/.test(f)) continue;
    const p = path.join(dir, f);
    const m = fs.statSync(p).mtimeMs;
    if (!best || m > best.m) best = { p, m };
  }
  return best; // {p, m} | null
}
const fstat = (p) => fs.statSync(p);
const mtimeS = (p) => fs.statSync(p).mtimeMs / 1000;

// ---- 通用等待器 ----
async function waitFor(name, pred, { timeoutMs = 120000, intervalMs = 2000, progMs = 30000 } = {}) {
  const start = nowS();
  let lastProgress = 0;
  for (;;) {
    const v = await pred();
    if (v) return v;
    if (nowS() - start > timeoutMs / 1000) {
      throw new Error(`等待超时［${name}］${Math.round(timeoutMs / 1000)}s（已等 ${Math.round(nowS() - start)}s）`);
    }
    if (nowS() - start - lastProgress >= progMs / 1000) {
      lastProgress = nowS() - start;
      log(`… 等［${name}］已 ${Math.round(lastProgress)}s`);
    }
    await sleep(intervalMs);
  }
}

// ---- 断言登记（票面清单映射；失败不中断=登记后由调用方决定） ----
const CHECKS = [];
function record(id, label, ok, detail = '') {
  CHECKS.push({ id, label, ok, detail: String(detail) });
  log(`${ok ? 'PASS' : 'FAIL'} [${id}] ${label}${detail ? ' — ' + detail : ''}`);
}
let fatal = null;

// ---- 诊断输出（断言失败可定位：账本行+gate.log+DOM 状态+截图） ----
const daemonToken = () => fs.readFileSync(path.join(DATA, 'daemon.token'), 'utf8').trim();
const gateLogPath = path.join(DATA, 'gate.log');
const gateLogLines = () => (fs.existsSync(gateLogPath) ? fs.readFileSync(gateLogPath, 'utf8').split('\n').filter((l) => l.trim()) : []);
const handoffDir = path.join(DATA, 'handoffs');
const tail = (p, n) => (fs.existsSync(p) ? fs.readFileSync(p, 'utf8').split('\n').slice(-n).join('\n') : '(不存在)');

let page = null;
const consoleErrs = [];
async function dumpDiagnostics(tag) {
  const sidLine = state.sid ? `\n# 会话 ${state.sid}\n` + rowsFor(state.sid).map((r) => JSON.stringify(r)).join('\n') : '\n#（会话未建）';
  const parts = [
    `# E2E 诊断 ${tag} @ ${new Date().toISOString()}`,
    `# 会话文件: ${state.sessFile || '(未发现)'}`
      + (state.sessFile ? ` size=${fstat(state.sessFile).size} mtime=${new Date(fstat(state.sessFile).mtimeMs).toISOString()} idle=${Math.round(nowS() - mtimeS(state.sessFile))}s` : ''),
    sidLine,
    `\n# gate.log 尾 25 行\n${gateLogLines().slice(-25).join('\n') || '(空)'}`,
    `\n# handoffs 目录\n${fs.existsSync(handoffDir) ? fs.readdirSync(handoffDir).join('\n') || '(空)' : '(不存在)'}`,
    `\n# daemon 日志尾 25 行\n${tail(DAEMON_LOG, 25)}`,
    `\n# web 日志尾 25 行\n${tail(path.join(LOGS, 'web.log'), 25)}`,
  ];
  fs.writeFileSync(path.join(ART, `diag-${tag}.txt`), parts.join('\n'));
  if (consoleErrs.length) {
    fs.appendFileSync(path.join(ART, `diag-${tag}.txt`),
      `\n\n# 页面 JS 错误（pageerror，尾 10）\n${consoleErrs.slice(-10).join('\n')}`);
  }
  if (page) {
    try {
      await page.screenshot({ path: path.join(ART, `diag-${tag}.png`), fullPage: false });
      const snap = await page.locator('body').ariaSnapshot().catch(() => '(ariaSnapshot 不可用)');
      fs.writeFileSync(path.join(ART, `diag-${tag}.aria.yml`), String(snap).split('\n').slice(0, 160).join('\n'));
    } catch { /* 页面可能已关 */ }
  }
  console.error(`\n===== 诊断已落 ${ART}/diag-${tag}.* =====`);
  console.error(`gate.log 尾 8 行：\n${gateLogLines().slice(-8).join('\n') || '(空)'}`);
  console.error(`本会话账本行（尾 8）：\n${state.sid ? rowsFor(state.sid).slice(-8).map((r) => JSON.stringify(r)).join('\n') : '(无)'}`);
}

// ---- daemon HTTP 面（/stats 观察 + P8 停 daemon） ----
async function daemonStats() {
  const r = await fetch(`http://127.0.0.1:${DAEMON_PORT}/stats`, {
    headers: { Authorization: `Bearer ${daemonToken()}` }, signal: AbortSignal.timeout(4000),
  });
  if (!r.ok) throw new Error(`daemon /stats HTTP ${r.status}`);
  return r.json();
}
async function stopDaemon() {
  await fetch(`http://127.0.0.1:${DAEMON_PORT}/shutdown`, {
    method: 'POST', headers: { Authorization: `Bearer ${daemonToken()}` }, signal: AbortSignal.timeout(5000),
  }).catch(() => {});
  await waitFor('daemon 端口关闭', async () => {
    try { await daemonStats(); return false; } catch { return true; }
  }, { timeoutMs: 20000, intervalMs: 1000 });
}

// ---- 浏览器半面（登录/发消息/DOM 探针/RPC） ----
// composer 名称两态（新会话页「描述你想要构建的内容…」/在会话中「发消息或创建
// 任务…」），以两态共有的「调用指令」锚定；lexical contenteditable，fill+Enter
const COMPOSER_RE = /调用指令/;
const state = { sid: null, sessDir: null, sessFile: null, compactRowsBefore: 0 };

/** 首跑弹窗（预览版说明／DeepSeek API Key 引导）在则关——非首跑静默跳过 */
async function dismissStartupDialogs() {
  for (const name of ['继续', '稍后配置', '知道了']) {
    const b = page.getByRole('button', { name, exact: true }).first();
    await b.click({ timeout: 4000 }).then(() => log(`已关弹窗「${name}」`)).catch(() => {});
  }
}
async function newSessionInUI() {
  // 点侧栏「新建会话」→ 聚焦空白会话（幂等：每次剧本新建，不依赖既有侧栏态）
  const btn = page.getByRole('button', { name: '新建会话' }).first();
  await btn.click({ timeout: 30000 });
  await page.getByRole('textbox', { name: COMPOSER_RE }).waitFor({ state: 'visible', timeout: 30000 });
}
async function sendMsg(text) {
  const box = page.getByRole('textbox', { name: COMPOSER_RE });
  await box.click({ timeout: 15000 }).catch(async () => {
    await box.click({ timeout: 5000, force: true }); // 选择卡浮层遮 composer 时兜底
  });
  await box.fill(text);
  await box.press('Enter');
}
/** 等一轮对话完成：会话文件出现增量且尺寸稳定（机器产出落盘=回合收口；UI 无关） */
async function waitTurnDone(sizeBefore, { minGrow = 150, timeoutS = 240, label = '回合' } = {}) {
  const start = nowS();
  let size = sizeBefore;
  let stableSince = null;
  let lastSize = -1;
  for (;;) {
    try { size = fstat(state.sessFile).size; } catch { /* 换代瞬间 */ }
    if (size >= sizeBefore + minGrow) {
      if (size === lastSize) {
        if (stableSince === null) stableSince = nowS();
        if (nowS() - stableSince >= 6) return size; // 6s 无新增字节＝回合收口
      } else stableSince = null;
      lastSize = size;
    }
    if (nowS() - start > timeoutS) {
      throw new Error(`等待${label}超时 ${timeoutS}s（size ${sizeBefore}→${size}）`);
    }
    await sleep(1000);
  }
}
async function cardCount() {
  return await page.locator('[data-ferryman-blocked]').count();
}
async function bannerCount() {
  return await page.locator('[data-ferryman-banner]').count();
}
/** 经页面同源 cookie 调宿主 RPC（client.js 同款信封）——卡面/横幅的 wire 真值 */
async function rpcList() {
  return await page.evaluate(async (sid) => {
    const r = await fetch('/api/ferrymanBlocked/list', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ type: 'client-request', rpcId: 'e2e-' + Math.random(), method: 'ferrymanBlocked/list', payload: { args: { sessionId: sid } } }),
    });
    return await r.json();
  }, state.sid);
}

// ---- 灌上下文素材（多变体写作任务；纯文字回复不触工具） ----
const TOPICS = [
  '丝绸之路的历史脉络与贸易品', '城市地下水系统的工作原理', '咖啡从种子到杯子的一生',
  '潮汐的成因与沿海生态', '印刷术如何改变欧洲社会', '蜂群决策的群体智慧',
  '铁路信号系统的演进史', '深海热泉生态圈', '古代灯塔的地中海航路意义', '疫苗冷链物流的难点',
];

// ================================================================ 主线
(async () => {
  log(`E2E 剧本启动：web=127.0.0.1:${WEB_PORT} daemon=127.0.0.1:${DAEMON_PORT} min_peak=${MIN_PEAK} ttl=${TTL_S}s`);
  log(`诊断目录：${ART}`);

  // ---- P0 登录 + 插件自检面 ----
  const browser = await launchBrowser();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  page = await ctx.newPage();
  page.on('pageerror', (e) => consoleErrs.push(String(e)));
  await page.goto(`http://127.0.0.1:${WEB_PORT}/?token=${encodeURIComponent(WEB_TOKEN)}`, { waitUntil: 'domcontentloaded', timeout: 60000 });
  await page.getByRole('textbox', { name: COMPOSER_RE }).waitFor({ state: 'visible', timeout: 90000 });
  await dismissStartupDialogs();
  await page.getByRole('textbox', { name: COMPOSER_RE }).waitFor({ state: 'visible', timeout: 30000 });
  log('登录完成，composer 就绪');

  // ---- 发消息并等回合归宿（文件增长=跑过 / 选择卡出现=被拦） ----
  async function sendAndWaitTurn(text, { label = '回合', timeoutS = 240 } = {}) {
    const size0 = fstat(state.sessFile).size;
    const cards0 = await cardCount();
    await sendMsg(text);
    const r = await waitFor(`${label}归宿`, async () =>
      (await cardCount()) > cards0 ? 'blocked' : fstat(state.sessFile).size >= size0 + 1000 ? 'ran' : false,
      { timeoutMs: timeoutS * 1000, intervalMs: 1500 });
    if (r === 'ran') await waitTurnDone(size0, { label });
    return r;
  }

  // ---- P0.5 幂等建会话（陈旧草稿自愈：工作区会持久化上次页载的自动草稿，点
  // 「新建会话」可能聚焦该旧草稿——旧闲置锚直接把首条消息拦死。验鲜=本会话
  // 账本零历史（首行不早于本 run）；陈旧则再点一次新建（脏草稿不再复用）） ----
  const marker = `e2e-${RUNSTAMP}`;
  let msg1BlockedOrStale = false;
  for (let attempt = 1; attempt <= 3; attempt++) {
    await newSessionInUI();
    await sendMsg(`你好。这是一次自动化验收会话（标记 ${marker}）。请只回复一句话：收到。不要使用任何工具。`);
    const cand = await waitFor('会话代文件落盘（本 run 内被写）', async () => {
      const cands = listSessionDirs()
        .map((d) => ({ d, f: sessionFileOf(d) }))
        .filter((x) => x.f && x.f.m / 1000 > t0 - 30)
        .sort((a, b) => b.f.m - a.f.m);
      return cands.length ? cands[0] : false;
    }, { timeoutMs: 120000, intervalMs: 1500 });
    state.sessDir = cand.d;
    state.sid = path.basename(state.sessDir);
    state.sessFile = cand.f.p;
    // 验鲜：无本 run 之前的账本行、无 block 行（被拦消息不产回复，waitTurn 会被
    // inbox 落盘假满足——以账本/卡面为准）
    const rows = rowsFor(state.sid);
    const stale = rows.some((r) => typeof r.ts === 'number' && r.ts < t0 - 10) ||
      rows.some((r) => r.kind === 'block');
    const blockedNow = (await cardCount()) > 0;
    if (!stale && !blockedNow) {
      await waitTurnDone(fstat(state.sessFile).size, { label: '首条回复' });
      log(`会话已建（第 ${attempt} 次尝试）：${state.sid}，首条回复已收`);
      msg1BlockedOrStale = false;
      break;
    }
    log(`第 ${attempt} 次拿到的会话陈旧/被拦（rows=${rows.length} blocked=${blockedNow}）——再点新建会话重试`);
    msg1BlockedOrStale = true;
  }
  if (msg1BlockedOrStale) throw new Error('3 次尝试均拿到陈旧草稿会话（工作区残留？）——见诊断');
  record('p0', '幂等新建会话（陈旧草稿自愈重试）', true, state.sid);

  // ---- P1 灌上下文至 peak ≥ min_peak ----
  let billed = 0;
  for (let i = 0; i < TOPICS.length && billed < MIN_PEAK + 800; i++) {
    const r = await sendAndWaitTurn(`请用中文写一篇约500字的短文，主题：${TOPICS[i]}。要求分段、内容具体、直接输出正文，不要使用任何工具。`, { label: `第${i + 2}轮` });
    if (r === 'blocked') throw new Error(`第${i + 2}轮被闸门拦（新鲜会话不应拦——触发/交接竞态？）——见诊断`);
    billed = latestBilled(state.sid);
    log(`第${i + 2}轮完成，账面前缀≈${billed} tokens（目标 ${MIN_PEAK + 800}）`);
  }
  record('ctx', `灌上下文至峰值≥min_peak（${MIN_PEAK}）`, billed >= MIN_PEAK, `账面前缀≈${billed}`);
  if (billed < MIN_PEAK) throw new Error(`上下文灌不够：${billed} < ${MIN_PEAK}（模型回复过短？）`);

  // ---- P2 闲置触发 → kind=compacted 账本行 ----
  state.compactRowsBefore = rowsFor(state.sid, 'compacted').length;
  const triggerLine = nowS();
  log(`进入闲置等待：触发线=${TRIGGER_RATIO * TTL_S}s，指令有效期=${COMMAND_TTL_RATIO * TTL_S}s，插件轮询≤10s`);
  let compactRow = null;
  let engineDead = false; // 票05 已知缺陷签名：连续多条 ok:false reason:"error"
  await waitFor('kind=compacted 账本行', async () => {
    const rows = rowsFor(state.sid, 'compacted');
    const fresh = rows.slice(state.compactRowsBefore);
    if (fresh.length) {
      const last = fresh[fresh.length - 1];
      const errs = fresh.filter((r) => r.ok !== true).length;
      if (last.ok === true) { compactRow = last; return true; }
      // 连续 3 条失败上报（每轮询周期一条）＝指令链路通、执行臂反复失败——不再干等
      if (errs >= 3 && fresh.every((r) => r.reason === 'error')) { engineDead = true; compactRow = last; return true; }
    }
    return false;
  }, { timeoutMs: (TRIGGER_RATIO * TTL_S + 150) * 1000, intervalMs: 2000 });

  if (engineDead) {
    // 钉死的根因链（2026-10-07 夜链真机实证，诊断见 diag-rootcause.txt）：
    //   daemon 触发✓→poll 派发✓→插件执行臂 resolveCompaction=UNDEFINED→上报
    //   ok:false reason:"error"。宿主 web 组成里 compaction 服务不活在插件域：
    //   web-app bundle 把 compaction-basic 从 host 面摘掉、presets patch 用
    //   cordis:group isolate:{compaction:true} 隔离在 preset 组内
    //   （packages/bundle/web-app/presets/cordis.patch.yml）——插件层
    //   ctx.inject('compaction') 与 agent.ctx.get('compaction') 都取不到
    //   （实测 UNDEFINED）；宿主自己的 /compact 命令（组内 command-compact）
    //   正常。修复属票05 插件面（如改走 agent.followup('/compact') 原生命令道
    //   并观察 compaction/end 上报），suite 无法在不造假的前提下绕过。
    const note = '宿主 compaction 服务不可达（preset 组隔离）——票05 插件 resolveCompaction 恒 UNDEFINED，见 diag-rootcause';
    record('a1', '① 账本 kind=compacted 行存在且字段齐（session_id/ok/prefix_tokens）', false, note);
    record('a2', '② poll→执行→compacted HTTP 往返（指令链通、执行臂失败）', false,
      `触发/派发/上报链全通（${rowsFor(state.sid, 'compacted').length - state.compactRowsBefore} 条上报），compactNow 执行失败 reason=error`);
    // 压缩未成功→③/④ 依赖面（横幅置位源=压缩成功+上报送达；gate compressed 标记
    // =ok:true 且 prefix>0）结构性不可达——如实记 FAIL 而非静默缺席
    record('b1', '③ 横幅 DOM 出现（[data-ferryman-banner]）', false, note);
    record('b2', '③b 横幅随下次发消息消失', false, '压缩未成功，横幅永不置位');
    record('c1', '④ 拦窗内发消息不被拦（回合执行、无选择卡）', false,
      '无 compressed 标记→照旧走 legacy 拦截（降级链·甲已验）');
    record('c2', '④ gate.log 放行痕（mode=compacted-short-prefix 新行）', false, note);
    fs.writeFileSync(path.join(ART, 'diag-rootcause.txt'),
      `kind=compacted 连续失败（指令链通、执行失败）——根因=宿主 web 组成将 compaction 服务隔离在 preset 组内\n` +
      `（web-app presets patch: cordis:group isolate:{compaction:true}），插件域 ctx.inject 与 agent.ctx.get 均不可达（实测 UNDEFINED）。\n` +
      `宿主自身 /compact 命令正常（command-compact 在组内）。修复属票05 插件面。\n\n` +
      `本会话 compacted 行：\n${rowsFor(state.sid, 'compacted').map((r) => JSON.stringify(r)).join('\n')}\n`);
    await dumpDiagnostics('rootcause');
  } else {
    const compactTs = compactRow.ts;
    const f_ok = compactRow.ok === true;
    const f_prefix = typeof compactRow.prefix_tokens === 'number' && compactRow.prefix_tokens > 0;
    const f_source = compactRow.source === 'host-plugin';
    const f_sid = compactRow.session_id === state.sid;
    record('a1', '① 账本 kind=compacted 行存在且字段齐（session_id/ok/prefix_tokens）',
      f_ok && f_prefix && f_sid, `ok=${compactRow.ok} prefix_tokens=${compactRow.prefix_tokens} source=${compactRow.source}`);
    record('a2', '② poll→执行→compacted HTTP 往返（daemon 触发日志 + 上报行推证）', f_ok && f_prefix,
      `触发→上报 ${Math.round(nowS() - triggerLine)}s；daemon.log 触发行=${(tail(DAEMON_LOG, 400).match(/\[compact\] dsh 触发/g) || []).length}`);
    if (!f_ok) throw new Error(`压缩上报失败：${JSON.stringify(compactRow)}`);
    if (!f_prefix) throw new Error(`compacted 行缺 prefix_tokens（插件读不到投影→gate 放行链必断）：${JSON.stringify(compactRow)}`);
    log(`compacted 行：ok=true prefix=${compactRow.prefix_tokens}（触发→上报 ${Math.round(nowS() - triggerLine)}s）`);

    // ---- P3 横幅 DOM ----
    await waitFor('横幅 [data-ferryman-banner]', async () => (await bannerCount()) > 0, { timeoutMs: 30000, intervalMs: 1500 });
    const bannerText = (await page.locator('[data-ferryman-banner]').first().innerText().catch(() => '')) || '';
    record('b1', '③ 横幅 DOM 出现（[data-ferryman-banner]，浏览器 4s 轮询内）', true, bannerText.trim().slice(0, 40));

    // ---- P4+P5 拦窗内发消息不被拦（compressed 放行窗口） ----
    // 窗口＝[压缩完成+blockS+5, 压缩完成+markRatio×TTL−12]；idle 钟以会话文件 mtime 为准
    const windowDeadline = compactTs + MARK_RATIO * TTL_S - 12;
    await waitFor('进入放行窗口（idle≥block_s ∧ compressed 标记未过期）', async () =>
      nowS() - mtimeS(state.sessFile) >= BLOCK_S + 5 && nowS() <= windowDeadline,
      { timeoutMs: Math.max(60, (windowDeadline - nowS()) * 1000 + 30000), intervalMs: 2000 });
    const gateLinesBefore = gateLogLines().length;
    const statsBefore = await daemonStats();
    const cardsBeforeP5 = await cardCount();
    log(`放行窗口内发消息（idle=${Math.round(nowS() - mtimeS(state.sessFile))}s，标记余 ${Math.round(windowDeadline - nowS())}s）`);
    const size0 = fstat(state.sessFile).size;
    await sendMsg('继续：请用一句话概括我们刚才聊过的主题。不要使用任何工具。');
    // 不被拦=回合真跑了（文件增长）；被拦=选择卡出现
    const blockedOrDone = await waitFor('放行消息归宿（文件增长=放行 / 选择卡=被拦）', async () =>
      (await cardCount()) > cardsBeforeP5 ? 'blocked' : fstat(state.sessFile).size >= size0 + 1000 ? 'allowed' : false,
      { timeoutMs: 180000, intervalMs: 1500 });
    let p5ok = blockedOrDone === 'allowed';
    if (p5ok) { await waitTurnDone(size0, { label: '放行回合收口' }); }
    const gateNew = gateLogLines().slice(gateLinesBefore);
    const allowLine = gateNew.find((l) => l.includes('mode=compacted-short-prefix'));
    record('c1', '④ 拦窗内发消息不被拦（回合执行、无选择卡）', p5ok,
      p5ok ? '文件增长=回合已跑' : `出现选择卡×${await cardCount()}`);
    record('c2', '④ gate.log 放行痕（mode=compacted-short-prefix 新行）', Boolean(allowLine),
      allowLine || `gate 新行：${gateNew.join(' | ') || '(无)'}`);
    // 横幅随下次用户消息消失（已知取舍：最长一个浏览器轮询周期 4s 的可视滞后，容忍 12s）
    if (p5ok) {
      const gone = await waitFor('横幅随用户消息消失', async () => (await bannerCount()) === 0,
        { timeoutMs: 12000, intervalMs: 1500 }).then(() => true).catch(() => false);
      record('b2', '③b 横幅随下次发消息消失（宿主清位→浏览器下轮拉到 false）', gone, '');
    } else {
      record('b2', '③b 横幅随下次发消息消失（宿主清位→浏览器下轮拉到 false）', false, '消息被拦，横幅生命周期断');
    }
    // gate 往返（/stats 计数）+ stats 快照留档
    const statsAfter = await daemonStats();
    const gateCallsDelta = (statsAfter.gate_calls_by_agent?.dsh || 0) - (statsBefore.gate_calls_by_agent?.dsh || 0);
    record('c3', '②b gate HTTP 往返（/stats dsh 调用计数递增）', gateCallsDelta >= 1, `Δ=${gateCallsDelta}`);
    if (!p5ok || !allowLine) throw new Error('P5 放行链断——见上 FAIL 项');
  }

  // ---- P6 交接文件落盘 ----
  const runStartMs = (t0 - 5) * 1000;
  const hFiles = fs.existsSync(handoffDir)
    ? fs.readdirSync(handoffDir).filter((f) => {
        try { return fs.statSync(path.join(handoffDir, f)).mtimeMs >= runStartMs; } catch { return false; }
      }) : [];
  record('d1', '⑤ 交接文件落盘（handoffs/ 有本 run 新文件）', hFiles.length > 0, hFiles.slice(0, 3).join(', '));

  // ---- P7 降级链回归·甲：压缩红利失效后照旧拦（选择卡兜底） ----
  // 前提=「压缩红利失效」：等闲置过线 ∧ 最后一条 ok 压缩上报的 compressed 标记
  // （mark_ratio×TTL）已过期。返工注记（2026-10-07）：上报加 2s 落盘稳定窗后
  // 标记不再自杀,④ 回合后的 watcher 二次压缩会以新鲜标记盖住本幕——只等
  // 闲置会把「照旧拦」测成「红利仍在、正确放行」（第二发无 pending 必不拦,
  // 30s 超时崩）,与本断言命题不符;两发序内再遇标记落地的窄竞态以重试收口。
  const markLapsed = async () => {
    const comps = rowsFor(state.sid, 'compacted').filter((r) => r.ok === true && typeof r.ts === 'number');
    const last = comps.at(-1);
    return last === undefined || nowS() >= last.ts + MARK_RATIO * TTL_S + 6;
  };
  const waitLegacyWindow = (label) =>
    waitFor(`${label}（闲置≥block_s ∧ 压缩标记已失效）`, async () =>
      nowS() - mtimeS(state.sessFile) >= BLOCK_S + 5 && markLapsed(),
      { timeoutMs: (MARK_RATIO * TTL_S + BLOCK_S + 120) * 1000, intervalMs: 3000 });
  log('降级甲：等闲置过线+压缩标记失效（进入 legacy 拦窗）');
  await waitLegacyWindow('legacy 拦窗');
  const statsP7a = engineDead ? await daemonStats().catch(() => null) : null;
  const blockMarker = `E2E被拦标记${RUNSTAMP.slice(-6)}`;
  let blocked = false;
  for (let attempt = 1; attempt <= 3 && !blocked; attempt++) {
    const sizeA = fstat(state.sessFile).size;
    const gateLen0 = gateLogLines().length; // 只看本发新增行——④ 幕的旧放行行不误判
    await sendMsg(`${blockMarker}${attempt > 1 ? `-${attempt}` : ''}：随便回一句话。`);
    const r5 = await waitFor('降级甲第一发归宿', async () =>
      (await cardCount()) > 0 ? 'blocked' : fstat(state.sessFile).size >= sizeA + 1000 ? 'allowed' : false,
      { timeoutMs: 180000, intervalMs: 1500 });
    blocked = r5 === 'blocked';
    if (blocked) break;
    await waitTurnDone(sizeA, { label: '放行回合' });
    const gateNewText = gateLogLines().slice(gateLen0).join(' ');
    if (gateNewText.includes('compacted-short-prefix')) {
      // 放行回合自身可能再触发一次热压缩（peak 回升过线）→ 新鲜标记落地——
      // 等它失效后重试两发序（标记窗口内「照旧拦」本就不成立,非剧本目标）
      log(`第 ${attempt} 发撞上在效压缩标记（gate 放行）——等标记失效后重试`);
      await waitLegacyWindow('legacy 拦窗·重试');
      continue;
    }
    log('第一发=分支7 警告放行（无新鲜交接）——紧接第二发验拦截');
    const sizeB = fstat(state.sessFile).size;
    await sendMsg(`${blockMarker}${attempt > 1 ? `-${attempt}` : ''}-2：再随便回一句话。`);
    await waitFor('降级甲第二发被拦（选择卡出现）', async () => (await cardCount()) > 0, { timeoutMs: 30000, intervalMs: 1500 });
    blocked = true;
    void sizeB;
  }
  const blockRows = rowsFor(state.sid, 'block');
  const cardText = await page.locator('[data-ferryman-blocked]').first().innerText().catch(() => '');
  record('e1', '⑥ 降级链·甲：闲置过线后照旧拦（[data-ferryman-blocked] 选择卡出现）', blocked, cardText.split('\n')[0]?.slice(0, 40) || '');
  record('e2', '⑥ 账本 kind=block 行落账（legacy 拦截兜底在账）', blockRows.length > 0, blockRows.length ? `共${blockRows.length}行` : '无');
  // 卡面 wire 真值：被拦原话在卡上（RPC list）
  const rpc1 = await rpcList().catch(() => null);
  const cardPromptHit = rpc1?.result?.value?.cards?.some((c) => (c.prompt || '').includes(blockMarker));
  record('e3', '⑥ 选择卡携带被拦原话（RPC list wire 真值）', Boolean(cardPromptHit), '');
  if (engineDead && statsP7a) {
    // engineDead 分支下 P5 未跑——gate 往返证据改在降级链测（msg5/msg6 各过一次闸门）
    const statsP7b = await daemonStats().catch(() => null);
    if (statsP7b) {
      const d = (statsP7b.gate_calls_by_agent?.dsh || 0) - (statsP7a.gate_calls_by_agent?.dsh || 0);
      record('c3', '②b gate HTTP 往返（/stats dsh 调用计数递增；降级链两发各过闸门）', d >= 1, `Δ=${d}`);
    }
  }

  // ---- P8 降级链·乙：停 daemon → 消息照发（fail-open，插件不因 daemon 死卡用户） ----
  // 发送路径二选一：有在办选择卡时走卡片「强续重发」动作（卡面浮层本就遮着
  // composer，顺手把卡片代发链也验了）；否则直接发。
  const cardsBeforeP8 = await cardCount();
  await stopDaemon();
  log('daemon 已停（插件-daemon 通道断）——发消息验 fail-open');
  const sizeC = fstat(state.sessFile).size;
  let viaCard = false;
  if ((await cardCount()) > 0) {
    await page.getByRole('button', { name: /强续重发/ }).first().click({ timeout: 15000 }).catch(() => {});
    viaCard = true;
    log('经选择卡「强续重发」代发——若 25s 内未起回合（代发通道可能抛错）回落手打「强续」');
    for (let i = 0; i < 25 && fstat(state.sessFile).size < sizeC + 1000; i++) await sleep(1000);
  }
  if (fstat(state.sessFile).size < sizeC + 1000) {
    if (viaCard) log('卡片代发未起回合（宿主侧 followup 抛错，卡片自带降级指引）——手打「强续」重发');
    await sendMsg('强续 daemon 现在不可达：请只回复「fail-open 正常」。不要使用任何工具。');
  }
  let failOpenOk = false;
  try {
    await waitTurnDone(sizeC, { label: 'fail-open 回合', timeoutS: 180 });
    failOpenOk = true;
  } catch { failOpenOk = false; }
  await sleep(8000); // 浏览器 4s 轮询两轮——确认无新卡
  const cardsAfterP8 = await cardCount();
  record('f1', '⑥b 停 daemon 后消息照发（pre-step 探测失败→fail-open 放行，回复到达）', failOpenOk, '');
  record('f2', '⑥b daemon 不可达不产生新选择卡（在办卡不增）', cardsAfterP8 <= cardsBeforeP8, `${cardsBeforeP8}→${cardsAfterP8}`);

  // ---- 收尾 ----
  await dumpDiagnostics('final');
  const failed = CHECKS.filter((c) => !c.ok);
  console.log('\n===== E2E 断言汇总（票面清单①-⑦ 映射） =====');
  for (const c of CHECKS) console.log(`${c.ok ? 'PASS' : 'FAIL'} [${c.id}] ${c.label}${c.detail ? ' — ' + c.detail : ''}`);
  console.log(`===== ${CHECKS.length - failed.length}/${CHECKS.length} 绿；⑦幂等重跑=重跑本剧本（每次新会话，setup.sh 全程幂等） =====`);
  await browser.close();
  process.exit(failed.length ? 1 : 0);
})().catch(async (e) => {
  fatal = e;
  console.error(`\n[E2E] 剧本中断：${e && e.stack ? e.stack.split('\n').slice(0, 6).join('\n') : e}`);
  try { await dumpDiagnostics('crash'); } catch { /* 尽力 */ }
  for (const c of CHECKS) console.log(`${c.ok ? 'PASS' : 'FAIL'} [${c.id}] ${c.label}${c.detail ? ' — ' + c.detail : ''}`);
  process.exit(2);
});
void fatal;
