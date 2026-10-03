// 版本解析与比较——照抄 Ferryman internal/update/semver.go 的语义（手写、零依赖）：
//   - ParseSemver（semver.go:19-52）：TrimSpace → 去 v 前缀 → 剥 +build 元数据 →
//     按 - 拆核心/预发布 → 必须恰好三段数字，否则解析失败（"dev"/commit 描述都算失败，
//     上层按「无从比较」跳过，同 update.go:134 CheckDevBuild）。
//   - Compare（semver.go:55-96）：major→minor→patch 逐段；无预发布 > 同号有预发布；
//     预发布标识「数字 < 字母、数字按数值、字母按字典序」。

export interface Semver {
  major: number;
  minor: number;
  patch: number;
  /** 预发布点分标识，如 ["alpha","1"]；无预发布为空数组 */
  pre: string[];
}

/** 解析失败返回 null（调用方语义：无从比较，不视为故障） */
export function parseVersion(input: string): Semver | null {
  let t = input.trim();
  if (t.startsWith("v")) t = t.slice(1);
  const plus = t.indexOf("+");
  if (plus >= 0) t = t.slice(0, plus);
  let core = t;
  let pre: string[] = [];
  const dash = t.indexOf("-");
  if (dash >= 0) {
    core = t.slice(0, dash);
    pre = t.slice(dash + 1).split(".");
  }
  const segs = core.split(".");
  if (segs.length !== 3) return null;
  const nums: number[] = [];
  for (const s of segs) {
    if (!/^\d+$/.test(s)) return null;
    nums.push(Number(s));
  }
  return { major: nums[0]!, minor: nums[1]!, patch: nums[2]!, pre };
}

function compareParsed(a: Semver, b: Semver): number {
  const core = [a.major - b.major, a.minor - b.minor, a.patch - b.patch];
  for (const d of core) {
    if (d !== 0) return Math.sign(d);
  }
  if (a.pre.length === 0 && b.pre.length === 0) return 0;
  if (a.pre.length === 0) return 1; // 无预发布 > 有预发布
  if (b.pre.length === 0) return -1;
  const n = Math.max(a.pre.length, b.pre.length);
  for (let i = 0; i < n; i++) {
    const x = a.pre[i];
    const y = b.pre[i];
    if (x === undefined) return -1; // 标识更少 < 标识更多（前缀相同时）
    if (y === undefined) return 1;
    const xNum = /^\d+$/.test(x);
    const yNum = /^\d+$/.test(y);
    if (xNum && yNum) {
      const d = Number(x) - Number(y);
      if (d !== 0) return Math.sign(d);
    } else if (xNum !== yNum) {
      return xNum ? -1 : 1; // 数字 < 字母
    } else if (x !== y) {
      return x < y ? -1 : 1;
    }
  }
  return 0;
}

/** 两串都能解析返回 -1/0/1；任一解析失败返回 null（无从比较） */
export function compareVersions(a: string, b: string): number | null {
  const pa = parseVersion(a);
  const pb = parseVersion(b);
  if (pa === null || pb === null) return null;
  return compareParsed(pa, pb);
}
