# 001 — 动效 token 基建（曲线变量 + 降动效地基）

- **Status**: DONE
- **Commit**: 8cfffc1
- **Severity**: LOW
- **Category**: Cohesion & tokens
- **Estimated scope**: 1 文件（style.css，+12 行）

## Problem

仓库没有任何缓动/时长 token（style.css 全文 0 个 transition/animation）。后续所有动效计划（002~005）若各写各的曲线必然漂移。本计划先立唯一曲线源，并定下降动效（prefers-reduced-motion）的处理原则：**位移类停、颜色/透明度类保留**。

## Target

在 `cmd/ferryman/web/style.css` 的 `body` 规则之后插入：

```css
/* ---------- 动效 token：全仓唯一曲线源（值出自 emil 动效审计手册，禁止近似） ---------- */
:root {
  --ease-out: cubic-bezier(0.23, 1, 0.32, 1);     /* 入场/退出：先快后缓 */
  --ease-in-out: cubic-bezier(0.77, 0, 0.175, 1); /* 屏内移动：两端缓 */
}
```

降动效原则（各计划自带具体规则，此处只立地基，不写全局一刀切）：
- `transform` 类（按压缩放、scaleX 生长、视窗补间）→ reduce 下禁用，瞬达
- `opacity` / 颜色类（淡入、hover 色过渡）→ reduce 下保留（助理解，非运动）

## Repo conventions to follow

- style.css 分节注释风格：`/* ---------- 节名 ---------- */`（见 `style.css:92` 时序页分节）
- 无 CSS 变量先例——本计划即先例，后续一律引用 token 不写裸曲线

## Steps

1. `cmd/ferryman/web/style.css` 在 `body { … }` 规则（第 5-11 行）之后插入上述 `:root` 块与分节注释。

## Boundaries

- 只动 style.css，不碰 app.js / index.html
- 不加任何 transition/animation 本体（那是 002~005 的活）
- 不引入外部资源（离线铁律）

## Verification

- **Mechanical**: 无（纯变量声明）
- **Feel check**: DevTools 控制台 `getComputedStyle(document.documentElement).getPropertyValue('--ease-out')` 返回 `cubic-bezier(0.23, 1, 0.32, 1)`
- **Done when**: 变量存在且后续计划全部引用 token
