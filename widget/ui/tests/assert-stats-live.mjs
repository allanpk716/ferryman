#!/usr/bin/env node
/**
 * 夜链票 05 · stats.html 成品页静态断言（node 直跑，零依赖，全程离线；无浏览器）。
 *
 * 跑法：node widget/ui/tests/assert-stats-live.mjs
 * 退出码：0=全绿；1=有红。
 *
 * 断言面（票面钦定 + 界面真源锁定）：
 *   ① stats.html 存在、成品出处头注（kimi 初稿 r1/r2 + 票05 接线）
 *   ② mock 数据依赖已断：无 stats.data.js 引用、无 STATS_DATA
 *   ③ 关键挂载点齐（mock 同款 16 个 + 错误条）
 *   ④ 深色令牌与热力/负值梯度与 mock 逐色同值（界面零改动锁）
 *   ⑤ 数据层接线：/stats/summary 与 /stats/usage、Authorization Bearer 构造、
 *      get_daemon_config 先例、30_000/30000 轮询常量 + 轮询回路
 *   ⑥ 错误重试分支：错误条文案在 + setErr 开关（失败不清空已渲染内容）
 *   ⑦ dayValue 成本/节省额档已接线（days[].savings gross/net 端点字段 +
 *      !computable 档位禁用保持）
 *   ⑧ 请求数卡「含子代理行」口径小字；页脚「守护进程实时 · 每 30 秒刷新」
 *   ⑨ 会话列 ≤12 字符截断（F9 口径）；离线面：零 https、http 仅限 127.0.0.1
 *   ⑩ 内联 JS 可编译（new Function 语法闸）
 */
import { readFileSync, statSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const UI_DIR = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const LIVE = join(UI_DIR, 'stats.html');

const results = [];
const check = (name, ok, detail = '') => results.push({ name, ok, detail: ok ? '' : detail });

let html = '';
try { html = readFileSync(LIVE, 'utf8'); } catch { /* 缺文件→全红 */ }

// ── ① 文件与头注 ──
let bytes = 0;
try { bytes = statSync(LIVE).size; } catch { /* 缺文件 */ }
check('file.stats.html 存在且 >3KB', bytes > 3072, `实际 ${bytes} 字节`);
check('file.成品出处头注(kimi 初稿迭代 + 票05 接线)',
  html.slice(0, 600).includes('kimi') && html.slice(0, 600).includes('票05'), '');

// ── ② mock 数据依赖已断 ──
check('live.无 stats.data.js 脚本引用(注释提及出处不算)',
  !/<script[^>]*stats\.data\.js/i.test(html) && !/src\s*=\s*["'][^"']*stats\.data\.js/i.test(html), '');
check('live.无 STATS_DATA 依赖', !html.includes('STATS_DATA'), '');

// ── ③ 挂载点(mock 同款 16 + 错误条) ──
const MOUNTS = [
  'id="subtitle"', 'id="kpis"',
  'id="seg"', 'id="months"', 'id="heat"', 'id="legend"',
  'id="f-from"', 'id="f-to"', 'id="f-project"', 'id="f-model"',
  'id="tbody"', 'id="pageinfo"', 'id="prev"', 'id="next"',
  'id="footer"', 'id="tip"',
];
check('mount.mock 同款挂载点 16 个齐', MOUNTS.every((s) => html.includes(s)),
  MOUNTS.filter((s) => !html.includes(s)).join(','));
check('mount.错误条 #errbar 在', html.includes('id="errbar"'), '');

// ── ④ 界面真源锁:与 mock 逐色同值 ──
const TOKENS = ['#0b0d12', '#101218', '#171b26', '#1c2230',
  '#5B9BD5', '#9CC4E8', '#E8A33D', '#9B7EDE', '#6BBF8A', '#4FC3C8',
  '#E8C33D', '#E85D5D'];
const lower = html.toLowerCase();
check('token.12 个深色令牌逐色在场(与 mock 同值)',
  TOKENS.every((t) => lower.includes(t.toLowerCase())),
  TOKENS.filter((t) => !lower.includes(t.toLowerCase())).join(','));
check('heat.正/负梯度 8 级类在场',
  ['.cell.l1', '.cell.l2', '.cell.l3', '.cell.l4',
   '.cell.n1', '.cell.n2', '.cell.n3', '.cell.n4'].every((s) => html.includes(s)), '');
check('resp.KPI 两列→900px 五列', /@media\(min-width:900px\)\{\.kpis\{grid-template-columns:repeat\(5,1fr\)\}\}/.test(html), '');

// ── ⑤ 数据层接线 ──
check('api.引用 /stats/summary', html.includes('/stats/summary'), '');
check('api.引用 /stats/usage', html.includes('/stats/usage'), '');
check('api.Authorization Bearer 头构造',
  html.includes('Authorization') && html.includes('"Bearer " + TOKEN'), '');
check('api.get_daemon_config 先例(壳内取 token)', html.includes("invoke(\"get_daemon_config\")"), '');
check('api.30s 轮询常量(30_000/30000)', /30_000|30000/.test(html), '');
check('api.轮询回路(setTimeout 挂 POLL_MS)', /setTimeout\(loop,\s*POLL_MS\)/.test(html), '');
check('api.取数超时保护(AbortSignal.timeout)', html.includes('AbortSignal.timeout'), '');

// ── ⑥ 错误重试分支 ──
check('err.错误条文案「连不上守护进程,正在重试…」',
  html.includes('连不上守护进程,正在重试…'), '');
check('err.setErr 开关 + 失败不清内容(loadSummary/loadUsage 失败路径只 setErr)',
  /function setErr\(/.test(html) && /catch\(e\)\{\s*\n\s*setErr\(true\)/.test(html), '');

// ── ⑦ dayValue 成本/节省额档接线 ──
check('day.成本档接 days[].savings.gross', /d\.savings/.test(html) && /s\.gross/.test(html), '');
check('day.节省额档接 days[].savings.net(真值带符号)', /s\.net/.test(html), '');
check('day.不可算仍禁用(!computable 双档位条件保持)',
  /disabled:!k\.cost\.computable/.test(html) && /disabled:!k\.savings\.computable/.test(html), '');
check('day.dayValue 内 computable 门(不可算恒 null)', /!s\.computable\) return null/.test(html), '');
check('day.净额以端点为真值(不移植 Python 舍入:无 round/toFixed 重算净额)',
  !/net\s*=\s*round/.test(html) && !/Math\.round\([^)]*net/.test(html), '');

// ── ⑧ 口径小字与页脚 ──
check('kpi.请求数卡「含子代理行」口径小字', html.includes('含子代理行'), '');
check('foot.页脚「数据:守护进程实时 · 每 30 秒刷新」',
  html.includes('数据:守护进程实时 · 每 30 秒刷新'), '');

// ── ⑨ 会话列口径 + 离线面 ──
check('tbl.会话列 ≤12 字符截断(session_id)', html.includes('.slice(0,12)'), '');
check('offline.零 https 字面', !(html.match(/https:\/\//gi) || []).length,
  (html.match(/https:\/\//gi) || []).join(','));
const httpAll = html.match(/http:\/\/[^"'\s)<>]+/gi) || [];
check('offline.http 字面仅限 127.0.0.1(daemon 本机口)',
  httpAll.every((u) => u.startsWith('http://127.0.0.1')), httpAll.join(','));
check('offline.无外链资源(<link / @import / src|href 指 http)',
  !/<link\b/i.test(html) && !/@import/i.test(html) &&
  !/(src|href)\s*=\s*["']https?:/i.test(html), '');

// ── ⑩ 内联 JS 语法闸 ──
let inlineOk = true, inlineErr = '';
try {
  const inline = [...html.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/gi)]
    .map((m) => m[1]).filter((s) => s.trim()).join('\n');
  new Function(inline);   // 只编译不执行(DOM/fetch 不触发)
} catch (e) { inlineOk = false; inlineErr = e.message; }
check('syntax.内联 JS 可编译(new Function 语法闸)', inlineOk, inlineErr);

const fail = results.filter((r) => !r.ok);
for (const r of results) console.log(`${r.ok ? '[PASS]' : '[FAIL]'} ${r.name}${r.detail ? ` —— ${r.detail}` : ''}`);
console.log(`\nassert-stats-live: ${results.length - fail.length}/${results.length} 通过`);
process.exit(fail.length ? 1 : 0);
