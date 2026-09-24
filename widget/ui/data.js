/**
 * 票 03 · 数据层：契约 v0 演示副本 + 取数接口。
 *
 * 职责边界：渲染层（app.js）只经本模块拿数据——换一份契约 JSON 即换全部显示，
 * 演示值不散落在渲染代码里。provenance 不进契约（spec rev1），文案映射在 app.js。
 *
 * 前端栈铁律（spec rev1）：vanilla ES 模块 + JSDoc 承载类型，零构建链、零外部引用。
 *
 * @typedef {'window_5h'|'week'|'month_tokens'|'balance_cny'|'spend_today_cny'|'spend_week_cny'|'spend_month_cny'|'handoffs_month'|'handoffs_week'} MetricKey
 * @typedef {'fetched'|'estimated'} MetricSource
 *
 * @typedef {Object} Metric 契约 v0 metric 元素
 * @property {MetricKey} key
 * @property {number} [remaining_pct] 剩余 %（环指标）
 * @property {string} [text] 展示文本（文字指标，如「月 3.2M tok」）
 * @property {number} [value] 文字指标的原始数值（v0 之外的附加字段，向前兼容读取）：
 *            month_tokens=本月 tok 数；spend_*=CNY 金额——票 04 预算环的分母计算（剩余制）用它
 * @property {string} [abs] 绝对数展示（如「1200 / 1500」）
 * @property {{granted:string, topped_up:string}} [breakdown] 余额拆分（DS 赠送/充值）
 * @property {boolean} [available] v0 之外的附加字段（DS 余额可用性）；widget 向前兼容读取
 * @property {MetricSource} source 查询值 fetched / 台账估算值 estimated
 * @property {string} [as_of] 最后更新时刻（展示串，如「12:03」）
 * @property {string} [resets_at] 距重置时刻（ISO 8601）
 *
 * @typedef {'coding_plan'|'paygo'|'handoff'} UpstreamKind
 *
 * @typedef {Object} Upstream 契约 v0 upstream 元素（handoff 同形状、单对象）
 * @property {string} id 稳定 id = 渡口上游表条目名
 * @property {UpstreamKind} kind
 * @property {string} label 展示名
 * @property {string} [plan] 套餐名（coding_plan）
 * @property {string} [note] 版本语义注记（2026-09-25 追加；如智谱 V1 套餐
 *           无周/月配额窗——周环缺席是套餐本身无此限制，非数据缺失）
 * @property {Metric[]} metrics
 * @property {{category:string, message?:string}} [error] 该上游整体查询失败（类别永不含钥/URL）
 *
 * @typedef {Object} Summary 契约 v0 顶层
 * @property {number} version
 * @property {string} generated_at 生成时刻（ISO 8601）
 * @property {Upstream[]} upstreams
 * @property {Upstream} handoff
 */

/**
 * 契约 v0 演示副本（与 mock 内嵌 JSON 同一份事实；无 provenance 键——文案映射在渲染层）。
 * @type {Summary}
 */
export const DEMO_SUMMARY = {
  version: 1,
  generated_at: '2026-09-21T12:03:41+08:00',
  upstreams: [
    {
      id: 'glm', label: 'GLM', kind: 'coding_plan', plan: 'Pro',
      metrics: [
        { key: 'window_5h', remaining_pct: 62, resets_at: '2026-09-21T14:32:00+08:00', source: 'fetched', as_of: '12:03' },
        { key: 'week', remaining_pct: 8, resets_at: '2026-09-28T00:00:00+08:00', source: 'fetched', as_of: '12:03' },
        { key: 'month_tokens', text: '月 3.2M tok', value: 3200000, source: 'estimated', as_of: '12:03' },
      ],
    },
    {
      id: 'kimi', label: 'Kimi', kind: 'coding_plan', plan: '',
      metrics: [
        { key: 'window_5h', remaining_pct: 80, abs: '1200 / 1500', resets_at: '2026-09-21T16:05:00+08:00', source: 'fetched', as_of: '12:02' },
        { key: 'week', remaining_pct: 17, abs: '850 / 5000', resets_at: '2026-09-28T00:00:00+08:00', source: 'fetched', as_of: '12:02' },
        { key: 'month_tokens', text: '月 5.1M tok', value: 5100000, source: 'estimated', as_of: '12:03' },
      ],
    },
    {
      id: 'deepseek', label: 'DeepSeek', kind: 'paygo',
      metrics: [
        { key: 'balance_cny', text: '¥87.50', breakdown: { granted: '10.00', topped_up: '77.50' }, available: true, source: 'fetched', as_of: '12:01' },
        { key: 'spend_month_cny', text: '月 ¥58.60', value: 58.6, source: 'estimated', as_of: '12:03' }, // 票 04：DS 预算环的已用值（月口径）
        { key: 'spend_today_cny', text: '今 ¥3.10', source: 'estimated', as_of: '12:03' },
        { key: 'spend_week_cny', text: '周 ¥22.40', source: 'estimated', as_of: '12:03' },
      ],
    },
  ],
  handoff: {
    id: 'handoff', label: '摆渡', kind: 'handoff',
    metrics: [
      { key: 'spend_month_cny', text: '月 ¥12.80', source: 'estimated', as_of: '12:03' },
      { key: 'spend_week_cny', text: '周 ¥4.20', source: 'estimated', as_of: '12:03' },
    ],
  },
};

/** 轮询周期（spec：30s 轮询 /widget/summary）。 */
export const POLL_MS = 30000;

const params = new URLSearchParams(typeof location !== 'undefined' ? location.search : '');

/**
 * dev 旗标：URL ?dev=1（Rust 侧 debug 构建经 navigate 注入；测试脚本同机制）
 * 或 window.__WIDGET_DEV__（预留给 eval 注入路径）。release 两者皆无。
 * dev=演示数据 + 角标；release=live 取数（daemon 不可达即整体灰化，不假造数据）。
 * @returns {boolean}
 */
export function isDev() {
  return params.has('dev') || window.__WIDGET_DEV__ === true;
}

/** 强制灰化（演示/断言注入）。 @returns {boolean} */
export function forceGray() {
  return params.has('gray');
}

/**
 * 票 05 · 契约防御注入（?superset=1，仅断言/演示语境）：把演示副本包成「契约超集」——
 * 未知顶层/上游字段 + 更高 version + 未知 metric key。widget 对超集必须向前兼容：
 * 不崩、未知字段被忽略（assert-static.mjs ⑫ 组断言背书）。实现上零特判：渲染层只按
 * 已知 key 取值，天然吃掉未知字段——本函数只是把「喂超集」这一动作做成可断言的入口。
 * 产品路径（无该参数）零影响；live 取数路径同样天然兼容。
 * @param {Summary} summary
 * @returns {Summary}
 */
export function maybeSuperset(summary) {
  if (!params.has('superset')) return summary;
  const c = structuredClone(summary);
  c.version = (c.version || 0) + 998; // 更高 version：widget 不设版本闸门，照常渲染
  c.experimental_rollout = { mode: 'canary' }; // 未知顶层字段 → 忽略
  c.schema_ext = ['future.field.v9']; // 未知顶层字段 → 忽略
  const glm = c.upstreams.find((u) => u.id === 'glm');
  if (glm) {
    glm.future_flag = true; // 上游级未知字段 → 忽略
    // 未知 metric key：环/倒计时按已知 key 取值自然跳过；详情卡如实列出（provenance 无映射则留空）
    glm.metrics.push({
      key: 'window_10h', remaining_pct: 50,
      resets_at: '2026-09-21T19:00:00+08:00', source: 'fetched', as_of: '12:03',
    });
  }
  return c;
}

// ── 时钟：倒计时本地推算的基准 ──
// live：真实时钟。demo：锚点=generated_at（载入时刻对齐，随真实分钟推进，
// 使「倒计时走字」可观察）；?static=1 冻结于 generated_at（断言确定性）。
const DEMO_BASE_MS = Date.parse(DEMO_SUMMARY.generated_at);
const LOAD_MS = Date.now();

/**
 * 当前基准时刻（ms epoch）。
 * @returns {number}
 */
export function now() {
  if (isDev()) {
    return params.has('static') ? DEMO_BASE_MS : DEMO_BASE_MS + (Date.now() - LOAD_MS);
  }
  return Date.now();
}

/**
 * 票 09 · live 取数目标解析：
 * 壳内=invoke get_daemon_config（Rust 每次现读 ~/ferryman/daemon.token——daemon
 * 重启换 token 后下次轮询自动生效；端点=127.0.0.1:7311/widget/summary）；
 * 浏览器/测试语境=window.__WIDGET_DAEMON_URL__ 注入（缺省=不可达，如实灰化）。
 * @returns {Promise<{url:string, token:string}|null>} null=不可达
 */
async function resolveDaemonTarget() {
  const t = window.__TAURI__;
  if (t && t.core && t.core.invoke) {
    try {
      const cfg = await t.core.invoke('get_daemon_config');
      if (cfg && typeof cfg.url === 'string' && cfg.url) return cfg;
    } catch { /* 读 token 失败：如实灰化（不假造可达） */ }
    return null;
  }
  const url = String(window.__WIDGET_DAEMON_URL__ || '');
  return url ? { url, token: String(window.__WIDGET_TOKEN__ || '') } : null;
}

/**
 * 30s 轮询框架。demo 模式对内嵌副本空转（可选注入灰化）；live 模式经
 * resolveDaemonTarget 拿端点+Bearer token（票 09 接线；壳外无注入=不可达，
 * 如实灰化）。立即回调一次，随后每 POLL_MS 一次。
 *
 * @param {(summary:Summary|null, reachable:boolean)=>void} onUpdate
 * @returns {()=>void} stop()
 */
export function startPolling(onUpdate) {
  async function tick() {
    if (isDev()) {
      onUpdate(maybeSuperset(DEMO_SUMMARY), !forceGray());
      return;
    }
    const target = await resolveDaemonTarget();
    if (!target) { onUpdate(null, false); return; }
    try {
      const r = await fetch(target.url, {
        headers: target.token ? { Authorization: `Bearer ${target.token}` } : {},
        signal: AbortSignal.timeout(8000), // daemon 冷缓存最长 5s 外呼+装配，留 8s
      });
      if (!r.ok) throw new Error(String(r.status));
      onUpdate(/** @type {Summary} */ (await r.json()), true);
    } catch {
      onUpdate(null, false); // daemon 不可达=整体灰化+⚠（不假造数据）
    }
  }
  tick(); // 立即一次（同步落定首屏）
  const h = setInterval(tick, POLL_MS);
  return () => clearInterval(h);
}
