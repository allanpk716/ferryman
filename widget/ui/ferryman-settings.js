/**
 * Ferryman 设置窗逻辑（ferryman-settings.html）。
 *
 * 布局/文案以定稿 mock（widget/ui/mock/settings.html）为准；数据层接 daemon
 * 设置端点族（127.0.0.1:15700，Bearer 鉴权）：
 *   GET  /settings                         全量读面（config 各节 + providers + prices + effects）
 *   PUT  /settings/{section}               节级整写（body=该节完整对象）
 *   PUT|DELETE /settings/dock/upstreams/{名} / providers/{名} / prices/{键}
 *   POST /settings/dock/switch             换绑活跃上游（即时生效）
 *   GET|POST /settings/snapshots[/{id}/restore]  快照
 *
 * 鉴权与取数机制复用 widget 主窗 data.js 同款：壳内经 invoke get_daemon_config
 * 现读 ~/ferryman/daemon.token；浏览器/断言语境走 window.__WIDGET_DAEMON_URL__ /
 * __WIDGET_TOKEN__ 注入；都没有则裸试默认地址，失败如实出横幅（不白屏）。
 *
 * 密钥铁律：读面密钥一律 {masked:"••••尾四", has_key} 对象形；输入框 placeholder
 * 「留空则不修改」，用户没改的密钥字段不进提交体（省略=服务端保留现值）。
 * 零构建 vanilla：本文件为普通脚本（非 ES 模块），file:// 双击直接可跑。
 */
'use strict';

const $ = (s) => document.querySelector(s);
const $$ = (s) => [...document.querySelectorAll(s)];

let BASE = '';      // daemon 基址（http://127.0.0.1:15700）
let TOKEN = '';     // Bearer token（壳外注入或 get_daemon_config）
let S = null;       // GET /settings 全量响应（唯一事实源）

// ── 小工具 ──
function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
}
const MODE_TXT = { off: '关', observe: '只提醒', enforce: '拦截' };
const toMin = (s) => Math.round(((Number(s) || 0) / 60) * 100) / 100;   // 秒→分钟（2100 → 35）
const toSec = (m) => Math.round((Number(m) || 0) * 60);
const toWan = (n) => Math.round(((Number(n) || 0) / 10000) * 100) / 100; // token→万（20000 → 2）
const fromWan = (w) => Math.round((Number(w) || 0) * 10000);
const cfgSec = (name) => (S && S.config && S.config[name]) || {};

/** 密钥显示口径：对象形（服务端掩）→ masked/未设置；字符串（非密钥位如 pushover_user）→ 客户端掩尾四位。 */
function keyLabel(v) {
  if (v && typeof v === 'object') return v.has_key ? (v.masked || '••••') : '未设置';
  if (typeof v === 'string' && v) return '••••' + [...v].slice(-4).join('');
  return '未设置';
}
/** data-f 输入框写入值并记对照基线。 */
function setF(elm, v) { elm.value = String(v); elm.dataset.old = String(v); }
function setC(elm, on) { elm.checked = !!on; elm.dataset.old = on ? '开' : '关'; }

// ── toast（带生效徽章；err 形为红字） ──
let toastTimer = null;
function toast(msg, badge /* 'live' | 'restart' | null */, isErr) {
  const t = $('#toast');
  t.innerHTML = '';
  const span = el('span', isErr ? 't-err' : '', msg);
  t.appendChild(span);
  if (badge === 'live') t.appendChild(el('span', 'badge green', '已生效'));
  if (badge === 'restart') t.appendChild(el('span', 'badge yellow', '已保存，重启守护后生效'));
  t.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), 3600);
}
/** 保存条内的就地反馈。 */
function feedback(fbEl, msg, isErr) {
  if (!fbEl) return;
  fbEl.innerHTML = '';
  fbEl.appendChild(el('span', isErr ? 'err' : '', msg));
}
/** 待重启标记（任何 needs_restart 的成功写都点亮）。 */
function markRestart() { $('#restartFlag').classList.add('show'); }

// ── 高风险二次确认弹窗 ──
let pendingOk = null;
function confirmRisk(title, body, okLabel, onOk) {
  $('#mTitle').textContent = title;
  $('#mBody').textContent = body;
  $('#mOk').textContent = okLabel || '确定';
  pendingOk = onOk;
  $('#mask').classList.add('show');
}
/** 关掉确认弹窗并清掉挂起的回调（取消/遮罩/进保护态共用）。 */
function closeConfirm() { $('#mask').classList.remove('show'); pendingOk = null; }
$('#mCancel').addEventListener('click', closeConfirm);
$('#mOk').addEventListener('click', () => {
  const f = pendingOk;
  closeConfirm();
  if (f) f();
});
$('#mask').addEventListener('click', (e) => { if (e.target.id === 'mask') closeConfirm(); });

// ── 导航切换 ──
$$('#nav .nav-it').forEach((b) => b.addEventListener('click', () => {
  $$('#nav .nav-it').forEach((x) => x.classList.toggle('on', x === b));
  $$('.sec').forEach((s) => s.classList.toggle('on', s.id === 'sec-' + b.dataset.sec));
  $('.main').scrollTop = 0;
}));

// ── daemon 连接 ──
async function resolveTarget() {
  const t = window.__TAURI__;
  if (t && t.core && t.core.invoke) {
    try {
      const cfg = await t.core.invoke('get_daemon_config');
      if (cfg && typeof cfg.url === 'string' && cfg.url) {
        BASE = new URL(cfg.url).origin;
        TOKEN = typeof cfg.token === 'string' ? cfg.token : '';
        return true;
      }
    } catch { /* 读 token 失败：如实报 */ }
    return false;
  }
  const url = String(window.__WIDGET_DAEMON_URL__ || '');
  TOKEN = String(window.__WIDGET_TOKEN__ || '');
  if (url) {
    try { BASE = new URL(url).origin; } catch { BASE = url; }
    return true;
  }
  BASE = 'http://127.0.0.1:15700'; // 裸试默认口；401/不可达在横幅如实报
  return true;
}

async function api(path, opts) {
  opts = opts || {};
  const headers = {};
  if (TOKEN) headers.Authorization = 'Bearer ' + TOKEN;
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
  const r = await fetch(BASE + path, {
    method: opts.method || 'GET',
    headers,
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    signal: AbortSignal.timeout(8000),
  });
  let data = null;
  try { data = await r.json(); } catch { /* 非 JSON 回话 */ }
  if (!r.ok) throw new Error((data && data.error) || ('HTTP ' + r.status));
  return data;
}

/** 节级 PUT 公共出口：成功→标记待重启；失败→就地反馈并上抛。 */
async function saveSection(section, body, fbEl) {
  try {
    const r = await api('/settings/' + section, { method: 'PUT', body });
    if (r && r.needs_restart) markRestart();
    return r;
  } catch (e) {
    feedback(fbEl, '保存失败：' + e.message, true);
    throw e;
  }
}

// ── 三态分段控件 ──
function segSet(seg, v) {
  seg.querySelectorAll('button').forEach((x) => x.classList.remove('on', 'v-off', 'v-observe', 'v-enforce'));
  const b = seg.querySelector('button[data-v="' + v + '"]');
  if (b) b.classList.add('on', 'v-' + v);
}
function segGet(seg) {
  const b = seg.querySelector('button.on');
  return b ? b.dataset.v : null;
}
/** 分段控件接线：从「拦截/保温」降级=高风险弹确认（文案照 mock）；其余直接提交。 */
function segWire(seg, who, onCommit) {
  seg.addEventListener('click', (e) => {
    const b = e.target.closest('button');
    if (!b || b.classList.contains('on')) return;
    const cur = seg.querySelector('.on');
    const commit = () => { segSet(seg, b.dataset.v); onCommit(b.dataset.v); };
    if (cur && cur.dataset.v === 'enforce' && b.dataset.v !== 'enforce') {
      confirmRisk(
        '降级' + who + '？',
        who + '从「' + MODE_TXT.enforce + '」降到「' + MODE_TXT[b.dataset.v] + '」后，凉掉的会话不会再被拦下，为死缓存全价重付的情况可能悄悄发生。确定降级？',
        '确定降级',
        commit
      );
    } else commit();
  });
}

// ── 中风险：保存前改动预览 ──
function collectDiffs(scope) {
  const rows = [];
  scope.querySelectorAll('[data-f]').forEach((elm) => {
    if (elm.closest('tr.pending-del')) return;
    const oldV = elm.dataset.old, newV = elm.value;
    if (String(oldV) !== String(newV)) {
      rows.push({
        lb: elm.dataset.label, el: elm,
        oldV: oldV + (elm.dataset.unit ? ' ' + elm.dataset.unit : ''),
        newV: newV + (elm.dataset.unit ? ' ' + elm.dataset.unit : ''),
      });
    }
  });
  scope.querySelectorAll('[data-f-check]').forEach((elm) => {
    const newV = elm.checked ? '开' : '关';
    if (elm.dataset.old !== newV) rows.push({ lb: elm.dataset.label, oldV: elm.dataset.old, newV, el: elm });
  });
  scope.querySelectorAll('tr.pending-del[data-key]').forEach((tr) => {
    rows.push({ lb: '删除价格行', oldV: tr.dataset.key, newV: '（删除）', el: tr, del: true });
  });
  return rows;
}
function renderPreview(listEl, diffs) {
  listEl.innerHTML = '';
  diffs.forEach((d) => {
    const row = el('div', 'pv-row');
    row.appendChild(el('span', 'pv-lb', d.lb));
    row.appendChild(el('span', 'pv-old', d.oldV));
    row.appendChild(el('span', 'pv-arrow', '→'));
    row.appendChild(el('span', 'pv-new', d.newV));
    listEl.appendChild(row);
  });
}
/** 提交成功后把预览涉及的控件基线推进到新值（删除行移除）。 */
function acceptDiffs(diffs) {
  diffs.forEach((d) => {
    if (d.del) { d.el.remove(); return; }
    if (d.el.type === 'checkbox') d.el.dataset.old = d.el.checked ? '开' : '关';
    else d.el.dataset.old = d.el.value;
  });
}

// ════════ 填充：1. 供应商 ════════
function codexBadge(up) {
  if (up.codex === 'unsupported') return el('span', 'badge red', 'codex 不支持');
  if (up.dialect === 'openai_responses') return el('span', 'badge gray', 'codex 原生');
  return el('span', 'badge blue', 'codex 需翻译');
}
function renderUpstreams() {
  const list = $('#upstreamList');
  list.innerHTML = '';
  const dock = S && S.config ? S.config.dock : null;
  const ups = dock && dock.upstreams ? dock.upstreams : {};
  const names = Object.keys(ups);
  if (!names.length) {
    list.appendChild(el('div', 'note', '还没有渡口上游条目（配置里缺 [dock] 节时渡口不启用）。用下面的表单新增第一家。'));
    return;
  }
  names.forEach((name) => {
    const up = ups[name];
    const active = dock.active === name;
    const card = el('div', 'upstream' + (active ? ' active' : ''));
    const head = el('div', 'u-head');
    head.appendChild(el('span', 'u-name', name));
    if (active) head.appendChild(el('span', 'badge green', '● 当前使用中'));
    head.appendChild(codexBadge(up));
    const acts = el('span', 'u-acts');
    const btnSwitch = el('button', 'btn sm', '切换到这家');
    btnSwitch.disabled = active;
    if (!active) btnSwitch.addEventListener('click', () => askSwitch(name));
    const btnDel = el('button', 'btn sm danger', '删除');
    btnDel.addEventListener('click', () => askDelUpstream(name, active));
    acts.appendChild(btnSwitch);
    acts.appendChild(btnDel);
    head.appendChild(acts);
    card.appendChild(head);
    card.appendChild(el('div', 'u-url', up.base_url || ''));
    const meta = el('div', 'u-meta');
    meta.appendChild(el('span', '', '方言（和上游对话用的协议格式）：' + (up.dialect || 'anthropic')));
    const keySpan = el('span', '');
    keySpan.appendChild(document.createTextNode('密钥 '));
    keySpan.appendChild(el('span', 'u-key', keyLabel(up.api_key)));
    meta.appendChild(keySpan);
    const defModel = up.model_map && up.model_map.default;
    if (defModel) meta.appendChild(el('span', '', '默认模型 ' + defModel));
    card.appendChild(meta);
    list.appendChild(card);
  });
}
function askSwitch(name) {
  confirmRisk(
    '切换到 ' + name + '？',
    '切换活跃供应商后，Claude Code 之后的请求都会改走 ' + name + '，切换立即生效；正在进行的对话不会被打断。确定切换？',
    '切换到这家',
    async () => {
      try {
        const r = await api('/settings/dock/switch', { method: 'POST', body: { name } });
        if (r && r.active) S.config.dock.active = r.active;
        renderUpstreams();
        toast('已切换到 ' + name, 'live');
      } catch (e) {
        // 409=本会话新增未重启（文案含「重启」）；其余照实
        toast(e.message, null, true);
      }
    }
  );
}
function askDelUpstream(name, active) {
  const body = active
    ? name + ' 是当前使用中的供应商，删掉后 Claude Code 将无上游可用，直到你换一家或重新添加。确定删除？'
    : '删除 ' + name + ' 后，Claude Code 将无法使用这家供应商，直到你重新添加。确定？';
  confirmRisk('删除「' + name + '」？', body, '确定删除', async () => {
    try {
      await api('/settings/dock/upstreams/' + encodeURIComponent(name), { method: 'DELETE' });
      markRestart();
      toast('已删除 ' + name, 'restart');
      await refreshData();
    } catch (e) {
      toast(e.message, null, true); // 409 被引用拒删：服务端文案点名引用方
    }
  });
}
function renderProviders() {
  const tbody = $('#providerRows');
  tbody.innerHTML = '';
  const ps = (S && S.providers) || {};
  const names = Object.keys(ps);
  if (!names.length) {
    const tr = el('tr');
    const td = el('td', '', '还没有摆渡供应商。');
    td.colSpan = 6;
    tr.appendChild(td);
    tbody.appendChild(tr);
    return;
  }
  names.forEach((name) => {
    const p = ps[name];
    const tr = el('tr');
    tr.appendChild(el('td', '', name));
    tr.appendChild(el('td', 'mono', p.base_url || ''));
    tr.appendChild(el('td', 'mono', p.model || ''));
    tr.appendChild(el('td', '', p.window ? Math.round(p.window / 10000) + ' 万 token' : '—'));
    tr.appendChild(el('td', 'mono', keyLabel(p.api_key)));
    const tdAct = el('td');
    const btn = el('button', 'btn sm danger', '删除');
    btn.addEventListener('click', () => askDelProvider(name));
    tdAct.appendChild(btn);
    tr.appendChild(tdAct);
    tbody.appendChild(tr);
  });
}
function askDelProvider(name) {
  confirmRisk(
    '删除摆渡供应商「' + name + '」？',
    '删除后如果「摆渡」组正选着它，摆渡会降级为只写一份骨架交接（没有模型叙事）。确定删除？',
    '确定删除',
    async () => {
      try {
        await api('/settings/providers/' + encodeURIComponent(name), { method: 'DELETE' });
        markRestart();
        toast('已删除 ' + name, 'restart');
        await refreshData();
      } catch (e) {
        toast(e.message, null, true); // 409 被 ferry.provider/chain 引用拒删
      }
    }
  );
}
function renderFerryProvider() {
  const sel = $('#ferryProvider');
  sel.innerHTML = '';
  const names = Object.keys((S && S.providers) || {});
  if (!names.length) {
    const o = el('option', '', '（还没有摆渡供应商）');
    o.disabled = true;
    sel.appendChild(o);
    return;
  }
  names.forEach((n) => sel.appendChild(el('option', '', n)));
  sel.value = cfgSec('ferry').provider || names[0];
}
function renderSameModel() {
  const sm = cfgSec('ferry').same_model || {};
  const box = $('#smInfo');
  box.innerHTML = '';
  box.appendChild(el('div', '', '开关：' + (sm.enabled ? '开' : '关')));
  box.appendChild(el('div', '', '触发阈值：' + (sm.threshold_min != null ? sm.threshold_min + ' 分钟' : '—')));
  const ups = Array.isArray(sm.upstreams) && sm.upstreams.length ? sm.upstreams.join('、') : '（空）';
  box.appendChild(el('div', '', '上游白名单：' + ups));
}

$('#btnAddUpstream').addEventListener('click', async () => {
  const fb = $('#fbAdd');
  const name = $('#auName').value.trim();
  const baseUrl = $('#auUrl').value.trim();
  const dialect = $('#auDialect').value;
  const key = $('#auKey').value;
  const mm = {};
  $$('.au-mm').forEach((i) => { const v = i.value.trim(); if (v) mm[i.dataset.k] = v; });
  if (!name) { feedback(fb, '名字必填', true); return; }
  if (!baseUrl) { feedback(fb, '服务地址必填', true); return; }
  if (!mm.default) { feedback(fb, '模型映射的 default 必填', true); return; }
  const body = { base_url: baseUrl, dialect, model_map: mm };
  if (key) body.api_key = key; // 留空=不带该字段
  try {
    await api('/settings/dock/upstreams/' + encodeURIComponent(name), { method: 'PUT', body });
    markRestart();
    feedback(fb, '');
    toast('新增成功，重启守护后才能切换到这家', 'restart');
    ['#auName', '#auUrl', '#auKey'].forEach((s) => { $(s).value = ''; });
    $$('.au-mm').forEach((i) => { i.value = ''; });
    await refreshData();
  } catch (e) {
    feedback(fb, '新增失败：' + e.message, true);
  }
});
$('#btnAddProvider').addEventListener('click', async () => {
  const fb = $('#fbProvider');
  const name = $('#apName').value.trim();
  const baseUrl = $('#apUrl').value.trim();
  const model = $('#apModel').value.trim();
  const win = Number($('#apWindow').value);
  const key = $('#apKey').value;
  if (!name) { feedback(fb, '名字必填', true); return; }
  if (!baseUrl) { feedback(fb, '服务地址必填', true); return; }
  if (!model) { feedback(fb, '模型必填', true); return; }
  const body = { base_url: baseUrl, model };
  if (isFinite(win) && win > 0) body.window = win;
  if (key) body.api_key = key;
  try {
    await api('/settings/providers/' + encodeURIComponent(name), { method: 'PUT', body });
    markRestart();
    feedback(fb, '');
    toast('已新增摆渡供应商 ' + name, 'restart');
    ['#apName', '#apUrl', '#apModel', '#apWindow', '#apKey'].forEach((s) => { $(s).value = ''; });
    await refreshData();
  } catch (e) {
    feedback(fb, '新增失败：' + e.message, true);
  }
});

// ════════ 填充：2. 闸门与阈值 ════════
function syncGateMain() {
  const vals = [segGet($('#gateCC')), segGet($('#gateCodex')), segGet($('#gateDsh'))];
  if (vals[0] && vals.every((v) => v === vals[0])) segSet($('#gateMain'), vals[0]);
  else segSet($('#gateMain'), ''); // 三轨不一致：总开关不显示选中态
}
function fillGate() {
  const g = cfgSec('gate');
  segSet($('#gateCC'), g.cc_mode || 'off');
  segSet($('#gateCodex'), g.codex_mode || 'off');
  segSet($('#gateDsh'), g.dsh_mode || g.codex_mode || 'off'); // 空=运行时回落 codex 档，显示同口径
  syncGateMain();
  const th = cfgSec('thresholds');
  setF($('#thSum'), toMin(th.summarize_s));
  setF($('#thBlock'), toMin(th.block_s));
  setF($('#thMinCtx'), toWan(th.min_ctx_tokens));
  setF($('#thCacheWarn'), toMin(th.cache_warn_s));
}
async function commitGateModes(fbEl) {
  const body = {
    cc_mode: segGet($('#gateCC')) || 'off',
    codex_mode: segGet($('#gateCodex')) || 'off',
    dsh_mode: segGet($('#gateDsh')) || 'off',
  };
  try {
    await saveSection('gate', body, fbEl);
    syncGateMain();
    toast('已保存', 'restart');
  } catch { /* feedback 已报 */ }
}
segWire($('#gateCC'), '这条轨道', () => commitGateModes($('#fbGate')));
segWire($('#gateCodex'), '这条轨道', () => commitGateModes($('#fbGate')));
segWire($('#gateDsh'), '这条轨道', () => commitGateModes($('#fbGate')));
segWire($('#gateMain'), '闸门总开关', (v) => {
  segSet($('#gateCC'), v); segSet($('#gateCodex'), v); segSet($('#gateDsh'), v);
  commitGateModes($('#fbGate'));
});
$('#btnSaveGate').addEventListener('click', () => {
  const scope = $('#sec-gate');
  const diffs = collectDiffs(scope);
  if (!diffs.length) { toast('没有改动', null); return; }
  renderPreview($('#pvGateList'), diffs);
  $('#pvGate').classList.add('show');
  $('#pvGateOk').onclick = async () => {
    const sum = Number($('#thSum').value), blk = Number($('#thBlock').value);
    const minCtx = Number($('#thMinCtx').value), warn = Number($('#thCacheWarn').value);
    if (![sum, blk, minCtx, warn].every((n) => isFinite(n) && n >= 0)) {
      feedback($('#fbGate'), '阈值都要是非负数字', true);
      return;
    }
    try {
      await saveSection('thresholds', {
        summarize_s: toSec(sum),
        block_s: toSec(blk),
        min_ctx_tokens: fromWan(minCtx),
        cache_warn_s: toSec(warn),
      }, $('#fbGate'));
      acceptDiffs(diffs);
      $('#pvGate').classList.remove('show');
      feedback($('#fbGate'), '已保存 ' + diffs.length + ' 处改动');
      toast('已保存 ' + diffs.length + ' 处改动', 'restart');
    } catch { /* feedback 已报 */ }
  };
  $('#pvGateCancel').onclick = () => $('#pvGate').classList.remove('show');
});

// ════════ 填充：3. 守望目录 ════════
function fillWatch() {
  const w = cfgSec('watch');
  $('#wCC').value = w.cc_projects_dir || '';
  $('#wCodex').value = w.codex_sessions_dir || '';
  $('#wDsh').value = w.dsh_sessions_dir || '';
  $('#wPoll').value = w.poll_interval_s != null ? w.poll_interval_s : '';
  $('#wHarvest').checked = !!w.harvest_usage;
}
$('#btnSaveWatch').addEventListener('click', async () => {
  const body = structuredClone(cfgSec('watch')); // 整写语义：codex_extra_dirs 等未展示字段原样带回
  body.cc_projects_dir = $('#wCC').value.trim();
  body.codex_sessions_dir = $('#wCodex').value.trim();
  body.dsh_sessions_dir = $('#wDsh').value.trim();
  body.poll_interval_s = Number($('#wPoll').value) || 0;
  body.harvest_usage = $('#wHarvest').checked;
  try {
    await saveSection('watch', body, $('#fbWatch'));
    feedback($('#fbWatch'), '已保存');
    toast('已保存', 'restart');
  } catch { /* feedback 已报 */ }
});

// ════════ 填充：4. 心跳与保温 ════════
function fillHeartbeat() {
  const hb = cfgSec('heartbeat');
  setC($('#hbEnabled'), hb.enabled);
  setF($('#hbTtl'), toMin(hb.ttl_s));
  const qw = cfgSec('question_watch');
  segSet($('#qwMode'), qw.mode || 'off');
  setF($('#qwMinQ'), qw.min_questions != null ? qw.min_questions : '');
  setF($('#qwBeat'), toMin(qw.beat_interval_s));
  setF($('#qwMaxBeats'), qw.max_beats != null ? qw.max_beats : '');
  setF($('#qwLead'), toMin(qw.ferry_deadline_lead_s));
}
async function commitQwMode() {
  const body = structuredClone(cfgSec('question_watch'));
  body.mode = segGet($('#qwMode')) || 'off';
  body.min_questions = Number($('#qwMinQ').value) || 0;
  body.beat_interval_s = toSec($('#qwBeat').value);
  body.max_beats = Number($('#qwMaxBeats').value) || 0;
  body.ferry_deadline_lead_s = toSec($('#qwLead').value);
  try {
    await saveSection('question_watch', body, $('#fbHb'));
    toast('已保存', 'restart');
  } catch { /* feedback 已报 */ }
}
segWire($('#qwMode'), '问询守望', () => commitQwMode());
$('#btnSaveHb').addEventListener('click', () => {
  const scope = $('#sec-heartbeat');
  const diffs = collectDiffs(scope);
  if (!diffs.length) { toast('没有改动', null); return; }
  renderPreview($('#pvHbList'), diffs);
  $('#pvHb').classList.add('show');
  $('#pvHbOk').onclick = async () => {
    const fb = $('#fbHb');
    try {
      const hbBody = structuredClone(cfgSec('heartbeat')); // ttl_measured_at/ttl_source 原样带回
      hbBody.enabled = $('#hbEnabled').checked;
      hbBody.ttl_s = toSec($('#hbTtl').value);
      await saveSection('heartbeat', hbBody, fb);
      const qwBody = structuredClone(cfgSec('question_watch')); // dsh_mode 原样带回
      qwBody.mode = segGet($('#qwMode')) || 'off';
      qwBody.min_questions = Number($('#qwMinQ').value) || 0;
      qwBody.beat_interval_s = toSec($('#qwBeat').value);
      qwBody.max_beats = Number($('#qwMaxBeats').value) || 0;
      qwBody.ferry_deadline_lead_s = toSec($('#qwLead').value);
      await saveSection('question_watch', qwBody, fb);
      acceptDiffs(diffs);
      $('#pvHb').classList.remove('show');
      feedback(fb, '已保存 ' + diffs.length + ' 处改动');
      toast('已保存 ' + diffs.length + ' 处改动', 'restart');
    } catch { /* feedback 已报 */ }
  };
  $('#pvHbCancel').onclick = () => $('#pvHb').classList.remove('show');
});

// ════════ 填充：5. 摆渡 ════════
$('#ferryProvider').addEventListener('change', async () => {
  const fb = $('#fbFerry');
  const body = { provider: $('#ferryProvider').value };
  const chain = cfgSec('ferry').chain;
  if (Array.isArray(chain) && chain.length) body.chain = chain; // same_model 子表不经节级端点
  try {
    await saveSection('ferry', body, fb);
    feedback(fb, '已保存，重启守护后生效');
    toast('已保存', 'restart');
  } catch { /* feedback 已报 */ }
});

// ════════ 填充：6. 价格表 ════════
function priceUnitText(book) {
  const per = book.per || 10000;
  return (book.unit || '') + (per === 10000 ? '/万 token' : '/' + per + ' token');
}
function latestVersion(book) {
  const vs = book.versions || [];
  return vs.length ? vs[vs.length - 1] : { p_in: '', p_cache: null, p_out: '' };
}
function renderPrices() {
  const tbody = $('#priceRows');
  tbody.innerHTML = '';
  const ps = (S && S.prices) || {};
  const keys = Object.keys(ps);
  if (!keys.length) {
    const tr = el('tr');
    const td = el('td', '', '价格表为空。点「新增一行」登记第一家。');
    td.colSpan = 6;
    tr.appendChild(td);
    tbody.appendChild(tr);
    return;
  }
  keys.forEach((key) => {
    const book = ps[key];
    const v = latestVersion(book);
    const unitText = priceUnitText(book);
    // 键=单段标识（对应 config 的 [prices.<key>]，如 glm）；条目无供应商/模型字段，
    // 键本身就是全部标识。
    const tr = el('tr');
    tr.dataset.key = key;
    tr.appendChild(el('td', 'cell-txt mono', key));
    [['p_in', '输入价'], ['p_cache', '缓存价'], ['p_out', '输出价']].forEach(([f, lb]) => {
      const td = el('td');
      const inp = document.createElement('input');
      inp.type = 'number';
      inp.step = '0.1';
      inp.dataset.f = '1';
      inp.dataset.label = key + ' ' + lb;
      inp.dataset.unit = unitText;
      const val = v[f];
      inp.value = val == null ? '' : String(val);
      inp.dataset.old = inp.value;
      td.appendChild(inp);
      tr.appendChild(td);
    });
    tr.appendChild(el('td', '', book.unit || ''));
    const tdAct = el('td');
    const btn = el('button', 'btn sm danger', '删除');
    btn.addEventListener('click', () => tr.classList.toggle('pending-del'));
    tdAct.appendChild(btn);
    tr.appendChild(tdAct);
    tbody.appendChild(tr);
  });
}
$('#btnAddPrice').addEventListener('click', () => {
  const tr = el('tr');
  tr.dataset.new = '1';
  const tdKey = el('td');
  const inKey = document.createElement('input');
  inKey.type = 'text'; inKey.placeholder = '键（如 glm）'; inKey.style.width = '130px';
  inKey.dataset.nf = 'key';
  // 键=单段标识：含斜杠会被后端路由切分错认（新增必 404），当场红字提示并拦住提交
  const keyErr = el('div', '', '键不能包含斜杠');
  keyErr.style.cssText = 'color:var(--red); font-size:11px; display:none';
  inKey.addEventListener('input', () => {
    keyErr.style.display = inKey.value.includes('/') ? '' : 'none';
  });
  tdKey.appendChild(inKey);
  tdKey.appendChild(keyErr);
  tr.appendChild(tdKey);
  ['输入价', '缓存价', '输出价'].forEach((lb) => {
    const td = el('td');
    const inp = document.createElement('input');
    inp.type = 'number';
    inp.step = '0.1';
    inp.dataset.f = '1';
    inp.dataset.label = '新行' + lb;
    inp.dataset.old = '';
    inp.dataset.unit = '/万 token';
    td.appendChild(inp);
    tr.appendChild(td);
  });
  const tdUnit = el('td');
  const inUnit = document.createElement('input');
  inUnit.type = 'text'; inUnit.placeholder = '元'; inUnit.style.width = '64px';
  inUnit.dataset.nf = 'unit';
  tdUnit.appendChild(inUnit);
  tr.appendChild(tdUnit);
  const tdAct = el('td');
  const btn = el('button', 'btn sm danger', '删除');
  btn.addEventListener('click', () => tr.remove());
  tdAct.appendChild(btn);
  tr.appendChild(tdAct);
  $('#priceRows').appendChild(tr);
});
function localToday() {
  const d = new Date();
  const p = (n) => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate());
}
$('#btnSavePrices').addEventListener('click', () => {
  const scope = $('#sec-prices');
  const diffs = collectDiffs(scope);
  const newRows = $$('#priceRows tr[data-new]');
  if (!diffs.length && !newRows.length) { toast('没有改动', null); return; }
  const badKey = newRows.find((tr) => tr.querySelector('[data-nf=key]').value.includes('/'));
  if (badKey) { feedback($('#fbPrices'), '新增行的键不能包含斜杠', true); return; }
  newRows.forEach((tr) => {
    const key = tr.querySelector('[data-nf=key]').value.trim();
    if (key) {
      const row = { lb: '新增价格行', oldV: '（无）', newV: key, el: tr };
      diffs.push(row);
    }
  });
  renderPreview($('#pvPricesList'), diffs);
  $('#pvPrices').classList.add('show');
  $('#pvPricesOk').onclick = async () => {
    const fb = $('#fbPrices');
    const today = localToday();
    const errs = [];
    // ① 删除（pending-del 且是真行）
    for (const tr of $$('#priceRows tr.pending-del[data-key]')) {
      try {
        await api('/settings/prices/' + encodeURIComponent(tr.dataset.key), { method: 'DELETE' });
        tr.remove();
      } catch (e) { errs.push(tr.dataset.key + '：' + e.message); }
    }
    // ② 既有行改价：追加一个今天生效的新版本（不动历史版本，旧账不重算）
    for (const tr of $$('#priceRows tr[data-key]:not(.pending-del)')) {
      const inputs = [...tr.querySelectorAll('[data-f]')];
      const changed = inputs.some((i) => i.dataset.old !== i.value);
      if (!changed) continue;
      const key = tr.dataset.key;
      const book = (S.prices || {})[key];
      if (!book) continue;
      const [pIn, pCache, pOut] = inputs.map((i) => i.value.trim());
      if (pIn === '' || pOut === '' || !isFinite(Number(pIn)) || !isFinite(Number(pOut))) {
        errs.push(key + '：输入价和输出价必填且要是数字');
        continue;
      }
      const nv = { effective_from: today, p_in: Number(pIn), p_out: Number(pOut) };
      if (pCache !== '' && isFinite(Number(pCache))) nv.p_cache = Number(pCache);
      const versions = (book.versions || []).map((v) => ({ ...v }));
      if (versions.length && versions[versions.length - 1].effective_from === today) {
        versions[versions.length - 1] = nv; // 当天已有一版：覆盖当天版
      } else {
        versions.push(nv);
      }
      try {
        await api('/settings/prices/' + encodeURIComponent(key), {
          method: 'PUT',
          body: { unit: book.unit || '', per: book.per || 10000, versions },
        });
      } catch (e) { errs.push(key + '：' + e.message); }
    }
    // ③ 新增行
    for (const tr of $$('#priceRows tr[data-new]')) {
      const key = tr.querySelector('[data-nf=key]').value.trim();
      const unit = tr.querySelector('[data-nf=unit]').value.trim() || '元';
      if (!key) { errs.push('新增行：键必填'); continue; }
      if (key.includes('/')) { errs.push('新增行 ' + key + '：键不能包含斜杠'); continue; }
      const [pIn, pCache, pOut] = [...tr.querySelectorAll('[data-f]')].map((i) => i.value.trim());
      if (pIn === '' || pOut === '' || !isFinite(Number(pIn)) || !isFinite(Number(pOut))) {
        errs.push('新增行 ' + key + '：输入价和输出价必填且要是数字');
        continue;
      }
      const nv = { effective_from: today, p_in: Number(pIn), p_out: Number(pOut) };
      if (pCache !== '' && isFinite(Number(pCache))) nv.p_cache = Number(pCache);
      try {
        await api('/settings/prices/' + encodeURIComponent(key), {
          method: 'PUT',
          body: { unit, per: 10000, versions: [nv] },
        });
        tr.remove();
      } catch (e) { errs.push('新增行 ' + key + '：' + e.message); }
    }
    if (errs.length) {
      feedback(fb, '部分没存上：' + errs.join('；'), true);
      toast(errs.join('；'), null, true);
      await refreshData();
      return;
    }
    $('#pvPrices').classList.remove('show');
    markRestart();
    feedback(fb, '已保存');
    toast('已保存', 'restart');
    await refreshData();
  };
  $('#pvPricesCancel').onclick = () => $('#pvPrices').classList.remove('show');
});

// ════════ 填充：7. 通知 ════════
function fillNotify() {
  const n = cfgSec('notify');
  $('#ntEnabled').checked = !!n.enabled;
  $('#ntPushover').checked = !!n.pushover;
  $('#ntToast').checked = !!n.toast;
  $('#ntTokenCur').textContent = '当前：' + keyLabel(n.pushover_token);
  $('#ntUserCur').textContent = '当前：' + keyLabel(n.pushover_user);
  $('#ntTokenIn').value = '';
  $('#ntUserIn').value = '';
}
/** notify 提交体：开关三件套 + pushover_user 原样带回（服务端不视其为密钥，
 *  省略会被整写清掉）；密钥字段用户没改就不带（省略=服务端保留现值）。 */
function notifyBody() {
  const n = cfgSec('notify');
  const body = {
    enabled: $('#ntEnabled').checked,
    pushover: $('#ntPushover').checked,
    toast: $('#ntToast').checked,
  };
  if (typeof n.pushover_user === 'string') body.pushover_user = n.pushover_user;
  return body;
}
$$('.sw-live').forEach((sw) => sw.addEventListener('change', async () => {
  try {
    await saveSection('notify', notifyBody(), $('#fbNotify'));
    toast(sw.dataset.name + '已' + (sw.checked ? '开启' : '关闭'), 'restart');
  } catch {
    sw.checked = !sw.checked; // 回滚开关位置
  }
}));
$('#btnSaveNotify').addEventListener('click', async () => {
  const fb = $('#fbNotify');
  const body = notifyBody();
  const token = $('#ntTokenIn').value;
  const user = $('#ntUserIn').value;
  if (token) body.pushover_token = token; // 留空=不带该字段=保留现值
  if (user) body.pushover_user = user;
  try {
    await saveSection('notify', body, fb);
    feedback(fb, '已保存');
    toast('已保存', 'restart');
    await refreshData(); // 让「当前：••••尾四」跟进新值
  } catch { /* feedback 已报 */ }
});

// ════════ 填充：8. 服务 ════════
function fillService() {
  const sv = cfgSec('server');
  setF($('#svPort'), sv.port != null ? sv.port : '');
  setF($('#svDataDir'), sv.data_dir || '');
}
$('#btnSaveService').addEventListener('click', () => {
  const scope = $('#sec-service');
  const diffs = collectDiffs(scope);
  if (!diffs.length) { toast('没有改动', null); return; }
  const doPreview = () => {
    renderPreview($('#pvServiceList'), diffs);
    $('#pvService').classList.add('show');
    $('#pvServiceOk').onclick = async () => {
      const port = Number($('#svPort').value);
      if (!isFinite(port) || port <= 0 || port > 65535) {
        feedback($('#fbService'), '端口要是 1–65535 的数字', true);
        return;
      }
      try {
        const r = await saveSection('server', {
          port,
          data_dir: $('#svDataDir').value.trim(),
        }, $('#fbService'));
        acceptDiffs(diffs);
        $('#pvService').classList.remove('show');
        feedback($('#fbService'), '已保存' + (r && r.new_port ? '（新端口 ' + r.new_port + '）' : ''));
        toast('已保存', 'restart');
      } catch { /* feedback 已报 */ }
    };
    $('#pvServiceCancel').onclick = () => $('#pvService').classList.remove('show');
  };
  // 高风险字段（改端口）先走二次确认，再出预览（顺序照 mock）
  const portDiff = diffs.find((d) => d.lb === '守护端口');
  if (portDiff) {
    confirmRisk(
      '改守护端口？',
      '守护端口从 ' + portDiff.oldV + ' 改成 ' + portDiff.newV + ' 后，所有指向旧端口的编辑器配置会立刻连不上渡口；重启守护后还要重新指一次。重启失败会自动回滚到改端口前的那份健康配置，不会把守护弄丢。确定要改？',
      '确定要改',
      doPreview
    );
  } else doPreview();
});

// ════════ 填充：9. 备份与还原 ════════
function fmtTs(ts) {
  const d = new Date(ts * 1000);
  const p = (n) => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
}
const REASON_TXT = {
  manual: ['手动备份', '（你点的「立即备份」）'],
  auto: ['自动备份', '（写入配置前）'],
  'pre-restore': ['自动备份', '（还原前）'],
};
async function loadSnapshots() {
  const list = $('#snapList');
  list.innerHTML = '';
  try {
    const r = await api('/settings/snapshots');
    const snaps = (r && r.snapshots) || [];
    if (!snaps.length) {
      list.appendChild(el('div', 'note', '还没有快照。点「立即备份」留第一份。'));
      return;
    }
    snaps.forEach((s) => {
      const row = el('div', 'snap');
      row.appendChild(el('span', 's-time', fmtTs(s.ts)));
      const why = el('span', 's-why');
      const rt = REASON_TXT[s.reason] || [s.reason || '备份', ''];
      why.appendChild(document.createTextNode(rt[0] + ' '));
      const tag = el('span', 'tag', rt[1] + ' · ' + (s.bytes ? (s.bytes / 1024).toFixed(1) + ' KB' : ''));
      why.appendChild(tag);
      row.appendChild(why);
      const btn = el('button', 'btn sm', '还原这份备份');
      btn.addEventListener('click', () => askRestore(s.id, fmtTs(s.ts)));
      row.appendChild(btn);
      list.appendChild(row);
    });
  } catch (e) {
    list.appendChild(el('div', 'note warn', '快照列表取不到：' + e.message));
  }
}
function askRestore(id, timeText) {
  confirmRisk(
    '还原到 ' + timeText + ' 的快照？',
    '还原后当前配置会被这份快照整体替换。放心：还原前会先自动备份当前配置，随时可以再换回来。',
    '还原这份备份',
    async () => {
      try {
        await api('/settings/snapshots/' + encodeURIComponent(id) + '/restore', { method: 'POST' });
        markRestart();
        toast('已还原，当前配置已自动另存一份', 'restart');
        await refreshData();
        await loadSnapshots();
      } catch (e) {
        toast(e.message, null, true);
      }
    }
  );
}
$('#btnBackupNow').addEventListener('click', async () => {
  const fb = $('#fbBackup');
  try {
    await api('/settings/snapshots', { method: 'POST' });
    feedback(fb, '');
    toast('已备份到快照库', 'live');
    await loadSnapshots();
  } catch (e) {
    feedback(fb, '备份失败：' + e.message, true);
  }
});

// ════════ 安全重启守护（POST /settings/restart，票 07 契约） ════════
// 契约：200 {"restarting":true[,"new_port":N]}——响应先于守护停止到达，随后守护优雅
// 停机再自动拉起，起不来自动回滚到上次健康配置重试；400 {"error":"…"}=重启前检查
// 没过，守护没动过。回 200 后本窗口与守护的连接会断开再恢复。
const RESTART_POLL_MS = 2000;             // 回连轮询间隔
const RESTART_BUDGET_MS = 6 * 60 * 1000;  // 回连总预算 6 分钟
function setAllDisabled(on) {
  $$('.main input, .main select, .main button').forEach((elm) => { elm.disabled = on; });
  $$('#nav .nav-it').forEach((elm) => { elm.disabled = on; }); // 导航不发包，禁用/恢复一起做完整
  $('#btnRestart').disabled = on;
  if (on) closeConfirm(); // 进禁用态时把可能开着的确认弹窗一并关掉，防挂起的动作再被点
}
/** 探一次：任何 HTTP 应答（含 401）都算守护回来了；网络层抛错=还没起来。 */
async function probeDaemon(base) {
  try {
    const headers = {};
    if (TOKEN) headers.Authorization = 'Bearer ' + TOKEN;
    await fetch(base + '/stats', { headers, signal: AbortSignal.timeout(4000) });
    return true;
  } catch { return false; }
}
/** 200 之后：禁用全部控件 + 重启专属横幅，轮询候选地址直到守护回来或超时。 */
function enterRestarting(candidates) {
  const banner = $('#errBanner');
  banner.textContent = '守护正在重启…有在途请求时会先等它跑完，最长几分钟。页面上的保存与操作先不可用，守护回来后自动恢复。';
  banner.classList.add('show');
  $('#btnRestart').textContent = '正在重启…';
  setAllDisabled(true);
  const deadline = Date.now() + RESTART_BUDGET_MS;
  const timeoutGiveUp = () => {
    // 超时：保持禁用，留人话指引（回滚与重试由守护侧负责，这里只指认通知渠道）
    banner.textContent = '守护 6 分钟没回来。失败时守护会自动回滚到上次健康配置并重试；若仍失败会停在安全状态，请看系统通知或 Pushover 的「Ferryman 安全重启失败」提示，按提示手动恢复后重开本窗。';
  };
  // 探活命中只代表「有 HTTP 进程在应答」：读到全量配置（GET /settings 成功）才算真回来。
  // 读不到就保持禁用与横幅，隔 2 秒重试，预算沿用同一个 6 分钟（从 enterRestarting 起算）。
  const confirmRead = async () => {
    if (Date.now() > deadline) { timeoutGiveUp(); return; }
    banner.textContent = '守护已回来，正在读取最新配置…页面上的保存与操作先不可用，读到后自动恢复。';
    try {
      S = await api('/settings');
    } catch {
      setTimeout(confirmRead, RESTART_POLL_MS);
      return;
    }
    recoverFromRestart();
  };
  const pollRound = async () => {
    if (Date.now() > deadline) { timeoutGiveUp(); return; }
    for (const base of candidates) {
      if (await probeDaemon(base)) {
        BASE = base; // 端口可能变了：换回连上的那个地址
        confirmRead();
        return;
      }
    }
    setTimeout(pollRound, RESTART_POLL_MS);
  };
  setTimeout(pollRound, RESTART_POLL_MS);
}
/** 读取确认成功后的收尾：解禁、重渲染、撤横幅与「待重启」标记、报成功。 */
function recoverFromRestart() {
  setAllDisabled(false); // 先解禁：fillAll 重建的动态控件会各自重设 disabled
  fillAll();
  $('#btnRestart').textContent = '重启守护';
  $('#errBanner').classList.remove('show');
  $('#restartFlag').classList.remove('show'); // 撤掉「待重启」标记
  loadSnapshots();
  toast('守护已重启，待生效的改动现在生效了', 'live');
}
$('#btnRestart').addEventListener('click', () => {
  confirmRisk(
    '重启守护？',
    '守护会先应答本请求，随后优雅停机（有在途请求会先等它跑完）再自动拉起；万一新配置起不来，会自动换回上一次正常运行的配置再拉起一次。这期间本窗口与守护的连接会断开，回来后自动接上。确定重启？',
    '确定重启',
    async () => {
      // 一进回调就进保护态：禁用重启按钮 + 关掉可能开着的确认弹窗，POST 在途期间防手滑
      $('#btnRestart').disabled = true;
      closeConfirm();
      try {
        const r = await api('/settings/restart', { method: 'POST', body: {} });
        const candidates = [];
        if (r && r.new_port) candidates.push('http://127.0.0.1:' + r.new_port); // 改了端口：先试新口
        candidates.push(BASE); // 再回落当前地址
        enterRestarting(candidates);
      } catch (e) {
        // 400=重启前检查没过，守护没动过：红字报人话原因，按钮恢复可点
        $('#btnRestart').disabled = false;
        toast('重启没批下来：' + e.message, null, true);
      }
    }
  );
});

// ════════ 全量装载 ════════
function fillAll() {
  renderUpstreams();
  renderProviders();
  renderFerryProvider();
  renderSameModel();
  fillGate();
  fillWatch();
  fillHeartbeat();
  renderPrices();
  fillNotify();
  fillService();
}
async function refreshData() {
  S = await api('/settings');
  fillAll();
}
/** 取数失败：结构照出、控件禁用、横幅说明——不许白屏。 */
function fatal(msg) {
  const b = $('#errBanner');
  b.textContent = msg + '。页面结构照常显示，但数据为空、所有保存与操作不可用；守护恢复后重开本窗即可。';
  b.classList.add('show');
  setAllDisabled(true); // 含侧栏导航与重启按钮：横幅声称所有操作不可用，按钮也该不可点
}
(async function boot() {
  const ok = await resolveTarget();
  if (!ok) {
    fatal('读不到本机守护的访问凭据（~/ferryman/daemon.token）。请确认 Ferryman 守护在运行');
    return;
  }
  try {
    await refreshData();
  } catch (e) {
    fatal('连不上守护（' + BASE + '）：' + e.message);
  }
  loadSnapshots(); // 快照面独立装载（主数据失败也照试，失败在列表区如实标注）
})();
