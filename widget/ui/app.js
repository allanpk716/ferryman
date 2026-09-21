/**
 * 票 03 · 渲染层：只吃契约 v0 JSON（数据层=data.js），产出门内全部 UI。
 *
 * 语义基线=已验收 mock（docs/research/20260921_悬浮窗mock.html）：
 * 环剩余制/基色/告警联动、倒计时行颜色随环+告警加粗、圆心标识、估算紫虚线角标与
 * 查询绿实线角标分标、tooltip、详情卡（值/上限/重置时刻+倒计时/最后更新）、横竖切换。
 *
 * widget 内置映射（不进契约，spec rev1）：
 *  - PROVENANCE_BASE / PROVENANCE_BY_ID：kind+key → 字段出处文案（九条 kind:key 全覆盖；
 *    演示上游的 id 级覆写保文案与设计稿逐字一致——同 kind 不同供应商文案有别，v0 的已知局限）
 *  - DETAIL_NAME_OF：详情卡长名（圆心短名=label 吃契约；长名是展示别名，缺省回落 label）
 */
import * as data from './data.js';

// ── 控制台错误捕获（自测第六项：控制台零报错） ──
const consoleErrors = [];
window.addEventListener('error', (e) => consoleErrors.push(String(e.message || e)));
const origError = console.error.bind(console);
console.error = (...a) => { consoleErrors.push(a.map(String).join(' ')); origError(...a); };

// ── DOM 句柄 ──
const widget = document.getElementById('widget');
const tip = document.getElementById('tooltip');
const detail = document.getElementById('detail');
const settings = document.getElementById('settings');
const grip = document.querySelector('#widget .grip');
const restoreBtn = document.getElementById('restoreBtn');

/** @type {{summary:data.Summary|null, reachable:boolean, detailId:string|null, showCdline:boolean}} */
const state = { summary: null, reachable: true, detailId: null, showCdline: true };

/** Tauri 壳内（v2 恒注入 __TAURI_INTERNALS__）：拖动/收出走原生（票 02），JS 演示路径不接管。 */
const inShell = () => !!window.__TAURI_INTERNALS__;

// ── 配色（环基色/告警色与 mock 同源） ──
function getVar(n) { return getComputedStyle(document.documentElement).getPropertyValue(n).trim(); }
const COLORS = { window_5h: getVar('--c5h'), week: getVar('--cweek'),
                 month_budget: getVar('--cmonth'), ds_budget: getVar('--cds') };

/** 环色：基色，<20% 黄、<10% 红（pct 为空=无环指标，回落基色）。 */
function ringColor(key, pct) {
  const base = COLORS[key] || COLORS.window_5h;
  if (pct == null) return base;
  if (pct < 10) return getVar('--alert-r');
  if (pct < 20) return getVar('--alert-y');
  return base;
}

// ── provenance 内置映射（不进契约） ──
/** kind+key → 文案（九条，覆盖契约 v0 全部 metric key 组合）。 */
const PROVENANCE_BASE = {
  'coding_plan:window_5h': 'quota/limit → data.limits[unit:3] → 剩余 = 100 − percentage(38)',
  'coding_plan:week': 'quota/limit → data.limits[unit:6] → 剩余 = 100 − percentage(92)',
  'coding_plan:month_tokens': '台账四列 · 本自然月聚合（智谱端点无绝对值，月度只能本地估算）',
  'paygo:balance_cny': 'user/balance → balance_infos[0].total_balance = granted + topped_up',
  'paygo:spend_today_cny': '台账 · 价格表计价（DeepSeek 官方无 usage API）',
  'paygo:spend_week_cny': '台账 · 价格表计价（DeepSeek 官方无 usage API）',
  'paygo:spend_month_cny': '台账 · 价格表计价（DeepSeek 官方无 usage API）', // mock 无此实例，按枚举补位
  'handoff:spend_month_cny': '账本 handoff 科目 · 本自然月（估算）',
  'handoff:spend_week_cny': '账本 handoff 科目 · 本周（估算）',
};
/** 演示上游 id 级覆写（同 kind 不同供应商文案有别，保设计稿逐字一致）。 */
const PROVENANCE_BY_ID = {
  kimi: {
    window_5h: 'coding/v1/usages → limits[].detail → remaining/limit',
    week: 'coding/v1/usages → usage → remaining/limit',
    month_tokens: '台账四列 · 本自然月聚合（估算）',
  },
};
/** @param {data.Upstream} u @param {data.Metric} m @returns {string} */
const provenanceOf = (u, m) => PROVENANCE_BY_ID[u.id]?.[m.key] ?? PROVENANCE_BASE[`${u.kind}:${m.key}`] ?? '';

/** 详情卡长名（展示别名；缺省回落契约 label）。 */
const DETAIL_NAME_OF = { glm: '智谱 GLM', kimi: 'Kimi Coding', deepseek: 'DeepSeek 按量', handoff: '摆渡行（handoff 供应商）' };
const PLAN_LINE = { coding_plan: (p) => `订阅套餐 · ${p.plan || 'Coding Plan'}`, paygo: () => '充值按量计费', handoff: () => 'OpenAI 协议 · 摆渡执行器专用' };

// ── 倒计时/时刻（演示态基准=契约生成时刻，接真数据后=真实时钟；见 data.now） ──
function countdownText(resetsAt) {
  let ms = new Date(resetsAt).getTime() - data.now();
  if (!(ms > 0)) ms = 0; // 防负值
  const tmin = Math.floor(ms / 60000);
  if (tmin < 60) return `${tmin}m`;
  const h = Math.floor(tmin / 60), mm = tmin % 60;
  if (h < 24) return mm ? `${h}h${String(mm).padStart(2, '0')}m` : `${h}h`;
  const d = Math.floor(h / 24), hh = h % 24;
  return hh ? `${d}d${hh}h` : `${d}d`;
}
/** 同一天 → HH:mm；跨天 → 周X HH:mm（按 +08:00 求星期）。 */
function absTimeText(resetsAt) {
  const hhmm = resetsAt.slice(11, 16);
  const nowDay = new Date(data.now() + 8 * 3600e3).toISOString().slice(0, 10);
  if (resetsAt.slice(0, 10) === nowDay) return hhmm;
  const wd = '日一二三四五六'[new Date(new Date(resetsAt).getTime() + 8 * 3600e3).getUTCDay()];
  return `周${wd} ${hhmm}`;
}
/** 告警联动：<10% 红、<20% 黄，否则所属环基色。 */
function resetColor(m) {
  if (m.remaining_pct < 10) return getVar('--alert-r');
  if (m.remaining_pct < 20) return getVar('--alert-y');
  return COLORS[m.key] || COLORS.window_5h;
}
function cdSpan(m) {
  if (!m || !m.resets_at) return '';
  const alert = m.remaining_pct < 20, c = resetColor(m);
  return `<span style="color:${c}${alert ? ';font-weight:600' : ''}"><span class="cdot" style="background:${c}"></span>${countdownText(m.resets_at)}</span>`;
}
function cdlineInner(p) {
  const m = (k) => p.metrics.find((x) => x.key === k);
  return `${cdSpan(m('window_5h'))}<span class="sep"> · </span>${cdSpan(m('week'))}`;
}
const cdlineHTML = (p) => `<div class="cap cdline${state.showCdline ? '' : ' hidden'}">${cdlineInner(p)}</div>`;

// ── 圆控件 ──
const metricOf = (p, k) => p.metrics.find((x) => x.key === k);
const estBadge = (m) => (m && m.source === 'estimated' ? '<i>估</i>' : '');
function ringSVG(m1, m2) { // m1=外环(5h) m2=中环(周)，均可空
  const C1 = 2 * Math.PI * 43, C2 = 2 * Math.PI * 33;
  const seg = (m, r, C) => m ? `<circle class="ring" cx="50" cy="50" r="${r}" style="stroke:${ringColor(m.key, m.remaining_pct)};stroke-dasharray:${(m.remaining_pct / 100 * C).toFixed(1)} ${C.toFixed(1)}" transform="rotate(-90 50 50)"></circle>` : '';
  return `<circle class="track" cx="50" cy="50" r="43"></circle>${seg(m1, 43, C1)}<circle class="track" cx="50" cy="50" r="33"></circle>${seg(m2, 33, C2)}`;
}
function discHTML(p) {
  const label = p.label || p.id;
  let svgInner = '', center = '', cap = '';
  if (p.error) { // 该上游整体查询失败：不画环不造数
    svgInner = '';
    center = `<text x="50" y="47" class="c-label">⚠</text><text x="50" y="60" class="c-sub">${label}</text>`;
    cap = `<div class="cap">查询失败 · ${p.error.category}</div>`;
  } else if (p.kind === 'coding_plan') {
    svgInner = ringSVG(metricOf(p, 'window_5h'), metricOf(p, 'week'));
    center = `<text x="50" y="47" class="c-label">${label}</text>
              <text x="50" y="60" class="c-sub">${p.plan || 'Coding Plan'}</text>`;
    const mt = metricOf(p, 'month_tokens');
    cap = cdlineHTML(p) + `<div class="cap">${mt ? mt.text : ''}${estBadge(mt)}</div>`;
  } else if (p.kind === 'paygo') {
    const bal = metricOf(p, 'balance_cny'), st = metricOf(p, 'spend_today_cny'), sw = metricOf(p, 'spend_week_cny');
    svgInner = `<circle class="track" cx="50" cy="50" r="43"></circle>`; // 预算环默认关（票 04 设置）
    const money = bal ? bal.text.replace(/^¥/, '') : '—'; // 圆心金额吃契约 text
    center = `<text x="50" y="38" class="c-money-sym">CNY</text>
              <text x="50" y="56" class="c-money">${money}</text>
              <text x="50" y="68" class="c-sub">${!bal || bal.available !== false ? '可用' : '不可用'}</text>`;
    cap = `<div class="cap">${st ? st.text : ''}${estBadge(st)} · ${sw ? sw.text : ''}${estBadge(sw)}</div>`;
  } else { // handoff
    const sm = metricOf(p, 'spend_month_cny'), sw = metricOf(p, 'spend_week_cny');
    svgInner = `<circle class="houtline" cx="50" cy="50" r="43"></circle>`;
    center = `<text x="50" y="47" class="c-label">${label}</text>
              <text x="50" y="60" class="c-sub">handoff</text>`;
    cap = `<div class="cap">${sm ? sm.text : ''}${estBadge(sm)} · ${sw ? sw.text : ''}${estBadge(sw)}</div>`;
  }
  return `<div class="disc${p.kind === 'handoff' ? ' handoff' : ''}" data-id="${p.id}" tabindex="0">
            <svg viewBox="0 0 100 100">${svgInner}${center}</svg>${cap}</div>`;
}

/** 全量重渲染（30s 轮询/灰化态切换后调用；显示配置即时项随 state 落盘到 markup）。 */
function render() {
  document.body.classList.toggle('conn-down', !state.reachable);
  for (const el of [...widget.querySelectorAll('.disc')]) el.remove();
  if (!state.summary) return;
  [...state.summary.upstreams, state.summary.handoff].forEach((p) =>
    widget.insertAdjacentHTML('beforeend', discHTML(p)));
  if (state.detailId) { // 重建后若详情卡开着，按 id 重挂
    const p = findUpstream(state.detailId);
    if (p) renderDetail(p); else closeDetail();
  }
}
const findUpstream = (id) => state.summary &&
  [...state.summary.upstreams, state.summary.handoff].find((x) => x.id === id);

// ── tooltip（hover 各环数字） ──
function labelOf(k) { return { window_5h: '5h', week: '周', month_budget: '月预算', ds_budget: '预算' }[k] || k; }
widget.addEventListener('mouseover', (e) => {
  const disc = e.target.closest('.disc'); if (!disc) return;
  const p = findUpstream(disc.dataset.id); if (!p) return;
  const rows = p.metrics.filter((m) => m.remaining_pct != null).map((m) =>
    `<div><span class="t-sw" style="background:${ringColor(m.key, m.remaining_pct)}"></span>${labelOf(m.key)} 剩 ${m.remaining_pct}%${m.resets_at ? ` · 重置 ${countdownText(m.resets_at)}` : ''}</div>`
  ).join('');
  tip.innerHTML = rows || "<div style='color:var(--txt-dim)'>无环指标 · 单击看详情</div>";
  tip.classList.remove('hidden');
});
widget.addEventListener('mousemove', (e) => {
  tip.style.left = Math.max(2, Math.min(e.clientX - tip.offsetWidth - 14, window.innerWidth - tip.offsetWidth - 4)) + 'px';
  tip.style.top = (e.clientY + 14) + 'px';
});
widget.addEventListener('mouseout', (e) => {
  const disc = e.target.closest('.disc'); if (!disc) return;
  if (e.relatedTarget && disc.contains(e.relatedTarget)) return;
  tip.classList.add('hidden');
});

// ── 详情卡（单击展开；小窗内覆盖式弹出） ──
function renderDetail(p) {
  const rows = p.metrics.map((m) => {
    if (m.remaining_pct != null) {
      const reset = m.resets_at
        ? `<span class="d-reset" style="color:${resetColor(m)}${m.remaining_pct < 20 ? ';font-weight:600' : ''}">重置 ${absTimeText(m.resets_at)}（${countdownText(m.resets_at)} 后）</span>`
        : '<span class="d-reset"></span>';
      return `<div class="drow"><span class="d-sw" style="background:${ringColor(m.key, m.remaining_pct)}"></span>
        <span class="d-key">${labelOf(m.key)}剩</span>
        <span class="d-val">${m.remaining_pct}%<span class="b ${m.source === 'fetched' ? 'fetch' : 'est'}">${m.source === 'fetched' ? '查询' : '估'}</span></span>
        ${m.abs ? `<span class="d-abs">${m.abs}</span>` : ''}
        ${reset}</div>`;
    }
    let extra = '';
    if (m.breakdown) extra = `<span class="d-abs">赠送 ${m.breakdown.granted} + 充值 ${m.breakdown.topped_up}</span>`;
    return `<div class="drow"><span class="d-sw" style="background:${m.source === 'fetched' ? '#7ECB9B' : 'var(--cmonth)'};opacity:.75"></span>
      <span class="d-key">${m.key.includes('today') ? '今日' : m.key.includes('week') ? '本周' : m.key.includes('month') ? '本月' : '值'}</span>
      <span class="d-val">${(m.text || '').replace(/^(今|周|月) /, '')}<span class="b ${m.source === 'fetched' ? 'fetch' : 'est'}">${m.source === 'fetched' ? '查询' : '估'}</span></span>
      ${extra}</div>`;
  }).join('');
  detail.innerHTML = `<h3>${DETAIL_NAME_OF[p.id] || p.label || p.id}</h3>
    <div class="plan">${(PLAN_LINE[p.kind] || (() => ''))(p)}</div>
    ${rows}
    <div class="prov">${p.metrics.map((m) => provenanceOf(p, m)).filter(Boolean).map((s) => `• ${s}`).join('<br>')}</div>
    <div class="dfoot">最后更新 ${p.metrics[0]?.as_of ?? '—'}（远端 5–15 分钟缓存） · 每 30s 轮询 /widget/summary</div>`;
  detail.classList.remove('hidden');
}
function closeDetail() { detail.classList.add('hidden'); state.detailId = null; }
widget.addEventListener('click', (e) => {
  const disc = e.target.closest('.disc'); if (!disc) return;
  state.detailId = disc.dataset.id;
  const p = findUpstream(state.detailId);
  if (p) renderDetail(p);
});
document.addEventListener('click', (e) => {
  if (!e.target.closest('.disc') && !e.target.closest('#detail')) closeDetail();
});

// ── 设置浮层（布局/倒计时开关/图例帮助；完整设置窗=票 04） ──
document.getElementById('btnSettings').addEventListener('click', () => settings.classList.remove('hidden'));
document.querySelectorAll('[data-close]').forEach((b) =>
  b.addEventListener('click', () => document.getElementById(b.dataset.close).classList.add('hidden')));
settings.addEventListener('click', (e) => { if (e.target === settings) settings.classList.add('hidden'); });

/** 横竖切换（窗体几何随动属票 04/09；此处先落 UI 类切换，语义与 mock 同）。 */
function setLayout(mode) {
  widget.classList.remove('vertical', 'horizontal');
  widget.classList.add(mode);
  document.querySelectorAll('input[name=layout]').forEach((r) => { r.checked = r.value === mode; });
}
document.querySelectorAll('input[name=layout]').forEach((r) =>
  r.addEventListener('change', () => setLayout(r.value)));

// 倒计时行显隐（widget 本地显示配置，daemon 不感知）
document.getElementById('optCdline').addEventListener('change', (e) => {
  state.showCdline = e.target.checked;
  document.querySelectorAll('.cdline').forEach((el) => el.classList.toggle('hidden', !state.showCdline));
});

// ── 收起/恢复（壳内=托盘菜单与关窗，票 02；浏览器/测试语境=dblclick 手柄的 UI 演示） ──
grip.addEventListener('dblclick', () => {
  if (inShell()) return; // 壳内真收起走原生托盘，不留只剩恢复钮的空窗口
  document.body.classList.add('tray-collapsed');
  closeDetail(); tip.classList.add('hidden');
});
restoreBtn.addEventListener('click', () => document.body.classList.remove('tray-collapsed'));

// ── 拖动（壳内=data-tauri-drag-region 原生拖动+位置记忆，票 02；浏览器语境=JS 演示） ──
(function jsDrag() {
  if (inShell()) return;
  let drag = null;
  widget.addEventListener('mousedown', (e) => {
    if (!e.target.closest('.grip')) return;
    const r = widget.getBoundingClientRect();
    drag = { dx: e.clientX - r.left, dy: e.clientY - r.top };
    e.preventDefault();
  });
  document.addEventListener('mousemove', (e) => {
    if (!drag) return;
    widget.style.left = `${e.clientX - drag.dx}px`;
    widget.style.top = `${e.clientY - drag.dy}px`;
    widget.style.right = 'auto';
  });
  document.addEventListener('mouseup', () => { drag = null; });
})();

// ── 倒计时逐分钟本地 ticker（零新增查询；详情卡同步刷新） ──
function refreshCountdowns() {
  if (!state.summary) return;
  for (const disc of document.querySelectorAll('.disc[data-id]')) {
    const p = findUpstream(disc.dataset.id);
    if (!p || p.kind !== 'coding_plan') continue;
    const line = disc.querySelector('.cdline');
    if (line) line.innerHTML = cdlineInner(p);
  }
  if (state.detailId && !detail.classList.contains('hidden')) {
    const p = findUpstream(state.detailId);
    if (p) renderDetail(p);
  }
}
setInterval(refreshCountdowns, 60000);

// ── 六项交互自测（?selftest=1 才跑；结果落 #selftest-results data-*，供静态断言） ──
function runSelftest() {
  const res = [];
  const set = (k, v) => res.push([k, v]);
  try {
    // 1 横竖切换
    const h = document.querySelector('input[name=layout][value=horizontal]');
    h.checked = true; h.dispatchEvent(new Event('change', { bubbles: true }));
    const wasH = widget.classList.contains('horizontal');
    const v = document.querySelector('input[name=layout][value=vertical]');
    v.checked = true; v.dispatchEvent(new Event('change', { bubbles: true }));
    set('layout', wasH && widget.classList.contains('vertical'));

    // 2 收起/恢复
    grip.dispatchEvent(new MouseEvent('dblclick', { bubbles: true, cancelable: true }));
    const wasDown = document.body.classList.contains('tray-collapsed')
      && getComputedStyle(restoreBtn).display !== 'none';
    restoreBtn.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    set('collapse', wasDown && !document.body.classList.contains('tray-collapsed'));

    // 3 拖动手柄位移
    grip.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, clientX: 60, clientY: 30 }));
    document.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, clientX: 120, clientY: 80 }));
    document.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
    set('drag', widget.style.left === '60px' && widget.style.top === '50px');

    // 4 tooltip hover 出数字
    const disc = widget.querySelector('.disc');
    disc.querySelector('circle').dispatchEvent(new MouseEvent('mouseover', { bubbles: true }));
    set('tooltip', !tip.classList.contains('hidden') && tip.textContent.includes('62%'));
    tip.classList.add('hidden');

    // 5 详情卡（放最后，保持展开态供 dump 断言）
    disc.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    set('detail', !detail.classList.contains('hidden') && detail.textContent.includes('重置'));

    // 6 控制台零报错
    set('console', consoleErrors.length === 0);
  } catch (err) {
    console.error('selftest 异常', err);
    set('console', false);
  }
  const el = document.getElementById('selftest-results');
  for (const [k, val] of res) el.dataset[k] = val ? '1' : '0';
  el.dataset.done = '1';
}

// ── 启动 ──
document.body.classList.toggle('dev', data.isDev()); // dev 构建角标（release 不带）
data.startPolling((summary, reachable) => {
  state.summary = summary; state.reachable = reachable;
  render();
});
if (new URLSearchParams(location.search).has('selftest')) runSelftest();
