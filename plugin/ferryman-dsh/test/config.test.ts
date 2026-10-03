// 票05 · 配置解析 TDD——daemonToken 读取接线（票03 遗留 TODO）＋环境变量约定。
// 约定钉点（Ferryman 仓内现行钩子脚本,只读）：
//   - token 文件缺省 ~/ferryman/daemon.token,FERRYMAN_TOKEN_FILE 覆盖
//     （hooks/ferryman-gate-codex.ps1:19-20）;
//   - 端口 FERRYMAN_PORT 覆盖缺省 15700（ferryman-gate-codex.ps1:7/18）;
//   - FERRYMAN_DISABLE=1 短路（ferryman-gate-codex.ps1:9 fail-open 总开关同款）。
import { test } from "node:test";
import assert from "node:assert/strict";
import { resolveConfig } from "../src/config.ts";
import { MIN_DAEMON_VERSION } from "../src/selfcheck.ts";

const DEFAULT_FILE = "C:/Users/t/ferryman/daemon.token";

function baseEnv(over: Record<string, string> = {}): Record<string, string> {
  return { USERPROFILE: "C:/Users/t", ...over };
}

function readFileMap(files: Record<string, string>) {
  return (p: string): string | null => files[p] ?? null;
}

test("缺省解析：15700 管理口＋缺省 token 文件读取（trim 尾白）", () => {
  const cfg = resolveConfig({}, baseEnv(), readFileMap({ [DEFAULT_FILE]: "tok-abc\n" }));
  assert.equal(cfg.daemonURL, "http://127.0.0.1:15700");
  assert.equal(cfg.daemonToken, "tok-abc");
  assert.equal(cfg.disabled, false);
  assert.equal(cfg.minDaemonVersion, MIN_DAEMON_VERSION);
});

test("FERRYMAN_PORT 覆盖端口;非法值回缺省", () => {
  assert.equal(
    resolveConfig({}, baseEnv({ FERRYMAN_PORT: "15701" }), readFileMap({ [DEFAULT_FILE]: "t" })).daemonURL,
    "http://127.0.0.1:15701",
  );
  assert.equal(
    resolveConfig({}, baseEnv({ FERRYMAN_PORT: "not-a-port" }), readFileMap({ [DEFAULT_FILE]: "t" })).daemonURL,
    "http://127.0.0.1:15700",
  );
});

test("FERRYMAN_TOKEN_FILE 覆盖 token 文件路径", () => {
  const cfg = resolveConfig({}, baseEnv({ FERRYMAN_TOKEN_FILE: "D:/alt/token.txt" }),
    readFileMap({ "D:/alt/token.txt": "tok-alt" }));
  assert.equal(cfg.daemonToken, "tok-alt");
});

test("config.daemonToken 显式值优先于文件;文件不可读 → 空串（自检呈鉴权失败）", () => {
  assert.equal(
    resolveConfig({ daemonToken: "explicit" }, baseEnv(), readFileMap({ [DEFAULT_FILE]: "file-tok" })).daemonToken,
    "explicit",
  );
  assert.equal(resolveConfig({}, baseEnv(), () => null).daemonToken, "");
});

test("HOME 回退（非 Windows 形）;USERPROFILE/HOME 均无 → 仍给可预期路径（读失败即空 token）", () => {
  const homeEnv = { HOME: "/home/u" };
  const cfg = resolveConfig({}, homeEnv, readFileMap({ "/home/u/ferryman/daemon.token": "tok-home" }));
  assert.equal(cfg.daemonToken, "tok-home");
  const none = resolveConfig({}, {}, () => null);
  assert.equal(none.daemonToken, "");
});

test("FERRYMAN_DISABLE=1 / config.disable=true → disabled", () => {
  assert.equal(resolveConfig({}, baseEnv({ FERRYMAN_DISABLE: "1" }), () => null).disabled, true);
  assert.equal(resolveConfig({}, baseEnv({ FERRYMAN_DISABLE: "0" }), () => null).disabled, false);
  assert.equal(resolveConfig({ disable: true }, baseEnv(), () => null).disabled, true);
});
