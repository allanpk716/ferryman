/**
 * 票 04 · 显示配置（display profile）纯逻辑层。
 *
 * 被 app.js（主窗渲染）、settings.js（设置窗编辑）与 node 断言（tests/assert-static.mjs
 * 直接 import 跑纯函数）三方共享——因此本模块**禁止任何 DOM/window/location 顶层访问**。
 *
 * 持久化：widget 本地 JSON（Rust 命令 get_profile/save_profile 读写
 * app_data_dir()/profile.json），daemon 零感知。文件损坏/缺失时 Rust 返回
 * profile=null + reset_reason，前端经 normalizeProfile(null) 回落默认并如实提示。
 *
 * 形状（JSDoc 即契约）：
 * @typedef {'vertical'|'horizontal'} Layout
 *
 * @typedef {Object} Thresholds 告警阈值（%）；红阈=更紧急=更低
 * @property {number} yellow 默认 20
 * @property {number} red 默认 10
 *
 * @typedef {Object} DsBudget DeepSeek 预算环
 * @property {boolean} enabled
 * @property {number} amount_cny 预算金额（元/月）
 *
 * @typedef {Object} ObjectProfile 单对象显示配置（按契约 id 键控；id=渡口上游表条目名，
 *           spec F6 已注记渡口合并前后条目名失配风险，不在本票处理）
 * @property {boolean} [visible] 显示开关（默认 true）
 * @property {{window_5h?:string, week?:string, month_budget?:string, ds_budget?:string}} [ring_colors]
 *           环基色覆写（缺省=主题默认色）
 * @property {Thresholds} [thresholds] 告警阈值（默认黄 20 / 红 10）
 * @property {number} [month_budget] GLM/Kimi 月预算分母（单位=契约原值口径 tok；不设=文字计数现状）
 * @property {DsBudget} [ds_budget] DeepSeek 预算环（不设=无绿环）
 * @property {number} [order] 排序（小在前；缺省=契约顺序）
 *
 * @typedef {Object} Profile
 * @property {Layout} layout 竖排（默认）/横排
 * @property {boolean} show_countdown 倒计时行开关（默认 true）
 * @property {Record<string, ObjectProfile>} objects
 *
 * @typedef {Object} Normalized 归一化结果
 * @property {Profile} profile
 * @property {boolean} repaired 输入含无效项被修复（缺失文件不算修复，由 reset_reason 表达）
 */

/** 环基色的四个键（ring_colors 允许覆写的范围）。 */
export const RING_KEYS = ['window_5h', 'week', 'month_budget', 'ds_budget'];

const isPlainObj = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);
const sameJson = (a, b) => JSON.stringify(a) === JSON.stringify(b);

/**
 * 归一化阈值：数值钳 0..100，红阈不高于黄阈；完全非法返回 null。
 * 单边缺省补默认（黄 20 / 红 10），补齐与钳位都算「修复」。
 * @param {*} t
 * @returns {Thresholds|null}
 */
function sanitizeThresholds(t) {
  if (!isPlainObj(t)) return null;
  let y = typeof t.yellow === 'number' && isFinite(t.yellow) ? Math.min(100, Math.max(0, t.yellow)) : null;
  let r = typeof t.red === 'number' && isFinite(t.red) ? Math.min(100, Math.max(0, t.red)) : null;
  if (y === null && r === null) return null;
  if (y === null) y = 20;
  if (r === null) r = 10;
  if (r > y) r = y;
  return { yellow: y, red: r };
}

/**
 * 净化单对象条目：只保留「提供了且合法」的字段（缺省字段不回填——默认值由
 * effectiveObject 在读取端补齐，保持存量与输入可比、repaired 不误报）。
 * @param {*} o
 * @returns {{out:ObjectProfile, dirty:boolean}} dirty=有字段非法被丢弃/改写
 */
function sanitizeObject(o) {
  const out = /** @type {ObjectProfile} */ ({});
  let dirty = false;
  if ('visible' in o) {
    if (typeof o.visible === 'boolean') out.visible = o.visible;
    else dirty = true; // 非法丢弃 → 读取端默认 true
  }
  if ('thresholds' in o) {
    const th = sanitizeThresholds(o.thresholds);
    if (th && sameJson(th, o.thresholds)) out.thresholds = th;
    else if (th) { out.thresholds = th; dirty = true; }
    else dirty = true;
  }
  if ('month_budget' in o) {
    if (typeof o.month_budget === 'number' && isFinite(o.month_budget) && o.month_budget > 0) {
      out.month_budget = o.month_budget;
    } else dirty = true;
  }
  if ('ds_budget' in o) {
    if (isPlainObj(o.ds_budget)) {
      const amt = o.ds_budget.amount_cny;
      const ds = {
        enabled: o.ds_budget.enabled === true,
        amount_cny: typeof amt === 'number' && isFinite(amt) && amt > 0 ? amt : 0,
      };
      if (sameJson(ds, o.ds_budget)) out.ds_budget = ds;
      else { out.ds_budget = ds; dirty = true; }
    } else dirty = true;
  }
  if ('ring_colors' in o) {
    if (isPlainObj(o.ring_colors)) {
      const rc = {};
      for (const k of RING_KEYS) {
        const v = o.ring_colors[k];
        if (typeof v === 'string' && v.trim()) rc[k] = v.trim();
      }
      if (Object.keys(rc).length > 0) {
        if (sameJson(rc, o.ring_colors)) out.ring_colors = rc;
        else { out.ring_colors = rc; dirty = true; }
      } else dirty = true; // 全部键非法 → 丢弃
    } else dirty = true;
  }
  if ('order' in o) {
    if (typeof o.order === 'number' && isFinite(o.order)) out.order = o.order;
    else dirty = true;
  }
  return { out, dirty };
}

/**
 * 归一化 profile：任意脏输入（null/损坏 JSON/半残对象）→ 合法 Profile。
 * repaired 只在「提供了但非法」时为真（缺文件/空配置不算修复，由 reset_reason 表达）。
 * @param {*} raw 存储层原始值（null=缺失/损坏）
 * @returns {Normalized}
 */
export function normalizeProfile(raw) {
  let repaired = false;
  if (raw !== null && raw !== undefined && !isPlainObj(raw)) repaired = true;
  const src = isPlainObj(raw) ? raw : null;
  let layout = 'vertical';
  if (src && (src.layout === 'horizontal' || src.layout === 'vertical')) layout = src.layout;
  else if (src && 'layout' in src) repaired = true;
  let show_countdown = true;
  if (src && src.show_countdown === false) show_countdown = false;
  else if (src && 'show_countdown' in src && src.show_countdown !== true) repaired = true;
  const objects = {};
  if (src && isPlainObj(src.objects)) {
    for (const [id, o] of Object.entries(src.objects)) {
      if (!isPlainObj(o)) { repaired = true; continue; }
      const { out, dirty } = sanitizeObject(o);
      objects[id] = out;
      if (dirty) repaired = true;
    }
  } else if (src && 'objects' in src) repaired = true;
  return { profile: { layout, show_countdown, objects }, repaired };
}

/**
 * 生效的单对象配置：profile 缺项逐级回落默认（渲染端唯一入口，永远返回全字段）。
 * @param {Profile|null} prof
 * @param {string} id
 * @returns {{visible:boolean, ring_colors:Object, thresholds:Thresholds,
 *            month_budget:number|null, ds_budget:DsBudget|null, order:number|null}}
 */
export function effectiveObject(prof, id) {
  const o = prof && prof.objects && prof.objects[id] ? prof.objects[id] : {};
  return {
    visible: o.visible !== false,
    ring_colors: o.ring_colors && typeof o.ring_colors === 'object' ? o.ring_colors : {},
    thresholds: sanitizeThresholds(o.thresholds) || { yellow: 20, red: 10 },
    month_budget: typeof o.month_budget === 'number' && isFinite(o.month_budget) && o.month_budget > 0
      ? o.month_budget : null,
    ds_budget: o.ds_budget && o.ds_budget.enabled === true &&
      typeof o.ds_budget.amount_cny === 'number' && isFinite(o.ds_budget.amount_cny) &&
      o.ds_budget.amount_cny > 0
      ? { enabled: true, amount_cny: o.ds_budget.amount_cny } : null,
    order: typeof o.order === 'number' && isFinite(o.order) ? o.order : null,
  };
}

/**
 * 预算剩余%（剩余制：1−已用/预算，钳 0..100）。
 * @param {number|null|undefined} used 已用原始值（契约原值口径：tok 或元）
 * @param {number|null|undefined} budget 预算分母
 * @returns {number|null} 预算未设/非法/契约无数值 → null（不画环，维持现状）
 */
export function remainingPctOfBudget(used, budget) {
  if (typeof used !== 'number' || !isFinite(used)) return null;
  if (typeof budget !== 'number' || !isFinite(budget) || budget <= 0) return null;
  const pct = (1 - used / budget) * 100;
  // 两位小数取整：吸收浮点尾差（如 1−0.8 → 19.999…96），环几何与告警判定都吃干净值
  return Math.max(0, Math.min(100, Math.round(pct * 100) / 100));
}

/**
 * 告警层级（阈值驱动；与 mock 同语义：<red 红、<yellow 黄、否则基色）。
 * @param {number|null} pct
 * @param {Thresholds} th
 * @returns {'red'|'yellow'|'base'}
 */
export function alertLevel(pct, th) {
  if (pct == null) return 'base';
  if (pct < th.red) return 'red';
  if (pct < th.yellow) return 'yellow';
  return 'base';
}

/**
 * 环描边色：对象基色覆写 → 阈值告警色 → 基色。
 * @param {string} key 环键（window_5h/week/month_budget/ds_budget）
 * @param {number|null} pct 剩余 %（null=无环指标，回落基色）
 * @param {{ring_colors:Object, thresholds:Thresholds}} obj effectiveObject 的产物
 * @param {{base:Record<string,string>, yellow:string, red:string}} palette 主题色（调用方从 CSS 变量取）
 * @returns {string} CSS 颜色
 */
export function ringStrokeColor(key, pct, obj, palette) {
  const base = (obj.ring_colors && obj.ring_colors[key]) || palette.base[key] || palette.base.window_5h;
  const lv = alertLevel(pct, obj.thresholds);
  return lv === 'red' ? palette.red : lv === 'yellow' ? palette.yellow : base;
}

/**
 * 显示排序 + 显隐过滤（稳定排序：order 相同/缺省保持契约顺序）。
 * @param {Array<{id:string}>} entries 契约顺序的对象数组（upstreams + handoff）
 * @param {Profile|null} prof
 * @returns {Array<{id:string}>}
 */
export function orderedVisible(entries, prof) {
  return entries
    .map((p, i) => ({ p, i, e: effectiveObject(prof, p.id) }))
    .filter((x) => x.e.visible)
    .sort((a, b) => (a.e.order ?? a.i) - (b.e.order ?? b.i))
    .map((x) => x.p);
}

/**
 * 测试/演示注入预设（URL ?profile= 参数选；产品路径不带该参数、永不命中）。
 * budgets：GLM 月预算 4,000,000 tok（剩 20% 紫环）+ 5h 基色覆写；Kimi 月预算 8,000,000
 * （已用 5.1M→剩 36.25%）+ 阈值覆写 85/70；DS 预算 ¥100（月花 58.6→剩 41.4% 绿环）；
 * handoff order=-1 提前。hidden：glm/handoff 关显隐。
 * @type {Record<string, Profile>}
 */
export const PRESETS = {
  budgets: {
    layout: 'vertical',
    show_countdown: true,
    objects: {
      glm: { visible: true, ring_colors: { window_5h: '#E056FD' },
             thresholds: { yellow: 20, red: 10 }, month_budget: 4000000, order: 0 },
      kimi: { visible: true, thresholds: { yellow: 85, red: 70 }, month_budget: 8000000, order: 1 },
      deepseek: { visible: true, thresholds: { yellow: 20, red: 10 },
                  ds_budget: { enabled: true, amount_cny: 100 }, order: 2 },
      handoff: { visible: true, order: -1 },
    },
  },
  hidden: {
    layout: 'vertical',
    show_countdown: true,
    objects: {
      glm: { visible: false },
      handoff: { visible: false },
    },
  },
};
