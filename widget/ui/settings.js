/**
 * 票 04 · 设置窗逻辑（settings.html；窗口按需创建、关闭即销毁——销毁在 Rust 侧）。
 *
 * 职责：对象目录（=data.js 演示契约的 id/label/kind；live 目录接线属票 06–08）
 * × 显示配置 profile（归一化/校验纯函数在 profile.js）→ 0.2.4 起分组卡片（每对象一张卡）。
 * 改动即收集即持久化：壳内 invoke save_profile 落盘 + emit('profile-changed') 广播，
 * 主窗订阅后即时重渲染。无 Tauri 壳（headless 断言/浏览器演示）：照常渲染与自测，
 * 仅不落盘并在状态行如实提示。
 *
 * reset_reason 处理：get_profile 返回 missing/corrupted 时回落默认（profile.js）并在
 * #resetNotice 如实提示——损坏/缺失不崩、保存后覆盖为合法配置。
 */
import * as data from './data.js';
import * as profileLib from './profile.js';

// ── 控制台错误捕获（自测项：控制台零报错） ──
const consoleErrors = [];
window.addEventListener('error', (e) => consoleErrors.push(String(e.message || e)));
const origError = console.error.bind(console);
console.error = (...a) => { consoleErrors.push(a.map(String).join(' ')); origError(...a); };

const getVar = (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
/** 主题基色（与 style.css :root 同源；未覆写时色块/选择器的初值）。 */
const PALETTE = { window_5h: getVar('--c5h'), week: getVar('--cweek'),
                  month_budget: getVar('--cmonth'), ds_budget: getVar('--cds') };
const COLOR_TITLE = { window_5h: '5h 环基色', week: '周环基色',
                      month_budget: '月预算环基色', ds_budget: 'DS 预算环基色' };
/** 色选器带名（0.2.4 卡片式：每字段就地一句人话）。 */
const COLOR_NAME = { window_5h: '5h', week: '周', month_budget: '月预算', ds_budget: '预算环' };

/** 对象目录（id 稳定=渡口上游表条目名；演示契约副本即目录，live 化在票 06–08）。 */
const CATALOG = [...data.DEMO_SUMMARY.upstreams, data.DEMO_SUMMARY.handoff]
  .map((p) => ({ id: p.id, label: p.label, kind: p.kind, plan: p.plan || '' }));

const cardsEl = document.getElementById('objCards');
const statusEl = document.getElementById('status');
const noticeEl = document.getElementById('resetNotice');
const optCdline = document.getElementById('optCdline');

/** @type {profileLib.Profile} 归一化+材料化后的当前配置（目录内每个 id 都有 entry+order）。 */
let currentProfile = profileLib.normalizeProfile(null).profile;

const status = (t) => { statusEl.textContent = t; };

const orderOf = (id) => {
  const o = currentProfile.objects[id];
  return o && typeof o.order === 'number' ? o.order : CATALOG.findIndex((c) => c.id === id);
};

/** 材料化：目录每个 id 补全 entry（visible/order/thresholds…），渲染与收集不再处理缺项。 */
function materialize() {
  const objects = {};
  CATALOG.forEach((c, i) => {
    const raw = currentProfile.objects[c.id] || {};
    const e = profileLib.effectiveObject(currentProfile, c.id);
    const entry = /** @type {Record<string, *>} */ ({
      visible: e.visible,
      order: typeof raw.order === 'number' && isFinite(raw.order) ? raw.order : i,
    });
    if (c.kind !== 'handoff') entry.thresholds = { ...e.thresholds };
    if (raw.ring_colors && typeof raw.ring_colors === 'object') entry.ring_colors = { ...raw.ring_colors };
    if (typeof raw.month_budget === 'number' && isFinite(raw.month_budget) && raw.month_budget > 0) {
      entry.month_budget = raw.month_budget;
    }
    if (raw.ds_budget && typeof raw.ds_budget === 'object') entry.ds_budget = { ...raw.ds_budget };
    objects[c.id] = entry;
  });
  currentProfile.objects = objects;
}

/** 生成一张卡（对象目录条目 c × 材料化 entry × 原始存量 raw）。
 *  0.2.4 卡片式：卡头=label（plan）+显隐+↑↓排序；卡体每字段一行、就地一句人话 hint。
 *  控件类名与类型不变（f-visible/f-color/f-th/f-mb/f-dsb-on/f-dsb-amt/f-up/f-down），
 *  collect() 只换遍历容器（tr→卡），产出的 profile 结构与净化规则与表格时代一致。 */
function cardHTML(c, e, raw) {
  const isPlan = c.kind === 'coding_plan', isPaygo = c.kind === 'paygo';
  const colorOf = (k) => (raw.ring_colors && typeof raw.ring_colors[k] === 'string' && raw.ring_colors[k]) || PALETTE[k];
  const colors = isPlan
    ? ['window_5h', 'week', 'month_budget'].map((k) =>
        `<label class="c-field">${COLOR_NAME[k]} <input type="color" class="f-color" data-key="${k}" value="${colorOf(k)}" title="${COLOR_TITLE[k]}"></label>`).join('')
      + '<span class="hint">点色块换基色；剩余低于阈值时自动变黄/红</span>'
    : isPaygo
      ? `<label class="c-field">预算环 <input type="color" class="f-color" data-key="ds_budget" value="${colorOf('ds_budget')}" title="${COLOR_TITLE.ds_budget}"></label> <span class="hint">DeepSeek 预算环基色</span>`
      : '<span class="hint">无环，仅显隐与顺序</span>';
  const rows = [`<div class="obj-row"><span class="k">环色</span>${colors}</div>`];
  if (isPlan || isPaygo) {
    rows.push(`<div class="obj-row"><span class="k">告警阈值</span>黄 <input type="number" class="f-th" data-th="yellow" min="0" max="100" value="${e.thresholds.yellow}"> ／ 红 <input type="number" class="f-th" data-th="red" min="0" max="100" value="${e.thresholds.red}"> % <span class="hint">剩余低于阈值，环与紧凑档圆心数字变黄/红</span></div>`);
  }
  if (isPlan) {
    rows.push(`<div class="obj-row"><span class="k">月预算</span><input type="number" class="f-mb" min="1" value="${typeof raw.month_budget === 'number' ? raw.month_budget : ''}" placeholder="未设"> <span class="hint">tok/月 · 不设=文字计数；设了=紫环（剩余制：1−已用/预算）</span></div>`);
  }
  if (isPaygo) {
    rows.push(`<div class="obj-row"><span class="k">月预算</span><label><input type="checkbox" class="f-dsb-on"${raw.ds_budget && raw.ds_budget.enabled ? ' checked' : ''}> 开</label> ¥<input type="number" class="f-dsb-amt" min="1" value="${raw.ds_budget && typeof raw.ds_budget.amount_cny === 'number' ? raw.ds_budget.amount_cny : ''}" placeholder="如 300"> <span class="hint">/月 · 已用=本月花费，剩余制绿环</span></div>`);
  }
  return `<div class="obj-card" data-id="${c.id}">
    <div class="obj-head">
      <input type="checkbox" class="f-visible"${e.visible ? ' checked' : ''} title="显示这个盘">
      <span class="obj-name">${c.label}${c.plan ? `（${c.plan}）` : ''}</span>
      <span class="obj-order"><button class="f-up" title="上移">↑</button><button class="f-down" title="下移">↓</button></span>
    </div>
    ${rows.join('')}
  </div>`;
}

function renderCards() {
  cardsEl.innerHTML = [...CATALOG].sort((a, b) => orderOf(a.id) - orderOf(b.id))
    .map((c) => cardHTML(c, profileLib.effectiveObject(currentProfile, c.id), currentProfile.objects[c.id] || {}))
    .join('');
}

/** 卡片控件 → profile（净化在 profile.js 语义内：阈值钳位/红≤黄、预算须正数）。
 *  0.2.4：遍历容器 tr→.obj-card，产出结构与净化规则与表格时代逐字段一致。 */
function collect() {
  const clamp = (v, d) => {
    const n = parseFloat(v);
    return isFinite(n) ? Math.min(100, Math.max(0, n)) : d;
  };
  const objects = {};
  for (const card of [...cardsEl.querySelectorAll('.obj-card[data-id]')]) {
    const id = card.dataset.id;
    const c = CATALOG.find((x) => x.id === id);
    const entry = /** @type {Record<string, *>} */ ({
      visible: card.querySelector('.f-visible').checked,
      order: orderOf(id),
    });
    if (c.kind !== 'handoff') {
      const yellow = clamp(card.querySelector('[data-th=yellow]').value, 20);
      entry.thresholds = { yellow, red: Math.min(clamp(card.querySelector('[data-th=red]').value, 10), yellow) };
    }
    const rc = {};
    [...card.querySelectorAll('.f-color')].forEach((i) => { rc[i.dataset.key] = i.value; });
    if (Object.keys(rc).length) entry.ring_colors = rc;
    if (c.kind === 'coding_plan') {
      const mb = parseFloat(card.querySelector('.f-mb').value);
      if (isFinite(mb) && mb > 0) entry.month_budget = mb;
    }
    if (c.kind === 'paygo') {
      const amt = parseFloat(card.querySelector('.f-dsb-amt').value);
      entry.ds_budget = {
        enabled: card.querySelector('.f-dsb-on').checked,
        amount_cny: isFinite(amt) && amt > 0 ? amt : 0,
      };
    }
    objects[id] = entry;
  }
  return {
    layout: (document.querySelector('input[name=layout]:checked') || { value: 'vertical' }).value,
    appearance: (document.querySelector('input[name=appearance]:checked') || { value: 'full' }).value,
    show_countdown: optCdline.checked,
    objects,
  };
}

/** 持久化 + 广播（壳内）；演示语境如实提示不落盘。 */
function persist() {
  currentProfile = collect();
  const t = window.__TAURI__;
  if (t && t.core && t.core.invoke && t.event && t.event.emit) {
    t.event.emit('profile-changed', currentProfile);
    t.core.invoke('save_profile', { profile: currentProfile })
      .then(() => status(`已保存 · 即时生效（${new Date().toLocaleTimeString()}）`))
      .catch((e) => { status('保存失败'); console.error('save_profile 失败', e); });
  } else {
    status('演示语境（无 Tauri 壳）：改动仅本页生效、不落盘');
  }
}

/** 上移/下移：相邻两项交换 order 值，重排卡片并持久化。 */
function moveCard(id, dir) {
  const seq = CATALOG.map((c) => ({ id: c.id, order: orderOf(c.id) }))
    .sort((a, b) => a.order - b.order);
  const i = seq.findIndex((x) => x.id === id), j = i + dir;
  if (i < 0 || j < 0 || j >= seq.length) return;
  const tmp = seq[i].order; seq[i].order = seq[j].order; seq[j].order = tmp;
  for (const x of [seq[i], seq[j]]) currentProfile.objects[x.id].order = x.order;
  renderCards();
  persist();
}

function syncHeader() {
  document.querySelectorAll('input[name=layout]').forEach((r) => { r.checked = r.value === currentProfile.layout; });
  document.querySelectorAll('input[name=appearance]').forEach((r) => { r.checked = r.value === currentProfile.appearance; });
  optCdline.checked = currentProfile.show_countdown;
}

/** reset_reason/修复提示（损坏/缺失→如实提示，不崩）。 */
function showNotice(resetReason, repaired) {
  const msgs = [];
  if (resetReason === 'corrupted') msgs.push('⚠ 配置文件损坏，已回落默认显示配置；下次保存将覆盖损坏文件。');
  else if (resetReason === 'missing') msgs.push('尚无配置文件（首次运行或被清理），当前为默认显示配置。');
  if (repaired) msgs.push('部分配置项无效，已按安全默认值修复。');
  if (msgs.length) {
    noticeEl.textContent = msgs.join(' ');
    noticeEl.classList.remove('hidden');
    console.warn('profile reset:', resetReason, 'repaired:', repaired);
  }
}

// ── 事件绑定（委托：cardsEl innerHTML 重建不丢监听） ──
cardsEl.addEventListener('change', persist);
cardsEl.addEventListener('click', (e) => {
  const card = e.target.closest('.obj-card[data-id]');
  if (!card) return;
  if (e.target.classList.contains('f-up')) moveCard(card.dataset.id, -1);
  else if (e.target.classList.contains('f-down')) moveCard(card.dataset.id, +1);
});
document.querySelectorAll('input[name=layout]').forEach((r) =>
  r.addEventListener('change', persist));
document.querySelectorAll('input[name=appearance]').forEach((r) =>
  r.addEventListener('change', persist));
optCdline.addEventListener('change', persist);

// ── Ferryman 设置窗入口（守护侧配置）：壳内 invoke 开常驻窗；壳外新开页面 ──
(function wireFerrymanSettingsEntry() {
  const btn = document.getElementById('btnFerrymanSettings');
  if (!btn) return;
  btn.addEventListener('click', () => {
    const t = window.__TAURI__;
    if (t && t.core && t.core.invoke) {
      t.core.invoke('open_ferryman_settings_window')
        .catch((e) => { status('打开 Ferryman 设置失败'); console.error('open_ferryman_settings_window 失败', e); });
    } else if (typeof window.open === 'function') {
      window.open('ferryman-settings.html', '_blank');
    }
  });
})();

// ── 自测（?selftest=1；结果落 #selftest-results data-*，供静态断言） ──
function runSelftest() {
  const res = [];
  const set = (k, v) => res.push([k, v]);
  try {
    set('rows', cardsEl.querySelectorAll('.obj-card[data-id]').length === 4);
    const glmCard = cardsEl.querySelector('.obj-card[data-id=glm]');
    set('defaults',
      document.querySelector('input[name=layout][value=vertical]').checked === true &&
      optCdline.checked === true &&
      glmCard.querySelector('[data-th=yellow]').value === '20' &&
      glmCard.querySelector('[data-th=red]').value === '10' &&
      !cardsEl.querySelector('.f-dsb-on').checked);
    set('console', consoleErrors.length === 0);
  } catch (err) {
    console.error('settings selftest 异常', err);
    set('console', false);
  }
  const el = document.getElementById('selftest-results');
  for (const [k, val] of res) el.dataset[k] = val ? '1' : '0';
  el.dataset.done = '1';
}

// ── 启动：壳内读盘（损坏/缺失→默认+提示）；演示/断言语境=URL 预设或默认 ──
(async function boot() {
  let raw = null, resetReason = null;
  const t = window.__TAURI__;
  if (t && t.core && t.core.invoke) {
    try {
      const r = await t.core.invoke('get_profile');
      raw = r && r.profile;
      resetReason = (r && r.reset_reason) || null;
    } catch (e) { console.error('get_profile 失败', e); }
  } else {
    const pv = new URLSearchParams(location.search).get('profile');
    if (pv && profileLib.PRESETS[pv]) raw = profileLib.PRESETS[pv];
  }
  const n = profileLib.normalizeProfile(raw);
  currentProfile = n.profile;
  materialize();
  syncHeader();
  renderCards();
  showNotice(resetReason, n.repaired);
  if (new URLSearchParams(location.search).has('selftest')) runSelftest();
})();
