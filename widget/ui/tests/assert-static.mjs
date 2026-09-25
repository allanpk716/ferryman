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
 *   profile=X  票 04 · 注入显示配置预设（budgets=预算环/覆写；hidden=显隐过滤），见 profile.js PRESETS
 *   superset=1 票 05 · 注入契约超集（未知字段+更高 version+未知 metric key），验证向前兼容不崩
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

// ── file:// 基址（页面绝对路径转 file URL，查询串接在后面；票 04 起支持 settings.html） ──
const pageUrl = (page) => pathToFileURL(join(UI_DIR, page)).href;

/**
 * 起一次 headless Edge dump 渲染后 DOM。
 * --allow-file-access-from-files：放行 file:// 下的 ES 模块加载（见文件头注记）。
 * --virtual-time-budget=1500：虚拟时间推进 1.5s 后才 dump，确保模块脚本执行完毕；
 * 预算小于 30s/60s 定时器周期，轮询与 ticker 不会在 dump 前触发，输出确定。
 * --user-data-dir 用一次性临时目录：避免与正在运行的 Edge 抢默认配置文件。
 */
function dumpDom(qs, page = 'index.html') {
  const profile = mkdtempSync(join(tmpdir(), 'widget-audit-'));
  try {
    const args = [
      '--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
      `--user-data-dir=${profile}`, '--allow-file-access-from-files',
      '--window-size=132,620', '--virtual-time-budget=1500',
      '--dump-dom', pageUrl(page) + qs,
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
/** 任意 dump 上找指定 r/dasharray/stroke 的环（票 05 超集断言与主断言共用几何判据）。 */
const findRingIn = (dump, r, dash, stroke) =>
  circles(dump, 'ring').some((t) => attr(t, 'r') === r && dashOf(t) === dash && strokeOf(t) === stroke);
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

    // ⑧ 票 04 · 设置窗静态形态（settings.html headless dump；目录×默认配置渲染出表格）
    const SETTINGS = stripScripts(dumpDom('?selftest=1', 'settings.html'));
    const sTable = (SETTINGS.match(/<tbody[^>]*>[\s\S]*?<\/tbody>/) || [''])[0];
    check('set.表格行=4', (sTable.match(/<tr\b/g) || []).length === 4,
      `实际 ${(sTable.match(/<tr\b/g) || []).length}`);
    check('set.四对象齐', ['GLM', 'Kimi', 'DeepSeek', '摆渡'].every((s) => sTable.includes(s)),
      sTable.slice(0, 200));
    check('set.默认竖排选中', /name="layout" value="vertical"[^>]*checked/.test(SETTINGS), '');
    check('set.默认倒计时选中', /id="optCdline"[^>]*checked/.test(SETTINGS), '');
    check('set.默认阈值 20/10 ×3 行', (sTable.match(/value="20"/g) || []).length >= 3 &&
      (sTable.match(/value="10"/g) || []).length >= 3, '');
    check('set.DS 预算默认关', /class="f-dsb-on"(?![^>]*checked)/.test(sTable), '');
    check('set.handoff 行无环/无阈值', (() => {
      const m = sTable.match(/<tr data-id="handoff">[\s\S]*?<\/tr>/);
      return !!m && m[0].includes('无环') && m[0].includes('—') && !m[0].includes('f-color');
    })(), '');
    check('set.月预算“不设=文字计数”说明在页', SETTINGS.includes('不设=文字计数'), '');
    check('set.重置提示元素存在', SETTINGS.includes('id="resetNotice"'), '');
    const sst = (SETTINGS.match(/<div id="selftest-results"[^>]*>/) || [''])[0];
    const sAttr = (k) => new RegExp(`${k}="1"`).test(sst);
    check('set.selftest 完成', sAttr('data-done'), sst || '缺 #selftest-results');
    check('set.行数自测', sAttr('data-rows'), sst);
    check('set.默认值自测', sAttr('data-defaults'), sst);
    check('set.控制台零报错', sAttr('data-console'), sst);

    // ⑨ 票 04 · profile 纯逻辑（node 直跑 profile.js 导出，零 DOM）
    let PJ = null;
    try { PJ = await import('../profile.js'); } catch { /* 缺文件→本组全红 */ }
    check('pure.profile.js 可被 node 导入', !!PJ, 'import 失败');
    if (PJ) {
      const d = PJ.normalizeProfile(null).profile;
      check('pure.空输入→默认', d.layout === 'vertical' && d.show_countdown === true &&
        Object.keys(d.objects).length === 0, JSON.stringify(d));
      const bad = PJ.normalizeProfile({ layout: 'diagonal', show_countdown: 'no',
        thresholds: { yellow: 'x', red: -3 },
        objects: { glm: 'junk', kimi: { month_budget: 4000000 } } });
      check('pure.坏值修复', bad.profile.layout === 'vertical' && bad.profile.show_countdown === true &&
        bad.repaired === true && bad.profile.objects.kimi.month_budget === 4000000 &&
        !bad.profile.objects.glm, JSON.stringify(bad));
      const okn = PJ.normalizeProfile({ layout: 'horizontal', show_countdown: false,
        objects: { glm: { visible: true, thresholds: { yellow: 30, red: 15 }, order: 2 },
                   deepseek: { ds_budget: { enabled: true, amount_cny: 300 } } } });
      check('pure.合法值保留且不误报修复', okn.profile.layout === 'horizontal' &&
        okn.profile.show_countdown === false && okn.profile.objects.glm.thresholds.yellow === 30 &&
        okn.profile.objects.glm.order === 2 &&
        okn.profile.objects.deepseek.ds_budget.amount_cny === 300 && okn.repaired === false,
        JSON.stringify(okn));
      const R = PJ.remainingPctOfBudget;
      check('pure.预算剩余 3.2M/4M=20%', R(3200000, 4000000) === 20, String(R(3200000, 4000000)));
      check('pure.预算钳制 0..100/非法 null',
        R(0, 100) === 100 && R(150, 100) === 0 && R(null, 100) === null &&
        R(10, 0) === null && R(10, -5) === null, '');
      const eo = PJ.effectiveObject({ layout: 'vertical', show_countdown: true, objects: {} }, 'glm');
      check('pure.effectiveObject 缺省=可见/20/10/无预算',
        eo.visible === true && eo.thresholds.yellow === 20 && eo.thresholds.red === 10 &&
        eo.month_budget === null && eo.ds_budget === null && eo.order === null, JSON.stringify(eo));
      const entries = [{ id: 'glm' }, { id: 'kimi' }, { id: 'deepseek' }, { id: 'handoff' }];
      const ov = PJ.orderedVisible(entries,
        PJ.normalizeProfile({ objects: { handoff: { order: -1 }, glm: { visible: false } } }).profile);
      check('pure.可见过滤+排序',
        JSON.stringify(ov.map((x) => x.id)) === JSON.stringify(['handoff', 'kimi', 'deepseek']),
        JSON.stringify(ov.map((x) => x.id)));
      const ov2 = PJ.orderedVisible(entries, PJ.normalizeProfile(null).profile);
      check('pure.无序值=契约序',
        JSON.stringify(ov2.map((x) => x.id)) === JSON.stringify(['glm', 'kimi', 'deepseek', 'handoff']),
        JSON.stringify(ov2.map((x) => x.id)));
    }

    // ⑩ 票 04 · 预算环（URL profile=budgets 注入演示配置；产品路径无该参数）
    // 预设：GLM 月预算 4M tok（已用 3.2M→剩 20%）+5h 基色覆写；Kimi 月预算 8M（已用
    // 5.1M→剩 36.25%）+阈值覆写 85/70；DeepSeek 预算 ¥100（月花 58.6→剩 41.4%）；handoff 提前。
    const PROF = stripScripts(dumpDom('?static=1&dev=1&profile=budgets'));
    const prings = circles(PROF, 'ring');
    const findRingP = (r, dash, stroke) =>
      prings.some((t) => attr(t, 'r') === r && dashOf(t) === dash && strokeOf(t) === stroke);
    check('prof.默认跑无 r23 内环（预算未设=无紫环）',
      !circles(MAIN, 'ring').some((t) => attr(t, 'r') === '23'), '');
    check('prof.GLM 紫环 20%→28.9 144.5 基色紫', findRingP('23', '28.9 144.5', '#9B7EDE'),
      prings.map((t) => `${attr(t, 'r')} ${dashOf(t)} ${strokeOf(t)}`).join(' ; '));
    check('prof.Kimi 月环 36.25%→52.4 144.5（阈值覆写→红）', findRingP('23', '52.4 144.5', '#E85D5D'), '');
    check('prof.Kimi 5h 80% 阈值覆写→黄', findRingP('43', '216.1 270.2', '#E8C33D'), '');
    check('prof.GLM 5h 基色覆写 #E056FD', findRingP('43', '167.5 270.2', '#E056FD'), '');
    check('prof.DS 绿环 41.4%→111.9 270.2', findRingP('43', '111.9 270.2', '#6BBF8A'), '');
    check('prof.预算环 caption 保留（月 3.2M tok 仍在）', PROF.includes('月 3.2M tok'), '');
    check('prof.handoff 提前（order 覆写）',
      PROF.indexOf('data-id="handoff"') >= 0 &&
      PROF.indexOf('data-id="handoff"') < PROF.indexOf('data-id="glm"'), '');

    // ⑪ 票 04 · 显隐过滤（URL profile=hidden 注入：glm/handoff visible=false）
    const HID = stripScripts(dumpDom('?static=1&dev=1&profile=hidden'));
    check('vis.hidden 预设滤除 glm/handoff',
      !HID.includes('data-id="glm"') && !HID.includes('data-id="handoff"') &&
      HID.includes('data-id="kimi"') && HID.includes('data-id="deepseek"'), '');

    // ⑫ 票 05 · 契约防御：超集 JSON 向前兼容（?superset=1 注入未知顶层/上游字段 +
    // 更高 version + 未知 metric key）。验收：渲染不崩、未知字段被忽略、已知内容零漂移。
    const SUP = stripScripts(dumpDom('?static=1&dev=1&selftest=1&superset=1'));
    const supSt = (SUP.match(/<div id="selftest-results"[^>]*>/) || [''])[0];
    check('sup.超集注入生效且渲染不崩（未知 metric 上详情卡+四 disc 齐+控制台零报错）',
      SUP.includes('window_10h') &&
      ['data-id="glm"', 'data-id="kimi"', 'data-id="deepseek"', 'data-id="handoff"'].every((s) => SUP.includes(s)) &&
      /data-console="1"/.test(supSt), supSt || '缺 #selftest-results');
    check('sup.更高 version 不拒渲染（version=999，GLM 5h 环几何与基线逐字一致）',
      findRingIn(SUP, '43', '167.5 270.2', '#5B9BD5'), '');
    check('sup.未知 metric key 不进环（环数仍=4，剩余 50% 的 window_10h 不加环）',
      circles(SUP, 'ring').length === 4, `实际 ${circles(SUP, 'ring').length}`);
    check('sup.未知顶层/上游字段被忽略（canary/experimental_rollout/future_flag 不落 DOM）',
      !['canary', 'experimental_rollout', 'future_flag'].some((s) => SUP.includes(s)), '');
    check('sup.已知内容与基线逐字一致（87.50 / 月 3.2M tok / 重置 14:32）',
      ['>87.50<', '月 3.2M tok', '重置 14:32（2h28m 后）'].every((s) => SUP.includes(s)), '');

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
    const shtml = src('settings.html'), sjs = src('settings.js'), pjs = src('profile.js');
    check('split.settings 页引模块且无内联数据', shtml.includes('<script type="module" src="settings.js">') &&
      !['generated_at', 'remaining_pct', '3.2M', '87.50'].some((s) => shtml.includes(s)), '');
    check('split.settings.js 无演示数据字面量', !['generated_at', '3.2M', '87.50', '12:03'].some((s) => sjs.includes(s)), '');
    check('split.profile.js 无演示数据字面量', !['generated_at', '3.2M', '87.50', '12:03'].some((s) => pjs.includes(s)), '');

    // m2 · 收尾票 03 · 自适应轮询（评审 M2）：退避常量/代际防护/poke 暴露/恢复事件接线。
    // 断言正则与所开代码形态逐字匹配（F9 教训：箭头包装调用不得用裸函数名字面量匹配，
    // 轮询在场必须命中 `setTimeout(() => loop` 形，严禁裸 /setTimeout\(loop/ 永假断言）。
    check('m2.退避常量与自适应轮询在场',
      datajs.includes('export const RETRY_MS = 10000;') &&
      datajs.includes('export const RETRY_MAX_MS = 60000;') &&
      /setTimeout\(\(\) => loop/.test(datajs), '');
    check('m2.代际防护在场（声明行 gen=0 + myGen !== gen 双闸）',
      /let timer = null, stopped = false, fails = 0, gen = 0;/.test(datajs) &&
      datajs.includes('myGen !== gen'), '');
    check('m2.startPolling 暴露 poke（return { stop: 形）',
      /function poke\(\)/.test(datajs) && datajs.includes('return { stop:'), '');
    check('m2.app 监听 widget-restored 即拉',
      appjs.includes("'widget-restored'") && appjs.includes('poller.poke()'), '');

    // ⑦ 离线铁律：无外部引用（运行时源零 URL 字面量；dump 无外链资源；票 04 起含设置窗三件）
    const runtime = { 'index.html': html, 'style.css': css, 'app.js': appjs, 'data.js': datajs,
                      'settings.html': shtml, 'settings.js': sjs, 'profile.js': pjs };
    const badUrl = Object.entries(runtime).filter(([, t]) => /https?:\/\//.test(t)).map(([f]) => f);
    check('offline.运行时源零 URL 字面量', badUrl.length === 0, badUrl.join(','));
    check('offline.全部 dump 无外链资源',
      [MAIN, REL, GRAY, SETTINGS, PROF, HID, SUP].every((d) => !/(src|href)\s*=\s*["']https?:\/\//i.test(d)), '');
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
