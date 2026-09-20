// Ferryman 查看器前端——原生 JS，无框架无构建无外部资源（离线铁律）。
// hash 路由：#/ = 会话列表；#/t/<lineage> = 单会话时序图（SVG 手绘）。
// DOM 纪律：账本数据一律 createElement(SVG 用 createElementNS)/textContent/setAttribute
// 进 DOM，不拼 innerHTML（唯一例外：零插值的静态骨架串）。
'use strict';

// ---------- 工具 ----------

// esc HTML 转义：标题/项目/lineage 来自账本数据，进 innerHTML 前一律过这里。
function esc(s) {
  return String(s).replace(/[&<>"']/g, function (c) {
    return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
  });
}

// fmtTS unix 秒 → 本地 MM-dd HH:mm（withSec 追加 :ss）；0 或非法值显示 -。
function fmtTS(ts, withSec) {
  if (!ts) return '-';
  var d = new Date(ts * 1000);
  if (isNaN(d.getTime())) return '-';
  function p(n) { return String(n).padStart(2, '0'); }
  var s = p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
  return withSec ? s + ':' + p(d.getSeconds()) : s;
}

// fmtMD unix 秒 → 本地 MM-DD（票02 兜底链的"月日"=族系最后活动时间，与列表
// 按最后活动倒序的语义对齐）；0 或非法值返回空串（兜底链里省略该段）。
function fmtMD(ts) {
  if (!ts) return '';
  var d = new Date(ts * 1000);
  if (isNaN(d.getTime())) return '';
  function p(n) { return String(n).padStart(2, '0'); }
  return p(d.getMonth() + 1) + '-' + p(d.getDate());
}

// pathTail 路径串按 / 与 \ 切分取末段（空串安全）；去 .jsonl 扩展名。
function pathTail(p) {
  var segs = String(p || '').split(/[\/\\]/).filter(Boolean);
  var tail = segs.length ? segs[segs.length - 1] : String(p || '');
  return tail.replace(/\.jsonl$/i, '');
}

// listTitle 列表页标题兜底链（票02）：ai-title → 「项目尾段 · agent · 月日」
// → project 也缺时「agent · 月日 · <会话号前8>」（会话号=lineage 尾段文件名
// 前 8 位）。project/agents/月日缺哪段省哪段；全缺回退 lineage 前 24 字符
// （现状行为）。修复"无 ai-title 会话显示转录路径前 24 字符"不可辨认问题。
function listTitle(s) {
  if (s.title) return s.title;
  var agent = (s.agents || '').split(',')[0].trim();
  var md = fmtMD(s.last_ts);
  var parts = [];
  if (s.project) {
    var segs = String(s.project).split(/[\/\\]/).filter(Boolean);
    if (segs.length) parts.push(segs[segs.length - 1]);
  }
  if (agent) parts.push(agent);
  if (md) parts.push(md);
  if (!s.project) {
    var u8 = pathTail(s.lineage_id).slice(0, 8);
    if (u8) parts.push(u8);
  }
  if (parts.length) return parts.join(' · ');
  return s.lineage_id ? s.lineage_id.slice(0, 24) : '(无 lineage)';
}

// fmtK token 数：>=1w 用 x.xw（1.2w=12000），否则千分位。
function fmtK(n) {
  if (n >= 10000) return (n / 10000).toFixed(1).replace(/\.0$/, '') + 'w';
  return Math.round(n).toLocaleString('en-US');
}

// fmtCost 成本（积分）：<0.001 用科学计数防一串 0；≥100 取整，否则 1 位小数。
function fmtCost(v) {
  if (!isFinite(v)) return '-';
  if (v !== 0 && Math.abs(v) < 0.001) return v.toExponential(2);
  if (Math.abs(v) >= 100) return Math.round(v).toLocaleString('en-US');
  return String(Math.round(v * 10) / 10);
}

// fmtDur 秒 → 人话时长（X 秒 / X 分 Y 秒 / X 时 Y 分）。
function fmtDur(s) {
  s = Math.round(s);
  if (s < 60) return s + ' 秒';
  if (s < 3600) return Math.floor(s / 60) + ' 分 ' + (s % 60 ? s % 60 + ' 秒' : '').trim();
  return Math.floor(s / 3600) + ' 时 ' + Math.round((s % 3600) / 60) + ' 分';
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

// fadeIn 页面内容淡入（150ms 纯 opacity）：各 render 成功收尾时调一次；
// remove+强制回流+add 保证连续导航可重播；降动效由 CSS media query 关动画。
function fadeIn(el) {
  el.classList.remove('fade-in');
  void el.offsetWidth; // 强制回流重启动画
  el.classList.add('fade-in');
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

// btSeq 反跑请求序号：与 navSeq 双守卫——切页（navSeq 变）弃响应，连点两次（btSeq 变）弃旧响应。
var btSeq = 0;

// BT_FIELDS 反跑表单可编辑参数：预填 GLM 口径（config [prices.glm] v2026-09-17 + 实测 TTL，
// 见 docs/20260917_1630 实验报告 §4）。opened_ts/closed_ts/prefix_tokens 不进表单——
// 随选中 window 行只读带出。
var BT_FIELDS = [
  { key: 'p_in', label: 'p_in 输入价', def: 6.9 },
  { key: 'p_cache', label: 'p_cache 缓存读价', def: 1.7 },
  { key: 'p_out', label: 'p_out 输出价', def: 24 },
  { key: 'per', label: 'per 计价块', def: 10000 },
  { key: 'ttl_s', label: 'ttl_s 实测TTL(秒)', def: 600 },
  { key: 'safety', label: 'safety τ系数', def: 0.8 },
  { key: 'beat_out_tokens', label: 'beat_out_tokens', def: 300 },
  { key: 'max_wait_s', label: 'max_wait_s 0=auto', def: 0 },
];

// PRICES 时序页积分估算口径：与 config.toml [prices.glm] v2026-09-17 一致
//（p_in 6.9 / p_cache 1.7 / p_out 24，每 1w token，单位 智谱积分）。账本 99% 为
// glm-5.x，单一口径成立；个别非 glm 模型行在明细里标注"价目未配，按 GLM 估"。
var PIN = 6.9, PC = 1.7, PO = 24, PER = 10000;

// ---------- 列表页 ----------

// renderList GET /api/sessions → 会话表格（后端已按最后活动倒序）。
// 列：标题（兜底链 listTitle：项目尾段·agent·月日 → agent·月日·会话号前8）、
// 项目目录、开始时间、最后活动、请求数、tokens 主/子内联拆分（不含 output）、
// 窗口数、心跳数、交接数。行点击进入该 lineage 的时序页。
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
    var title = listTitle(s);
    // tokens 主/子内联拆分（票02）：口径不动=输入+缓存读+建缓存（不含 output）；
    // 子=0 只显主数，子>0 显「主+子」。防御：响应缺新键时回退既有合计。
    var mainTok = s.main_tokens, subTok = s.sub_tokens;
    if (mainTok == null && subTok == null) {
      mainTok = (s.input || 0) + (s.cache_read || 0) + (s.cache_creation || 0);
      subTok = 0;
    }
    mainTok = mainTok || 0; subTok = subTok || 0;
    var tokens = subTok > 0 ? fmtK(mainTok) + '+' + fmtK(subTok) : fmtK(mainTok);
    return '<tr data-lineage="' + esc(s.lineage_id) + '">' +
      '<td class="cell-title">' + esc(title) + '</td>' +
      '<td class="cell-project">' + esc(s.project || '-') + '</td>' +
      '<td>' + fmtTS(s.first_ts) + '</td>' +
      '<td>' + fmtTS(s.last_ts) + '</td>' +
      '<td class="num">' + (s.requests || 0) + '</td>' +
      '<td class="num" title="主 ' + fmtK(mainTok) + ' · 子 ' + fmtK(subTok) + '（口径=输入+缓存读+建缓存，不含输出）">' + tokens + '</td>' +
      '<td class="num">' + (s.windows || 0) + '</td>' +
      '<td class="num">' + (s.beats || 0) + '</td>' +
      '<td class="num">' + (s.handoffs || 0) + '</td>' +
      '</tr>';
  }).join('');

  app.innerHTML =
    '<table id="session-table"><thead><tr>' +
    '<th>标题</th><th>项目目录</th><th>开始时间</th><th>最后活动</th>' +
    '<th class="num">请求数</th><th class="num" title="主+子拆分，口径=输入+缓存读+建缓存（不含输出）">tokens（主+子）</th>' +
    '<th class="num">窗口</th><th class="num">心跳</th><th class="num">交接</th>' +
    '</tr></thead><tbody>' +
    (rows || '<tr><td colspan="9" class="empty">暂无会话数据</td></tr>') +
    '</tbody></table>';
  fadeIn(app);

  app.querySelectorAll('tr[data-lineage]').forEach(function (tr) {
    tr.addEventListener('click', function () {
      location.hash = '#/t/' + encodeURIComponent(tr.dataset.lineage);
    });
  });
}

// ---------- 时序页 ----------

var SVG_NS = 'http://www.w3.org/2000/svg';

// svgEl 创建 SVG 元素并批量 setAttribute。
function svgEl(tag, attrs) {
  var el = document.createElementNS(SVG_NS, tag);
  if (attrs) {
    for (var k in attrs) el.setAttribute(k, attrs[k]);
  }
  return el;
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

// SUBCOLORS 子代理紫系色板（原型 D 变体）；subColorOf 按 stem 稳定取色
//（charCode 杂凑 → 色板下标：同一 agent 跨刷新/跨缩放同色，与出现序无关）。
var SUBCOLORS = ['#ba68c8', '#9575cd', '#7986cb'];
function subColorOf(stem) {
  var h = 0;
  for (var i = 0; i < stem.length; i++) h = (h * 31 + stem.charCodeAt(i)) >>> 0;
  return SUBCOLORS[h % SUBCOLORS.length];
}

// numOrDash 反跑结果数值兜底：非有限数显示 -（后端字段理论上恒为数，防畸形账本连带崩卡）。
function numOrDash(v, suffix) {
  return isFinite(v) ? String(Math.round(v * 10) / 10) + (suffix || '') : '-';
}

// 时序页静态骨架：零数据插值（账本数据全部经 createElement/textContent/setAttribute 进 DOM）。
// 布局：页头 → 保活计划卡 → 统计卡行 → 图例行 → 图区（SVG+详情面板）→
// 子代理甘特（票02，有子行才显示）→ 控制条 → 反跑结果卡。
var TL_SCAFFOLD =
  '<div id="tl-head">' +
  '<a href="#/">← 返回会话列表</a>' +
  '<span id="tl-title"></span>' +
  '</div>' +
  '<details id="tl-plan" open class="plan-card">' +
  '<summary>保活计划（策略计算器）</summary>' +
  '<div id="plan-body"></div>' +
  '</details>' +
  '<div id="tl-cards" class="tl-cards"></div>' +
  '<div id="tl-lg" class="tl-lg"></div>' +
  '<div class="tl-chart">' +
  '<div id="tl-wrap">' +
  '<svg id="tl-svg" viewBox="0 0 1200 560" preserveAspectRatio="xMidYMid meet" role="img" aria-label="单会话 token 时序图（上道主会话、下道子代理合计）"></svg>' +
  '<div id="tl-tooltip">' +
  '<div class="tip-time"></div>' +
  '<div class="tip-model"></div>' +
  '<div class="tip-rows"></div>' +
  '<div class="tip-cost"></div>' +
  '</div>' +
  '</div>' +
  '<div id="tl-detail"></div>' +
  '</div>' +
  '<div id="tl-gantt" hidden></div>' +
  '<div id="tl-ctrl">' +
  '<button id="tl-play" type="button" title="回放播放/暂停" aria-label="回放播放/暂停">▶</button>' +
  '<input id="tl-cursor" type="range" aria-label="回放游标">' +
  '<span id="tl-cum"></span>' +
  '<span id="tl-ctrl-hint">滚轮缩放 · 拖拽平移 · 双击复位 · 点柱/事件/窗口/斜纹区看详情</span>' +
  '</div>' +
  '<div id="tl-bt-result" hidden></div>';

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
  fadeIn(app);
}

// deriveReqs 每条 usage 派生花费字段（GLM 口径）与累计；并给行编全局下标 idx。
// sub=子代理标记（票01 字段：stem，主行空串）、subColor=紫系稳定取色（票02）。
function deriveReqs(requests) {
  var rs = requests.map(function (r) {
    var cr = r.cache_read_tokens || 0;
    var red = (r.input_tokens || 0) + (r.cache_creation_tokens || 0);
    var out = r.output_tokens || 0;
    var sub = r.subagent || '';
    return {
      ts: r.ts, m: r.model || '', t: r.title || '',
      in: r.input_tokens || 0, cr: cr,
      cc: r.cache_creation_tokens || 0, out: out,
      red: red,
      cCr: cr * PC / PER, cRed: red * PIN / PER, cOut: out * PO / PER,
      tokTot: cr + red + out,
      sub: sub, subColor: sub ? subColorOf(sub) : '',
    };
  });
  var cum = 0;
  rs.forEach(function (r, i) {
    r.idx = i;
    r.cTot = r.cCr + r.cRed + r.cOut;
    r.repaid = false;
    cum += r.cTot;
    r.cum = cum;
  });
  return rs;
}

// deriveZones 断缓存区：覆盖源 = 请求 ∪ 心跳（beat 也续命；observe 演练跳未真发
// 不算覆盖——T51 票04）；相邻覆盖源间隔 > TTL
// 且下一个是请求（不是 beat——beat 已把缓存焐热，无重付）→ 一段死亡区。
// 损失估算 = 下一条请求的新输入 × (全价 − 缓存价)。TTL 变更后须重算。
// 票02：调用方只喂主会话请求（mainReqs）——子代理请求走子代理自己的转录与
// 缓存，不参与主会话断缓存判定；TTL 绿带同理只由主请求∪心跳续命。
function deriveZones(reqs, events, ttl) {
  var cov = [];
  reqs.forEach(function (r) { cov.push({ ts: r.ts, req: r }); });
  events.forEach(function (e) { if (e.kind === 'beat' && e.outcome !== 'observe') cov.push({ ts: e.ts, beat: true }); });
  cov.sort(function (a, b) { return a.ts - b.ts; });
  var zones = [];
  for (var i = 1; i < cov.length; i++) {
    var gap = cov[i].ts - cov[i - 1].ts;
    if (gap > ttl && cov[i].req) {
      var nx = cov[i].req;
      zones.push({
        from: cov[i - 1].ts + ttl, to: cov[i].ts, gap: gap,
        reqIdx: nx.idx, loss: nx.in * (PIN - PC) / PER,
      });
      nx.repaid = true;
    }
  }
  return zones;
}

// 图区几何（viewBox 0 0 1200 560）：事件行 y=24 基线；TTL 存活带 y=46..62；
// 单道柱区 y=78..470；双道（票02 D 形态，draw 内现算）：主道 78..268、子道
// 296..470，窗口带/斜纹/游标/x 轴贯穿两道（底=470）；绘图区 x 46..1160。
var X0 = 46, X1 = 1160, YTOP = 78, YBOT = 470, BAND_Y = 46, BAND_H = 16, EV_Y = 24;

// buildTimelinePage 搭骨架 → 派生数据 → 统计卡/图例/详情面板 → 挂交互 → draw()。
function buildTimelinePage(app, lineage, requests, events, windows) {
  app.innerHTML = TL_SCAFFOLD;

  var reqs = deriveReqs(requests);

  // 主/子分道（票02 D 形态）：usage 行按 subagent 标记分组——主道语义（断缓存/
  // TTL/标题/计划卡 prefix）只吃 mainReqs；子代理请求进子道与甘特。reqs 保持
  // 全量（全局 idx/累计/统计卡的族系总账口径不变）。
  var mainReqs = reqs.filter(function (r) { return !r.sub; });
  var subReqs = reqs.filter(function (r) { return r.sub; });
  var hasSub = subReqs.length > 0;
  var subStems = []; // 子代理 stem 首现序（甘特行序）
  subReqs.forEach(function (r) {
    if (subStems.indexOf(r.sub) < 0) subStems.push(r.sub);
  });

  // 页头标题：主会话 usage 行最后一个非空 title（子行 title 不抢主会话标题），
  // 缺省 lineage 前 24 字符；悬停显全量 lineage
  var title = '';
  mainReqs.forEach(function (r) { if (r.t) title = r.t; });
  var titleEl = document.getElementById('tl-title');
  titleEl.textContent = title || lineage.slice(0, 24);
  titleEl.title = lineage;

  // ---- 可变状态 ----
  var state = {
    ttl: 600,      // TTL 存活秒数（图例行输入可改，改后断缓存区重算）
    view: 'token', // 'token' | 'cost' 柱高口径
    seg: { cr: true, red: true, out: true }, // 图例科目开关
    showSub: true, // 子代理泳道+甘特显隐（"子代理请求" chip）
    selAg: null,   // 甘特选中的子代理 stem（两道同色参考线；null=无）
    t0: 0, t1: 0,           // 当前可视时间窗（缩放/平移改变）
    full0: 0, full1: 0,     // 全时间范围（复位用）
    cursor: 0,              // 回放游标（缺省拉满 = 显示全部）
    sel: null,              // 选中 {type:'req'|'ev'|'win'|'zone', i}
    zones: [],              // 断缓存区（随 TTL 重算）
  };

  // 时间轴全范围：全部元素 min/max，两侧各扩 3%
  var tMin = Infinity, tMax = -Infinity;
  function eat(ts) {
    if (!isFinite(ts)) return;
    if (ts < tMin) tMin = ts;
    if (ts > tMax) tMax = ts;
  }
  reqs.forEach(function (r) { eat(r.ts); });
  events.forEach(function (e) { eat(e.ts); });
  windows.forEach(function (w) { eat(w.opened_ts); eat(w.closed_ts); });
  if (!isFinite(tMin)) { tMin = 0; tMax = 1; }
  var rawRange = tMax - tMin;
  if (rawRange <= 0) { tMin -= 30; tMax += 30; rawRange = 60; }
  state.full0 = tMin - rawRange * 0.03;
  state.full1 = tMax + rawRange * 0.03;
  state.t0 = state.full0;
  state.t1 = state.full1;
  state.cursor = state.full1;
  recomputeZones();

  function recomputeZones() { state.zones = deriveZones(mainReqs, events, state.ttl); }

  // ---- 视窗补间（plans/002）：跳转类视窗变化 300ms ease-in-out 过渡 ----
  // 可打断（新跳转/拖拽/滚轮即停）；navSeq 变（切页）自毁；降动效偏好下瞬跳。
  // 曲线 = CSS token --ease-in-out 同一条 cubic-bezier(0.77,0,0.175,1)，JS 侧求解复刻。
  var viewTween = null;
  function stopViewTween() {
    if (viewTween) { cancelAnimationFrame(viewTween.raf); viewTween = null; }
  }
  // cubicBezier 贝塞尔求解（牛顿法 8 轮足够收敛）：t∈[0,1] 的缓动值。
  function cubicBezier(x1, y1, x2, y2) {
    function bx(t) { return 3 * (1 - t) * (1 - t) * t * x1 + 3 * (1 - t) * t * t * x2 + t * t * t; }
    function by(t) { return 3 * (1 - t) * (1 - t) * t * y1 + 3 * (1 - t) * t * t * y2 + t * t * t; }
    return function (x) {
      var t = x;
      for (var i = 0; i < 8; i++) {
        var e = bx(t) - x;
        if (Math.abs(e) < 1e-5) break;
        var d = 3 * (1 - t) * (1 - t) * x1 + 6 * (1 - t) * t * (x2 - x1) + 3 * t * t * (1 - x2);
        if (Math.abs(d) < 1e-6) break;
        t -= e / d;
      }
      return by(Math.max(0, Math.min(1, t)));
    };
  }
  var easeInOut = cubicBezier(0.77, 0, 0.175, 1);
  function tweenView(to0, to1) {
    stopViewTween();
    var rm = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    if (rm || (Math.abs(to0 - state.t0) < 1 && Math.abs(to1 - state.t1) < 1)) {
      state.t0 = to0; state.t1 = to1; clampView(); draw(); return;
    }
    var seqAtTween = navSeq; // 切页守卫：与回放定时器同款自毁
    var from0 = state.t0, from1 = state.t1, start = performance.now(), DUR = 300;
    function step(now) {
      if (seqAtTween !== navSeq) { viewTween = null; return; }
      var p = Math.min(1, (now - start) / DUR);
      var e = easeInOut(p);
      state.t0 = from0 + (to0 - from0) * e;
      state.t1 = from1 + (to1 - from1) * e;
      clampView();
      draw();
      if (p < 1) viewTween.raf = requestAnimationFrame(step);
      else { state.t0 = to0; state.t1 = to1; clampView(); draw(); viewTween = null; } // 末帧钉死目标值
    }
    viewTween = { raf: requestAnimationFrame(step) };
  }

  // ---- 元素引用 ----
  var svg = document.getElementById('tl-svg');
  var wrap = document.getElementById('tl-wrap');
  var tip = document.getElementById('tl-tooltip');
  var tipTime = tip.querySelector('.tip-time');
  var tipModel = tip.querySelector('.tip-model');
  var tipRows = tip.querySelector('.tip-rows');
  var tipCost = tip.querySelector('.tip-cost');
  var cumEl = document.getElementById('tl-cum');
  var cardsEl = document.getElementById('tl-cards');
  var lgEl = document.getElementById('tl-lg');
  var detail = document.getElementById('tl-detail');
  var btResult = document.getElementById('tl-bt-result');
  var slider = document.getElementById('tl-cursor');
  var playBtn = document.getElementById('tl-play');

  slider.min = String(state.full0);
  slider.max = String(state.full1);
  slider.step = String((state.full1 - state.full0) / 500);
  slider.value = String(state.cursor);

  // 保活计划卡（策略计算器）：独立于图区状态，只依赖主会话 requests 与 lineage
  //（子代理请求的前缀与主会话缓存无关——prefix S 取主会话末次请求）
  buildPlanCard(requests.filter(function (r) { return !r.subagent; }));

  // ---- 坐标 ----
  function xOf(ts) {
    return X0 + (ts - state.t0) / (state.t1 - state.t0) * (X1 - X0);
  }
  function clampView() {
    var full = state.full1 - state.full0;
    var span = state.t1 - state.t0;
    if (span < 30) {
      var mid = (state.t0 + state.t1) / 2;
      state.t0 = mid - 15; state.t1 = mid + 15;
    }
    if (state.t1 - state.t0 > full) { state.t0 = state.full0; state.t1 = state.full1; }
    if (state.t0 < state.full0) { state.t1 += state.full0 - state.t0; state.t0 = state.full0; }
    if (state.t1 > state.full1) { state.t0 -= state.t1 - state.full1; state.t1 = state.full1; }
  }
  function yMax() {
    var mx = 0;
    mainReqs.forEach(function (r) {
      var v = state.view === 'cost' ? r.cTot : r.tokTot;
      if (v > mx) mx = v;
    });
    return niceCeil(mx);
  }
  // subYMax 子道独立 y 轴上限（票02 D 形态）：子代理请求自己的量纲，不与主道
  // 抢刻度（子柱矮小时仍可辨）。
  function subYMax() {
    var mx = 0;
    subReqs.forEach(function (r) {
      var v = state.view === 'cost' ? r.cTot : r.tokTot;
      if (v > mx) mx = v;
    });
    return niceCeil(mx);
  }

  // ---- tooltip：DOM 构建（不拼 HTML 串） ----
  function tipRow(k, v) {
    var d = document.createElement('div');
    d.className = 'tip-row';
    var a = document.createElement('span'); a.textContent = k;
    var b = document.createElement('span'); b.textContent = v;
    d.appendChild(a); d.appendChild(b);
    tipRows.appendChild(d);
  }
  function tipFill(title, model, rows, cost, costWarn) {
    tipTime.textContent = title;
    tipModel.textContent = model || '';
    tipModel.hidden = !model;
    tipRows.textContent = '';
    rows.forEach(function (r) { tipRow(r[0], r[1]); });
    tipCost.textContent = cost || '';
    tipCost.hidden = !cost;
    tipCost.className = costWarn ? 'tip-cost tip-warn' : 'tip-cost';
  }
  function attachTip(target, fillFn) {
    target.addEventListener('mousemove', function (ev) {
      fillFn();
      tip.classList.add('show');
      var wr = wrap.getBoundingClientRect();
      if (!wr.width) return;
      var left = ev.clientX - wr.left + 14;
      var top = ev.clientY - wr.top + 12;
      if (left + tip.offsetWidth > wr.width - 6) left = ev.clientX - wr.left - tip.offsetWidth - 14;
      if (top + tip.offsetHeight > wr.height - 6) top = ev.clientY - wr.top - tip.offsetHeight - 12;
      tip.style.left = Math.max(0, left) + 'px';
      tip.style.top = Math.max(0, top) + 'px';
    });
    target.addEventListener('mouseleave', function () { tip.classList.remove('show'); });
  }

  // 各类 tooltip 内容
  function tipReq(r) {
    var m = r.m && !/glm/i.test(r.m) ? r.m + '（价目未配，按 GLM 估）' : r.m;
    var gapPrev = r.idx > 0 ? r.ts - reqs[r.idx - 1].ts : 0;
    tipFill((r.sub ? '子代理 ' + r.sub : '请求 #' + (r.idx + 1)) + ' · ' + fmtTS(r.ts, true), m, [
      ['缓存读(绿)', fmtK(r.cr) + ' → ' + fmtCost(r.cCr) + ' 积分'],
      ['新输入+建缓存(红)', fmtK(r.red) + ' → ' + fmtCost(r.cRed) + ' 积分'],
      ['输出(蓝)', fmtK(r.out) + ' → ' + fmtCost(r.cOut) + ' 积分'],
      ['与上一条间隔', r.idx > 0 ? fmtDur(gapPrev) + (r.repaid ? '（缓存已断）' : gapPrev > state.ttl * 0.8 ? '（接近 TTL）' : '（缓存存活）') : '-'],
    ], '本条 ' + fmtCost(r.cTot) + ' 积分 · 累计 ' + fmtCost(r.cum) + ' 积分' + (r.repaid ? ' · ↯ 本条全额重付' : ''), r.repaid);
  }
  // beatOutcomeLabel 心跳三态＋演练（T51 票04：账目 hit 布尔已改 outcome 三态）
  function beatOutcomeLabel(o) {
    return { hit: '命中', miss: 'MISS', error: 'ERROR', observe: '演练（未真发）' }[o] || (o || '-');
  }
  function tipEv(e) {
    if (e.kind === 'handoff') {
      tipFill('⚑ 交接文档', null, [
        ['时间', fmtTS(e.ts, true)], ['生成方', e.provider || '-'], ['状态', e.outcome || '-'],
      ], '会话闲置后自动生成的交接稿，/clear 后新会话开场自动收到');
    } else if (e.kind === 'block') {
      tipFill('✕ 输入被拦截', null, [
        ['时间', fmtTS(e.ts, true)], ['已闲置', fmtDur(e.idle_s || 0)],
      ], '闸门拦下这条输入：原话已保管，/clear 开新会话自动带回（交接+原话）；或以「强续」开头强制继续', true);
    } else if (e.kind === 'inject') {
      tipFill('↑ 注入交接', null, [
        ['时间', fmtTS(e.ts, true)], ['tokens', fmtK(e.tokens || 0)],
      ], '新会话开场自动带入交接文档');
    } else if (e.kind === 'bypass') {
      tipFill('◯ 强续放行', null, [
        ['时间', fmtTS(e.ts, true)], ['前缀', fmtK(e.prefix_tokens || 0)],
      ], '以「强续」开头强制放行，不计拦截次数');
    } else if (e.kind === 'beat') {
      tipFill('● 心跳', null, [
        ['时间', fmtTS(e.ts, true)], ['结果', beatOutcomeLabel(e.outcome)],
        ['实收缓存读', fmtK(e.cache_read || 0)],
      ], '预测成本 ' + fmtCost(e.cost_pred) + ' / 实际 ' + fmtCost(e.cost_actual) + ' 积分');
    } else if (e.kind === 'qwatch_hit') {
      tipFill('◎ 问询命中', null, [
        ['时间', fmtTS(e.ts, true)], ['问题单元', String(e.unit_count || 0)],
        ['标记/问号/编号行', (e.marker_lines || 0) + ' / ' + (e.qmark_lines || 0) + ' / ' + (e.numbered_lines || 0)],
      ], 'AI 末条被判定为提问潮（等答复窗口的心跳保温由此触发）');
    } else {
      tipFill(e.kind || '未知事件', null, [['时间', fmtTS(e.ts, true)]]);
    }
  }
  function tipWin(w, wi) {
    tipFill('子代理等待窗口 #' + (wi + 1), null, [
      ['等待', fmtDur(w.dur_s)],
      ['结束原因', w.close_reason || '?'],
      ['当时前缀', fmtK(w.prefix_tokens || 0)],
    ], w.dur_s > state.ttl
      ? '超过 TTL：等待期间缓存已死，恢复要全款重付 ≈ ' + fmtCost((w.prefix_tokens || 0) * PIN / PER) + ' 积分'
      : '在 TTL 内：缓存存活，零损失', w.dur_s > state.ttl);
  }
  function tipZone(z) {
    var nx = reqs[z.reqIdx];
    tipFill('断缓存区', null, [
      ['时段', fmtTS(z.from) + ' ~ ' + fmtTS(z.to)],
      ['断了', fmtDur(z.gap - state.ttl)],
      ['下一条请求', '#' + (z.reqIdx + 1) + ' · 新输入 ' + fmtK(nx.in)],
    ], '这部分从 1.7 涨回 6.9 全价，多付约 ' + fmtCost(z.loss) + ' 积分（估算）', true);
  }

  // ---- 统计卡行 ----
  function mkCard(k, v, s, opt) {
    opt = opt || {};
    var d = document.createElement('div');
    d.className = 'card' + (opt.click ? ' clickable' : '');
    var ke = document.createElement('div'); ke.className = 'k'; ke.textContent = k;
    var ve = document.createElement('div');
    ve.className = 'v' + (opt.cls ? ' ' + opt.cls : '');
    ve.textContent = v;
    var se = document.createElement('div'); se.className = 's'; se.textContent = s;
    d.appendChild(ke); d.appendChild(ve); d.appendChild(se);
    if (opt.title) d.title = opt.title;
    if (opt.click) d.addEventListener('click', opt.click);
    return d;
  }

  function sums() {
    var n = reqs.length, tIn = 0, tCr = 0, tCc = 0, tOut = 0, cost = 0;
    reqs.forEach(function (r) {
      tIn += r.in; tCr += r.cr; tCc += r.cc; tOut += r.out; cost += r.cTot;
    });
    return {
      n: n, tokens: tIn + tCr + tCc + tOut, cost: cost,
      hitRate: tCr / Math.max(1, tIn + tCr + tCc),
      deadN: state.zones.length,
      deadLoss: state.zones.reduce(function (s, z) { return s + z.loss; }, 0),
      saved: tCr * (PIN - PC) / PER,
      t0: n ? reqs[0].ts : 0, t1: n ? reqs[n - 1].ts : 0,
    };
  }

  var deadHop = 0;
  function jumpDead() {
    if (!state.zones.length) return;
    var z = state.zones[deadHop++ % state.zones.length];
    var mid = (z.from + z.to) / 2;
    var span = Math.max(600, (z.to - z.from) * 3);
    tweenView(mid - span / 2, mid + span / 2);
    pick({ type: 'zone', i: state.zones.indexOf(z) });
  }

  function renderCards() {
    var m = sums();
    cardsEl.textContent = '';
    cardsEl.appendChild(mkCard('请求数', String(m.n),
      fmtTS(m.t0) + ' ~ ' + fmtTS(m.t1) + (hasSub ? ' · 含子代理 ' + subReqs.length + ' 条' : '')));
    cardsEl.appendChild(mkCard('总 tokens', fmtK(m.tokens),
      '输入+缓存读+建缓存+输出'));
    cardsEl.appendChild(mkCard('总花费', fmtCost(m.cost) + ' 积分', '按 GLM 价目估算'));
    cardsEl.appendChild(mkCard('缓存命中率', (m.hitRate * 100).toFixed(1) + '%',
      '命中=走 1.7 的便宜价', { cls: 'good', title: '缓存读 tokens ÷ 输入侧总 tokens（新输入+缓存读+建缓存）' }));
    cardsEl.appendChild(mkCard('缓存净省', fmtCost(m.saved) + ' 积分',
      '读缓存比全价省下的钱', { cls: 'good', title: '缓存读 tokens × (6.9 − 1.7) ÷ 1万' }));
    cardsEl.appendChild(mkCard('断缓存次数', String(m.deadN),
      m.deadN ? '多付约 ' + fmtCost(m.deadLoss) + ' 积分' : '全程未断', {
        cls: m.deadN ? 'bad' : 'good',
        click: m.deadN ? jumpDead : null,
        title: '相邻请求间隔超过 TTL 即缓存死亡，下一条新输入从 1.7 涨回 6.9。点击依次跳到每个断点。',
      }));
  }

  // ---- 图例行：科目 chips + TTL 输入 + 视图切换 + 事件图例 ----
  var SEGS = [
    { key: 'cr', color: '#4caf50', name: '缓存读', human: '旧内容便宜价读回', price: '1.7/万' },
    { key: 'red', color: '#e57373', name: '新输入+建缓存', human: '新进模型的内容，全价', price: '6.9/万' },
    { key: 'out', color: '#64b5f6', name: '模型输出', human: '模型生成的回复，最贵', price: '24/万' },
  ];
  // 事件图例五件套：[kind, 符号, 颜色(与图上标记一致), 名字, 人话解释]。
  // 配合 renderLegend 的计数：0 次灰暗——一眼分清"没发生过"和"画丢了"。
  var EVLEG = [
    ['beat', '●', '#4caf50', '心跳', '保活心跳——问询守望逐跳落账（空心圈=observe 演练跳）'],
    ['handoff', '⚑', '#ffb74d', '交接', '会话闲置后自动生成交接文档'],
    ['block', '✕', '#e57373', '拦截', '闲置超时，输入被闸门拦下（原话已保管）'],
    ['inject', '↑', '#4dd0e1', '注入', '/clear 后新会话开场自动带入交接'],
    ['bypass', '◯', '#aaa', '强续', '以「强续」开头强制放行'],
  ];

  function renderLegend() {
    lgEl.textContent = '';
    SEGS.forEach(function (sg) {
      var c = document.createElement('span');
      c.className = 'chip' + (state.seg[sg.key] ? '' : ' off');
      var sw = document.createElement('i');
      sw.className = 'sw';
      sw.style.background = sg.color;
      c.appendChild(sw);
      c.appendChild(document.createTextNode(sg.name));
      var pr = document.createElement('span');
      pr.className = 'price';
      pr.textContent = sg.human + ' · ' + sg.price;
      c.appendChild(pr);
      c.title = '点击在图上隐藏/显示「' + sg.name + '」段';
      c.addEventListener('click', function () {
        state.seg[sg.key] = !state.seg[sg.key];
        renderLegend();
        draw();
      });
      lgEl.appendChild(c);
    });

    // 子代理请求 chip（票02）：控制子道+甘特显隐（沿用科目 chips 形态）；
    // 纯主会话/纯 codex 会话无子行 → 不渲染本 chip（布局不塌）。
    if (hasSub) {
      var subChip = document.createElement('span');
      subChip.className = 'chip' + (state.showSub ? '' : ' off');
      var subSw = document.createElement('i');
      subSw.className = 'sw';
      subSw.style.background = '#ba68c8';
      subChip.appendChild(subSw);
      subChip.appendChild(document.createTextNode('子代理请求' + (state.showSub ? '' : '（已隐藏）')));
      subChip.title = '点击显示/隐藏子代理泳道与下方甘特（上道主会话不受影响）';
      subChip.addEventListener('click', function () {
        state.showSub = !state.showSub;
        renderLegend();
        renderGantt();
        draw();
      });
      lgEl.appendChild(subChip);
    }

    // TTL 输入：改后存活带与断缓存区重算（统计卡同步）
    var ttlLabel = document.createElement('label');
    ttlLabel.className = 'chip';
    ttlLabel.appendChild(document.createTextNode('TTL '));
    var ttlInput = document.createElement('input');
    ttlInput.type = 'number';
    ttlInput.min = '0';
    ttlInput.step = '60';
    ttlInput.value = String(state.ttl);
    ttlInput.setAttribute('aria-label', 'TTL 缓存存活秒数');
    ttlInput.addEventListener('change', function () {
      var v = parseFloat(ttlInput.value);
      state.ttl = isFinite(v) && v > 0 ? v : 0;
      recomputeZones();
      renderLegend(); // 断缓存 chip 计数/损失随 TTL 重算
      renderCards();
      draw();
    });
    ttlLabel.appendChild(ttlInput);
    ttlLabel.appendChild(document.createTextNode(' 秒'));
    ttlLabel.title = '缓存存活时长（实测 600 秒）；改它，存活带与断缓存区重算';
    lgEl.appendChild(ttlLabel);

    // 视图切换：token 量 / 积分
    var segCtl = document.createElement('span');
    segCtl.className = 'segview';
    [['token', '按 token 量'], ['cost', '按积分(钱)']].forEach(function (kv) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = state.view === kv[0] ? 'on' : '';
      b.textContent = kv[1];
      b.addEventListener('click', function () {
        state.view = kv[0];
        renderLegend();
        draw();
      });
      segCtl.appendChild(b);
    });
    lgEl.appendChild(segCtl);

    // 断缓存 chip：斜纹小样与图上同款；计数与多付损失随 TTL 重算；可点循环跳断点
    var zn = state.zones.length;
    var zChip = document.createElement('span');
    zChip.className = 'chip chip-dead' + (zn ? '' : ' off');
    var zSw = document.createElement('i');
    zSw.className = 'sw sw-dead';
    zChip.appendChild(zSw);
    zChip.appendChild(document.createTextNode('断缓存' + (zn ? ' ×' + zn : ' 0')));
    if (zn) {
      var zp = document.createElement('span');
      zp.className = 'price';
      zp.textContent = '多付≈' + fmtCost(state.zones.reduce(function (s, z) { return s + z.loss; }, 0)) + ' 积分';
      zChip.appendChild(zp);
    }
    zChip.title = '斜纹区=相邻请求间隔超过 TTL，缓存死亡，下一条请求的新输入全价重付' +
      (zn ? '。点击循环跳到每个断点现场' : '——本会话全程未断，图上没有斜纹区');
    if (zn) zChip.addEventListener('click', jumpDead);
    lgEl.appendChild(zChip);

    // 事件图例：符号颜色=图上标记色；计数=本会话真实发生次数（0=灰暗）；
    // 有事件的种类可点，循环跳到每次发生现场并选中。
    var evs = document.createElement('span');
    evs.className = 'lg-ev';
    EVLEG.forEach(function (e) {
      var idxs = [];
      events.forEach(function (ev, i) { if (ev.kind === e[0]) idxs.push(i); });
      var s = document.createElement('span');
      s.className = 'ev-item' + (idxs.length ? '' : ' ev-zero');
      var sym = document.createElement('i');
      sym.className = 'ev-sym';
      sym.style.color = e[2];
      sym.textContent = e[1];
      s.appendChild(sym);
      s.appendChild(document.createTextNode(e[3] + ' ×' + idxs.length));
      s.title = e[4] + (idxs.length
        ? '——标记在图顶部事件行，点击跳到现场'
        : '——本会话没发生过，图上不会有标记');
      if (idxs.length) {
        var hop = 0;
        s.style.cursor = 'pointer';
        s.addEventListener('click', function () {
          var ei = idxs[hop++ % idxs.length];
          var t = events[ei].ts;
          var span = Math.max(600, (state.full1 - state.full0) * 0.1);
          tweenView(t - span / 2, t + span / 2);
          state.sel = { type: 'ev', i: ei }; // 直选不 toggle：循环跳转逐次定位
          btResult.hidden = true;
          renderDetail();
          draw();
        });
      }
      evs.appendChild(s);
    });
    lgEl.appendChild(evs);
  }

  // ---- 子代理甘特（票02 D 形态）：每 agent 一行 ----
  // 条=首末请求跨度、竖刻点=每次请求、尾标 tokens/时长/请求数；点击行在上方
  // 两道打该 agent 首末请求的同色参考线（再点取消）。横轴百分比对齐图区全时间
  // 范围（full0..full1）——与泳道共用同一时间轴。无子行或 chip 隐藏时整区不渲染。
  function renderGantt() {
    var gEl = document.getElementById('tl-gantt');
    if (!gEl) return;
    gEl.textContent = '';
    if (!hasSub || !state.showSub) {
      gEl.hidden = true;
      return;
    }
    gEl.hidden = false;
    var headEl = document.createElement('div');
    headEl.className = 'g-title';
    headEl.textContent = '子代理甘特 · 每 agent 一行（条=首末请求跨度 · 刻点=每次请求 · 点击行在上方两道打同色参考线）';
    gEl.appendChild(headEl);
    var full = state.full1 - state.full0;
    function pct(t) { return Math.max(0, Math.min(100, (t - state.full0) / full * 100)); }
    subStems.forEach(function (ag) {
      var rs = subReqs.filter(function (r) { return r.sub === ag; });
      var lo = Infinity, hi = -Infinity, tk = 0;
      rs.forEach(function (r) {
        if (r.ts < lo) lo = r.ts;
        if (r.ts > hi) hi = r.ts;
        tk += r.tokTot;
      });
      var row = document.createElement('div');
      row.className = 'grow' + (state.selAg === ag ? ' sel' : '');
      row.title = '点击在两道打 ' + ag + ' 首末请求的参考线（再点取消）';
      var nm = document.createElement('span');
      nm.className = 'gname';
      nm.textContent = ag;
      var tr = document.createElement('div');
      tr.className = 'gtrack';
      var barEl = document.createElement('div');
      barEl.className = 'gbar';
      barEl.style.left = pct(lo) + '%';
      barEl.style.width = Math.max(0.6, pct(hi) - pct(lo)) + '%';
      barEl.style.background = subColorOf(ag);
      barEl.style.opacity = state.selAg && state.selAg !== ag ? 0.35 : 0.85;
      tr.appendChild(barEl);
      rs.forEach(function (r) {
        var tick = document.createElement('div');
        tick.className = 'gtick';
        tick.style.left = pct(r.ts) + '%';
        tick.title = ag + ' · ' + fmtTS(r.ts, true) + ' · 输出 ' + fmtK(r.out) + ' · 合计 ' + fmtK(r.tokTot);
        tr.appendChild(tick);
      });
      var mt = document.createElement('span');
      mt.className = 'gmeta';
      mt.textContent = fmtK(tk) + ' tok · ' + fmtDur(hi - lo) + ' · ' + rs.length + ' 请求';
      row.appendChild(nm);
      row.appendChild(tr);
      row.appendChild(mt);
      row.addEventListener('click', function () {
        var wasSel = state.selAg === ag;
        state.selAg = wasSel ? null : ag;
        if (!wasSel && (lo < state.t0 || hi > state.t1)) {
          // 参考线必须看得见：当前视窗不含首末就扩到含（留 15% 余量）
          var margin = (hi - lo) * 0.15 + 30;
          tweenView(Math.min(state.t0, lo - margin), Math.max(state.t1, hi + margin));
        }
        renderGantt();
        draw();
      });
      gEl.appendChild(row);
    });
  }

  // ---- 选中与详情面板 ----
  function pick(sel) {
    state.sel = state.sel && sel && state.sel.type === sel.type && state.sel.i === sel.i
      ? null : sel; // 再点同一个 = 取消
    btResult.hidden = true; // 反跑结果随选择切换收起，避免旧结果被误读
    renderDetail();
    draw();
  }

  function dRow(k, v) {
    var d = document.createElement('div');
    d.className = 'd-row';
    var a = document.createElement('span'); a.textContent = k;
    var b = document.createElement('span'); b.textContent = v;
    d.appendChild(a); d.appendChild(b);
    return d;
  }
  function dSec(titleText) {
    var d = document.createElement('div');
    d.className = 'd-sec';
    var t = document.createElement('div');
    t.className = 'd-sec-t';
    t.textContent = titleText;
    d.appendChild(t);
    return d;
  }
  function dSw(color) {
    var i = document.createElement('i');
    i.className = 'd-sw';
    i.style.background = color;
    return i;
  }
  function dNote(text, cls) {
    var d = document.createElement('div');
    d.className = 'd-note' + (cls ? ' ' + cls : '');
    d.textContent = text;
    return d;
  }

  function renderDetail() {
    detail.textContent = '';
    if (!state.sel) {
      var h = document.createElement('div');
      h.className = 'd-hint';
      h.textContent = '点图上任意柱子 / 事件标记 / 等待窗口带 / 斜纹断缓存区，这里显示详情。';
      detail.appendChild(h);
      return;
    }
    var head = document.createElement('div');
    head.className = 'd-head';
    var ht = document.createElement('span');
    var close = document.createElement('button');
    close.type = 'button';
    close.className = 'd-close';
    close.textContent = '×';
    close.setAttribute('aria-label', '关闭详情');
    close.addEventListener('click', function () { pick(null); });
    head.appendChild(ht);
    head.appendChild(close);
    detail.appendChild(head);
    var sel = state.sel;

    if (sel.type === 'req') {
      var r = reqs[sel.i];
      ht.textContent = r.sub ? '子代理 ' + r.sub + ' · 请求' : '请求 #' + (sel.i + 1);
      detail.appendChild(dRow('时间', fmtTS(r.ts, true)));
      if (r.sub) detail.appendChild(dRow('子代理', r.sub));
      if (r.m) detail.appendChild(dRow('模型', r.m));
      if (sel.i > 0) {
        var gap = r.ts - reqs[sel.i - 1].ts;
        detail.appendChild(dRow('与上一条间隔', fmtDur(gap) +
          (r.repaid ? '（缓存已断 ↯）' : gap > state.ttl * 0.8 ? '（接近 TTL）' : '（缓存存活）')));
      }
      var sec = dSec('tokens 与花费（GLM 价）');
      [
        ['#4caf50', '缓存读', fmtK(r.cr), r.cCr],
        ['#e57373', '新输入+建缓存', fmtK(r.red), r.cRed],
        ['#64b5f6', '模型输出', fmtK(r.out), r.cOut],
      ].forEach(function (q) {
        var d = document.createElement('div');
        d.className = 'd-row';
        var a = document.createElement('span');
        a.appendChild(dSw(q[0]));
        a.appendChild(document.createTextNode(q[1]));
        var b = document.createElement('span');
        b.textContent = q[2] + ' · ' + fmtCost(q[3]) + ' 积分';
        d.appendChild(a); d.appendChild(b);
        sec.appendChild(d);
      });
      detail.appendChild(sec);
      var tot = dSec('合计');
      tot.appendChild(dRow('本条花费', fmtCost(r.cTot) + ' 积分'));
      tot.appendChild(dRow('累计花费', fmtCost(r.cum) + ' 积分（' + (sel.i + 1) + '/' + reqs.length + ' 条）'));
      detail.appendChild(tot);
      if (r.repaid) {
        detail.appendChild(dNote('↯ 缓存已断：新输入按 6.9 全价，多付约 ' +
          fmtCost(r.in * (PIN - PC) / PER) + ' 积分', 'd-warn'));
      }
      if (r.m && !/glm/i.test(r.m)) {
        detail.appendChild(dNote('模型 ' + r.m + ' 价目未配，花费按 GLM 口径估算'));
      }
    } else if (sel.type === 'ev') {
      var e = events[sel.i];
      var names = { handoff: '⚑ 交接文档', block: '✕ 输入被拦截', inject: '↑ 注入交接', bypass: '◯ 强续放行', beat: '● 心跳', qwatch_hit: '◎ 问询命中', qwatch_open: '◎ 等答复开窗', qwatch_close: '◎ 等答复关窗' };
      ht.textContent = names[e.kind] || e.kind;
      detail.appendChild(dRow('时间', fmtTS(e.ts, true)));
      if (e.kind === 'handoff') {
        detail.appendChild(dRow('生成方', e.provider || '-'));
        detail.appendChild(dRow('状态', e.outcome || '-'));
        detail.appendChild(dNote('会话闲置后自动生成的交接稿，/clear 后的新会话开场自动收到'));
      } else if (e.kind === 'block') {
        detail.appendChild(dRow('已闲置', fmtDur(e.idle_s || 0)));
        detail.appendChild(dNote('闸门拦下这条输入：原话已保管，/clear 开新会话自动带回（交接+原话）；或以「强续」开头强制继续本会话'));
      } else if (e.kind === 'inject') {
        detail.appendChild(dRow('tokens', fmtK(e.tokens || 0)));
        detail.appendChild(dNote('新会话开场自动带入交接文档'));
      } else if (e.kind === 'bypass') {
        detail.appendChild(dRow('前缀', fmtK(e.prefix_tokens || 0)));
        detail.appendChild(dNote('以「强续」开头强制放行，不计拦截次数'));
      } else if (e.kind === 'beat') {
        detail.appendChild(dRow('结果', beatOutcomeLabel(e.outcome)));
        detail.appendChild(dRow('实收缓存读', fmtK(e.cache_read || 0)));
        detail.appendChild(dNote('预测 ' + fmtCost(e.cost_pred) + ' / 实际 ' + fmtCost(e.cost_actual) + ' 积分'));
      } else if (e.kind === 'qwatch_hit') {
        detail.appendChild(dRow('问题单元', String(e.unit_count || 0)));
        detail.appendChild(dRow('标记/问号/编号行',
          (e.marker_lines || 0) + ' / ' + (e.qmark_lines || 0) + ' / ' + (e.numbered_lines || 0)));
        if (e.transcript_path) detail.appendChild(dRow('转录', e.transcript_path));
        detail.appendChild(dNote('AI 末条被判定为提问潮——命中清单供人工复核（只记计数与路径，不含消息正文）'));
      }
    } else if (sel.type === 'win') {
      renderWinDetail(ht, sel.i);
    } else if (sel.type === 'zone') {
      var z = state.zones[sel.i];
      ht.textContent = '断缓存区';
      detail.appendChild(dRow('时段', fmtTS(z.from) + ' ~ ' + fmtTS(z.to)));
      detail.appendChild(dRow('断缓存时长', fmtDur(z.gap - state.ttl)));
      var nx = reqs[z.reqIdx];
      detail.appendChild(dRow('下一条请求', '#' + (z.reqIdx + 1) + ' · ' + fmtTS(nx.ts, true)));
      detail.appendChild(dRow('其新输入', fmtK(nx.in) + ' tokens'));
      detail.appendChild(dNote('这部分从缓存价 1.7 涨回全价 6.9，多付约 ' + fmtCost(z.loss) +
        ' 积分（估算）。该条全款总额 ≈ ' + fmtCost(nx.cTot) + ' 积分。', 'd-warn'));
    }
  }

  // renderWinDetail 窗口详情：基础行 + TTL 判读 + 反跑表单入口（buildBtForm 挂本面板内）。
  function renderWinDetail(ht, wi) {
    var w = windows[wi];
    ht.textContent = '子代理等待窗口 #' + (wi + 1);
    detail.appendChild(dRow('开始', fmtTS(w.opened_ts, true)));
    detail.appendChild(dRow('结束', fmtTS(w.closed_ts, true)));
    detail.appendChild(dRow('等待时长', fmtDur(w.dur_s)));
    detail.appendChild(dRow('当时前缀', fmtK(w.prefix_tokens || 0) + ' tokens'));
    detail.appendChild(dRow('结束原因', w.close_reason || '-'));
    if (w.dur_s > state.ttl) {
      detail.appendChild(dNote('超过 TTL：等待期间缓存已死，恢复要全款重付 ≈ ' +
        fmtCost((w.prefix_tokens || 0) * PIN / PER) + ' 积分（这正是心跳想救的场景）', 'd-warn'));
    } else {
      detail.appendChild(dNote('在 TTL 内：缓存存活，零损失，无需任何干预', 'd-good'));
    }
    var btnRow = document.createElement('div');
    btnRow.className = 'd-btns';
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'win-backtest';
    btn.textContent = '反跑此窗口';
    btn.title = '展开反跑参数表单（价格预填 GLM 口径）';
    btn.setAttribute('aria-expanded', 'false');
    btn.addEventListener('click', function () {
      var form = detail.querySelector('.bt-form');
      if (!form) {
        form = buildBtForm(w, wi);
        detail.appendChild(form);
      } else {
        form.hidden = !form.hidden;
      }
      btn.setAttribute('aria-expanded', form.hidden ? 'false' : 'true');
    });
    btnRow.appendChild(btn);
    detail.appendChild(btnRow);
  }

  // ---- 反跑（POST /api/backtest）----

  // windowBeatActual 该窗口"实际发生"线：账本 events 里 kind=beat 且 ts 落窗内的行，
  // Σcost_actual。畸形行（ts/cost_actual 非数）兜底跳过不炸卡。
  function windowBeatActual(win) {
    var sum = 0, n = 0;
    events.forEach(function (e) {
      if (!e || e.kind !== 'beat' || !isFinite(e.ts)) return;
      if (e.ts < win.opened_ts - 1e-6 || e.ts > win.closed_ts + 1e-6) return;
      sum += isFinite(e.cost_actual) ? e.cost_actual : 0;
      n++;
    });
    return { sum: sum, n: n };
  }

  // beatOffsets 模拟跳点（绝对时间戳）→ 相对 T0 的偏移串（>4 个截断加 …）。
  function beatOffsets(beats, t0) {
    var parts = beats.slice(0, 4).map(function (b) { return String(Math.round((b - t0) * 10) / 10) + 's'; });
    if (beats.length > 4) parts.push('…');
    return parts.join('、');
  }

  // buildBtForm 反跑参数表单：8 个可编辑参数（GLM 预填）+ 窗口三参只读带出行。
  // 提交前逐字段校验为有限数，畸形输入不发请求、就地红字提示。
  function buildBtForm(win, wi) {
    var form = document.createElement('form');
    form.className = 'bt-form';

    var ro = document.createElement('div');
    ro.className = 'bt-ro';
    ro.textContent = '随窗口带出：prefix=' + fmtK(win.prefix_tokens || 0) +
      ' · ' + fmtTS(win.opened_ts) + ' ~ ' + fmtTS(win.closed_ts);
    form.appendChild(ro);

    var grid = document.createElement('div');
    grid.className = 'bt-grid';
    var inputs = {};
    BT_FIELDS.forEach(function (f) {
      var cell = document.createElement('label');
      cell.className = 'bt-field';
      var cap = document.createElement('span');
      cap.textContent = f.label;
      var inp = document.createElement('input');
      inp.type = 'number';
      inp.step = 'any';
      inp.value = String(f.def);
      inp.setAttribute('aria-label', f.label);
      inp.addEventListener('input', function () { hint.textContent = ''; });
      inputs[f.key] = inp;
      cell.appendChild(cap);
      cell.appendChild(inp);
      grid.appendChild(cell);
    });
    form.appendChild(grid);

    var hint = document.createElement('div');
    hint.className = 'bt-hint';
    form.appendChild(hint);

    var go = document.createElement('button');
    go.type = 'submit';
    go.className = 'bt-go';
    go.textContent = '反跑（POST /api/backtest）';
    form.appendChild(go);

    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var params = {};
      for (var i = 0; i < BT_FIELDS.length; i++) {
        var f = BT_FIELDS[i];
        var v = parseFloat(inputs[f.key].value);
        if (!isFinite(v)) { // 畸形输入兜底：不发送，就地提示
          hint.textContent = '参数 ' + f.key + ' 不是数字';
          return;
        }
        params[f.key] = v;
      }
      runBacktest(win, wi, params, go);
    });
    return form;
  }

  // runBacktest POST /api/backtest。双守卫：navSeq 变 = 已切页；btSeq 变 = 用户连点，
  // 旧响应一律丢弃，绝不覆盖新结果。期间按钮置灰防重复提交。
  function runBacktest(win, wi, params, goBtn) {
    var myBt = ++btSeq;
    var seqAtGo = navSeq;
    goBtn.disabled = true;
    goBtn.textContent = '反跑中…';
    fetchJSON('/api/backtest', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        lineage: lineage,
        opened_ts: win.opened_ts,
        closed_ts: win.closed_ts,
        prefix_tokens: win.prefix_tokens || 0,
        params: params,
      }),
    }).then(function (data) {
      goBtn.disabled = false;
      goBtn.textContent = '反跑（POST /api/backtest）';
      if (seqAtGo !== navSeq || myBt !== btSeq) return;
      showBtResult(win, wi, data);
    }).catch(function (e) {
      goBtn.disabled = false;
      goBtn.textContent = '反跑（POST /api/backtest）';
      if (seqAtGo !== navSeq || myBt !== btSeq) return;
      showBtError('反跑失败：' + e.message);
    });
  }

  // showBtError 结果卡整卡错误态（网络失败 / 4xx 等）。
  function showBtError(msg) {
    btResult.textContent = '';
    var err = document.createElement('div');
    err.className = 'bt-error';
    err.textContent = msg;
    btResult.appendChild(err);
    btResult.hidden = false;
  }

  // showBtResult 反跑结果卡：三线对比横条（若这样配 / 实际发生 / 什么都不做）+
  // cap/τ/单跳/全款四推导数 + cap 语义注。ok:false → 红字拒绝文案。全部
  // createElement/textContent 进 DOM，数值经 isFinite 兜底。
  function showBtResult(win, wi, data) {
    btResult.textContent = '';
    var head = document.createElement('div');
    head.className = 'bt-head';
    var h = document.createElement('span');
    h.textContent = '反跑结果 · 窗口 #' + (wi + 1) + '（' + fmtTS(win.opened_ts) + ' ~ ' + fmtTS(win.closed_ts) + '）';
    var close = document.createElement('button');
    close.type = 'button';
    close.className = 'bt-close';
    close.textContent = '×';
    close.setAttribute('aria-label', '关闭反跑结果卡');
    close.addEventListener('click', function () { btResult.hidden = true; });
    head.appendChild(h);
    head.appendChild(close);
    btResult.appendChild(head);

    if (!data || data.ok !== true) { // 业务拒绝（p_cache 缺省 / ttl_s 非法）：红字文案
      var err = document.createElement('div');
      err.className = 'bt-error';
      err.textContent = '反跑被拒绝：' + ((data && data.error) || '未知原因');
      btResult.appendChild(err);
      btResult.hidden = false;
      return;
    }

    var r = data.result || {};
    var beats = Array.isArray(data.beats) ? data.beats.filter(function (b) { return isFinite(b); }) : [];
    var planned = isFinite(data.beats_cost) ? data.beats_cost : 0;
    var nothing = isFinite(data.do_nothing_cost) ? data.do_nothing_cost : 0;
    var act = windowBeatActual(win);
    var maxV = Math.max(planned, act.n ? act.sum : 0, nothing);

    // btLine 一条对比横条：val=null 显示灰"—（未启用）"；idx 行序（三线 40ms 错峰生长）。
    function btLine(label, val, sub, fillCls, idx) {
      var line = document.createElement('div');
      line.className = 'bt-line';
      var lab = document.createElement('span');
      lab.className = 'bt-label';
      lab.textContent = label;
      var track = document.createElement('div');
      track.className = 'bt-track';
      var fill = document.createElement('div');
      fill.className = 'bt-fill ' + fillCls;
      if (val != null && maxV > 0) fill.style.width = (val / maxV * 100) + '%';
      fill.style.animationDelay = (idx * 40) + 'ms';
      track.appendChild(fill);
      var v = document.createElement('span');
      v.className = val == null ? 'bt-val bt-dim' : 'bt-val';
      v.textContent = val == null ? '—（未启用）' : fmtCost(val) + ' 积分';
      line.appendChild(lab);
      line.appendChild(track);
      line.appendChild(v);
      btResult.appendChild(line);
      if (sub) {
        var subEl = document.createElement('div');
        subEl.className = 'bt-sub';
        subEl.textContent = sub;
        btResult.appendChild(subEl);
      }
    }

    btLine('若当时这样配', planned,
      beats.length + ' 跳' + (beats.length ? ' @ T0+' + beatOffsets(beats, win.opened_ts) : '') +
      ' · 共 ' + fmtCost(planned) + ' 积分',
      'bt-fill-green', 0);
    if (act.n > 0) {
      btLine('实际发生', act.sum, act.n + ' 跳真实 beat', 'bt-fill-actual', 1);
    } else {
      btLine('实际发生', null, '', 'bt-fill-actual', 1);
    }
    if (nothing > 0) {
      btLine('什么都不做', nothing, '整窗超 TTL，过期一次全款', 'bt-fill-red', 2);
    } else {
      btLine('什么都不做', 0, '存活无损', 'bt-fill-red', 2);
    }

    var stats = document.createElement('div');
    stats.className = 'bt-stats';
    [
      ['cap 上限', numOrDash(r.cap_s, 's')],
      ['τ 间隔', numOrDash(r.tau_s, 's')],
      ['单跳', isFinite(r.per_beat) ? fmtCost(r.per_beat) : '-'],
      ['全款', isFinite(r.expire) ? fmtCost(r.expire) : '-'],
    ].forEach(function (sd) {
      var sp = document.createElement('span');
      sp.className = 'bt-stat';
      var k = document.createElement('span');
      k.className = 'bt-stat-k';
      k.textContent = sd[0] + ' ';
      sp.appendChild(k);
      sp.appendChild(document.createTextNode(sd[1]));
      stats.appendChild(sp);
    });
    btResult.appendChild(stats);

    if (data.note) {
      var note = document.createElement('div');
      note.className = 'bt-note';
      note.textContent = data.note;
      btResult.appendChild(note);
    }
    btResult.hidden = false;
  }

  // buildPlanCard 保活计划卡（策略计算器）：参数复用 BT_FIELDS + 自动前缀 S
  // （本会话末次请求 input+cache_read+creation 取整，可改），提交 POST /api/backtest
  // 假设窗一周（opened_ts=0 → beats 天然是相对 T0 的偏移；closed_ts=604800 让 cap
  // 而非窗末截断跳数）。竞态只守 navSeq——计划卡结果区与反跑卡结果区互不相邻，
  // 不会互相覆盖，无需 btSeq；请求期间按钮置灰防连点。
  function buildPlanCard(requests) {
    var body = document.getElementById('plan-body');

    var ro = document.createElement('div');
    ro.className = 'plan-ro';
    ro.textContent = '闲置泳道（与心跳无关）：闲置 1500s 摆渡 · 2100s 拦截——人不在的归摆渡';
    body.appendChild(ro);

    var form = document.createElement('form');
    form.className = 'bt-form';
    var roPrefix = document.createElement('div');
    roPrefix.className = 'bt-ro';
    roPrefix.textContent = 'prefix S 自动填 ≈ 本会话末次请求前缀（input+cache_read+creation 取整），可改';
    form.appendChild(roPrefix);

    var last = requests.length ? requests[requests.length - 1] : null;
    var autoPrefix = last
      ? Math.round(last.input_tokens + last.cache_read_tokens + last.cache_creation_tokens)
      : 0;
    var fields = BT_FIELDS.concat([
      { key: 'prefix_tokens', label: 'prefix S 前缀 tokens', def: autoPrefix },
    ]);

    var grid = document.createElement('div');
    grid.className = 'bt-grid';
    var inputs = {};
    fields.forEach(function (f) {
      var cell = document.createElement('label');
      cell.className = 'bt-field';
      var cap = document.createElement('span');
      cap.textContent = f.label;
      var inp = document.createElement('input');
      inp.type = 'number';
      inp.step = 'any';
      inp.value = String(f.def);
      inp.setAttribute('aria-label', f.label);
      inp.addEventListener('input', function () { hint.textContent = ''; });
      inputs[f.key] = inp;
      cell.appendChild(cap);
      cell.appendChild(inp);
      grid.appendChild(cell);
    });
    form.appendChild(grid);

    var hint = document.createElement('div');
    hint.className = 'bt-hint';
    form.appendChild(hint);

    var go = document.createElement('button');
    go.type = 'submit';
    go.className = 'bt-go';
    go.textContent = '计算计划';
    form.appendChild(go);

    var resultBox = document.createElement('div');
    resultBox.className = 'plan-result';

    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var params = {};
      var prefix = NaN;
      for (var i = 0; i < fields.length; i++) {
        var f = fields[i];
        var v = parseFloat(inputs[f.key].value);
        if (!isFinite(v)) { // 畸形输入兜底：不发送，就地红字提示
          hint.textContent = '参数 ' + f.key + ' 不是数字';
          return;
        }
        if (f.key === 'prefix_tokens') prefix = v; // S 走请求体顶层，不进 params
        else params[f.key] = v;
      }
      runPlan(params, prefix);
    });

    // runPlan 提交假设窗一周的策略推导，回填结果区（切页后响应丢弃）。
    function runPlan(params, prefix) {
      var seqAtGo = navSeq;
      go.disabled = true;
      go.textContent = '计算中…';
      fetchJSON('/api/backtest', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          lineage: lineage,
          opened_ts: 0,
          closed_ts: 604800,
          prefix_tokens: prefix,
          params: params,
        }),
      }).then(function (data) {
        go.disabled = false;
        go.textContent = '计算计划';
        if (seqAtGo !== navSeq) return; // 已切页：卡片随页销毁，弃响应
        showPlanResult(data, params.ttl_s);
      }).catch(function (e) {
        go.disabled = false;
        go.textContent = '计算计划';
        if (seqAtGo !== navSeq) return;
        planError('计算失败：' + e.message);
      });
    }

    // planError 结果区红字一行（业务拒绝 / 网络失败共用）。
    function planError(msg) {
      resultBox.textContent = '';
      var err = document.createElement('div');
      err.className = 'bt-error';
      err.textContent = msg;
      resultBox.appendChild(err);
    }

    // planRow 结果区追加一行说明/数据。
    function planRow(cls, text) {
      var d = document.createElement('div');
      d.className = cls;
      d.textContent = text;
      resultBox.appendChild(d);
    }

    // showPlanResult 计划结果：行1 数字行 → 跳点偏移 → TTL 三区 → 触发/停跳规则
    // → note。数值 isFinite 兜底（非有限显 -）；全部 createElement/textContent。
    function showPlanResult(data, ttl) {
      resultBox.textContent = '';
      if (!data || data.ok !== true) { // 业务拒绝（p_cache 缺省 / ttl_s 非法）：红字
        planError('计划被拒绝：' + ((data && data.error) || '未知原因'));
        return;
      }
      var r = data.result || {};
      var beats = Array.isArray(data.beats) ? data.beats.filter(function (b) { return isFinite(b); }) : [];

      planRow('plan-numbers',
        'τ 间隔 ' + numOrDash(r.tau_s, 's') +
        ' · 首跳=开窗后闲置满 ' + numOrDash(r.tau_s, 's') +
        ' · 等待上限 ' + numOrDash(r.cap_s, 's') +
        ' · 最多 ' + beats.length + ' 跳' +
        ' · 单跳 ' + (isFinite(r.per_beat) ? fmtCost(r.per_beat) : '-') + ' 积分' +
        ' · 全程上限 ' + (isFinite(data.beats_cost) ? fmtCost(data.beats_cost) : '-') + ' 积分' +
        ' · 放任过期（全款重付）' + (isFinite(r.expire) ? fmtCost(r.expire) : '-') + ' 积分');
      if (beats.length) { // 跳点：相对 T0 的偏移，>4 个截断加 …
        var parts = beats.slice(0, 4).map(function (b) { return '+' + String(Math.round(b * 10) / 10) + 's'; });
        if (beats.length > 4) parts.push('…');
        planRow('plan-sub', '跳点：' + parts.join('、'));
      }
      planRow('plan-rule',
        '实测 TTL ' + String(Math.round(ttl * 10) / 10) + 's 三区：≤' + String(Math.round(ttl * 10) / 10) + 's 必活 / ' +
        String(Math.round(ttl * 10) / 10) + '~1800s 看驱逐脸色 / ≥1800s 必死（死线 1800s=实测口径，必活线随 TTL 配置移动）');
      planRow('plan-rule',
        '触发：子代理在飞＋主会话闲置满 τ＋四道预检（全局开关/窗口开/无新写入/前缀≥30k）→ 体外重放刷新缓存，不写会话文件');
      planRow('plan-rule',
        '停跳：子代理全回（自动续跑兑现暖缓存）/ 主会话有写入 / 任一跳 miss（立即停＋告警，绝不重试）/ 到等待上限');
      if (data.note) {
        var note = document.createElement('div');
        note.className = 'bt-note';
        note.textContent = data.note;
        resultBox.appendChild(note);
      }
    }

    body.appendChild(form);
    body.appendChild(resultBox);
  }

  // ---- 主重绘：每次全量重建 SVG 子节点（断缓存区/窗口/TTL 带/柱/事件/游标/轴）----
  function draw() {
    svg.textContent = '';
    // 泳道几何（票02 D 形态）：有子代理请求且未被 chip 隐藏 → 双道（主上/子下
    // 共用时间轴、各自 y 轴）；否则单道（几何与既往逐像素一致——纯主会话/
    // 纯 codex 会话零回归）。窗口带/断缓存斜纹/游标/x 轴贯穿两道（高度=两道全高）。
    var dual = hasSub && state.showSub;
    var botMain = dual ? 268 : YBOT; // 主道底（双道时让出下半场）
    var topSub = 296, botSub = YBOT; // 子道（dual 才启用）
    var botAll = dual ? botSub : botMain; // 贯穿元素的底
    var ym = yMax();
    function yOf(v) { return botMain - v / ym * (botMain - YTOP); }
    function inView(ts) { return ts >= state.t0 && ts <= state.t1; }
    function past(ts) { return ts <= state.cursor; }

    // defs：断缓存斜纹图案
    var defs = svgEl('defs');
    var pat = svgEl('pattern', {
      id: 'dead', width: '8', height: '8', patternUnits: 'userSpaceOnUse',
      patternTransform: 'rotate(45)',
    });
    pat.appendChild(svgEl('rect', { width: '8', height: '8', fill: 'rgba(229,115,115,0.07)' }));
    pat.appendChild(svgEl('line', { x1: 0, y1: 0, x2: 0, y2: 8, stroke: 'rgba(229,115,115,0.38)', 'stroke-width': '2' }));
    defs.appendChild(pat);
    svg.appendChild(defs);

    // TTL 存活带：主会话请求∪心跳各续 ttl 秒（游标左侧才画；ttl<=0 不画；
    // 子代理请求走自己的转录与缓存，不续主会话的带——票02）
    var gBand = svgEl('g', { 'pointer-events': 'none' });
    if (state.ttl > 0) {
      var bandNote = false;
      function bandRect(ts) {
        if (!past(ts) || ts > state.t1 || ts + state.ttl < state.t0) return;
        var x1 = Math.max(xOf(ts), X0), x2 = Math.min(xOf(ts + state.ttl), X1);
        if (x2 <= x1) return;
        gBand.appendChild(svgEl('rect', { x: x1, y: BAND_Y, width: x2 - x1, height: BAND_H, fill: 'rgba(76,175,80,0.22)' }));
        bandNote = true;
      }
      mainReqs.forEach(function (r) { bandRect(r.ts); });
      events.forEach(function (e) { if (e.kind === 'beat') bandRect(e.ts); });
      if (bandNote) {
        var bandLab = svgEl('text', { x: X0 + 4, y: BAND_Y + BAND_H - 4, 'font-size': '10', fill: '#7fa97f' });
        bandLab.textContent = '绿带=缓存存活（每条请求续 ' + state.ttl + ' 秒）';
        gBand.appendChild(bandLab);
      }
    }
    svg.appendChild(gBand);

    // 断缓存区（斜纹贯穿两道全高，可点可选）
    var gDead = svgEl('g');
    state.zones.forEach(function (z, zi) {
      if (z.to > state.cursor || z.to < state.t0 || z.from > state.t1) return;
      var x1 = Math.max(xOf(Math.max(z.from, state.t0)), X0);
      var x2 = Math.min(xOf(Math.min(z.to, state.t1)), X1);
      if (x2 - x1 < 1) return;
      var rect = svgEl('rect', {
        x: x1, y: YTOP - 6, width: x2 - x1, height: botAll - YTOP + 6,
        fill: 'url(#dead)', cursor: 'pointer',
        stroke: 'rgba(229,115,115,0.55)', 'stroke-width': '0.8',
      });
      if (state.sel && state.sel.type === 'zone' && state.sel.i === zi) {
        rect.setAttribute('stroke', '#e57373');
        rect.setAttribute('stroke-dasharray', '4 3');
      }
      attachTip(rect, function () { tipZone(z); });
      rect.addEventListener('click', function (ev) { ev.stopPropagation(); pick({ type: 'zone', i: zi }); });
      gDead.appendChild(rect);
      if (x2 - x1 > 90) {
        var lab = svgEl('text', { x: (x1 + x2) / 2, y: YTOP + 10, 'text-anchor': 'middle', 'font-size': '10.5', fill: '#d99', 'pointer-events': 'none' });
        lab.textContent = '缓存断 ' + fmtDur(z.gap - state.ttl) + ' · 多付≈' + fmtCost(z.loss) + '积分';
        gDead.appendChild(lab);
      } else {
        // 窄区放不下整句：中央一个大「断」字占位（区内没有柱，不遮数据）
        var tag = svgEl('text', {
          x: (x1 + x2) / 2, y: (YTOP + botAll) / 2 + 4, 'text-anchor': 'middle',
          'font-size': '11', 'font-weight': 'bold', fill: '#e57373', 'pointer-events': 'none',
        });
        tag.textContent = '断';
        gDead.appendChild(tag);
      }
    });
    svg.appendChild(gDead);

    // 等待窗口带（子代理在飞，贯穿两道全高，可点选中）
    var gWin = svgEl('g');
    windows.forEach(function (w, wi) {
      if (w.closed_ts < state.t0 || w.opened_ts > state.t1) return;
      var x1 = Math.max(xOf(w.opened_ts), X0);
      var x2 = Math.min(xOf(w.closed_ts), X1);
      if (x2 - x1 < 0.5) return;
      var rect = svgEl('rect', {
        x: x1, y: YTOP - 6, width: Math.max(x2 - x1, 1), height: botAll - YTOP + 6,
        fill: 'rgba(100,181,246,0.07)', cursor: 'pointer',
      });
      if (state.sel && state.sel.type === 'win' && state.sel.i === wi) {
        rect.setAttribute('stroke', '#64b5f6');
        rect.setAttribute('stroke-width', '1.5');
      }
      attachTip(rect, function () { tipWin(w, wi); });
      rect.addEventListener('click', function (ev) { ev.stopPropagation(); pick({ type: 'win', i: wi }); });
      gWin.appendChild(rect);
      if (x2 - x1 > 60) {
        var lab = svgEl('text', { x: (x1 + x2) / 2, y: YTOP + 24, 'text-anchor': 'middle', 'font-size': '10', fill: '#7aa', 'pointer-events': 'none' });
        lab.textContent = '等子代理 ' + fmtDur(w.dur_s);
        gWin.appendChild(lab);
      }
    });
    svg.appendChild(gWin);

    // token 柱：三段堆叠自底向上 绿 cache_read / 红 input+creation / 蓝 output。
    // 柱宽随可视间距现算；每柱带透明命中矩形（细柱也点得到）。
    // 票02 D 形态：主请求画主道、子代理请求画子道（各自 y 轴）；子柱带 agent
    // 色虚线描边 + ▼ 角标（悬停/详情可辨 agentId）。
    var gBars = svgEl('g');
    function drawLane(list, yT, yB, ymLane, sub) {
      function yOfLane(v) { return yB - v / ymLane * (yB - yT); }
      var visible = list.filter(function (r) { return inView(r.ts) && past(r.ts); });
      var xs = visible.map(function (r) { return xOf(r.ts); });
      visible.forEach(function (r, i) {
        var gapL = i > 0 ? xs[i] - xs[i - 1] : Infinity;
        var gapR = i < xs.length - 1 ? xs[i + 1] - xs[i] : Infinity;
        var sp = Math.min(gapL, gapR);
        if (!isFinite(sp)) sp = 24;
        var w = Math.min(Math.max(2, sp * 0.6), sub ? 14 : 18);

        var vCr = state.seg.cr ? (state.view === 'cost' ? r.cCr : r.cr) : 0;
        var vRed = state.seg.red ? (state.view === 'cost' ? r.cRed : r.red) : 0;
        var vOut = state.seg.out ? (state.view === 'cost' ? r.cOut : r.out) : 0;
        var yG = yOfLane(vCr), yR = yOfLane(vCr + vRed), yTopB = yOfLane(vCr + vRed + vOut);

        var g = svgEl('g', { cursor: 'pointer' });
        function seg(y, h, color) {
          if (h > 0.5) g.appendChild(svgEl('rect', { x: xs[i] - w / 2, y: y, width: w, height: h, fill: color }));
        }
        seg(yG, yB - yG, '#4caf50');
        seg(yR, yG - yR, '#e57373');
        seg(yTopB, yR - yTopB, '#64b5f6');
        if (sub) {
          // 子柱：agent 色虚线描边整柱 + ▼ 角标（原型 D 的区分记号）
          var sy = Math.min(yTopB - 1.5, yB - 6);
          g.appendChild(svgEl('rect', {
            x: xs[i] - w / 2 - 1.5, y: sy, width: w + 3, height: yB - sy,
            fill: 'none', stroke: r.subColor, 'stroke-width': '1.3',
            'stroke-dasharray': '3 2', 'pointer-events': 'none',
          }));
          var mk = svgEl('text', {
            x: xs[i], y: yTopB - 5, 'font-size': '9.5', 'text-anchor': 'middle',
            fill: r.subColor, 'pointer-events': 'none',
          });
          mk.textContent = '▼';
          g.appendChild(mk);
        } else if (r.repaid) {
          // 重付柱红描边整柱：比 ↯ 更显眼——"这根=缓存死过，上下文全价重交了一遍"
          var ry = Math.min(yTopB - 1.5, yB - 7);
          g.appendChild(svgEl('rect', {
            x: xs[i] - w / 2 - 1.5, y: ry, width: w + 3, height: yB - ry,
            fill: 'none', stroke: '#e57373', 'stroke-width': '1.5', 'pointer-events': 'none',
          }));
        }
        if (state.sel && state.sel.type === 'req' && state.sel.i === r.idx) {
          g.appendChild(svgEl('rect', {
            x: xs[i] - w / 2 - 2, y: yTopB - 2, width: w + 4,
            height: yB - yTopB + 2, fill: 'none', stroke: '#fff', 'stroke-width': '1.5',
          }));
        }
        var hit = svgEl('rect', {
          x: xs[i] - Math.max(w, 10) / 2, y: yT - 6,
          width: Math.max(w, 10), height: yB - yT + 6, fill: 'transparent',
        });
        g.appendChild(hit);
        attachTip(g, function () { tipReq(r); });
        g.addEventListener('click', function (ev) { ev.stopPropagation(); pick({ type: 'req', i: r.idx }); });
        gBars.appendChild(g);

        // 断缓存后的第一根柱：↯ 角标（可点可悬停）——主道专属
        if (!sub && r.repaid) {
          var zap = svgEl('text', {
            x: xs[i], y: yTopB - 6, 'text-anchor': 'middle', 'font-size': '12',
            fill: '#e57373', cursor: 'pointer',
          });
          zap.textContent = '↯';
          attachTip(zap, function () {
            tipFill('↯ 全额重付', null, [
              ['与上一条间隔', '超过 TTL，缓存已死'],
              ['本条新输入', fmtK(r.in) + ' 按 6.9 全价'],
            ], '多付约 ' + fmtCost(r.in * (PIN - PC) / PER) + ' 积分（估算）', true);
          });
          zap.addEventListener('click', function (ev) { ev.stopPropagation(); pick({ type: 'req', i: r.idx }); });
          gBars.appendChild(zap);
        }
      });
    }
    drawLane(mainReqs, YTOP, botMain, ym, false);
    if (dual) drawLane(subReqs, topSub, botSub, subYMax(), true);
    svg.appendChild(gBars);

    // 事件行 y=24 基线，符号按 kind；带透明命中圈（小符号也点得到）
    var gEv = svgEl('g');
    events.forEach(function (e, ei) {
      if (!inView(e.ts) || !past(e.ts)) return;
      var x = xOf(e.ts), g;
      if (e.kind === 'beat') {
        // observe 演练跳空心圈（未真发，样式区分——T51 票04）
        g = e.outcome === 'observe'
          ? svgEl('circle', { cx: x, cy: EV_Y, r: 4, fill: 'none', stroke: '#4caf50', 'stroke-width': '1.5', cursor: 'pointer' })
          : svgEl('circle', { cx: x, cy: EV_Y, r: 4, fill: '#4caf50', cursor: 'pointer' });
      } else if (e.kind === 'handoff') {
        g = svgEl('g', { cursor: 'pointer' }); // 向下小旗：杆 + 倒三角旗面
        g.appendChild(svgEl('line', { x1: x, y1: EV_Y - 10, x2: x, y2: EV_Y + 8, stroke: '#ffb74d', 'stroke-width': '1.5' }));
        g.appendChild(svgEl('polygon', {
          points: x + ',' + (EV_Y - 10) + ' ' + (x + 9) + ',' + (EV_Y - 10) + ' ' + (x + 4.5) + ',' + (EV_Y - 3),
          fill: '#ffb74d',
        }));
      } else if (e.kind === 'block') {
        g = svgEl('g', { cursor: 'pointer', stroke: '#e57373', 'stroke-width': '2' }); // 红叉
        g.appendChild(svgEl('line', { x1: x - 4, y1: EV_Y - 4, x2: x + 4, y2: EV_Y + 4 }));
        g.appendChild(svgEl('line', { x1: x - 4, y1: EV_Y + 4, x2: x + 4, y2: EV_Y - 4 }));
      } else if (e.kind === 'inject') {
        g = svgEl('g', { cursor: 'pointer', stroke: '#4dd0e1', 'stroke-width': '2', fill: 'none' }); // 上箭头
        g.appendChild(svgEl('line', { x1: x, y1: EV_Y + 8, x2: x, y2: EV_Y - 4 }));
        g.appendChild(svgEl('polyline', { points: (x - 4) + ',' + EV_Y + ' ' + x + ',' + (EV_Y - 5) + ' ' + (x + 4) + ',' + EV_Y }));
      } else if (e.kind === 'bypass') {
        g = svgEl('circle', { cx: x, cy: EV_Y, r: 4, fill: 'none', stroke: '#888', 'stroke-width': '1.5', cursor: 'pointer' });
      } else {
        g = svgEl('circle', { cx: x, cy: EV_Y, r: 3, fill: '#666', cursor: 'pointer' }); // 未知 kind 容错
      }
      g.appendChild(svgEl('circle', { cx: x, cy: EV_Y, r: 10, fill: 'transparent' }));
      if (state.sel && state.sel.type === 'ev' && state.sel.i === ei) {
        g.appendChild(svgEl('circle', { cx: x, cy: EV_Y, r: 7, fill: 'none', stroke: '#fff', 'stroke-width': '1.2' }));
      }
      attachTip(g, function () { tipEv(e); });
      g.addEventListener('click', function (ev) { ev.stopPropagation(); pick({ type: 'ev', i: ei }); });
      gEv.appendChild(g);
    });
    svg.appendChild(gEv);

    // 事件行基线与标注：左缘小标说明这行是干嘛的；空行就地说明"没有"而非"画丢了"
    var evBase = svgEl('line', { x1: X0, y1: EV_Y + 12, x2: X1, y2: EV_Y + 12, stroke: '#222' });
    svg.appendChild(evBase);
    var evLab = svgEl('text', { x: X0 - 6, y: EV_Y + 3, 'text-anchor': 'end', 'font-size': '10', fill: '#666', 'pointer-events': 'none' });
    evLab.textContent = '事件';
    svg.appendChild(evLab);
    if (!events.length) {
      var noEv = svgEl('text', { x: X0 + 8, y: EV_Y + 4, 'font-size': '10.5', fill: '#555', 'pointer-events': 'none' });
      noEv.textContent = '本会话无事件——心跳未实装；交接/拦截/注入/强续只在闸门触发时才有';
      svg.appendChild(noEv);
    }

    // 回放游标竖线（在可视窗内才画；贯穿两道）
    if (state.cursor >= state.t0 && state.cursor <= state.t1) {
      svg.appendChild(svgEl('line', {
        x1: xOf(state.cursor), y1: 12, x2: xOf(state.cursor), y2: botAll,
        stroke: 'rgba(255,255,255,0.25)', 'stroke-width': '1',
      }));
    }

    // y 轴网格 + 刻度（token / 积分 两口径；主道范围）
    var gGrid = svgEl('g', { 'pointer-events': 'none' });
    for (var gi = 0; gi <= 4; gi++) {
      var v = ym * gi / 4, gy = yOf(v);
      gGrid.appendChild(svgEl('line', { x1: X0, y1: gy, x2: X1, y2: gy, stroke: '#222' }));
      var gl = svgEl('text', { x: X0 - 6, y: gy + 4, 'text-anchor': 'end', 'font-size': '10.5', fill: '#888' });
      gl.textContent = state.view === 'cost' ? fmtCost(v) : (v >= 10000 ? fmtK(v) : String(Math.round(v)));
      gGrid.appendChild(gl);
    }
    var yUnit = svgEl('text', { x: X0 - 6, y: YTOP - 14, 'text-anchor': 'end', 'font-size': '10.5', fill: '#666' });
    yUnit.textContent = state.view === 'cost' ? '积分' : 'tokens';
    gGrid.appendChild(yUnit);
    svg.appendChild(gGrid);

    // 子道 y 轴 + 泳道分隔线 + 两道标注 + 甘特参考线（票02 D 形态，dual 才有）
    if (dual) {
      var ym2 = subYMax();
      var gSub = svgEl('g', { 'pointer-events': 'none' });
      [0, 0.5, 1].forEach(function (f) {
        var gy2 = topSub + f * (botSub - topSub);
        gSub.appendChild(svgEl('line', { x1: X0, y1: gy2, x2: X1, y2: gy2, stroke: '#222' }));
        var sv = ym2 * (1 - f);
        var sl = svgEl('text', { x: X0 - 6, y: gy2 + 4, 'text-anchor': 'end', 'font-size': '10.5', fill: '#888' });
        sl.textContent = state.view === 'cost' ? fmtCost(sv) : (sv >= 10000 ? fmtK(sv) : String(Math.round(sv)));
        gSub.appendChild(sl);
      });
      svg.appendChild(gSub);
      // 泳道分隔虚线
      svg.appendChild(svgEl('line', {
        x1: X0, y1: (botMain + topSub) / 2, x2: X1, y2: (botMain + topSub) / 2,
        stroke: '#3a3a3a', 'stroke-dasharray': '2 4', 'pointer-events': 'none',
      }));
      var labMain = svgEl('text', { x: X0 + 4, y: YTOP - 6, 'font-size': '10.5', fill: '#9ab', 'pointer-events': 'none' });
      labMain.textContent = '主会话（子代理在跑时本道照常对话，始终可见）';
      svg.appendChild(labMain);
      var labSub = svgEl('text', { x: X0 + 4, y: topSub - 6, 'font-size': '10.5', fill: '#ba8', 'pointer-events': 'none' });
      labSub.textContent = '子代理合计（' + subStems.length + ' 个 · ' + subReqs.length + ' 请求 · 独立 y 轴，逐个见下方甘特）';
      svg.appendChild(labSub);
      // 甘特选中 agent 的首末请求参考线：贯穿两道，同色虚线（点击甘特行触发）
      if (state.selAg) {
        var lo = Infinity, hi = -Infinity;
        subReqs.forEach(function (r) {
          if (r.sub !== state.selAg) return;
          if (r.ts < lo) lo = r.ts;
          if (r.ts > hi) hi = r.ts;
        });
        if (isFinite(lo)) {
          [lo, hi].forEach(function (t) {
            if (t < state.t0 || t > state.t1) return;
            svg.appendChild(svgEl('line', {
              x1: xOf(t), y1: YTOP - 4, x2: xOf(t), y2: botAll,
              stroke: subColorOf(state.selAg), 'stroke-dasharray': '4 3',
              'stroke-width': '1.2', 'pointer-events': 'none',
            }));
          });
        }
      }
    }

    // x 轴：基线 + 6 等分 7 刻度（短跨度带秒）；两道共用，画在最底道之下
    var gAx = svgEl('g', { 'pointer-events': 'none' });
    gAx.appendChild(svgEl('line', { x1: X0, y1: botAll, x2: X1, y2: botAll, stroke: '#333' }));
    var span = state.t1 - state.t0;
    for (var ai = 0; ai <= 6; ai++) {
      var ts = state.t0 + span * ai / 6, tx = xOf(ts);
      gAx.appendChild(svgEl('line', { x1: tx, y1: botAll, x2: tx, y2: botAll + 6, stroke: '#444' }));
      var al = svgEl('text', { x: tx, y: botAll + 18, 'text-anchor': 'middle', 'font-size': '10.5', fill: '#888' });
      al.textContent = span > 6 * 3600 ? fmtTS(ts) : fmtTS(ts, span < 900);
      gAx.appendChild(al);
    }
    svg.appendChild(gAx);

    // 控制条累计（游标左侧）
    var cnt = 0, sum = 0;
    reqs.forEach(function (r) {
      if (!past(r.ts)) return;
      cnt++;
      sum += r.tokTot;
    });
    cumEl.textContent = '已看 ' + cnt + ' / ' + reqs.length + ' 次请求 · tokens ' + fmtK(sum) +
      (state.t1 - state.t0 < state.full1 - state.full0 - 1 ? ' · 已缩放' : '');
  }

  // ---- 缩放 / 平移 / 拖点区分 ----
  var drag = null, moved = false;
  svg.addEventListener('mousedown', function (ev) {
    stopViewTween(); // 补间进行中抢拖拽：从当前视窗无缝接手
    drag = { x: ev.clientX, t0: state.t0, t1: state.t1 };
    moved = false;
  });
  window.addEventListener('mousemove', function (ev) {
    if (!drag) return;
    var dx = ev.clientX - drag.x;
    if (Math.abs(dx) > 3) {
      moved = true;
      svg.classList.add('dragging');
      var wr = wrap.getBoundingClientRect();
      if (!wr.width) return;
      var span = state.t1 - state.t0;
      var dt = dx / wr.width * (1200 / (X1 - X0)) * span;
      state.t0 = drag.t0 - dt;
      state.t1 = drag.t1 - dt;
      clampView();
      draw();
    }
  });
  window.addEventListener('mouseup', function () {
    drag = null;
    svg.classList.remove('dragging');
    if (moved) setTimeout(function () { moved = false; }, 0); // click 在 mouseup 后同步触发，仍读到 moved=true
  });
  svg.addEventListener('click', function () {
    if (moved) return;              // 拖拽后的松开不算点击
    if (!state.sel) return;
    pick(null);                     // 点空白处取消选择
  });
  svg.addEventListener('wheel', function (ev) {
    ev.preventDefault();
    stopViewTween(); // 补间进行中滚轮：以当前视窗为缩放基准
    var wr = wrap.getBoundingClientRect();
    if (!wr.width) return;
    var mx = (ev.clientX - wr.left) / wr.width * 1200; // viewBox x
    var f = ev.deltaY > 0 ? 1.25 : 0.8;
    var tAt = state.t0 + (mx - X0) / (X1 - X0) * (state.t1 - state.t0);
    state.t0 = tAt - (tAt - state.t0) * f;
    state.t1 = tAt + (state.t1 - tAt) * f;
    clampView();
    draw();
  }, { passive: false });
  svg.addEventListener('dblclick', function () {
    tweenView(state.full0, state.full1);
  });

  // ---- 控制条交互 ----
  slider.addEventListener('input', function () {
    state.cursor = parseFloat(slider.value);
    draw();
  });
  playBtn.addEventListener('click', function () {
    if (tlPlayTimer) {
      stopTlPlay();
      playBtn.textContent = '▶';
      return;
    }
    playBtn.textContent = '⏸';
    var seqAtPlay = navSeq; // 捕获当前导航序号；切页后 tick 自毁
    var stepv = (state.full1 - state.full0) / 500 * 2; // 每 tick 2 步，全程约 4s
    tlPlayTimer = setInterval(function () {
      if (seqAtPlay !== navSeq) { stopTlPlay(); return; } // 已切页：定时器自毁
      var c = state.cursor + stepv;
      if (c >= state.full1) {
        c = state.full1;
        stopTlPlay();
        playBtn.textContent = '▶';
      }
      state.cursor = c;
      slider.value = String(c);
      draw();
    }, 16);
  });

  renderCards();
  renderLegend();
  renderGantt();
  renderDetail();
  draw();
}

// ---------- 配置页（#/cfg）：守护进程 config.toml 只读展示 ----------

// renderCfg GET /api/config → 分段表格。密钥行后端已脱敏（••• 已隐藏），
// 前端照单全收绝不二次猜测；找不到/解析失败也如实渲染说明，不白屏。
async function renderCfg() {
  var app = document.getElementById('app');
  var seq = ++navSeq;
  stopTlPlay();
  setNote('');
  app.textContent = '';
  var loading = document.createElement('p');
  loading.className = 'loading';
  loading.textContent = '加载中…';
  app.appendChild(loading);
  var data;
  try {
    data = await fetchJSON('/api/config');
  } catch (e) {
    if (seq !== navSeq) return;
    app.textContent = '';
    var err = document.createElement('p');
    err.className = 'error';
    err.textContent = '加载失败：' + e.message;
    app.appendChild(err);
    return;
  }
  if (seq !== navSeq) return;

  app.textContent = '';
  var head = document.createElement('div');
  head.className = 'cfg-head';
  var back = document.createElement('a');
  back.href = '#/';
  back.textContent = '← 返回会话列表';
  var title = document.createElement('span');
  title.className = 'cfg-title';
  title.textContent = '守护进程参数（只读）';
  head.appendChild(back);
  head.appendChild(title);
  app.appendChild(head);

  var meta = document.createElement('div');
  meta.className = 'cfg-meta';
  meta.textContent = data.found
    ? '文件：' + data.path + (data.mtime ? ' · 最后修改 ' + data.mtime : '')
    : '';
  if (meta.textContent) app.appendChild(meta);

  if (!data.ok) {
    var warn = document.createElement('p');
    warn.className = 'cfg-warn';
    warn.textContent = data.note || '配置不可读';
    app.appendChild(warn);
  } else {
    (data.sections || []).forEach(function (sec) {
      var box = document.createElement('div');
      box.className = 'cfg-sec';
      var h = document.createElement('h3');
      h.textContent = '[' + sec.name + ']';
      box.appendChild(h);
      var tb = document.createElement('table');
      tb.className = 'cfg-table';
      var tbody = document.createElement('tbody');
      (sec.rows || []).forEach(function (r) {
        var tr = document.createElement('tr');
        if (r.redact) tr.className = 'redacted';
        var k = document.createElement('td');
        k.className = 'k';
        k.textContent = r.key;
        var v = document.createElement('td');
        v.className = 'v';
        v.textContent = r.value;
        v.title = r.redact ? '形似密钥的键已脱敏，页面永远不显示明文' : '';
        tr.appendChild(k);
        tr.appendChild(v);
        tbody.appendChild(tr);
      });
      tb.appendChild(tbody);
      box.appendChild(tb);
      app.appendChild(box);
    });
    if (data.note) {
      var note = document.createElement('p');
      note.className = 'cfg-note';
      note.textContent = data.note;
      app.appendChild(note);
    }
  }

  // 查看器自身的固定口径（非 config.toml，写在前端）：一并交代，参数问题一站式可查
  var viewerNote = document.createElement('p');
  viewerNote.className = 'cfg-note';
  viewerNote.textContent = '另：时间线页的积分口径与 TTL 是查看器前端的固定值（GLM 价 6.9/1.7/24 每万 tokens、'
    + 'TTL 默认 600 秒），不随 config.toml 变；TTL 可在时序页图例行临时改（只影响当次显示）。';
  app.appendChild(viewerNote);
  fadeIn(app);
}

// ---------- 路由 ----------

// route 按 hash 前缀分发：#/t/<lineage> → renderTimeline；#/cfg → renderCfg；
// 其余（#/ 或空）→ renderList。
// 畸形 % 序列（如手输 #/t/%zz）decode 抛 URIError → 回退原样串，时序页以"暂无数据"兜底。
function route() {
  var h = location.hash || '#/';
  if (h === '#/cfg') {
    renderCfg();
    return;
  }
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
