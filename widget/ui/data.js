/**
 * 票 03 · 数据层：契约 v0 演示副本 + 取数接口。
 *
 * 职责边界：渲染层（app.js）只经本模块拿数据——换一份契约 JSON 即换全部显示，
 * 演示值不散落在渲染代码里。provenance 不进契约（spec rev1），文案映射在 app.js。
 *
 * 前端栈铁律（spec rev1）：vanilla ES 模块 + JSDoc 承载类型，零构建链、零外部引用。
 *
 * @typedef {'window_5h'|'week'|'month_tokens'|'balance_cny'|'spend_today_cny'|'spend_week_cny'|'spend_month_cny'} MetricKey
 * @typedef {'fetched'|'estimated'} MetricSource
 *
 * @typedef {Object} Metric 契约 v0 metric 元素
 * @property {MetricKey} key
 * @property {number} [remaining_pct] 剩余 %（环指标）
 * @property {string} [text] 展示文本（文字指标，如「月 3.2M tok」）
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
        { key: 'month_tokens', text: '月 3.2M tok', source: 'estimated', as_of: '12:03' },
      ],
    },
    {
      id: 'kimi', label: 'Kimi', kind: 'coding_plan', plan: '',
      metrics: [
        { key: 'window_5h', remaining_pct: 80, abs: '1200 / 1500', resets_at: '2026-09-21T16:05:00+08:00', source: 'fetched', as_of: '12:02' },
        { key: 'week', remaining_pct: 17, abs: '850 / 5000', resets_at: '2026-09-28T00:00:00+08:00', source: 'fetched', as_of: '12:02' },
        { key: 'month_tokens', text: '月 5.1M tok', source: 'estimated', as_of: '12:03' },
      ],
    },
    {
      id: 'deepseek', label: 'DeepSeek', kind: 'paygo',
      metrics: [
        { key: 'balance_cny', text: '¥87.50', breakdown: { granted: '10.00', topped_up: '77.50' }, available: true, source: 'fetched', as_of: '12:01' },
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
 * 30s 轮询框架。demo 模式对内嵌副本空转（可选注入灰化）；live 模式取
 * window.__WIDGET_DAEMON_URL__（票 08 接线前为空=不可达，如实灰化）。
 * 立即回调一次，随后每 POLL_MS 一次。
 *
 * @param {(summary:Summary|null, reachable:boolean)=>void} onUpdate
 * @returns {()=>void} stop()
 */
export function startPolling(onUpdate) {
  async function tick() {
    if (isDev()) {
      onUpdate(DEMO_SUMMARY, !forceGray());
      return;
    }
    const url = String(window.__WIDGET_DAEMON_URL__ || '');
    if (!url) { onUpdate(null, false); return; } // 端点未接线：不可达，不假造
    try {
      const r = await fetch(url, { signal: AbortSignal.timeout(5000) });
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
