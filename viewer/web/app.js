// Ferryman T43 查看器前端——原生 JS，无框架无构建无外部资源（离线铁律）。
// hash 路由：#/ = 会话列表；#/t/<lineage> = 单会话时序图（SVG 手绘）。
// DOM 纪律：时序页账本数据一律 createElement(SVG 用 createElementNS)/textContent/setAttribute
// 进 DOM，不拼 innerHTML（唯一例外：零插值的静态骨架串）。
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

// ---------- 页面级竞态守卫 ----------

// navSeq 导航序号：每次 renderList/renderTimeline 进入时自增并捕获；
// await 返回后序号不匹配 = 用户已切走，旧响应直接丢弃，绝不覆盖新视图。
var navSeq = 0;

// tlPlayTimer 时序页回放定时器（模块级：切页时必须停，tick 内另有 navSeq 双保险）。
var tlPlayTimer = null;

function stopTlPlay() {
  if (tlPlayTimer) {
    clearInterval(tlPlayTimer);
    tlPlayTimer = null;
  }
}

// ---------- 列表页 ----------

// renderList GET /api/sessions → 会话表格（后端已按最后活动倒序）。
// 列：标题（无则 lineage 前 24 字符）、项目目录、开始时间、最后活动、
// 请求数、tokens 合计（input+cache_read+creation）、窗口数、心跳数、交接数。
// 行点击进入该 lineage 的时序页。
async function renderList() {
  var app = document.getElementById('app');
  var seq = ++navSeq; // 竞态守卫：列表页与时序页共用 navSeq，跨页慢响应同样不互踩
  stopTlPlay();
  app.innerHTML = '<p class="loading">加载中…</p>';
  var data;
  try {
    data = await fetchJSON('/api/sessions');
  } catch (e) {
    if (seq !== navSeq) return;
    setNote(''); // 失败时清掉旧目录的 note，避免残留
    app.innerHTML = '<p class="error">加载失败：' + esc(e.message) + '</p>';
    return;
  }
  if (seq !== navSeq) return;
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

// ---------- 时序页（Task 5） ----------

var SVG_NS = 'http://www.w3.org/2000/svg';

// svgEl 创建 SVG 元素并批量 setAttribute。
function svgEl(tag, attrs) {
  var el = document.createElementNS(SVG_NS, tag);
  if (attrs) {
    for (var k in attrs) el.setAttribute(k, attrs[k]);
  }
  return el;
}

// svgTitleEl 挂 SVG 原生悬停提示（<title> 子元素；textContent 写入，不走 markup）。
function svgTitleEl(parent, text) {
  var t = svgEl('title');
  t.textContent = text;
  parent.appendChild(t);
}

// niceCeil yMax 阶梯向上取整：10^k × {1,2,3,5}。brief 示例 260k→300k 决定阶梯含 3
//（若按 {1,2,5} 则 260k→500k，与示例矛盾，从示例）。
function niceCeil(v) {
  if (v <= 0) return 1;
  var base = Math.pow(10, Math.floor(Math.log10(v)));
  var m = v / base;
  var step = m <= 1 ? 1 : m <= 2 ? 2 : m <= 3 ? 3 : m <= 5 ? 5 : 10;
  return step * base;
}

// fmtDur 秒：最多 1 位小数，整数不带尾零。
function fmtDur(s) {
  return String(Math.round(s * 10) / 10);
}

// fmtCost 成本（美元）：<0.001 用科学计数防一串 0，其余 4 位截断。
function fmtCost(v) {
  if (!isFinite(v)) return '-';
  if (v !== 0 && Math.abs(v) < 0.001) return v.toExponential(2);
  return String(Math.round(v * 10000) / 10000);
}

// nearestIdx 二分找 arr（升序）中离 v 最近的下标。
function nearestIdx(arr, v) {
  if (!arr.length) return -1;
  var lo = 0, hi = arr.length - 1;
  while (lo < hi) {
    var mid = (lo + hi) >> 1;
    if (arr[mid] < v) lo = mid + 1; else hi = mid;
  }
  if (lo > 0 && Math.abs(arr[lo - 1] - v) <= Math.abs(arr[lo] - v)) return lo - 1;
  return lo;
}

// 时序页静态骨架：零数据插值（账本数据全部经 createElement/textContent/setAttribute 进 DOM）。
var TL_SCAFFOLD =
  '<div id="tl-head">' +
  '<a href="#/">← 返回会话列表</a>' +
  '<span id="tl-title"></span>' +
  '</div>' +
  '<div id="tl-wrap">' +
  '<svg id="tl-svg" viewBox="0 0 1200 520" preserveAspectRatio="xMidYMid meet" role="img" aria-label="单会话 token 时序图"></svg>' +
  '<div id="tl-side">' +
  '<div id="tl-cum"></div>' +
  '<div id="tl-wininfo" hidden></div>' +
  '</div>' +
  '<div id="tl-tooltip" hidden>' +
  '<div class="tip-time"></div>' +
  '<div class="tip-model"></div>' +
  '<div class="tip-row"><span>input</span><span class="v-in"></span></div>' +
  '<div class="tip-row"><span>cache_read</span><span class="v-cr"></span></div>' +
  '<div class="tip-row"><span>creation</span><span class="v-cc"></span></div>' +
  '<div class="tip-row"><span>output</span><span class="v-out"></span></div>' +
  '<div class="tip-title"></div>' +
  '</div>' +
  '</div>' +
  '<div id="tl-ctrl">' +
  '<button id="tl-play" type="button" title="回放播放/暂停">▶</button>' +
  '<input id="tl-cursor" type="range" aria-label="回放游标">' +
  '<label id="tl-ttl-label">TTL 存活 <input id="tl-ttl" type="number" min="0" step="60" value="600"> 秒</label>' +
  '<span id="tl-legend">绿=cache_read 红=input+creation 蓝=output</span>' +
  '</div>';

// tlNotice 时序页整页提示（空 lineage / 无数据 / 加载失败的兜底，绝不白屏）。
// isErr 为真用红色错误样式。
function tlNotice(app, msg, isErr) {
  app.textContent = '';
  var p = document.createElement('p');
  p.className = isErr ? 'tl-empty error' : 'tl-empty';
  p.textContent = msg;
  var back = document.createElement('a');
  back.href = '#/';
  back.className = 'tl-back';
  back.textContent = '← 返回会话列表';
  app.appendChild(p);
  app.appendChild(back);
}

// renderTimeline 单个 lineage 的时序图：拉 /api/timeline → buildTimelinePage。
// 纪律：①畸形 hash 兜底提示不白屏；②navSeq 守卫防旧响应覆盖新视图。
async function renderTimeline(lineage) {
  var app = document.getElementById('app');
  var seq = ++navSeq;
  stopTlPlay();
  setNote('');

  if (!lineage) {
    if (seq !== navSeq) return;
    tlNotice(app, 'lineage 为空：请从会话列表点击行进入');
    return;
  }

  app.textContent = '';
  var loading = document.createElement('p');
  loading.className = 'loading';
  loading.textContent = '加载中…';
  app.appendChild(loading);

  var data;
  try {
    data = await fetchJSON('/api/timeline?lineage=' + encodeURIComponent(lineage));
  } catch (e) {
    if (seq !== navSeq) return;
    setNote('');
    tlNotice(app, '加载失败：' + e.message, true);
    return;
  }
  if (seq !== navSeq) return; // 旧响应：新视图已接管，丢弃
  setNote(data.note || '');   // 数据目录缺失时后端带 note

  var requests = data.requests || [];
  var events = data.events || [];
  var windows = data.windows || [];
  if (!requests.length && !events.length && !windows.length) {
    tlNotice(app, '该族系暂无账本数据（lineage 前 24 字符：' + lineage.slice(0, 24) + '）');
    return;
  }
  buildTimelinePage(app, lineage, requests, events, windows);
}

// buildTimelinePage 搭骨架 → 算标尺/柱几何 → 挂交互 → draw()。
// 坐标系（brief 固定）：viewBox 0 0 1200 520；事件行 y=12..36；TTL 阴影带 y=40..58；
// 主图 y=60..460；x 轴刻度 y=470..490；x 绘图区 40..1160。
function buildTimelinePage(app, lineage, requests, events, windows) {
  app.innerHTML = TL_SCAFFOLD;

  // 页头标题：usage 行最后一个非空 title，缺省 lineage 前 24 字符；悬停显全量 lineage
  var title = '';
  requests.forEach(function (r) { if (r.title) title = r.title; });
  var titleEl = document.getElementById('tl-title');
  titleEl.textContent = title || lineage.slice(0, 24);
  titleEl.title = lineage;

  // ---- 时间标尺：全部元素（柱/事件/窗口两端）的 min/max，两侧各扩 3% ----
  var tMin = Infinity, tMax = -Infinity;
  function eat(ts) {
    if (!isFinite(ts)) return;
    if (ts < tMin) tMin = ts;
    if (ts > tMax) tMax = ts;
  }
  requests.forEach(function (r) { eat(r.ts); });
  events.forEach(function (e) { eat(e.ts); });
  windows.forEach(function (w) { eat(w.opened_ts); eat(w.closed_ts); });
  if (!isFinite(tMin)) { tMin = 0; tMax = 1; } // 前面已挡全空，保险
  var rawRange = tMax - tMin;
  if (rawRange <= 0) { tMin -= 30; tMax += 30; rawRange = 60; } // 单点时间：前后各让 30s
  tMin -= rawRange * 0.03;
  tMax += rawRange * 0.03;

  // ---- token 标尺：柱分段合计的最大值 → 1/2/3/5 阶梯 ----
  var yMax = 0;
  requests.forEach(function (r) {
    var tot = r.input_tokens + r.cache_read_tokens + r.cache_creation_tokens + r.output_tokens;
    if (tot > yMax) yMax = tot;
  });
  yMax = niceCeil(yMax);

  // ---- 可变状态（draw() 按它全量重绘 SVG）----
  var state = {
    tMin: tMin,
    tMax: tMax,
    yMax: yMax,
    ttl: 600,     // TTL 存活阴影（控制条输入框可改，缺省 600s）
    cursor: tMax, // 回放游标：缺省拉满 = 显示全部
    selected: -1, // 选中的窗口下标（-1 = 无）
  };
  function xOf(ts) {
    return 40 + (ts - state.tMin) / (state.tMax - state.tMin) * 1120;
  }
  function yOf(v) {
    return 460 - v / state.yMax * 390;
  }

  // 柱几何只依赖时间分布，加载时算一次：
  // 宽 = max(2px, 相邻柱间距 60%)；封顶 20px（孤立柱按字面会算出几百 px 宽，视觉失效）
  var xs = requests.map(function (r) { return xOf(r.ts); });
  state.bars = requests.map(function (_, i) {
    var gapL = i > 0 ? xs[i] - xs[i - 1] : Infinity;
    var gapR = i < xs.length - 1 ? xs[i + 1] - xs[i] : Infinity;
    var sp = Math.min(gapL, gapR);
    if (!isFinite(sp)) sp = 24; // 仅一根柱
    var w = Math.max(2, sp * 0.6);
    if (w > 20) w = 20;
    return { x: xs[i], w: w };
  });
  // TTL 刷新源 = request + beat
  state.refreshes = [];
  requests.forEach(function (r) { state.refreshes.push(r.ts); });
  events.forEach(function (e) { if (e.kind === 'beat') state.refreshes.push(e.ts); });
  state.refreshes.sort(function (a, b) { return a - b; });

  // ---- 元素引用 ----
  var svg = document.getElementById('tl-svg');
  var wrap = document.getElementById('tl-wrap');
  var tip = document.getElementById('tl-tooltip');
  var tipTime = tip.querySelector('.tip-time');
  var tipModel = tip.querySelector('.tip-model');
  var tipIn = tip.querySelector('.v-in');
  var tipCr = tip.querySelector('.v-cr');
  var tipCc = tip.querySelector('.v-cc');
  var tipOut = tip.querySelector('.v-out');
  var tipTitle = tip.querySelector('.tip-title');
  var cumEl = document.getElementById('tl-cum');
  var wininfo = document.getElementById('tl-wininfo');
  var slider = document.getElementById('tl-cursor');
  var ttlInput = document.getElementById('tl-ttl');
  var playBtn = document.getElementById('tl-play');

  slider.min = String(state.tMin);
  slider.max = String(state.tMax);
  slider.step = String((state.tMax - state.tMin) / 500);
  slider.value = String(state.cursor);

  // seg 追加一段矩形（h<=0 跳过）
  function seg(parent, x, y, w, h, color) {
    if (h <= 0) return;
    parent.appendChild(svgEl('rect', { x: x, y: y, width: w, height: h, fill: color }));
  }

  // ---- 窗口信息卡（右上角，点窗口带弹出）----
  function fillWinInfo(win, wi) {
    wininfo.textContent = '';
    var head = document.createElement('div');
    head.className = 'win-head';
    var h = document.createElement('span');
    h.textContent = '窗口 #' + (wi + 1) + ' / ' + windows.length;
    var close = document.createElement('button');
    close.type = 'button';
    close.className = 'win-close';
    close.textContent = '×';
    close.setAttribute('aria-label', '关闭窗口信息卡');
    close.addEventListener('click', function () { selectWindow(wi); }); // 再点一次取消选中
    head.appendChild(h);
    head.appendChild(close);
    wininfo.appendChild(head);

    function row(k, v) {
      var d = document.createElement('div');
      d.className = 'win-row';
      var kEl = document.createElement('span');
      kEl.className = 'win-k';
      kEl.textContent = k;
      var vEl = document.createElement('span');
      vEl.className = 'win-v';
      vEl.textContent = v;
      d.appendChild(kEl);
      d.appendChild(vEl);
      wininfo.appendChild(d);
    }
    row('打开', fmtTS(win.opened_ts));
    row('关闭', fmtTS(win.closed_ts));
    row('时长', fmtDur(win.dur_s) + 's');
    row('前缀 tokens', fmtK(win.prefix_tokens || 0));
    row('关闭原因', win.close_reason || '-');

    var btnRow = document.createElement('div');
    btnRow.className = 'win-btns';
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'win-backtest';
    btn.textContent = '反跑此窗口';
    btn.disabled = true; // Task 6 接真逻辑，先占位
    btn.setAttribute('title', '反跑仿真将在 Task 6 接入');
    var note = document.createElement('span');
    note.className = 'win-note';
    note.textContent = '（Task 6 接入反跑）';
    btnRow.appendChild(btn);
    btnRow.appendChild(note);
    wininfo.appendChild(btnRow);
  }

  function selectWindow(wi) {
    state.selected = state.selected === wi ? -1 : wi; // 同一带再点 = 取消选中
    if (state.selected >= 0) {
      fillWinInfo(windows[wi], wi);
      wininfo.hidden = false;
    } else {
      wininfo.hidden = true;
    }
    draw();
  }

  // ---- 主重绘：每次全量重建 SVG 子节点（窗口/TTL/柱/事件/游标/轴/累计）----
  function draw() {
    svg.textContent = '';

    // 窗口竖带（背景层，可点击选中；标签不挡点击）
    var gWin = svgEl('g');
    windows.forEach(function (win, wi) {
      var x1 = xOf(win.opened_ts);
      var x2 = xOf(win.closed_ts);
      if (x2 < x1) { var tmp = x1; x1 = x2; x2 = tmp; }
      var w = Math.max(x2 - x1, 1);
      var attrs = { x: x1, y: 60, width: w, height: 400, fill: 'rgba(255,255,255,0.06)' };
      if (wi === state.selected) {
        attrs.stroke = '#64b5f6';
        attrs['stroke-width'] = '1.5';
      }
      var rect = svgEl('rect', attrs);
      rect.setAttribute('cursor', 'pointer');
      svgTitleEl(rect, '窗口：等待 ' + fmtDur(win.dur_s) + 's（' + (win.close_reason || '未知原因') + '）——点击查看参数');
      rect.addEventListener('click', function () { selectWindow(wi); });
      gWin.appendChild(rect);
      var label = svgEl('text', {
        x: x1 + w / 2, y: 72, 'text-anchor': 'middle',
        'font-size': '10', fill: '#999', 'pointer-events': 'none',
      });
      label.textContent = '等待 ' + fmtDur(win.dur_s) + 's（' + (win.close_reason || '?') + '）';
      gWin.appendChild(label);
    });
    svg.appendChild(gWin);

    // TTL 存活阴影：每个 request+beat 一条 y=40..58 横条，重叠自然加深；
    // 游标左侧才画；TTL<=0 或非法时不画
    var gSh = svgEl('g', { 'pointer-events': 'none' });
    state.refreshes.forEach(function (ts) {
      if (ts > state.cursor) return;
      var x1 = xOf(ts);
      var x2 = xOf(ts + state.ttl);
      if (x2 <= x1) return;
      if (x2 > 1160) x2 = 1160; // 右缘裁进绘图区
      gSh.appendChild(svgEl('rect', { x: x1, y: 40, width: x2 - x1, height: 18, fill: 'rgba(76,175,80,0.10)' }));
    });
    svg.appendChild(gSh);

    // token 柱：三段堆叠自底向上 绿 cache_read / 红 input+creation / 蓝 output
    var gBars = svgEl('g', { 'pointer-events': 'none' }); // 悬停明细由 svg 级 mousemove 统一接管
    requests.forEach(function (r, i) {
      if (r.ts > state.cursor) return;
      var b = state.bars[i];
      var x = b.x - b.w / 2;
      var cr = r.cache_read_tokens;
      var red = r.input_tokens + r.cache_creation_tokens;
      var out = r.output_tokens;
      var yG = yOf(cr), yR = yOf(cr + red), yB = yOf(cr + red + out);
      seg(gBars, x, yG, b.w, 460 - yG, '#4caf50');
      seg(gBars, x, yR, b.w, yG - yR, '#e57373');
      seg(gBars, x, yB, b.w, yR - yB, '#64b5f6');
    });
    svg.appendChild(gBars);

    // 事件行 y=24 基线，符号按 kind；悬停用 SVG <title>
    var gEv = svgEl('g');
    events.forEach(function (e) {
      if (e.ts > state.cursor) return;
      var x = xOf(e.ts);
      var g;
      switch (e.kind) {
        case 'beat':
          g = svgEl('circle', { cx: x, cy: 24, r: 4, fill: '#4caf50' });
          svgTitleEl(g, 'beat 命中=' + e.hit + ' 实收=' + (e.cache_read || 0) + ' 预测成本=' + fmtCost(e.cost_pred));
          break;
        case 'handoff':
          g = svgEl('g'); // 向下小旗：杆 + 倒三角旗面
          g.appendChild(svgEl('line', { x1: x, y1: 14, x2: x, y2: 32, stroke: '#ffb74d', 'stroke-width': '1.5' }));
          g.appendChild(svgEl('polygon', {
            points: x + ',14 ' + (x + 9) + ',14 ' + (x + 4.5) + ',21',
            fill: '#ffb74d',
          }));
          svgTitleEl(g, 'handoff ' + (e.provider || '?') + ' ' + (e.outcome || ''));
          break;
        case 'block':
          g = svgEl('g', { stroke: '#e57373', 'stroke-width': '2' }); // 红叉
          g.appendChild(svgEl('line', { x1: x - 4, y1: 20, x2: x + 4, y2: 28 }));
          g.appendChild(svgEl('line', { x1: x - 4, y1: 28, x2: x + 4, y2: 20 }));
          svgTitleEl(g, 'block 空闲=' + fmtDur(e.idle_s || 0) + 's');
          break;
        case 'inject':
          g = svgEl('g', { stroke: '#4dd0e1', 'stroke-width': '2', fill: 'none' }); // 上箭头
          g.appendChild(svgEl('line', { x1: x, y1: 30, x2: x, y2: 18 }));
          g.appendChild(svgEl('polyline', { points: (x - 4) + ',22 ' + x + ',17 ' + (x + 4) + ',22' }));
          svgTitleEl(g, 'inject tokens=' + (e.tokens || 0) + ' handoff=' + (e.handoff_id || '-'));
          break;
        case 'bypass':
          g = svgEl('circle', { cx: x, cy: 24, r: 4, fill: 'none', stroke: '#888', 'stroke-width': '1.5' }); // 灰空心点
          svgTitleEl(g, 'bypass 前缀=' + (e.prefix_tokens || 0));
          break;
        default:
          g = svgEl('circle', { cx: x, cy: 24, r: 3, fill: '#666' }); // 未知 kind 容错
          svgTitleEl(g, e.kind || '未知事件');
      }
      gEv.appendChild(g);
    });
    svg.appendChild(gEv);

    // 回放游标竖线
    var cx = xOf(state.cursor);
    svg.appendChild(svgEl('line', {
      x1: cx, y1: 12, x2: cx, y2: 460,
      stroke: 'rgba(255,255,255,0.25)', 'stroke-width': '1',
    }));

    // x 轴：基线 + 6 等分 7 刻度，标签 MM-dd HH:mm
    var gAx = svgEl('g', { 'pointer-events': 'none' });
    gAx.appendChild(svgEl('line', { x1: 40, y1: 460, x2: 1160, y2: 460, stroke: '#333' }));
    gAx.appendChild(svgEl('line', { x1: 40, y1: 36, x2: 1160, y2: 36, stroke: '#222' }));
    for (var i = 0; i <= 6; i++) {
      var ts = state.tMin + (state.tMax - state.tMin) * i / 6;
      var tx = xOf(ts);
      gAx.appendChild(svgEl('line', { x1: tx, y1: 460, x2: tx, y2: 466, stroke: '#444' }));
      var lab = svgEl('text', { x: tx, y: 482, 'text-anchor': 'middle', 'font-size': '11', fill: '#888' });
      lab.textContent = fmtTS(ts);
      gAx.appendChild(lab);
    }
    svg.appendChild(gAx);

    // 右上角实时累计（游标左侧）
    var cnt = 0, sum = 0;
    requests.forEach(function (r) {
      if (r.ts > state.cursor) return;
      cnt++;
      sum += r.input_tokens + r.cache_read_tokens + r.cache_creation_tokens + r.output_tokens;
    });
    cumEl.textContent = '已看 ' + cnt + ' / ' + requests.length + ' 次请求 · tokens 合计 ' + fmtK(sum);
    cumEl.setAttribute('title', 'tokens 合计 = input + cache_read + cache_creation + output');
  }

  // ---- 柱悬停明细：svg 级 mousemove + 二分最近柱（2px 细柱也有足够命中宽度）----
  function hideTip() { tip.hidden = true; }
  svg.addEventListener('mousemove', function (ev) {
    var wr = wrap.getBoundingClientRect();
    if (!wr.width) return;
    var sx = (ev.clientX - wr.left) * (1200 / wr.width);
    var i = nearestIdx(xs, sx);
    if (i < 0) { hideTip(); return; }
    var r = requests[i];
    if (r.ts > state.cursor) { hideTip(); return; }
    var half = Math.max(state.bars[i].w, 8) / 2 + 3;
    if (Math.abs(xs[i] - sx) > half) { hideTip(); return; }
    tipTime.textContent = fmtTS(r.ts);
    tipModel.textContent = r.model || '-';
    tipIn.textContent = fmtK(r.input_tokens);
    tipCr.textContent = fmtK(r.cache_read_tokens);
    tipCc.textContent = fmtK(r.cache_creation_tokens);
    tipOut.textContent = fmtK(r.output_tokens);
    tipTitle.textContent = r.title || '';
    tipTitle.hidden = !r.title;
    tip.hidden = false;
    var left = ev.clientX - wr.left + 14;
    var top = ev.clientY - wr.top + 12;
    var tw = tip.offsetWidth, th = tip.offsetHeight;
    if (left + tw > wr.width - 6) left = ev.clientX - wr.left - tw - 14; // 右缘翻转
    if (top + th > wr.height - 6) top = ev.clientY - wr.top - th - 12;   // 下缘翻转
    if (left < 0) left = 0;
    if (top < 0) top = 0;
    tip.style.left = left + 'px';
    tip.style.top = top + 'px';
  });
  svg.addEventListener('mouseleave', hideTip);

  // ---- 控制条交互 ----
  slider.addEventListener('input', function () {
    state.cursor = parseFloat(slider.value);
    draw();
  });
  ttlInput.addEventListener('input', function () {
    var v = parseFloat(ttlInput.value);
    state.ttl = isFinite(v) && v > 0 ? v : 0; // 非法/0 → 不画阴影
    draw();
  });
  playBtn.addEventListener('click', function () {
    if (tlPlayTimer) {
      stopTlPlay();
      playBtn.textContent = '▶';
      return;
    }
    playBtn.textContent = '⏸';
    var seqAtPlay = navSeq; // 捕获当前导航序号；切页后 tick 自毁（buildTimelinePage 无 seq 形参，直接读全局）
    var stepv = (state.tMax - state.tMin) / 500 * 2; // 每 tick 2 步，全程约 4s
    tlPlayTimer = setInterval(function () {
      if (seqAtPlay !== navSeq) { stopTlPlay(); return; } // 已切页：定时器自毁
      var c = state.cursor + stepv;
      if (c >= state.tMax) {
        c = state.tMax;
        stopTlPlay();
        playBtn.textContent = '▶';
      }
      state.cursor = c;
      slider.value = String(c);
      draw();
    }, 16);
  });

  draw();
}

// ---------- 路由 ----------

// route 按 hash 前缀分发：#/t/<lineage> → renderTimeline；其余（#/ 或空）→ renderList。
// 畸形 % 序列（如手输 #/t/%zz）decode 抛 URIError → 回退原样串，时序页以"暂无数据"兜底。
function route() {
  var h = location.hash || '#/';
  var m = h.match(/^#\/t\/(.+)$/);
  if (!m) {
    renderList();
    return;
  }
  var lin;
  try {
    lin = decodeURIComponent(m[1]);
  } catch (_) {
    lin = m[1];
  }
  renderTimeline(lin);
}

window.onhashchange = route;
route(); // script 在 body 末尾，DOM 已就绪，直接首渲染
