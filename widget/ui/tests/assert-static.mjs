#!/usr/bin/env node
/**
 * 票 03 · ui/ 静态产物断言（headless Edge --dump-dom，与 mock 验收同机制）。
 *
 * 跑法：node widget/ui/tests/assert-static.mjs     （Node ≥22，零依赖，全程离线）
 * 退出码：0=全绿；1=有红。
 *
 * file:// + --allow-file-access-from-files：Chromium 默认按 CORS（null origin）拦截
 * file:// 下的 ES 模块；该开关（Chromium 官方开关）放行同目录模块加载，页面得以
 * 零 http 引用地渲染。本机实测：headless Edge 对任何 http（含 127.0.0.1 回环）均
 * 不发请求直接挂起，file:// 是唯一可用通道——离线铁律不受影响。
 *
 * 页面侧配合的 URL 参数（仅测试/演示语境生效，产品路径不受影响）：
 *   static=1   冻结演示时钟于 generated_at（倒计时断言确定性）
 *   dev=1      等效 dev 构建旗标（演示数据 + “演示数据”角标）
 *   gray=1     强制 daemon 不可达灰化态
 *   selftest=1 页面同步自跑六项交互并把结果写进 #selftest-results 的 data-* 属性
 */
import { spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const UI_DIR = resolve(dirname(fileURLToPath(import.meta.url)), '..');

// ── Edge 定位（票面钦定 x86 路径，逐级回退） ──
const EDGE_CANDIDATES = [
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
];
const EDGE = EDGE_CANDIDATES.find((p) => existsSync(p));
if (!EDGE) {
  console.error(`找不到 msedge.exe（试过：${EDGE_CANDIDATES.join(' ; ')}）`);
  process.exit(1);
}

// ── file:// 基址（index.html 绝对路径转 file URL，查询串接在后面） ──
const BASE = pathToFileURL(join(UI_DIR, 'index.html')).href;

/**
 * 起一次 headless Edge dump 渲染后 DOM。
 * --allow-file-access-from-files：放行 file:// 下的 ES 模块加载（见文件头注记）。
 * --virtual-time-budget=1500：虚拟时间推进 1.5s 后才 dump，确保模块脚本执行完毕；
 * 预算小于 30s/60s 定时器周期，轮询与 ticker 不会在 dump 前触发，输出确定。
 * --user-data-dir 用一次性临时目录：避免与正在运行的 Edge 抢默认配置文件。
 */
function dumpDom(qs) {
  const profile = mkdtempSync(join(tmpdir(), 'widget-audit-'));
  try {
    const args = [
      '--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
      `--user-data-dir=${profile}`, '--allow-file-access-from-files',
      '--window-size=132,620', '--virtual-time-budget=1500',
      '--dump-dom', BASE + qs,
    ];
    const r = spawnSync(EDGE, args, { encoding: 'utf8', timeout: 60000, maxBuffer: 64 * 1024 * 1024 });
    const out = r.stdout || '';
    const i = out.search(/<!DOCTYPE html|<html/i);
    if (i < 0) {
      const why = `dump 里找不到 HTML（status=${r.status}，前 300 字：${out.slice(0, 300)}；stderr：${String(r.stderr).slice(0, 300)}`;
      throw new Error(why);
    }
    return out.slice(i);
  } finally {
    try { rmSync(profile, { recursive: true, force: true }); } catch { /* 尽力而为 */ }
  }
}

// ── DOM 解析小工具（属性序无关） ──
const attr = (tag, name) => {
  const m = tag.match(new RegExp(`${name}="([^"]*)"`));
  return m ? m[1] : null;
};
const circles = (dump, cls) =>
  [...dump.matchAll(/<circle\b[^>]*>/g)].map((m) => m[0]).filter((t) => attr(t, 'class') === cls);
const strokeOf = (tag) => (attr(tag, 'style') || '').match(/stroke:\s*(#[0-9A-Fa-f]{6})/)?.[1] || null;
const dashOf = (tag) => {
  const m = (attr(tag, 'style') || '').match(/stroke-dasharray:\s*([\d.]+)\s+([\d.]+)/);
  return m ? `${m[1]} ${m[2]}` : null;
};
const bodyClass = (dump) => attr(dump.match(/<body[^>]*>/)?.[0] || '', 'class') || '';
/** 结构断言一律在剥掉 <script> 内联源码后的 DOM 上做（模板字符串会以假乱真）。 */
const stripScripts = (dump) => dump.replace(/<script\b[\s\S]*?<\/script>/gi, '');

// ══════════════════ 断言集 ══════════════════
const results = [];
const check = (name, ok, detail = '') => results.push({ name, ok, detail: ok ? '' : detail });

async function main() {
  try {
    const MAIN = stripScripts(dumpDom('?static=1&dev=1&selftest=1'));   // 主断言：演示数据 + 自测
    const REL = stripScripts(dumpDom('?static=1'));                     // release 形态：无 dev、live 无 daemon
    const GRAY = stripScripts(dumpDom('?static=1&dev=1&gray=1'));       // 灰化态（dev 开关注入）

    // ① 环几何与 dasharray（剩余%×周长：r43→C270.2，r33→C207.3）
    const rings = circles(MAIN, 'ring');
    check('ring.数量=4', rings.length === 4, `实际 ${rings.length}`);
    check('ring.全部 cx=50 cy=50', rings.every((t) => attr(t, 'cx') === '50' && attr(t, 'cy') === '50'),
      rings.filter((t) => attr(t, 'cx') !== '50' || attr(t, 'cy') !== '50').join(' | ') || '有环圆心偏移');
    const findRing = (r, dash, stroke) => rings.some((t) => attr(t, 'r') === r && dashOf(t) === dash && strokeOf(t) === stroke);
    check('ring.GLM 5h 62%→167.5 270.2 基色蓝', findRing('43', '167.5 270.2', '#5B9BD5'),
      rings.map((t) => `${attr(t, 'r')} ${dashOf(t)} ${strokeOf(t)}`).join(' ; '));
    check('ring.GLM 周 8%→16.6 207.3 红', findRing('33', '16.6 207.3', '#E85D5D'), '');
    check('ring.Kimi 5h 80%→216.1 270.2 基色蓝', findRing('43', '216.1 270.2', '#5B9BD5'), '');
    check('ring.Kimi 周 17%→35.2 207.3 黄', findRing('33', '35.2 207.3', '#E8C33D'), '');
    const tracks = circles(MAIN, 'track');
    check('ring.轨道圈≥5 且圆心 50/50', tracks.length >= 5 && tracks.every((t) => attr(t, 'cx') === '50' && attr(t, 'cy') === '50'),
      `track=${tracks.length}`);

    // ② 结构：四 disc、圆心标识、文字行全部来自契约 JSON
    const discIds = ['glm', 'kimi', 'deepseek', 'handoff'];
    check('disc.四个 data-id 齐', discIds.every((id) => MAIN.includes(`data-id="${id}"`)), '');
    check('disc.圆心标识 GLM/Kimi/摆渡（DS 圆心为金额）',
      ['>GLM<', '>Kimi<', '>摆渡<'].every((s) => MAIN.includes(s)), '');
    check('disc.DS 圆心金额 87.50（吃契约 text）', MAIN.includes('>87.50<'), '');
    check('cap.月/今/周文字行（契约 text）',
      ['月 3.2M tok', '月 5.1M tok', '今 ¥3.10', '周 ¥22.40', '月 ¥12.80', '周 ¥4.20'].every((s) => MAIN.includes(s)), '');
    check('cap.估算紫虚线角标 <i>估</i> ≥6', (MAIN.match(/<i>估<\/i>/g) || []).length >= 6,
      `实际 ${((MAIN.match(/<i>估<\/i>/g) || []).length)}`);
    check('disc.handoff 虚线外框', MAIN.includes('class="houtline"'), '');

    // ③ 倒计时行：格式（演示基准=generated_at）与告警联动
    const c2h28m = (MAIN.match(/2h28m/g) || []).length;
    const c4h01m = (MAIN.match(/4h01m/g) || []).length;
    const c6d11h = (MAIN.match(/6d11h/g) || []).length;
    check('cd.2h28m(GLM 5h) 存在', c2h28m >= 1, `实际 ${c2h28m}`);
    check('cd.4h01m(Kimi 5h) 存在', c4h01m >= 1, `实际 ${c4h01m}`);
    check('cd.6d11h(两个周窗) ≥2', c6d11h >= 2, `实际 ${c6d11h}`);
    check('cd.倒计时行 .cap.cdline ≥2', (MAIN.match(/class="cap cdline"/g) || []).length >= 2, '');
    check('cd.告警加粗 font-weight:600 ≥2', (MAIN.match(/font-weight:600/g) || []).length >= 2, '');

    // ④ 六项交互（selftest 同步自跑，结果落 data-*）
    const st = MAIN.match(/<div id="selftest-results"[^>]*>/)?.[0] || '';
    const stAttr = (k) => new RegExp(`${k}="1"`).test(st);
    check('ix.selftest 完成', stAttr('data-done'), st || '缺 #selftest-results');
    check('ix.tooltip hover 出数字', stAttr('data-tooltip'), st);
    check('ix.详情卡单击展开', stAttr('data-detail'), st);
    check('ix.横竖切换', stAttr('data-layout'), st);
    check('ix.收起/恢复', stAttr('data-collapse'), st);
    check('ix.拖动手柄位移', stAttr('data-drag'), st);
    check('ix.控制台零报错', stAttr('data-console'), st);
    check('ix.详情卡内容（重置+倒计时+口径+最后更新）',
      !MAIN.includes('class="detail hidden"') &&
      MAIN.includes('重置 14:32（2h28m 后）') &&
      MAIN.includes('data.limits[unit:3]') &&
      MAIN.includes('最后更新 12:03'), '');

    // ⑤ dev / release / 灰化
    check('dev.主跑 body 带 dev 且角标在', /(^|\s)dev(\s|$)/.test(bodyClass(MAIN)) && MAIN.includes('id="devBadge"'), bodyClass(MAIN));
    check('dev.release 跑无 dev 类', !/(^|\s)dev(\s|$)/.test(bodyClass(REL)), bodyClass(REL));
    check('dev.release 无演示数据（不假造）', !REL.includes('data-id="glm"') && !REL.includes('87.50'), '');
    check('gray.release=live 无 daemon→conn-down 灰化', /(^|\s)conn-down(\s|$)/.test(bodyClass(REL)) && REL.includes('id="connWarn"'), bodyClass(REL));
    check('gray.dev+gray 注入：有数据但灰化', /(^|\s)conn-down(\s|$)/.test(bodyClass(GRAY)) &&
      GRAY.includes('data-id="glm"') && GRAY.includes('id="connWarn"'), bodyClass(GRAY));

    // ⑥ 数据层/渲染层分离 + provenance 内置映射（源码级；缺文件按空串计，落到断言红）
    const src = (f) => { try { return readFileSync(join(UI_DIR, f), 'utf8'); } catch { return ''; } };
    const html = src('index.html'), css = src('style.css'), appjs = src('app.js'), datajs = src('data.js');
    check('split.index 引模块且无内联数据', html.includes('<script type="module" src="app.js">') &&
      !['generated_at', 'remaining_pct', '3.2M', '87.50'].some((s) => html.includes(s)), '');
    check('split.app.js 无演示数据字面量', !['generated_at', '3.2M', '87.50', '12:03'].some((s) => appjs.includes(s)), '');
    check('split.演示契约只活在 data.js', datajs.includes('generated_at') && datajs.includes('remaining_pct: 62'), '');
    check('split.契约演示副本无 provenance 键', !/['"]provenance['"]\s*:/.test(datajs), '');
    const provKeys = ['coding_plan:window_5h', 'coding_plan:week', 'coding_plan:month_tokens',
      'paygo:balance_cny', 'paygo:spend_today_cny', 'paygo:spend_week_cny', 'paygo:spend_month_cny',
      'handoff:spend_month_cny', 'handoff:spend_week_cny'];
    check('prov.kind+key 映射九条齐', provKeys.every((k) => appjs.includes(`'${k}'`)),
      provKeys.filter((k) => !appjs.includes(`'${k}'`)).join(','));
    check('prov.文案照设计稿原样', ['quota/limit → data.limits[unit:3] → 剩余 = 100 − percentage(38)',
      'coding/v1/usages → usage → remaining/limit',
      'user/balance → balance_infos[0].total_balance = granted + topped_up',
      '台账 · 价格表计价（DeepSeek 官方无 usage API）',
      '账本 handoff 科目 · 本周（估算）'].every((s) => appjs.includes(s)), '');
    check('css.dev 角标规则存在', css.includes('body.dev .dev-badge'), '');
    check('css.灰化规则存在', css.includes('body.conn-down'), '');

    // ⑦ 离线铁律：无外部引用（运行时四文件零 URL 字面量；dump 无外链资源）
    const runtime = { 'index.html': html, 'style.css': css, 'app.js': appjs, 'data.js': datajs };
    const badUrl = Object.entries(runtime).filter(([, t]) => /https?:\/\//.test(t)).map(([f]) => f);
    check('offline.运行时源零 URL 字面量', badUrl.length === 0, badUrl.join(','));
    check('offline.三份 dump 无外链资源', [MAIN, REL, GRAY].every((d) => !/(src|href)\s*=\s*["']https?:\/\//i.test(d)), '');
    check('drag.壳内手柄带 data-tauri-drag-region', html.includes('data-tauri-drag-region'), '');
  } finally {
    // 无常驻资源（file:// 直读，不起服务）
  }

  const fail = results.filter((r) => !r.ok);
  for (const r of results) console.log(`${r.ok ? '[PASS]' : '[FAIL]'} ${r.name}${r.detail ? ` —— ${r.detail}` : ''}`);
  console.log(`\nassert-static: ${results.length - fail.length}/${results.length} 通过`);
  process.exit(fail.length ? 1 : 0);
}

main().catch((e) => { console.error(e); process.exit(1); });
