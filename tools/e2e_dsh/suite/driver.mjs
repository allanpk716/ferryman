#!/usr/bin/env node
// driver.mjs — 票08 · DSH 热缓存压缩 E2E 全链剧本（tools/e2e_dsh/suite）
//            ＋ 票03 · 多会话拓扑常驻矩阵（T1-T8，dsh-cross-inject）。
//
// 【票03 拓扑矩阵】（先跑；setup.sh 已备明文转录+web2 第二 profile）
// 单人/同目录多会话/跨 profile 三形态，断言按票01 修复后行为：
//   T1 接班基线：A 被拦 → 手动新开同目录会话 → 收交接+被拦原话（现状保持）
//   T2 事故复刻（硬门禁）：A 收工闲置（交接已落盘）→ 新会话 B 问新主题 →
//      B 零注入：账本无 kind=inject 痕 + B 转录无交接文本；语义软信号
//      （B 的回答不含 A 主题专名）独立记录，只告警不拦截（F6）
//   T3 多候选：同目录 2+ 会话各有新鲜交接 → 新会话零注入（清单也不进上下文）
//   T4 锚窗边界：窗内（<2h）被拦 → 新会话接班；无被拦待领 → 新会话零注入
//      （PendingAnchorS=7200 窗值本身不动）
//   T5 子代理探针（记录事实）：被守望会话 spawn 子代理，钉「子代理会话创建
//      是否触发 agent/created、payload 何字段可识别子代理形态」——结论落
//      ART/t5-subagent-probe.md（供票04）。判定法=锚窗探针：线内留未消费
//      pending，若子代理 created 触发→askHandoff→锚定归还→子代理转录/账本
//      必有注入痕（双向可判定）；不触发则 pending 原样留存。
//   T6 跨 profile（web2）：两 profile 同目录各开一会话（无锚面）→ 互不注入
//      （硬断言）；窗内锚+跨 profile 只记录行为不断言（观察项）
//   T7 续用/强续回归：强续首条=放行+bypass 落账+零注入（phase A）；压缩后
//      回来首条=放行+零注入（phase C 压缩链上叠加断言）
//   T8 一键新会话按钮：被拦后走卡片「新会话继续」→ 接班照旧（交接+原话）
//
// 【票08 压缩链】（后跑；15 项断言 p0..f2 逐字保留）
//   登录 → 幂等新建会话 → 多轮灌上下文至 peak≥min_peak → 闲置等触发 →
//   断言 kind=compacted 账本行（字段齐）+ poll/compacted HTTP 往返（推证）→
//   横幅 DOM 出现（且随下次发消息消失）→ 拦窗内发消息不被拦（无选择卡 +
//   gate.log mode=compacted-short-prefix）→ 交接文件落盘 → 降级链回归
//   （压缩红利失效后照旧拦=选择卡出现；停 daemon 后消息照发=fail-open）。
//
// 纪律：单场景失败=记录后继续其他场景（长跑带终结）；断言失败输出可定位诊断
// （账本行+gate.log+DOM 状态+截图）。硬门禁=账本+转录两面；语义软信号独立
// 输出不拦截。依赖：全局 playwright（本仓不装依赖）；env 契约见 run.sh。
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
const WEB2_PORT = Number(E('E2E_SUITE_WEB2_PORT'));
const WEB2_TOKEN = E('E2E_SUITE_WEB2_TOKEN');
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
/** 会话标题（账本 usage 行携带的现行标题；缺省空串） */
function titleOf(sid) {
  const rs = rowsFor(sid, 'usage').filter((r) => typeof r.title === 'string' && r.title).sort((a, b) => a.ts - b.ts);
  return rs.length ? rs[rs.length - 1].title : '';
}

// ---- 会话文件面（票03：明文转录=直接读；发现含子代理裸 uuid 目录） ----
function listSessionDirs() {
  const out = [];
  if (!fs.existsSync(SESS)) return out;
  for (const proj of fs.readdirSync(SESS)) {
    const pd = path.join(SESS, proj);
    let st;
    try { st = fs.statSync(pd); } catch { continue; }
    if (!st.isDirectory()) continue;
    for (const s of fs.readdirSync(pd)) {
      // 主会话= session-<uuid>；子代理会话目录名=裸 uuid（2026-10-07 T5 探针实测）
      if (!/^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/.test(s) && !s.startsWith('session-')) continue;
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
/** 读一个会话的转录明文（setup.sh 已把沙箱转录落明文；zstd 兜底=读不动报错） */
function transcriptOf(dir) {
  const f = sessionFileOf(dir);
  if (!f) return { text: '', file: null };
  if (f.p.endsWith('.zstd')) {
    return { text: '', file: f.p, err: 'zstd 转录不可直读（setup.sh 明文覆写未生效？）' };
  }
  return { text: fs.readFileSync(f.p, 'utf8'), file: f.p };
}
/** 头行 JSON（会话身份/谱系：id/cwd/parentSession/origin/delegationDepth） */
function headerOf(dir) {
  const t = transcriptOf(dir);
  if (!t.text) return null;
  try { return JSON.parse(t.text.split('\n')[0]); } catch { return null; }
}
/** assistant 文本（软信号素材） */
function assistantText(dir) {
  const t = transcriptOf(dir);
  const out = [];
  if (!t.text) return out;
  for (const line of t.text.split('\n')) {
    const s = line.trim();
    if (!s) continue;
    try {
      const r = JSON.parse(s);
      if (r.type === 'assistant/message') {
        for (const b of r.data?.message?.content ?? []) {
          if (b?.type === 'text' && typeof b.text === 'string') out.push(b.text);
        }
      }
    } catch { /* 坏行跳过 */ }
  }
  return out;
}
// 交接注入的可辨识文本标记（硬门禁用；warnCtx 的「建议新建会话」提示不含这些）
const HANDOFF_MARK = /\[Ferryman 交接|\[Ferryman\] 本项目有|完整交接文档|被拦时的原话|没有丢，原话如下/;

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
/** 软信号登记（F6：语义面只告警不拦截——ok 恒真，泄漏时 detail 带 SOFT-WARN） */
function recordSoft(id, label, clean, detail = '') {
  CHECKS.push({ id, label, ok: true, detail: (clean ? '干净' : 'SOFT-WARN 泄漏嫌疑') + (detail ? ` — ${detail}` : ''), soft: true });
  log(`${clean ? 'SOFT-OK' : 'SOFT-WARN'} [${id}] ${label}${detail ? ' — ' + detail : ''}`);
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
    `\n# index.json pending_prompts\n${JSON.stringify(readIndex()?.pending_prompts ?? [])}`,
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
}

// ---- daemon HTTP 面（/stats 观察 + P8 停 daemon + 票03 闸门直驱） ----
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
/** /dsh/handoff 直驱（票03：锚消费——DshHandoff 对无转录的新 sid 走锚定归还，
 *  PopPendingPrompt 即消费；场景间隔清场用它，不动任何真实会话） */
async function handoffPost(body) {
  const r = await fetch(`http://127.0.0.1:${DAEMON_PORT}/dsh/handoff`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', Authorization: `Bearer ${daemonToken()}` },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(5000),
  });
  if (!r.ok) throw new Error(`/dsh/handoff HTTP ${r.status}`);
  return r.json();
}
function readIndex() {
  try { return JSON.parse(fs.readFileSync(path.join(DATA, 'index.json'), 'utf8')); } catch { return null; }
}
/** 未消费的 dsh pending 锚（归还锚定面的事实源） */
function pendingAnchors() {
  const idx = readIndex();
  if (!idx) return [];
  return (idx.pending_prompts ?? []).filter((p) => p.agent === 'dsh' && !p.consumed_by);
}
/**
 * pending 清场：残留锚会让后续「新会话零注入」场景吃到锚定归还→假阳性。
 * 事实钉点（2026-10-07 run2 实证）：强续只清闸门运行时 pending，**store 锚
 * （pending_prompts/consumed_by）不经新会话归还链不消费**——清场必须经
 * /dsh/handoff：对每个锚的 cwd 用一次性假 sid 问归还真消费之（consumed_by=假
 * sid；inject 行落假 sid，断言全按 session_id 过滤不受扰）。
 */
async function sweepPendings(tag) {
  const pend = pendingAnchors();
  for (const p of pend) {
    try {
      const fake = `e2e-sweep-${RUNSTAMP}-${Math.random().toString(36).slice(2, 8)}`;
      const r = await handoffPost({ cwd: p.cwd, session_id: fake });
      log(`[sweep:${tag}] 残留锚 ${String(p.session_id).slice(0, 16)} 已消费（归还 context=${r.context ? '非空' : 'null'}）`);
    } catch (e) {
      log(`[sweep:${tag}] 清锚失败 ${String(p.session_id).slice(0, 16)}：${e.message}`);
    }
  }
  const left = pendingAnchors();
  if (left.length) log(`[sweep:${tag}] ⚠ 仍有 ${left.length} 个未消费锚（后续零注入场景可能受染）`);
  return pend.length;
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
/** 打开一个 web 页并登录（web1/web2 同形） */
async function openWeb(port, token, label) {
  const p = await page.context().newPage();
  page = p;
  p.on('pageerror', (e) => consoleErrs.push(String(e)));
  await p.goto(`http://127.0.0.1:${port}/?token=${encodeURIComponent(token)}`, { waitUntil: 'domcontentloaded', timeout: 60000 });
  await p.getByRole('textbox', { name: COMPOSER_RE }).waitFor({ state: 'visible', timeout: 90000 });
  await dismissStartupDialogs();
  await p.getByRole('textbox', { name: COMPOSER_RE }).waitFor({ state: 'visible', timeout: 30000 });
  log(`${label} 登录完成，composer 就绪（127.0.0.1:${port}）`);
  return p;
}
async function newSessionInUI() {
  // 点侧栏「新建会话」→ 聚焦空白会话（幂等：每次剧本新建，不依赖既有侧栏态）
  const btn = page.getByRole('button', { name: '新建会话' }).first();
  await btn.click({ timeout: 30000 });
  await page.getByRole('textbox', { name: COMPOSER_RE }).waitFor({ state: 'visible', timeout: 30000 });
}
const escapeRe = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
/** 当前聚焦会话标题（顶栏「会话层级」面包屑文本；读不到回空串） */
async function currentFocusTitle() {
  const nav = page.getByRole('navigation', { name: '会话层级' });
  return ((await nav.innerText({ timeout: 3000 }).catch(() => '')) || '').trim();
}
/** 侧栏点开既有会话（按标题前缀匹配 treeitem；标题=账本 usage 行携带的现行标题）。
 *  三级自愈（2026-10-07 两轮实锚）：①焦点已在目标上（面包屑对得上——刚建/刚发
 *  的会话都在焦点）免点；②侧栏只列最近几场，更早的折在「展开其余 N 个会话」
 *  按钮后；③新页载（web2）整个会话列表可能折叠进「未分组」/工作区节点——点
 *  工作区 treeitem 展开；④标题向侧栏异步传播（先显示「未命名/新会话」）——
 *  等目标可见再点。 */
async function focusSession(titlePart, label = '会话') {
  const t = titleOf(titlePart.sid) || titlePart.fallback || '';
  if (!t) throw new Error(`focusSession：会话 ${label} 无标题可匹配`);
  const prefix = t.slice(0, 10);
  const cur = await currentFocusTitle();
  if (cur.includes(prefix)) {
    log(`已聚焦${label}（面包屑≈${cur.slice(0, 16)}，免侧栏点选）`);
    return;
  }
  const re = new RegExp(escapeRe(t.slice(0, 12)));
  const target = page.getByRole('treeitem', { name: re }).first();
  const deadline = nowS() + 40;
  while (nowS() < deadline && !(await target.isVisible().catch(() => false))) {
    const expander = page.getByRole('button', { name: /展开其余/ }).first();
    if (await expander.isVisible().catch(() => false)) {
      await expander.click({ timeout: 8000 }).catch(() => {});
      await sleep(800);
      continue;
    }
    const ws = page.getByRole('treeitem', { name: /research_things|未分组/ }).first();
    if (await ws.isVisible().catch(() => false)
        && (await ws.getAttribute('aria-expanded').catch(() => null)) !== 'true') {
      await ws.click({ timeout: 8000 }).catch(() => {}); // 折叠态才点开（展开态点=收起）
      await sleep(1000);
      continue;
    }
    await sleep(1500); // 标题向侧栏传播中
  }
  await target.click({ timeout: 20000 });
  await page.getByRole('textbox', { name: COMPOSER_RE }).waitFor({ state: 'visible', timeout: 30000 });
  log(`已点开${label}（treeitem≈${t.slice(0, 16)}）`);
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
async function waitTurnDone(file, sizeBefore, { minGrow = 150, timeoutS = 240, label = '回合' } = {}) {
  const start = nowS();
  let size = sizeBefore;
  let stableSince = null;
  let lastSize = -1;
  for (;;) {
    try { size = fstat(file).size; } catch { /* 换代瞬间 */ }
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

// ---- 票03 共用剧本件 ----
/** 已知会话集（新会话发现=目录集合差集；子代理裸 uuid 目录也在内） */
const knownSids = new Set();
function snapshotKnownSids() {
  for (const d of listSessionDirs()) knownSids.add(path.basename(d));
}
snapshotKnownSids();
/** 等一个全新会话目录出现并落首帧（发首条消息后调用） */
async function waitNewSessionDir(sinceS, { timeoutS = 120 } = {}) {
  return await waitFor('新会话目录落盘', async () => {
    const cands = listSessionDirs()
      .filter((d) => !knownSids.has(path.basename(d)) || mtimeS(d) > sinceS)
      .map((d) => ({ d, f: sessionFileOf(d) }))
      .filter((x) => x.f && x.f.m / 1000 > sinceS - 10)
      .sort((a, b) => b.f.m - a.f.m);
    return cands.length ? cands[0] : false;
  }, { timeoutMs: timeoutS * 1000, intervalMs: 1500 });
}
/** 等一个回合收口（明文转录里出现 sendTs 之后的 turn/end；兜底=尺寸稳定 8s） */
async function waitTurnEnd(dir, sendTs, { timeoutS = 240, label = '回合收口' } = {}) {
  return await waitFor(label, async () => {
    const t = transcriptOf(dir);
    if (t.err || !t.text) return false;
    for (const line of t.text.split('\n').reverse()) {
      const s = line.trim();
      if (!s) continue;
      try {
        const r = JSON.parse(s);
        if (r.type === 'turn/end') {
          return typeof r.time === 'number' && r.time / 1000 >= sendTs - 1;
        }
      } catch { /* 坏行跳过 */ }
    }
    return false;
  }, { timeoutMs: timeoutS * 1000, intervalMs: 1500 });
}
/**
 * 建新会话并发首条（幂等自愈×3：消息误入旧会话/陈旧草稿/首条被拦/落盘未现
 * ——全部按次重试；2026-10-07 run2 实锚：拦截幕刚收卡后偶发首条不落盘，重试即愈）。
 * 返回 {sid, dir, file}；抛错=3 次尝试未成。
 */
async function createSession(firstMsg, { label = '新会话', timeoutS = 300 } = {}) {
  let lastErr = null;
  for (let attempt = 1; attempt <= 3; attempt++) {
    try {
      const sendTs = nowS();
      await newSessionInUI();
      await sleep(1500); // 视图切换落定（拦截幕刚收卡后的竞态缓冲）
      const cardsBefore = await cardCount();
      await sendMsg(firstMsg);
      const cand = await waitNewSessionDir(sendTs, { timeoutS: 90 });
      const sid = path.basename(cand.d);
      const misdirected = knownSids.has(sid); // 消息进了既有会话（新建点击没生效）
      const stale = misdirected && rowsFor(sid).some((r) => typeof r.ts === 'number' && r.ts < t0 - 10);
      const blockedNow = (await cardCount()) > cardsBefore;
      if (!misdirected && !blockedNow) {
        knownSids.add(sid);
        await waitTurnEnd(cand.d, sendTs, { timeoutS, label: `${label}首条收口` });
        log(`${label} 已建：${sid}`);
        return { sid, dir: cand.d, file: cand.f.p };
      }
      lastErr = new Error(`第 ${attempt} 次未成（${sid}，误入旧会话=${misdirected} 陈旧=${stale} 被拦=${blockedNow}）`);
      log(`${label} ${lastErr.message}——重点新建重试`);
      if (blockedNow) await sleep(3000); // 卡面浮层退场
    } catch (e) {
      lastErr = e;
      log(`${label} 第 ${attempt} 次异常（${e.message.split('\n')[0]}）——重试`);
      await sleep(3000);
    }
  }
  throw new Error(`${label} 3 次尝试均未建成新会话：${lastErr?.message ?? '?'}`);
}
/** 发消息并等回合归宿（文件增长=跑了 / 选择卡出现=被拦） */
async function sendAndWaitTurn(sess, text, { label = '回合', timeoutS = 240 } = {}) {
  const size0 = fstat(sess.file).size;
  const cards0 = await cardCount();
  await sendMsg(text);
  const r = await waitFor(`${label}归宿`, async () =>
    (await cardCount()) > cards0 ? 'blocked' : fstat(sess.file).size >= size0 + 1000 ? 'ran' : false,
    { timeoutMs: timeoutS * 1000, intervalMs: 1500 });
  if (r === 'ran') await waitTurnDone(sess.file, size0, { label });
  return r;
}
/** 等会话可拦（闲置≥block_s ∧ 压缩标记已失效；压缩再触发会顶新闲置钟——循环等） */
async function waitBlockable(sess, label, { deadlineS = 900 } = {}) {
  const markLapsed = () => {
    const comps = rowsFor(sess.sid, 'compacted').filter((r) => r.ok === true && typeof r.ts === 'number');
    const last = comps.at(-1);
    return last === undefined || nowS() >= last.ts + MARK_RATIO * TTL_S + 6;
  };
  const start = nowS();
  for (;;) {
    const idle = nowS() - mtimeS(sess.file);
    if (idle >= BLOCK_S + 5 && markLapsed()) {
      await sleep(2500); // 二次确认：避开压缩指令落地的窄竞态
      if (nowS() - mtimeS(sess.file) >= BLOCK_S + 5 && markLapsed()) {
        log(`${label} 已进拦窗（idle=${Math.round(nowS() - mtimeS(sess.file))}s，压缩标记已失效）`);
        return;
      }
    }
    if (nowS() - start > deadlineS) {
      throw new Error(`${label} 等拦窗超时 ${deadlineS}s（idle=${Math.round(nowS() - mtimeS(sess.file))}s markLapsed=${markLapsed()}）`);
    }
    await sleep(3000);
  }
}
/** 等会话所在线攒出新鲜交接（**线级语义**：交接按 (agent,cwd) 线去重生成——别家
 *  会话的交接只要 covers 盖过本会话最后写入即算"本线交接已备"（compact_trigger
 *  的 ValidHandoff 去重同款；单会话自属交接不保证存在，等它会白等超时）。
 *  返回命中的交接条目（含 covers_until_s，供错峰播种用）。 */
async function waitHandoffFor(sess, label, { timeoutS = 180 } = {}) {
  return await waitFor(`${label} 线交接落盘`, async () => {
    const idx = readIndex();
    if (!idx) return false;
    const lastUsageTs = rowsFor(sess.sid, 'usage').filter((r) => typeof r.ts === 'number').map((r) => r.ts).sort((a, b) => b - a)[0] ?? 0;
    return (idx.handoffs ?? []).find((h) => h.consumed_at === null
      && (h.covers_until_s ?? 0) + 60 >= lastUsageTs - 5) || false;
  }, { timeoutMs: timeoutS * 1000, intervalMs: 3000 });
}
/**
 * 拦一个会话：侧栏聚焦 → 等拦窗 → 发消息 → 选择卡 + pending 落锚。
 * 有效交接缺席形态（首发=分支7 警告放行）自动补第二发（pending 已置→必落分支6）。
 * 返回 {anchor, blockedText}——blockedText=实际被拦的那条原话（接班断言用）。
 */
async function blockSession(sess, promptText, { label = '拦截' } = {}) {
  await focusSession({ sid: sess.sid }, `${label}目标会话`);
  await waitBlockable(sess, label);
  let blockedText = promptText;
  for (let shot = 1; shot <= 2; shot++) {
    const sendTs = nowS();
    const cards0 = await cardCount();
    const size0 = fstat(sess.file).size;
    await sendMsg(blockedText);
    const outcome = await waitFor(`${label}第${shot}发归宿`, async () =>
      (await cardCount()) > cards0 ? 'blocked'
        : fstat(sess.file).size >= size0 + 1000 ? 'ran' : false,
      { timeoutMs: 90000, intervalMs: 1500 });
    if (outcome === 'blocked') break;
    if (shot === 2) throw new Error(`${label} 两发均未被拦（末发归宿=${outcome}——有效交接与 pending 双缺？）`);
    log(`${label} 首发=警告放行（分支7）——pending 已置，紧接第二发验拦截`);
    await waitTurnEnd(sess.dir, sendTs, { label: `${label}首发收口`, timeoutS: 120 }).catch(() => {});
    blockedText = `${promptText}（重发）`;
  }
  const anchored = await waitFor(`${label} pending 落锚`, async () =>
    pendingAnchors().find((p) => p.session_id === sess.sid) || false, { timeoutMs: 30000, intervalMs: 1500 });
  log(`${label} 已拦（pending blocked_at=${anchored.blocked_at}，被拦原话=${blockedText.slice(0, 24)}…）`);
  return { anchor: anchored, blockedText };
}
/** 硬门禁断言：零注入（账本无 inject 行 + 转录无交接文本） */
function assertZeroInject(scenario, sess, extra = '') {
  const rows = rowsFor(sess.sid, 'inject');
  const tr = transcriptOf(sess.dir);
  const leak = tr.err ? null : HANDOFF_MARK.test(tr.text);
  const ok = rows.length === 0 && !leak && !tr.err;
  record(scenario, '硬门禁：零注入（账本无 kind=inject + 转录无交接文本）', ok,
    `inject行=${rows.length} 转录交接痕=${leak === null ? `不可读(${tr.err})` : leak}${extra ? ' ' + extra : ''}`);
  return ok;
}
/** 接班断言：交接+被拦原话（账本 inject 痕 + 转录两面可验） */
function assertSuccession(scenario, sess, originalPrompt, extra = '') {
  const rows = rowsFor(sess.sid, 'inject');
  const tr = transcriptOf(sess.dir);
  const hasHandoff = tr.err ? false : /\[Ferryman 交接/.test(tr.text);
  const hasPendingOnly = tr.err ? false : /没有丢，原话如下/.test(tr.text);
  const hasPrompt = tr.err ? false : tr.text.includes(originalPrompt);
  const ok = rows.length >= 1 && (hasHandoff || hasPendingOnly) && hasPrompt;
  record(scenario, '接班：新会话收交接+被拦原话（账本 inject 痕+转录可验）', ok,
    `inject行=${rows.length}(handoff_id=${rows.at(-1)?.handoff_id ?? '?'}) 交接头=${hasHandoff} 仅原话头=${hasPendingOnly} 原话=${hasPrompt}${extra ? ' ' + extra : ''}`);
  return ok;
}

// ---- 灌上下文素材（多变体写作任务；纯文字回复不触工具） ----
const TOPICS = [
  '丝绸之路的历史脉络与贸易品', '城市地下水系统的工作原理', '咖啡从种子到杯子的一生',
  '潮汐的成因与沿海生态', '印刷术如何改变欧洲社会', '蜂群决策的群体智慧',
  '铁路信号系统的演进史', '深海热泉生态圈', '古代灯塔的地中海航路意义', '疫苗冷链物流的难点',
];

// ================================================================ 主线
(async () => {
  log(`E2E 剧本启动：web=127.0.0.1:${WEB_PORT} web2=127.0.0.1:${WEB2_PORT} daemon=127.0.0.1:${DAEMON_PORT} min_peak=${MIN_PEAK} ttl=${TTL_S}s`);
  log(`诊断/产物目录：${ART}`);
  const browser = await launchBrowser();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  page = await ctx.newPage();
  page.on('pageerror', (e) => consoleErrs.push(String(e)));

  // 残留锚清场（上一 run 遗留 pending 会让零注入场景吃到锚定归还→假阳性）
  const swept = await sweepPendings('run-start');

  // ============================================================ Phase A · 拓扑矩阵（web1）
  // 时序总览（秒级阈值 summarize45/block90/触发30/标记180 并行交错）：
  //   种子会话 A1,A2,A2T,A1T 先建（各自一轮；并行闲置攒交接）→ T3/T2 零注入
  //   （此窗无 pending）→ T1 拦+接班 → T8 拦+按钮 → T5 锚窗探针+子代理 →
  //   T4 拦+接班 → T7 强续 → 尾场锚清场。每幕自带 try/catch（失败记录后继续）。
  {
    const pageA = await openWeb(WEB_PORT, WEB_TOKEN, 'web1(拓扑矩阵)');
    void pageA;

    // ---- 种子会话（各一轮短文；标题由首条生成,互不同题） ----
    // T3 前置（2+ 会话**各**有自属新鲜交接）需要错峰：交接按 (agent,cwd) 线去重
    // 生成（60s covers 容差内的后写内容被既有交接覆盖→不再生成）——A1 攒出交接
    // 后再种 A2，A2 的写入超出容差即得自属交接（run2 实锚：四场 45s 内连种只有
    // 1 份线交接）。A2T/A1T 跟种即可（T2/T1 前置只要求线交接在场/末写者自属）。
    const mkSeed = async (name, topic) => {
      const sess = await createSession(
        `${name}：请用中文写一段约120字的短文，主题：${topic}。直接输出正文，不要使用任何工具。`,
        { label: name, timeoutS: 300 });
      state.sid = sess.sid; state.sessDir = sess.dir; state.sessFile = sess.file;
      return sess;
    };
    const A1 = await mkSeed('T3种子甲', '龙泉青瓷的开片工艺');
    // 等 A1 线交接落盘**且越过 60s covers 容差**再种 A2——容差内 A2 的写入会被
    // 既有交接"覆盖"而不再生成自属交接（run3 实锚：40s 错峰仍塌成 1 份）。
    {
      const a1h = await waitHandoffFor(A1, 'T3甲线', { timeoutS: 150 })
        .catch((e) => { log(`[warn] T3甲线交接等待异常：${e.message}`); return null; });
      if (a1h?.covers_until_s) {
        const wait = Math.max(0, a1h.covers_until_s + 68 - nowS());
        if (wait > 0) { log(`等 A1 交接 covers 容差窗越过（+${Math.ceil(wait)}s）…`); await sleep(wait * 1000); }
      }
    }
    const A2 = await mkSeed('T3种子乙', '岭南醒狮的扎作技艺');
    await waitHandoffFor(A2, 'T3乙线', { timeoutS: 150 }).catch((e) => log(`[warn] T3乙线交接等待异常：${e.message}`));
    const A2T = await mkSeed('T2主题会话', '敦煌星图与唐代天文');           // T2 的 A（主题专名=敦煌星图）
    const A1T = await mkSeed('T1基线会话', '蓝铁矿的矿物学特征');           // T1 的 A（末写者，自属交接）

    // ---- 等四场种子都攒出新鲜交接（闲置 45s 线并行；压缩线在 30s 线先触发也产交接） ----
    log('等种子会话交接落盘（summarize 45s 线，四场并行）…');
    for (const [nm, s] of [['T3甲', A1], ['T3乙', A2], ['T2', A2T], ['T1', A1T]]) {
      await waitHandoffFor(s, nm, { timeoutS: 240 }).catch((e) => {
        log(`[warn] ${nm} 交接等待异常（后续场景可能受累）：${e.message}`);
      });
    }

    // ---- T3 多候选：同目录 2+ 新鲜交接 → 新会话零注入（清单也不进上下文） ----
    try {
      const C3 = await createSession('T3场景：请只回复一句话：新会话就绪。不要使用任何工具。', { label: 'T3 新会话' });
      const cands = (readIndex()?.handoffs ?? []).filter((h) => h.consumed_at === null
        && (h.status === 'fresh' || h.status === 'skeleton') && (h.covers_until_s ?? 0) > t0);
      assertZeroInject('t3', C3, `（同目录本 run 新鲜交接=${cands.length}≥2）`);
      record('t3b', 'T3 前置：同目录确有 2+ 新鲜交接（拓扑成立）', cands.length >= 2, `候选=${cands.length}`);
    } catch (e) { record('t3', 'T3 多候选零注入', false, `场景中断：${e.message}`); }

    // ---- T2 事故复刻（硬门禁）：A 收工闲置（交接已落盘）→ B 问新主题 → 零注入 ----
    let B2 = null;
    try {
      B2 = await createSession('T2场景：请用中文写一段约100字的短文，主题：昆明湖的冬季结冰。直接输出正文，不要使用任何工具。',
        { label: 'T2 新会话B' });
      assertZeroInject('t2', B2, '（A=敦煌星图主题，交接在场）');
      // 语义软信号（F6）：B 的回答不含 A 主题专名——独立输出，不拦截
      const replies = assistantText(B2.dir).join('\n');
      const leak = replies.includes('敦煌星图');
      recordSoft('t2s', 'T2 软信号：B 的回答不含 A 主题专名（敦煌星图）', !leak,
        leak ? `回答含专名（前120字：${replies.slice(0, 120)}）` : '');
    } catch (e) { record('t2', 'T2 事故复刻零注入', false, `场景中断：${e.message}`); }

    // ---- T1 接班基线：A1T 被拦 → 手动新开同目录会话 → 交接+原话 ----
    try {
      const t1Prompt = `T1被拦原话${RUNSTAMP.slice(-6)}：请记住编号ARC-1。`;
      const { blockedText: t1Blocked } = await blockSession(A1T, t1Prompt, { label: 'T1' });
      const B1 = await createSession('T1接班：请根据开场收到的材料用一句话继续。不要使用任何工具。', { label: 'T1 新会话' });
      assertSuccession('t1', B1, t1Blocked);
    } catch (e) { record('t1', 'T1 接班基线', false, `场景中断：${e.message}`); await sweepPendings('t1-fail'); }

    // ---- T8 一键新会话按钮：拦 A2 → 卡片「新会话继续」→ 接班照旧 ----
    try {
      const t8Prompt = `T8被拦原话${RUNSTAMP.slice(-6)}：请记住编号BTN-8。`;
      const { blockedText: t8Blocked } = await blockSession(A2, t8Prompt, { label: 'T8' });
      // 点卡片「新会话继续」→ RPC newSession → agents.create 即刻触发 agent/created
      const btn = page.getByRole('button', { name: /新会话继续/ }).first();
      await btn.click({ timeout: 15000 });
      // 新会话 sid 从账本 inject 行识别（created 即锚定归还→inject 先落账）
      const clickTs = nowS();
      let n8Sid = null;
      for (let i = 0; i < 40 && !n8Sid; i++) {
        await sleep(2000);
        const row = readRows().filter((r) => r.kind === 'inject' && r.ts >= clickTs - 5
          && !knownSids.has(r.session_id)).at(-1);
        if (row) n8Sid = row.session_id;
      }
      if (!n8Sid) throw new Error('按钮点后 80s 未见新会话 inject 行（RPC create/归还链断？）');
      knownSids.add(n8Sid);
      record('t8a', 'T8 一键新会话：按钮→RPC create→created 锚定归还（inject 行落账）', true, n8Sid);
      // 给 N8 送首条让排队注入落转录。通道=宿主网关 RPC `session/prompt`（=UI 自家
      // 消息通道，同源 cookie 鉴权；信封 2026-10-07 探针验证通过）。侧栏点选不可达：
      // RPC 创建的未题名会话是 blank 会话，**不进侧栏列表**（run2-4 三轮实锚——
      // reload 也不进；有消息后才会出现）。N8 无台账记录，闸门 no-ledger 放行。
      let N8 = null;
      {
        log('T8 首条经宿主网关 session/prompt 送达（blank 会话不进侧栏）');
        const sendTs = nowS();
        const res = await page.evaluate(async ([sid, text]) => {
          const r = await fetch('/api/session/prompt', {
            method: 'POST',
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({
              type: 'client-request', rpcId: 'e2e-t8-' + Math.random(),
              method: 'session/prompt',
              payload: { args: { request: {
                requestId: 'e2e-t8-' + Math.random().toString(36).slice(2, 10),
                sessionId: sid, mode: 'queue',
                content: [{ type: 'text', text }],
              } } },
            }),
          });
          return { status: r.status, body: await r.text() };
        }, [n8Sid, 'T8接班：请根据开场收到的材料用一句话继续。不要使用任何工具。']);
        record('t8b', 'T8 首条送达通道：宿主网关 session/prompt（UI 同通道）', /"accepted":true/.test(res.body), `HTTP ${res.status} ${res.body.slice(0, 80)}`);
        const hit = await waitFor('T8 网关首条代文件落盘', async () => {
          const c = listSessionDirs().map((d) => ({ d, f: sessionFileOf(d) }))
            .filter((x) => x.f && x.f.m / 1000 > sendTs - 10 && path.basename(x.d) === n8Sid);
          return c.length ? c[0] : false;
        }, { timeoutMs: 120000, intervalMs: 2000 }).catch(() => null);
        if (!hit) throw new Error('网关首条后 120s 未见 N8 代文件');
        N8 = { sid: n8Sid, dir: hit.d, file: hit.f.p };
      }
      await waitTurnEnd(N8.dir, nowS() - 300, { label: 'T8 接班回合收口', timeoutS: 240 }).catch(() => {});
      assertSuccession('t8', N8, t8Blocked);
    } catch (e) { record('t8', 'T8 一键新会话按钮接班', false, `场景中断：${e.message}`); await sweepPendings('t8-fail'); }

    // ---- T5 子代理探针（记录事实，供票04） ----
    const t5 = { spawned: null, firedCreated: null, identified: null, notes: [] };
    globalThis.__t5 = t5;
    try {
      if (!B2) throw new Error('T2 的 B 会话缺席（前置场景失败）——探针复用其会话不可得');
      // 锚窗探针：A1 拦下留未消费 pending → B2 发 spawn 请求 → 子代理 created 若
      // 触发→askHandoff→锚定归还→子代理必有注入痕（双向可判定）
      const t5Prompt = `T5被拦原话${RUNSTAMP.slice(-6)}：锚窗探针占位。`;
      await blockSession(A1, t5Prompt, { label: 'T5锚' });
      // B2 已闲置多分钟（直接发必被拦）——「强续」回鲜：bypass 放行并把 B2 闲置钟
      // 归零（清的是 B2 自家 pending/交接，不动 A1 的探针锚）
      await focusSession({ sid: B2.sid }, 'T5 spawn 母会话');
      const fresh0 = nowS();
      await sendMsg(`强续 T5回鲜：请只回复两个字：就绪。不要使用任何工具。`);
      await waitTurnEnd(B2.dir, fresh0, { label: 'T5回鲜收口', timeoutS: 240 });
      const spawnTs = nowS();
      const spawnMsg = '请调用你的 subagent 工具创建一个子代理，给它的任务指令是：「请只回复四个字：子代理就位」，不要给它其他任务，也不要自己再使用别的工具。等它完成后把它的原话告诉我。';
      await sendMsg(spawnMsg);
      // 观察窗：新目录（含裸 uuid 子会话）+ 子会话转录/账本注入痕
      const spawnDeadline = nowS() + 300;
      let childDir = null;
      while (nowS() < spawnDeadline && !childDir) {
        await sleep(5000);
        for (const d of listSessionDirs()) {
          const sid = path.basename(d);
          if (knownSids.has(sid)) continue;
          const h = headerOf(d);
          if (h && h.parentSession === B2.sid) { childDir = d; break; }
        }
      }
      await waitTurnEnd(B2.dir, spawnTs, { label: 'T5 spawn 回合收口', timeoutS: 300 }).catch(() => {});
      await sleep(5000); // 尾部事件（catalog/usage 行）落账缓冲
      if (!childDir) {
        t5.spawned = false;
        t5.notes.push('模型未在观察窗内 spawn 子代理（或子会话目录未落盘）——「沙箱不可造」候选');
        record('t5', 'T5 子代理探针：沙箱不可造（模型未调用 subagent 工具）——如实记录', true, '');
      } else {
        t5.spawned = true;
        const childSid = path.basename(childDir);
        const h = headerOf(childDir);
        const childTr = transcriptOf(childDir);
        const childInject = rowsFor(childSid, 'inject');
        const anchorConsumedByChild = (readIndex()?.pending_prompts ?? [])
          .some((p) => p.session_id === A1.sid && p.consumed_by === childSid);
        t5.identified = {
          childSid,
          header: { origin: h?.origin ?? '(缺)', parentSession: (h?.parentSession ?? '').slice(0, 24), delegationDepth: h?.delegationDepth },
        };
        // 双向判定：锚在场时 created 触发 ⟺ 子会话有注入痕
        t5.firedCreated = childInject.length > 0 || anchorConsumedByChild || HANDOFF_MARK.test(childTr.text || '');
        t5.notes.push(`子会话注入痕：inject行=${childInject.length} 转录交接痕=${childTr.err ? '不可读' : HANDOFF_MARK.test(childTr.text)} 锚consumed_by子=${anchorConsumedByChild}`);
        t5.notes.push(`插件可识别字段（子会话头行，CreatedPayload.agent.session.header 同源）：origin=${h?.origin ?? '(缺)'} parentSession=${h?.parentSession ? '在场' : '缺'} delegationDepth=${h?.delegationDepth ?? '(缺)'}`);
        const subRows = rowsFor(B2.sid, 'usage').filter((r) => r.subagent === childSid);
        t5.notes.push(`子会话事件直报：随父入账 usage 行 subagent=子键 共${subRows.length}条（plugin buildEventBody origin+parentSession 面可达）`);
        // 探针自身不做硬断言（票面=记录事实）；子代理被注入=票04 硬禁的实证输入
        record('t5', 'T5 子代理探针：事实已记录（agent/created 触发与否+识别形态）', true,
          `spawned=true created触发=${t5.firedCreated} 子=${childSid.slice(0, 14)} origin=${h?.origin ?? '?'}`);
      }
      // 探针收尾：若子代理未吃锚（created 不触发/未 spawn），残留锚清场防污染后续场景
      await sweepPendings('t5-done');
    } catch (e) {
      record('t5', 'T5 子代理探针', false, `场景中断：${e.message}`);
      await sweepPendings('t5-fail');
    }

    // ---- T4 锚窗边界：窗内被拦→接班（leg1）；无被拦待领→零注入（leg2） ----
    try {
      const t4Prompt = `T4被拦原话${RUNSTAMP.slice(-6)}：请记住编号WIN-4。`;
      const { blockedText: t4Blocked } = await blockSession(A2T, t4Prompt, { label: 'T4' });
      const B4 = await createSession('T4接班：请根据开场收到的材料用一句话继续。不要使用任何工具。', { label: 'T4 新会话' });
      assertSuccession('t4a', B4, t4Blocked, '（窗内 <2h 腿）');
    } catch (e) { record('t4a', 'T4 窗内被拦→接班', false, `场景中断：${e.message}`); await sweepPendings('t4-fail'); }
    try {
      // leg2：此刻线内无未消费 pending（T4 的锚已被 B4 吃掉）→ 新会话零注入
      const pend = pendingAnchors().length;
      const C4 = await createSession('T4无锚腿：请只回复一句话：干净开场。不要使用任何工具。', { label: 'T4 无锚新会话' });
      assertZeroInject('t4b', C4, `（进场时未消费锚=${pend}）`);
    } catch (e) { record('t4b', 'T4 无被拦待领→零注入', false, `场景中断：${e.message}`); }

    // ---- T7 强续回归：被拦后「强续」首条=放行+bypass 落账+零注入 ----
    try {
      const t7Prompt = `T7被拦原话${RUNSTAMP.slice(-6)}：请记住编号FORCE-7。`;
      await blockSession(A1T, t7Prompt, { label: 'T7' });
      const bypassRows0 = rowsFor(A1T.sid, 'bypass').length;
      const r = await sendAndWaitTurn(A1T, `强续 ${t7Prompt}请只回复一句话：强续成功。`, { label: 'T7强续' });
      await sleep(3000);
      const bypassRows = rowsFor(A1T.sid, 'bypass').length - bypassRows0;
      const inj = rowsFor(A1T.sid, 'inject').length;
      record('t7', 'T7 强续首条：放行+bypass 落账+零注入（与 v0.9.7 一致）',
        r === 'ran' && bypassRows >= 1 && inj === 0, `归宿=${r} bypass新行=${bypassRows} inject行=${inj}`);
    } catch (e) { record('t7', 'T7 强续回归', false, `场景中断：${e.message}`); await sweepPendings('t7-fail'); }

    // ---- 尾场：锚清场 + 软硬汇总落盘（T6 前置） ----
    await sweepPendings('phaseA-end');
    await dumpDiagnostics('phaseA');
  }

  // ============================================================ Phase B · 跨 profile 拓扑（web2）
  {
    try {
      const pageB = await openWeb(WEB2_PORT, WEB2_TOKEN, 'web2(跨profile)');
      void pageB;
      // 无锚面（硬断言）：profile1 侧会话（A1/A2/A2T/A1T）的交接都在场——
      // profile2 侧新会话零注入（票01 后：无锚=零注入终态,跨 profile 无差别）
      const pendBefore = pendingAnchors().length;
      const P2B = await createSession('T6跨profile：请只回复一句话：第二档案位就绪。不要使用任何工具。',
        { label: 'T6 profile2 新会话' });
      assertZeroInject('t6', P2B, `（进场时未消费锚=${pendBefore}；同目录 profile1 交接在场）`);
      // 锚+跨 profile（观察项,只记录不断言）：profile2 侧拦下留锚 → profile2 再开新会话
      // ——记录其实际收到什么（现实现按 agent+cwd 线锚定,预期=接班；票面裁定只记录）
      try {
        const t6Prompt = `T6观察锚${RUNSTAMP.slice(-6)}：跨 profile 观察项。`;
        await blockSession(P2B, t6Prompt, { label: 'T6观察' });
        const P2C = await createSession('T6观察：请根据开场收到的材料用一句话继续。不要使用任何工具。', { label: 'T6 profile2 第二新会话' });
        const rows = rowsFor(P2C.sid, 'inject');
        const tr = transcriptOf(P2C.dir);
        const got = rows.length > 0 ? `交接+原话（inject ${rows.length} 行，handoff_id=${rows.at(-1)?.handoff_id}）`
          : HANDOFF_MARK.test(tr.text || '') ? '转录有交接痕但账本无行' : '零注入';
        record('t6o', 'T6 观察项：窗内锚+跨 profile 新会话实际行为（只记录不断言）', true, got);
      } catch (e) {
        record('t6o', 'T6 观察项：窗内锚+跨 profile（只记录）', true, `观察链中断：${e.message}`);
        await sweepPendings('t6-obs-fail');
      }
      await sweepPendings('phaseB-end');
      await dumpDiagnostics('phaseB');
    } catch (e) {
      record('t6', 'T6 跨 profile 互不注入', false, `场景中断（web2 不可用？）：${e.message}`);
      await sweepPendings('phaseB-fail').catch(() => {});
    }
  }

  // ============================================================ Phase C · 票08 压缩链（15 项）+ T7 续用腿
  // 复用 web1 页（web2 页留着不影响）；state 重置
  {
    const pageC = await openWeb(WEB_PORT, WEB_TOKEN, 'web1(压缩链)');
    void pageC;
    // 锚清场（t7c 续用腿断言「零注入」要求链会话创建时线内无锚；phaseA/B 尾扫
    // 理应清空——此处兜底复验）
    await sweepPendings('phaseC-start');

    // ---- P0.5 幂等建会话（陈旧草稿自愈：工作区会持久化上次页载的自动草稿，点
    // 「新建会话」可能聚焦该旧草稿——旧闲置锚直接把首条消息拦死。验鲜=本会话
    // 账本零历史（首行不早于本 run）；陈旧则再点一次新建（脏草稿不再复用）） ----
    const marker = `e2e-${RUNSTAMP}`;
    let chain = null;
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
        await waitTurnDone(state.sessFile, fstat(state.sessFile).size, { label: '首条回复' })
          .catch(() => log('首条回复在检测窗口内已收（快回复）——以现有尺寸继续'));
        log(`会话已建（第 ${attempt} 次尝试）：${state.sid}，首条回复已收`);
        chain = { sid: state.sid, dir: state.sessDir, file: state.sessFile };
        break;
      }
      log(`第 ${attempt} 次拿到的会话陈旧/被拦（rows=${rows.length} blocked=${blockedNow}）——再点新建会话重试`);
    }
    if (!chain) throw new Error('3 次尝试均拿到陈旧草稿会话（工作区残留？）——见诊断');
    record('p0', '幂等新建会话（陈旧草稿自愈重试）', true, state.sid);

    // ---- P1 灌上下文至 peak ≥ min_peak ----
    let billed = 0;
    for (let i = 0; i < TOPICS.length && billed < MIN_PEAK + 800; i++) {
      const r = await sendAndWaitTurn(chain, `请用中文写一篇约500字的短文，主题：${TOPICS[i]}。要求分段、内容具体、直接输出正文，不要使用任何工具。`, { label: `第${i + 2}轮` });
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
      //   cordis:group isolate:{compaction:true} 把它隔离在 preset 组内
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
      if (p5ok) { await waitTurnDone(state.sessFile, size0, { label: '放行回合收口' }).catch(() => {}); p5ok = true; }
      const gateNew = gateLogLines().slice(gateLinesBefore);
      const allowLine = gateNew.find((l) => l.includes('mode=compacted-short-prefix'));
      record('c1', '④ 拦窗内发消息不被拦（回合执行、无选择卡）', p5ok,
        p5ok ? '文件增长=回合已跑' : `出现选择卡×${await cardCount()}`);
      record('c2', '④ gate.log 放行痕（mode=compacted-short-prefix 新行）', Boolean(allowLine),
        allowLine || `gate 新行：${gateNew.join(' | ') || '(无)'}`);
      // ---- T7 续用腿（票03）：压缩后回来首条=放行+零注入（与 v0.9.7 一致） ----
      {
        const inj = rowsFor(state.sid, 'inject').length;
        const tr = transcriptOf(state.sessDir);
        const leak = tr.err ? false : HANDOFF_MARK.test(tr.text);
        record('t7c', 'T7 续用腿：压缩后回来首条=放行+零注入', p5ok && inj === 0 && !leak,
          `归宿=${blockedOrDone} inject行=${inj} 转录交接痕=${leak}`);
      }
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
        { timeoutMs: (MARK_RATIO * TTL_S + BLOCK_S + 300) * 1000, intervalMs: 3000 });
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
      await waitTurnDone(state.sessFile, sizeA, { label: '放行回合' });
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
      await waitTurnDone(state.sessFile, sizeC, { label: 'fail-open 回合', timeoutS: 180 });
      failOpenOk = true;
    } catch { failOpenOk = false; }
    await sleep(8000); // 浏览器 4s 轮询两轮——确认无新卡
    const cardsAfterP8 = await cardCount();
    record('f1', '⑥b 停 daemon 后消息照发（pre-step 探测失败→fail-open 放行，回复到达）', failOpenOk, '');
    record('f2', '⑥b daemon 不可达不产生新选择卡（在办卡不增）', cardsAfterP8 <= cardsBeforeP8, `${cardsBeforeP8}→${cardsAfterP8}`);
  }

  // ---- 收尾：矩阵报告 + T5 探针结论落盘 + 汇总 ----
  writeMatrixReport(swept);
  await dumpDiagnostics('final');
  const failed = CHECKS.filter((c) => !c.ok);
  console.log('\n===== E2E 断言汇总（票03 矩阵 T1-T8 ＋ 票08 清单①-⑦ 映射） =====');
  for (const c of CHECKS) console.log(`${c.ok ? 'PASS' : 'FAIL'}${c.soft ? '(软)' : ''} [${c.id}] ${c.label}${c.detail ? ' — ' + c.detail : ''}`);
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

// ---- 票03 矩阵报告 + T5 探针结论（供票04 与晨报引用） ----
function writeMatrixReport(swept) {
  const t5cache = globalThis.__t5 ?? { spawned: null, firedCreated: null, identified: null, notes: [] };
  const lines = [];
  lines.push(`# E2E 多会话拓扑矩阵报告 · ${RUNSTAMP}`);
  lines.push('');
  lines.push(`- 沙箱：web=127.0.0.1:${WEB_PORT} web2=127.0.0.1:${WEB2_PORT} daemon=127.0.0.1:${DAEMON_PORT}（${DATA}）`);
  lines.push(`- 起跑残留锚清场：${swept} 个`);
  lines.push(`- 时序参数：ttl=${TTL_S}s 触发=${TRIGGER_RATIO * TTL_S}s block=${BLOCK_S}s 标记=${MARK_RATIO * TTL_S}s（summarize=45s 栈缺省）`);
  lines.push('');
  lines.push('## 断言红绿（含软信号）');
  lines.push('');
  lines.push('| 检查 | 结果 | 说明 |');
  lines.push('|---|---|---|');
  for (const c of CHECKS) {
    lines.push(`| ${c.id} | ${c.ok ? (c.soft ? '软-OK' : '绿') : '红'} | ${c.label}${c.detail ? `（${c.detail}）` : ''} |`);
  }
  lines.push('');
  lines.push('## 硬/软门禁分层（F6）');
  lines.push('');
  lines.push('- 硬门禁（拦截合入）：账本 kind=inject 痕有无 ＋ 新会话转录有无交接文本（HANDOFF_MARK 集：`[Ferryman 交接`/`[Ferryman] 本项目有`/`完整交接文档`/`被拦时的原话`）。');
  lines.push('- 软信号（只告警不拦截）：语义面「B 的回答不含 A 主题专名」——模型措辞不可控，泄漏≠注入回归，独立记录。');
  lines.push('');
  fs.writeFileSync(path.join(ART, 'matrix-report.md'), lines.join('\n'));

  const p = [];
  p.push(`# T5 子代理探针结论 · ${RUNSTAMP}（供票04：子代理硬禁条件实施）`);
  p.push('');
  p.push('## 事实');
  if (t5cache.spawned === null) {
    p.push('- 探针未跑成（前置场景失败或场景中断）——见断言 t5 明细。');
  } else if (!t5cache.spawned) {
    p.push('- 模型未在观察窗内调用 subagent 工具（或子会话目录未落盘）——本 run 沙箱不可造，钉死结论留待重跑。');
  } else {
    p.push(`- 子代理会话创建：触发=${t5cache.firedCreated === null ? '未判定' : t5cache.firedCreated}（判定法=锚窗探针：线内留未消费 pending，created 触发⟺子会话有注入痕）。`);
    p.push(`- 可识别形态（子会话头行，插件 CreatedPayload.agent.session.header 同源可达）：${JSON.stringify(t5cache.identified?.header ?? {})}`);
    p.push(`- 子会话目录名=裸 uuid（不带 session- 前缀）；账本随父入账行 subagent 列=子键。`);
  }
  for (const n of t5cache.notes ?? []) p.push(`- ${n}`);
  p.push('');
  p.push('## 票04 裁定输入（spec R3/F5：钉不死则记「不适用/延后」，禁止硬凑识别规则）');
  p.push('');
  p.push('- 若「触发=true 且注入痕在场」：子代理 created 会问 handoff——硬禁（onCreated 对 origin=subagent 形态跳过问询）有实证必要性；识别字段以头行 origin+parentSession 为准。');
  p.push('- 若「触发=false」：子代理不触发 agent/created（或事件面不可见）——插件面无需硬禁，票04 记「不适用」。');
  fs.writeFileSync(path.join(ART, 't5-subagent-probe.md'), p.join('\n'));
}
void fatal;
