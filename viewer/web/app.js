// Ferryman T43 查看器前端——原生 JS，无框架无构建无外部资源（离线铁律）。
// hash 路由：#/ = 会话列表；#/t/<lineage> = 时序图（Task 5 实现，此处留分发位）。
'use strict';

// ---------- 工具 ----------

// esc HTML 转义：标题/项目/lineage 来自账本数据，进 innerHTML 前一律过这里。
function esc(s) {
  return String(s).replace(/[&<>"']/g, function (c) {
    return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
  });
}

// fmtTS unix 秒 → 本地 MM-dd HH:mm；0 或非法值显示 -。
function fmtTS(ts) {
  if (!ts) return '-';
  var d = new Date(ts * 1000);
  if (isNaN(d.getTime())) return '-';
  function p(n) { return String(n).padStart(2, '0'); }
  return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
}

// fmtK token 数：>=1w 用 x.xw（1.2w=12000），否则千分位。
function fmtK(n) {
  if (n >= 10000) return (n / 10000).toFixed(1).replace(/\.0$/, '') + 'w';
  return n.toLocaleString('en-US');
}

// fetchJSON fetch + JSON 解析；网络错误、非 JSON 体、非 2xx（优先带后端 error 字段）一律抛错。
async function fetchJSON(url, opts) {
  var res;
  try {
    res = await fetch(url, opts);
  } catch (e) {
    throw new Error('网络错误：' + e.message);
  }
  var body = null;
  try { body = await res.json(); } catch (_) { /* 响应体非 JSON：按 null 处理 */ }
  if (!res.ok) {
    var msg = (body && body.error) ? body.error : (res.status + ' ' + res.statusText);
    throw new Error('请求失败（' + msg + '）');
  }
  return body;
}

// setNote 更新顶部工具条右侧说明（来自 /api/sessions 的 note 字段，若有）。
function setNote(text) {
  document.getElementById('toolbar-note').textContent = text || '';
}

// ---------- 列表页 ----------

// renderList GET /api/sessions → 会话表格（后端已按最后活动倒序）。
// 列：标题（无则 lineage 前 24 字符）、项目目录、开始时间、最后活动、
// 请求数、tokens 合计（input+cache_read+creation）、窗口数、心跳数、交接数。
// 行点击进入该 lineage 的时序页。
async function renderList() {
  var app = document.getElementById('app');
  app.innerHTML = '<p class="loading">加载中…</p>';
  var data;
  try {
    data = await fetchJSON('/api/sessions');
  } catch (e) {
    app.innerHTML = '<p class="error">加载失败：' + esc(e.message) + '</p>';
    return;
  }
  setNote(data.note || '');

  var sessions = data.sessions || [];
  var rows = sessions.map(function (s) {
    var title = s.title || (s.lineage_id ? s.lineage_id.slice(0, 24) : '(无 lineage)');
    var tokens = (s.input || 0) + (s.cache_read || 0) + (s.cache_creation || 0);
    return '<tr data-lineage="' + esc(s.lineage_id) + '">' +
      '<td class="cell-title">' + esc(title) + '</td>' +
      '<td class="cell-project">' + esc(s.project || '-') + '</td>' +
      '<td>' + fmtTS(s.first_ts) + '</td>' +
      '<td>' + fmtTS(s.last_ts) + '</td>' +
      '<td class="num">' + (s.requests || 0) + '</td>' +
      '<td class="num">' + fmtK(tokens) + '</td>' +
      '<td class="num">' + (s.windows || 0) + '</td>' +
      '<td class="num">' + (s.beats || 0) + '</td>' +
      '<td class="num">' + (s.handoffs || 0) + '</td>' +
      '</tr>';
  }).join('');

  app.innerHTML =
    '<table id="session-table"><thead><tr>' +
    '<th>标题</th><th>项目目录</th><th>开始时间</th><th>最后活动</th>' +
    '<th class="num">请求数</th><th class="num">tokens</th>' +
    '<th class="num">窗口</th><th class="num">心跳</th><th class="num">交接</th>' +
    '</tr></thead><tbody>' +
    (rows || '<tr><td colspan="9" class="empty">暂无会话数据</td></tr>') +
    '</tbody></table>';

  app.querySelectorAll('tr[data-lineage]').forEach(function (tr) {
    tr.addEventListener('click', function () {
      location.hash = '#/t/' + encodeURIComponent(tr.dataset.lineage);
    });
  });
}

// ---------- 时序页（Task 5 实现位） ----------

// renderTimeline 单个 lineage 的时序图。Task 5 前先占位，点进不至于白屏。
async function renderTimeline(lineage) {
  var app = document.getElementById('app');
  app.innerHTML = '<p class="loading">时序图开发中（lineage: ' + esc(lineage) + '）</p>';
}

// ---------- 路由 ----------

// route 按 hash 前缀分发：#/t/<lineage> → renderTimeline；其余（#/ 或空）→ renderList。
function route() {
  var h = location.hash || '#/';
  var m = h.match(/^#\/t\/(.+)$/);
  if (m) {
    renderTimeline(decodeURIComponent(m[1]));
  } else {
    renderList();
  }
}

window.onhashchange = route;
route(); // script 在 body 末尾，DOM 已就绪，直接首渲染
