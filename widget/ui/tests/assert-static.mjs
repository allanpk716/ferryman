#!/usr/bin/env node
/**
 * 票 03 · ui/ 静态产物断言（headless Chromium 系 --dump-dom，Edge 优先逐级回退，与 mock 验收同机制）。
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
import { createHash } from 'node:crypto';
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const UI_DIR = resolve(dirname(fileURLToPath(import.meta.url)), '..');

// ── headless 浏览器定位（票面钦定 x86 Edge 路径，逐级回退） ──
// 2026-09-29 事故：本机 Edge（153/154 共存，疑似升级半截状态）headless 对一切
// URL（含 about:blank）status=0 但零输出——existsSync 探测不够，候选要「真能
// dump 出 HTML」才算数，故启动时逐个试 dump about:blank，首个出活的当选。
const BROWSER_CANDIDATES = [
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
];
/** 试 dump about:blank，出 HTML 才认（防空输出的半截安装）。 */
function probeBrowser(bin) {
  const profile = mkdtempSync(join(tmpdir(), 'widget-probe-'));
  try {
    const r = spawnSync(bin, ['--headless', '--disable-gpu', '--no-first-run',
      '--no-default-browser-check', `--user-data-dir=${profile}`, '--dump-dom', 'about:blank'],
      { encoding: 'utf8', timeout: 30000 });
    return /<html/i.test(r.stdout || '');
  } catch { return false; } finally {
    try { rmSync(profile, { recursive: true, force: true }); } catch { /* 尽力而为 */ }
  }
}
const EDGE = BROWSER_CANDIDATES.find((p) => existsSync(p) && probeBrowser(p));
if (!EDGE) {
  console.error(`找不到可用 headless 浏览器（试过：${BROWSER_CANDIDATES.join(' ; ')}）`);
  process.exit(1);
}
console.log(`headless 浏览器：${EDGE}`);

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

/**
 * 票 02 · 几何通道：--dump-dom 只出顶层文档，而 getComputedStyle/getBoundingClientRect
 * 需要真实布局——故把 index.html（?profile=compact）装进临时 wrapper 页的 iframe，由
 * wrapper 的父页脚本量测并把结果写进自身 #geo-results-done 节点随 dump 带回。
 * iframe 先给高 700 量 .widget scrollHeight，再把视口收到该高（headless 里复刻壳层
 * fitHeight 语义「窗高=内容高」，app.js 的 fitHeight 无壳静默跳过）：此后
 * innerHeight−6−末盘底 才是真「内容到面板底边距」。零依赖、离线、无窗口。
 * 夜链票 04（2026-09-30）同通道扩展：悬浮态首/末盘左右对称（left+right≈80）、
 * 手动挂 docked-left 后内容重心补偿（2×内容中心≈74）、hover 触发 tooltip 后
 * tip.right≤80（收口不裁切）——app.js 监听的是 mouseover/mousemove（非 mouseenter），
 * 故 wrapper 里 dispatchEvent 同名事件复刻 hover。
 */
function dumpGeo(qs) {
  const profile = mkdtempSync(join(tmpdir(), 'widget-geo-'));
  try {
    const wrapper = [
      '<!DOCTYPE html><html><head><meta charset="utf-8">',
      '<style>html,body{margin:0;padding:0}</style></head><body>',
      '<iframe id="f" frameborder="0" style="border:0;display:block"></iframe>',
      '<pre id="geo-results">pending</pre>',
      '<script>',
      '(function(){',
      '  var f=document.getElementById("f");',
      '  function write(a){var el=document.getElementById("geo-results");',
      '    var p=[];for(var k in a)p.push(k+"="+a[k]);el.textContent=p.join(" ");el.id="geo-results-done";}',
      '  f.style.width="80px";f.style.height="700px";f.src=__INDEX_URL__;',
      '  var armed=false;',
      '  f.addEventListener("load",function(){if(armed)return;armed=true;setTimeout(step1,200);});',
      '  function step1(){try{',
      '    var d=f.contentDocument,wg=d&&d.getElementById("widget");',
      '    if(!wg){write({err:"no-widget"});return;}',
      '    f.style.height=wg.scrollHeight+"px";', // 视口收到内容高=壳层 fitHeight 语义
      // 老版 headless+虚拟时间下 rAF 不触发（dump 实证 step1 跑了 rAF 没跑）；
      // 读 rect 本就强制同步重排，setTimeout 一跳足矣（虚拟时间推进定时器已实证）
      '    setTimeout(step2,50);',
      '  }catch(e){write({err:String(e)});}}',
      '  function step2(){try{',
      '    var w=f.contentWindow,d=w.document,wg=d.getElementById("widget");',
      '    var discs=d.querySelectorAll(".disc");',
      '    if(!discs.length){write({err:"no-discs"});return;}',
      '    var cs=w.getComputedStyle(wg),innerH=w.innerHeight;',
      '    var lb=discs[discs.length-1].getBoundingClientRect().bottom;',
      '    var grip=d.querySelector(".grip");',
      '    var gt=grip?grip.getBoundingClientRect().top:-999;',
      // 夜链票 04：对称/重心/tooltip 同通道量测（r0/rN 必须在挂 docked-left 前量，
      // 才是悬浮态；tooltip 用 app.js 真实监听的事件名 mouseover/mousemove 复刻 hover）
      '    var r0=discs[0].getBoundingClientRect(),rN=discs[discs.length-1].getBoundingClientRect();',
      '    var tip=d.getElementById("tooltip");',
      '    var mev=function(t){return new w.MouseEvent(t,{clientX:40,clientY:60,bubbles:true,cancelable:true,view:w});};',
      '    discs[0].dispatchEvent(mev("mouseover"));',
      '    discs[0].dispatchEvent(mev("mousemove"));',
      '    var tr=tip.getBoundingClientRect();',
      '    wg.classList.add("docked-left");',
      '    var rd=discs[0].getBoundingClientRect();',
      '    var dck=rd.left+rd.right;', // 内容中心×2；docked-left 面板可视区 0..74 中心 37→目标 74
      '    wg.classList.remove("docked-left");',
      '    write({done:1,pt:cs.paddingTop,pb:cs.paddingBottom,h:innerH.toFixed(2),',
      '      lb:lb.toFixed(2),bg:(innerH-6-lb).toFixed(2),gg:(gt-6).toFixed(2),',
      '      sym1:(r0.left+r0.right).toFixed(3),sym2:(rN.left+rN.right).toFixed(3),',
      '      dck:dck.toFixed(3),tipR:tr.right.toFixed(3),tipH:tip.classList.contains("hidden")?1:0});',
      '  }catch(e){write({err:String(e)});}}',
      '})();',
      '<\/script></body></html>',
    ].join('\n');
    const wrapperPath = join(profile, 'wrapper.html');
    writeFileSync(wrapperPath, wrapper.replace('__INDEX_URL__', JSON.stringify(pageUrl('index.html') + qs)));
    const args = [
      '--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
      `--user-data-dir=${join(profile, 'edge')}`, '--allow-file-access-from-files',
      '--window-size=200,800', '--virtual-time-budget=3000',
      '--dump-dom', pathToFileURL(wrapperPath).href,
    ];
    const r = spawnSync(EDGE, args, { encoding: 'utf8', timeout: 60000, maxBuffer: 64 * 1024 * 1024 });
    return r.stdout || '';
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
    check('disc.圆心标识 GLM/Kimi + 交接月次数（DS 圆心为金额；0.2.3 交接盘圆心=次数）',
      ['>GLM<', '>Kimi<', '>23<', '>次 · 本月<'].every((s) => MAIN.includes(s)), '');
    check('disc.DS 圆心金额 87.50（吃契约 text）', MAIN.includes('>87.50<'), '');
    check('cap.月文字行 + 交接计数两行（契约 text；0.2.3 DS 只显月花费、交接盘月/周分行）',
      ['月 3.2M tok', '月 5.1M tok', '月 ¥58.60', '月 23 次', '周 5 次'].every((s) => MAIN.includes(s)) &&
      !MAIN.includes('今 ¥3.10'), '');
    check('cap.估算紫虚线角标 <i>估</i> =5（0.2.4 浮层退役：图例帮助里那条 <i>估</i> 随 overlay 迁入 settings.html 帮助区，MAIN 只剩 5 条 cap 角标）',
      (MAIN.match(/<i>估<\/i>/g) || []).length === 5,
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

    // ⑧ 票 04 · 设置窗静态形态（settings.html headless dump；目录×默认配置渲染出卡片）
    // 0.2.4 卡片式重排：表格→每对象一张卡（.obj-card[data-id]），区块「外观与布局」/
    // 「图例与帮助」（<details> 默认收起），旧 6 列表头退役。
    const SETTINGS = stripScripts(dumpDom('?selftest=1', 'settings.html'));
    const sCards = (SETTINGS.match(/<div id="objCards">[\s\S]*?(?=<details)/) || [''])[0];
    check('set.卡片=4', (sCards.match(/<div class="obj-card" data-id=/g) || []).length === 4,
      `实际 ${(sCards.match(/<div class="obj-card" data-id=/g) || []).length}`);
    check('set.四对象齐', ['GLM', 'Kimi', 'DeepSeek', '交接'].every((s) => sCards.includes(s)),
      sCards.slice(0, 200));
    check('set.默认竖排选中', /name="layout" value="vertical"[^>]*checked/.test(SETTINGS), '');
    check('set.默认倒计时选中', /id="optCdline"[^>]*checked/.test(SETTINGS), '');
    check('set.默认阈值 20/10 ×3 卡', (sCards.match(/value="20"/g) || []).length >= 3 &&
      (sCards.match(/value="10"/g) || []).length >= 3, '');
    check('set.DS 预算默认关', /class="f-dsb-on"(?![^>]*checked)/.test(sCards), '');
    check('set.handoff 卡无环/无阈值（仅显隐与顺序）', (() => {
      const m = sCards.slice(sCards.indexOf('data-id="handoff"')); // handoff 默认序为末卡
      return !!m && m.includes('无环，仅显隐与顺序') && !m.includes('f-color') && !m.includes('f-th');
    })(), '');
    check('set.月预算“不设=文字计数”说明在页', SETTINGS.includes('不设=文字计数'), '');
    check('set.区块字样（外观与布局/图例与帮助）+ details 默认收起',
      SETTINGS.includes('外观与布局') && SETTINGS.includes('图例与帮助') &&
      SETTINGS.includes('<details') && !SETTINGS.includes('<details open'), '');
    check('set.无旧表头残留', !SETTINGS.includes('>环色</th>') && !SETTINGS.includes('<table'), '');
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
    check('index.浮层退役（0.2.4：不含 id="settings" 的窗内设置浮层）', !html.includes('id="settings"'), '');
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
    check('set.src 区块字样（外观与布局/图例与帮助）+ <details>（0.2.4 卡片式）',
      shtml.includes('外观与布局') && shtml.includes('图例与帮助') && shtml.includes('<details'), '');
    check('set.src 无旧表头残留（0.2.4：无 >环色</th> 六列结构与 <table>）',
      !shtml.includes('>环色</th>') && !shtml.includes('<table'), '');
    check('split.settings 页引模块且无内联数据', shtml.includes('<script type="module" src="settings.js">') &&
      !['generated_at', 'remaining_pct', '3.2M', '87.50'].some((s) => shtml.includes(s)), '');
    check('split.settings.js 无演示数据字面量', !['generated_at', '3.2M', '87.50', '12:03'].some((s) => sjs.includes(s)), '');
    check('split.profile.js 无演示数据字面量', !['generated_at', '3.2M', '87.50', '12:03'].some((s) => pjs.includes(s)), '');

    // ⑬ 0.2.5 · 紧凑档迷你标签行 + 交接盘大数字（源码级 + 紧凑 dump 级）
    const COMP = stripScripts(dumpDom('?static=1&dev=1&profile=compact'));
    check('tag.app.js mini-tag 出口 + 三品牌色 hex + handoff 正名「交接」',
      appjs.includes('mini-tag') && appjs.includes('#1783FF') && appjs.includes('#4268FA') &&
      appjs.includes('#4D6BFE') && appjs.includes("p.id === 'handoff' ? '交接'"), '');
    check('tag.style.css .mini-tag 与 .disc.handoff .c-pct 规则在场',
      css.includes('.mini-tag{') && css.includes('.disc.handoff .c-pct'), '');
    check('tag.settings 帮助句在场（名字+品牌色小点 / 圆心大数字口径）',
      shtml.includes('每盘下方显示名字+品牌色小点') && shtml.includes('交接本月次数'), '');
    const librs = (() => { try { return readFileSync(join(UI_DIR, '..', 'src-tauri', 'src', 'lib.rs'), 'utf8'); } catch { return ''; } })();
    check('tag.lib.rs COMPACT_H=322 与推导注释同新值（内容实测 63×303）',
      librs.includes('COMPACT_H_LOGICAL: f64 = 322.0') && librs.includes('内容实测 63×303'), '');
    check('tag.紧凑 dump：4 条 mini-tag（GLM/Kimi/DeepSeek/交接 + 品牌色点）',
      (COMP.match(/class="mini-tag"/g) || []).length === 4 &&
      ['background:#4268FA"></i>GLM', 'background:#1783FF"></i>Kimi',
       'background:#4D6BFE"></i>DeepSeek', 'background:var(--cmonth)"></i>交接']
        .every((s) => COMP.includes(s)), '');
    check('tag.紧凑交接盘圆心=单行 c-pct 23（无「次 · 本月」）；完整档 MAIN 无 mini-tag',
      COMP.includes('<text x="50" y="60" class="c-pct">23</text>') &&
      !COMP.includes('次 · 本月') && !MAIN.includes('mini-tag'), '');

    // ⑭ 0.2.6 · 窗高随可见盘数自适应（缺省+前端上报；源码级断言，librs 见 ⑬）
    check('fit.lib.rs 注册 fit_height 命令 + DesignHeights 状态在场',
      /generate_handler!\[[\s\S]*\bfit_height\b[\s\S]*\]/.test(librs) &&
      librs.includes('struct DesignHeights(Mutex<(f64, f64)>)'), '');
    check('fit.lib.rs design_size_for / fit_height_decide 纯函数在场',
      librs.includes('fn design_size_for(') && librs.includes('fn fit_height_decide('), '');
    check('fit.lib.rs 常量注释口径已改「缺省」（0.2.6 起实际高度由前端上报覆盖）',
      librs.includes('缺省高度') && librs.includes('fit_height'), '');
    check('fit.app.js fitHeight 出口 + scrollHeight 量法（max-height 钳 rect 不钳 scrollHeight）',
      appjs.includes('function fitHeight()') && appjs.includes('widget.scrollHeight') &&
      appjs.includes('fitHeight();'), '');
    check('fit.app.js 守卫在场（tray-collapsed 量出 0 / 无 summary 防启动期收缩）',
      appjs.includes("classList.contains('tray-collapsed') || !state.summary"), '');

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

    // ⑮ 票 02 · 紧凑档底部 8 逻辑 px＋顶部顺手修：padding 8/4/4→10/4/14
    //（面板画在 .widget::before 四边 inset 6、scrollHeight 不含 ::before：底距=14−6=8、
    // 顶距=10−6=4；内容/窗高 +12 经 fitHeight 链收敛。Rust 零改动。）
    check('pad.style.css 紧凑行恰为新值 10/4/14 且旧值 8/4/4 退场',
      css.includes('body.compact .widget{gap:4px; padding:10px 4px 14px}') &&
      !css.includes('padding:8px 4px 4px'), '');
    check('pad.style.css 全档通用 .widget padding 10/6/6 未动（本票只改 compact 块）',
      css.includes('padding:10px 6px 6px;'), '');
    // 完整档逐字节基线（sha256 全串；票 02 改 CSS 前定格，compact 不在列——它本票就是要变）
    const FULL_BASELINE = {
      MAIN: 'a32811fe1ff085f50dfea0b34e3b09bad91b8bb766812821a32e9d42be96a2f2',
      REL: '0fd413877fd73327929ef74f491e6ae66caf8cdf0cc050e94ade7657327f473f',
      GRAY: 'f0aa38cd07ec208f6174943808b1ff926d441cb1df2cf374a1fd97689f3bcd5d',
      PROF: 'e13636ac03425bd2d97c6304b20c57fb609c84a6e0807d74650f42d38d3f88b7',
      HID: '2a61d41fec16099298f752931a3f45fec89361ee9200276f7d1965555b3f3b69',
      SUP: '25e0bc7ceeebd505e562843f3b4c048c469fc3416a7e5d183de9197656b74c53',
    };
    const sha = (s) => createHash('sha256').update(s).digest('hex');
    const actualSha = { MAIN: sha(MAIN), REL: sha(REL), GRAY: sha(GRAY), PROF: sha(PROF), HID: sha(HID), SUP: sha(SUP) };
    const drift = Object.keys(FULL_BASELINE).filter((k) => FULL_BASELINE[k] !== actualSha[k]);
    check('full.完整档逐字节基线不回归（MAIN/REL/GRAY/PROF/HID/SUP 渲染产物 sha256）',
      drift.length === 0, drift.map((k) => `${k} 实际=${actualSha[k]}`).join(' ; '));
    // 运行时几何（wrapper iframe 通道，见 dumpGeo）：compact computed padding + 底距/顶距
    const GEODUMP = stripScripts(dumpGeo('?static=1&dev=1&profile=compact'));
    const geoRaw = (GEODUMP.match(/id="geo-results-done">([^<]*)</) || [])[1] || '';
    const geoKV = Object.fromEntries(geoRaw.split(' ').filter(Boolean).map((s) => s.split('=')));
    check('geo.headless 几何通道出活（wrapper iframe 量到紧凑渲染完成）',
      geoKV.done === '1', geoRaw || 'dump 无 geo-results-done——wrapper 通道未出活');
    check('geo.compact computed padding-top=10px 且 padding-bottom=14px',
      geoKV.pt === '10px' && geoKV.pb === '14px', `实际 pt=${geoKV.pt} pb=${geoKV.pb}`);
    check('geo.底距 innerHeight−6−末盘底 ≥ 8（面板底边内 8 逻辑 px）',
      parseFloat(geoKV.bg) >= 8, `实际 ${geoKV.bg}`);
    check('geo.grip 顶距 grip.top−6 ≥ 3（顺手修 2→4）',
      parseFloat(geoKV.gg) >= 3, `实际 ${geoKV.gg}`);
    console.log(`geo.观测：窗高(scrollHeight 收敛)=${geoKV.h} 末盘底=${geoKV.lb} 底距=${geoKV.bg} grip顶距=${geoKV.gg}（padding pt=${geoKV.pt} pb=${geoKV.pb}）`);

    // ⑯ 夜链票 04 · 内容收口对称＋贴边重心补偿＋tooltip 收口（几何通道扩展，见 dumpGeo）
    // 面板净宽 68=80−左右 inset 6；.disc 限宽 60 → 悬浮态左右缝各 10 逻辑 px 整数对称。
    check('geo4.悬浮对称：首盘 left+right≈80（±0.6）',
      Math.abs(parseFloat(geoKV.sym1) - 80) <= 0.6, `实际 sym1=${geoKV.sym1}`);
    check('geo4.悬浮对称：末盘 left+right≈80（±0.6）',
      Math.abs(parseFloat(geoKV.sym2) - 80) <= 0.6, `实际 sym2=${geoKV.sym2}`);
    // docked-left：面板可视区 0..74（左 inset 归零），内容中心×2 目标 74（±0.6）
    check('geo4.docked-left 重心补偿：内容中心×2≈74（±0.6）',
      Math.abs(parseFloat(geoKV.dck) - 74) <= 0.6, `实际 dck=${geoKV.dck}`);
    // tooltip：hover 后可见且右缘不伸出 80 宽窗（app.js clamp 以 innerWidth−4 为限）
    check('geo4.tooltip 收口：hover 后 tip.right≤80',
      geoKV.tipH === '0' && parseFloat(geoKV.tipR) <= 80,
      `tipR=${geoKV.tipR} hidden=${geoKV.tipH}`);
    console.log(`geo4.观测：首盘 left+right=${geoKV.sym1} 末盘=${geoKV.sym2} docked-left 中心×2=${geoKV.dck} tooltip right=${geoKV.tipR}（hidden=${geoKV.tipH}）`);

    // ⑦ 离线铁律：无外部引用（运行时源零 URL 字面量；dump 无外链资源；票 04 起含设置窗三件）
    const runtime = { 'index.html': html, 'style.css': css, 'app.js': appjs, 'data.js': datajs,
                      'settings.html': shtml, 'settings.js': sjs, 'profile.js': pjs };
    const badUrl = Object.entries(runtime).filter(([, t]) => /https?:\/\//.test(t)).map(([f]) => f);
    check('offline.运行时源零 URL 字面量', badUrl.length === 0, badUrl.join(','));
    check('offline.全部 dump 无外链资源',
      [MAIN, REL, GRAY, SETTINGS, PROF, HID, SUP, COMP].every((d) => !/(src|href)\s*=\s*["']https?:\/\//i.test(d)), '');
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
