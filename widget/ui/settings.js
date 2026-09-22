/**
 * 票 04 · 设置窗逻辑（settings.html；窗口按需创建、关闭即销毁——销毁在 Rust 侧）。
 *
 * 职责：对象目录（=data.js 演示契约的 id/label/kind；live 目录接线属票 06–08）
 * × 显示配置 profile（归一化/校验纯函数在 profile.js）→ 配置表格。
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

/** 对象目录（id 稳定=渡口上游表条目名；演示契约副本即目录，live 化在票 06–08）。 */
const CATALOG = [...data.DEMO_SUMMARY.upstreams, data.DEMO_SUMMARY.handoff]
  .map((p) => ({ id: p.id, label: p.label, kind: p.kind, plan: p.plan || '' }));

const tbody = document.getElementById('objRows');
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

/** 生成一行（对象目录条目 c × 材料化 entry × 原始存量 raw）。 */
function rowHTML(c, e, raw) {
  const isPlan = c.kind === 'coding_plan', isPaygo = c.kind === 'paygo';
  const colorOf = (k) => (raw.ring_colors && typeof raw.ring_colors[k] === 'string' && raw.ring_colors[k]) || PALETTE[k];
  const colors = isPlan
    ? ['window_5h', 'week', 'month_budget'].map((k) =>
        `<input type="color" class="f-color" data-key="${k}" value="${colorOf(k)}" title="${COLOR_TITLE[k]}">`).join('')
    : isPaygo
      ? `<input type="color" class="f-color" data-key="ds_budget" value="${colorOf('ds_budget')}" title="${COLOR_TITLE.ds_budget}"> <span class="hint">预算环</span>`
      : '<span class="hint">无环</span>';
  const th = (isPlan || isPaygo)
    ? `黄 <input type="number" class="f-th" data-th="yellow" min="0" max="100" value="${e.thresholds.yellow}"> ／ 红 <input type="number" class="f-th" data-th="red" min="0" max="100" value="${e.thresholds.red}"> %`
    : '<span class="hint">—</span>';
  const budget = isPlan
    ? `<input type="number" class="f-mb" min="1" value="${typeof raw.month_budget === 'number' ? raw.month_budget : ''}" placeholder="未设"> <span class="hint">tok/月 · 不设=文字计数</span>`
    : isPaygo
      ? `<label><input type="checkbox" class="f-dsb-on"${raw.ds_budget && raw.ds_budget.enabled ? ' checked' : ''}> 开</label> ¥<input type="number" class="f-dsb-amt" min="1" value="${raw.ds_budget && typeof raw.ds_budget.amount_cny === 'number' ? raw.ds_budget.amount_cny : ''}" placeholder="如 300"> <span class="hint">/月</span>`
      : '<span class="hint">—</span>';
  return `<tr data-id="${c.id}">
    <td><input type="checkbox" class="f-visible"${e.visible ? ' checked' : ''}></td>
    <td>${c.label}${c.plan ? `（${c.plan}）` : ''}</td>
    <td>${colors}</td>
    <td>${th}</td>
    <td>${budget}</td>
    <td><button class="f-up" title="上移">↑</button><button class="f-down" title="下移">↓</button></td>
  </tr>`;
}

function renderTable() {
  tbody.innerHTML = [...CATALOG].sort((a, b) => orderOf(a.id) - orderOf(b.id))
    .map((c) => rowHTML(c, profileLib.effectiveObject(currentProfile, c.id), currentProfile.objects[c.id] || {}))
    .join('');
}

/** 表格控件 → profile（净化在 profile.js 语义内：阈值钳位/红≤黄、预算须正数）。 */
function collect() {
  const clamp = (v, d) => {
    const n = parseFloat(v);
    return isFinite(n) ? Math.min(100, Math.max(0, n)) : d;
  };
  const objects = {};
  for (const tr of [...tbody.querySelectorAll('tr[data-id]')]) {
    const id = tr.dataset.id;
    const c = CATALOG.find((x) => x.id === id);
    const entry = /** @type {Record<string, *>} */ ({
      visible: tr.querySelector('.f-visible').checked,
      order: orderOf(id),
    });
    if (c.kind !== 'handoff') {
      const yellow = clamp(tr.querySelector('[data-th=yellow]').value, 20);
      entry.thresholds = { yellow, red: Math.min(clamp(tr.querySelector('[data-th=red]').value, 10), yellow) };
    }
    const rc = {};
    [...tr.querySelectorAll('.f-color')].forEach((i) => { rc[i.dataset.key] = i.value; });
    if (Object.keys(rc).length) entry.ring_colors = rc;
    if (c.kind === 'coding_plan') {
      const mb = parseFloat(tr.querySelector('.f-mb').value);
      if (isFinite(mb) && mb > 0) entry.month_budget = mb;
    }
    if (c.kind === 'paygo') {
      const amt = parseFloat(tr.querySelector('.f-dsb-amt').value);
      entry.ds_budget = {
        enabled: tr.querySelector('.f-dsb-on').checked,
        amount_cny: isFinite(amt) && amt > 0 ? amt : 0,
      };
    }
    objects[id] = entry;
  }
  return {
    layout: (document.querySelector('input[name=layout]:checked') || { value: 'vertical' }).value,
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

/** 上移/下移：相邻两项交换 order 值，重排表格并持久化。 */
function moveRow(id, dir) {
  const seq = CATALOG.map((c) => ({ id: c.id, order: orderOf(c.id) }))
    .sort((a, b) => a.order - b.order);
  const i = seq.findIndex((x) => x.id === id), j = i + dir;
  if (i < 0 || j < 0 || j >= seq.length) return;
  const tmp = seq[i].order; seq[i].order = seq[j].order; seq[j].order = tmp;
  for (const x of [seq[i], seq[j]]) currentProfile.objects[x.id].order = x.order;
  renderTable();
  persist();
}

function syncHeader() {
  document.querySelectorAll('input[name=layout]').forEach((r) => { r.checked = r.value === currentProfile.layout; });
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

// ── 事件绑定（委托：tbody innerHTML 重建不丢监听） ──
tbody.addEventListener('change', persist);
tbody.addEventListener('click', (e) => {
  const tr = e.target.closest('tr[data-id]');
  if (!tr) return;
  if (e.target.classList.contains('f-up')) moveRow(tr.dataset.id, -1);
  else if (e.target.classList.contains('f-down')) moveRow(tr.dataset.id, +1);
});
document.querySelectorAll('input[name=layout]').forEach((r) =>
  r.addEventListener('change', persist));
optCdline.addEventListener('change', persist);

// ── 自测（?selftest=1；结果落 #selftest-results data-*，供静态断言） ──
function runSelftest() {
  const res = [];
  const set = (k, v) => res.push([k, v]);
  try {
    set('rows', tbody.querySelectorAll('tr[data-id]').length === 4);
    const glmRow = tbody.querySelector('tr[data-id=glm]');
    set('defaults',
      document.querySelector('input[name=layout][value=vertical]').checked === true &&
      optCdline.checked === true &&
      glmRow.querySelector('[data-th=yellow]').value === '20' &&
      glmRow.querySelector('[data-th=red]').value === '10' &&
      !tbody.querySelector('.f-dsb-on').checked);
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
  renderTable();
  showNotice(resetReason, n.repaired);
  if (new URLSearchParams(location.search).has('selftest')) runSelftest();
})();
