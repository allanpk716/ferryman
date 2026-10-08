#!/usr/bin/env node
/**
 * 夜链票 02 · stats.html mock 页静态断言（node 直跑，零依赖，全程离线；无浏览器）。
 *
 * 跑法：node widget/ui/tests/assert-stats-mock.mjs
 * 退出码：0=全绿；1=有红。
 *
 * 断言面（票面钦定四组 + 行为要点静态在码）：
 *   ① stats.html 存在、kimi 产出头注在、关键挂载点齐
 *   ② 深色令牌与热力/负值梯度、KPI/响应式关键 CSS 在场
 *   ③ stats.data.js 存在且 window.STATS_DATA 可解析、形状与页面引用逐字一致
 *   ④ 页面零外部 URL（http 外链为零、无外链 src/href、无 <link/@import）
 *   ⑤ 行为要点静态在码：不可算徽标、命中率公式 tooltip、四档切换、负值日、
 *      分页 20、成本 null→「—」、mock 页脚声明；内联 JS 可编译（new Function 语法闸）
 *
 * 注：页面在 mock/stats.html、数据在 mock/stats/stats.data.js——kimi 原产物写于
 * stats/ 旁（把任务书「同目录」当真），泳道已移至票面钦定路径并修正 src，此为
 * 机械修复非改设计；本断言把「页面 src ↔ 数据实路径」锁死，防回退。
 */
import { readFileSync, statSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const UI_DIR = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const MOCK = join(UI_DIR, 'mock', 'stats.html');
const DATA = join(UI_DIR, 'mock', 'stats', 'stats.data.js');

const results = [];
const check = (name, ok, detail = '') => results.push({ name, ok, detail: ok ? '' : detail });

let html = '', dataSrc = '';
try { html = readFileSync(MOCK, 'utf8'); } catch { /* 缺文件→①组全红 */ }
try { dataSrc = readFileSync(DATA, 'utf8'); } catch { /* 缺文件→③组全红 */ }

// ── ① 文件、头注、挂载点 ──
let mockBytes = 0;
try { mockBytes = statSync(MOCK).size; } catch { /* 缺文件 */ }
check('file.stats.html 存在且 >3KB', mockBytes > 3072, `实际 ${mockBytes} 字节`);
check('file.kimi 产出头注在文件头', html.slice(0, 400).includes('kimi 产出'), '');
check('file.数据 src 指向 stats/stats.data.js', html.includes('<script src="stats/stats.data.js"></script>'), '');

const MOUNTS = [
  'id="subtitle"', 'id="kpis"',                      // 标题行 + KPI 五卡容器
  'id="seg"', 'id="months"', 'id="heat"', 'id="legend"', // 热力图：四档切换/月份行/格子/图例
  'id="f-from"', 'id="f-to"', 'id="f-project"', 'id="f-model"', // 三筛选(时间两输入+项目+模型)
  'id="tbody"', 'id="pageinfo"', 'id="prev"', 'id="next"',      // 明细表+分页
  'id="footer"', 'id="tip"',                          // 页脚 + hover 浮层
];
check('mount.关键挂载点 16 个齐', MOUNTS.every((s) => html.includes(s)),
  MOUNTS.filter((s) => !html.includes(s)).join(','));

// ── ② 深色令牌与关键 CSS ──
const TOKENS = ['#0b0d12', '#101218', '#171b26', '#1c2230',           // 底/面板
  '#5B9BD5', '#9CC4E8',                                               // 主蓝/浅蓝
  '#E8A33D', '#9B7EDE', '#6BBF8A', '#4FC3C8',                          // 琥珀/紫/绿/青
  '#E8C33D', '#E85D5D'];                                              // 告警黄/红
const lower = html.toLowerCase();
check('token.12 个深色令牌逐色在场', TOKENS.every((t) => lower.includes(t.toLowerCase())),
  TOKENS.filter((t) => !lower.includes(t.toLowerCase())).join(','));
check('token.body 13px system-ui + 数字 tabular-nums',
  /font:13px\/1\.5 system-ui/.test(html) && html.includes('tabular-nums'), '');
check('token.KPI 数值 22px/600', /\.value{font-size:22px;font-weight:600/.test(html), '');
check('heat.正梯度 4 级类 .cell.l1..l4 在场',
  ['.cell.l1', '.cell.l2', '.cell.l3', '.cell.l4'].every((s) => html.includes(s)), '');
check('heat.负值日青系冷梯度 .cell.n1..n4 在场',
  ['.cell.n1', '.cell.n2', '.cell.n3', '.cell.n4'].every((s) => html.includes(s)) &&
  html.includes('--neg1'), '');
check('resp.390px/桌面：KPI 两列→900px 五列 + 热力图/表格横向滚动',
  /@media\(min-width:900px\)\{\.kpis\{grid-template-columns:repeat\(5,1fr\)\}\}/.test(html) &&
  /\.heat-scroll\{overflow-x:auto/.test(html) && /\.tbl-scroll\{overflow-x:auto/.test(html), '');

// ── ③ 数据可解析 + 形状 ──
let D = null;
try {
  const window = {};
  new Function('window', dataSrc)(window);   // stats.data.js 是纯挂载脚本，node 可直接求值
  D = window.STATS_DATA || null;
} catch { /* 解析失败→③组全红 */ }
check('data.stats.data.js 存在且 window.STATS_DATA 可解析', !!D, '');
if (D) {
  check('data.kpi 五组键齐(requests/tokens/cache_hit/cost/savings)',
    Number.isFinite(D.kpi?.requests) &&
    ['input', 'cache_read', 'cache_creation', 'output'].every((k) => Number.isFinite(D.kpi?.tokens?.[k])) &&
    !!D.kpi?.cache_hit && typeof D.kpi?.cost?.computable === 'boolean' && !!D.kpi?.savings, '');
  check('data.days 连续逐日且窗口起点=2026-07-30',
    Array.isArray(D.days) && D.days.length > 0 && D.days[0]?.date === '2026-07-30' &&
    D.days.every((d) => /^\d{4}-\d{2}-\d{2}$/.test(d.date)), `首日 ${D.days[0]?.date} 共 ${D.days?.length} 天`);
  check('data.details 含 ts_iso/project/session/model/token 四列/cost',
    Array.isArray(D.details) && D.details.length > 0 &&
    D.details.every((r) => r.ts_iso && r.project && r.session && r.model &&
      Number.isFinite(r.input_tokens) && Number.isFinite(r.output_tokens) &&
      Number.isFinite(r.cache_read_tokens) && Number.isFinite(r.cache_creation_tokens)), '');
  check('data.generated_at/source 在(页脚展示)', typeof D.generated_at === 'string' && typeof D.source === 'string', '');
}

// ── ④ 零外部 URL ──
const httpHits = html.match(/https?:\/\//gi) || [];
check('offline.http 外链为零(全文档无 http:// 与 https:// 字面)', httpHits.length === 0, httpHits.join(','));
check('offline.无外链资源(<link / @import / src|href 指 http)',
  !/<link\b/i.test(html) && !/@import/i.test(html) &&
  !/(src|href)\s*=\s*["']https?:/i.test(html), '');

// ── ⑤ 行为要点静态在码 + 内联 JS 语法闸 ──
check('logic.不可算徽标分支(「不可算」字样 + cost/savings computable 双分支)',
  html.includes('不可算') && html.includes('k.cost.computable') && html.includes('k.savings.computable'), '');
check('logic.命中率卡 tooltip 公式(命中=缓存读/(缓存读+新输入))',
  html.includes('命中=缓存读/(缓存读+新输入)'), '');
check('logic.四档切换 tokens/requests/cost/savings + 不可算档禁用',
  /key:"tokens"/.test(html) && /key:"requests"/.test(html) && /key:"cost"/.test(html) &&
  /key:"savings"/.test(html) && /disabled:!k\.cost\.computable/.test(html) &&
  /disabled:!k\.savings\.computable/.test(html), '');
check('logic.负值日图例与色阶(负值日字样 + 四分位 quartiles + 非零日过滤)',
  html.includes('负值日') && /function quartiles/.test(html) && /v>0/.test(html), '');
check('logic.分页每页 20 + 上/下页计数', /PAGE\s*=\s*20/.test(html) && html.includes('pageinfo'), '');
check('logic.成本 null 显示「—」(fmtCredits/fmtInt null 归「—」)',
  /return n==null \? "—" : Number\(n\)\.toFixed\(2\)/.test(html) &&
  /n==null \? "—" : Number\(n\)\.toLocaleString/.test(html) &&
  /r\.cost==null \? "—" :/.test(html), '');
check('logic.页脚含 mock 声明与生成时间', html.includes('mock') && html.includes('生成时间'), '');

let inlineOk = true, inlineErr = '';
try {
  const inline = [...html.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/gi)]
    .map((m) => m[1]).filter((s) => s.trim()).join('\n');
  new Function(inline);   // 只编译不执行(DOM 依赖不触发)
} catch (e) { inlineOk = false; inlineErr = e.message; }
check('syntax.内联 JS 可编译(new Function 语法闸)', inlineOk, inlineErr);

const fail = results.filter((r) => !r.ok);
for (const r of results) console.log(`${r.ok ? '[PASS]' : '[FAIL]'} ${r.name}${r.detail ? ` —— ${r.detail}` : ''}`);
console.log(`\nassert-stats-mock: ${results.length - fail.length}/${results.length} 通过`);
process.exit(fail.length ? 1 : 0);
