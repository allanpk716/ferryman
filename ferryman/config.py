"""Ferryman 配置：~/ferryman/config.toml（可选）+ 内置默认 + 启动校验（DESIGN §4）。

校验铁律（违例拒启）：
  - summarize_threshold < block_threshold（严格小于，按 Agent 分组）；
  - block_threshold − summarize_threshold ≥ 2min（独立硬约束，不依赖 SLA 定义）；
  - gate_mode ∈ {off, observe, enforce}。
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

GATE_MODES = ("off", "observe", "enforce")
CONFIG_PATH = Path.home() / "ferryman" / "config.toml"


@dataclass
class WatchCfg:
    poll_interval_s: float = 3.0
    cc_projects_dir: str = ""            # 空 = ~/.claude/projects（测试可指临时目录）
    codex_sessions_dir: str = ""         # 空 = ~/.codex/sessions
    # 额外 codex 会话目录（2026-09-17 实测发现：经 Orca 启动的 codex 把 CODEX_HOME
    # 重定向到 %APPDATA%\orca\codex-runtime-home\home\sessions——不扫则这些会话
    # gate 能收到但永远不被摆渡）。Orca 目录存在时自动追加，无需配置。
    codex_extra_dirs: list[str] = field(default_factory=list)


@dataclass
class ThresholdCfg:
    summarize_s: float = 25 * 60
    block_s: float = 35 * 60             # E0a 实测拐点+5min（reports/e0a-cc-glm.md）
    min_ctx_tokens: int = 20_000


@dataclass
class ServerCfg:
    port: int = 7311
    data_dir: str = ""                   # 空 = ~/ferryman（token/handoffs/index 所在地）


@dataclass
class NotifyCfg:
    enabled: bool = False                # 默认关：未配置不响，测试套件不弹 toast/不出网（T25）
    pushover: bool = True
    pushover_token: str = ""             # 空 → 回落环境变量 PUSHOVER_TOKEN
    pushover_user: str = ""              # 空 → 回落环境变量 PUSHOVER_USER
    toast: bool = True


@dataclass
class Config:
    gate_cc: str = "observe"             # 验证期默认 observe（DESIGN §6.2）
    gate_codex: str = "off"              # E0b 后再议
    thresholds: ThresholdCfg = field(default_factory=ThresholdCfg)
    watch: WatchCfg = field(default_factory=WatchCfg)
    server: ServerCfg = field(default_factory=ServerCfg)
    notify: NotifyCfg = field(default_factory=NotifyCfg)
    ferry_provider: str = "local"

    @property
    def data_dir(self) -> Path:
        return Path(self.server.data_dir) if self.server.data_dir else Path.home() / "ferryman"

    def threshold_for(self, agent: str) -> ThresholdCfg:
        """按 Agent 分设留口：目前共用全局，Codex gate 本就 off。"""
        return self.thresholds


def load(path: Path | None = None, relax_min_gap: bool = False) -> Config:
    """relax_min_gap=True：冒烟/测试用——放宽"阈值差≥120s"（仍强制 summarize<block）。
    路径优先级：显式参数 > 环境变量 FERRYMAN_CONFIG > ~/ferryman/config.toml。"""
    cfg = Config()
    p = path or Path(os.environ.get("FERRYMAN_CONFIG") or CONFIG_PATH)
    if p.exists():
        data = tomllib.loads(p.read_text(encoding="utf-8"))
        if "gate" in data:
            cfg.gate_cc = data["gate"].get("cc_mode", cfg.gate_cc)
            cfg.gate_codex = data["gate"].get("codex_mode", cfg.gate_codex)
        if "thresholds" in data:
            t = data["thresholds"]
            cfg.thresholds = ThresholdCfg(
                summarize_s=float(t.get("summarize_s", cfg.thresholds.summarize_s)),
                block_s=float(t.get("block_s", cfg.thresholds.block_s)),
                min_ctx_tokens=int(t.get("min_ctx_tokens", cfg.thresholds.min_ctx_tokens)),
            )
        if "watch" in data:
            w = data["watch"]
            cfg.watch = WatchCfg(
                poll_interval_s=float(w.get("poll_interval_s", cfg.watch.poll_interval_s)),
                cc_projects_dir=str(w.get("cc_projects_dir", "")),
                codex_sessions_dir=str(w.get("codex_sessions_dir", "")),
                codex_extra_dirs=[str(d) for d in w.get("codex_extra_dirs", [])],
            )
        if "server" in data:
            s = data["server"]
            cfg.server = ServerCfg(
                port=int(s.get("port", cfg.server.port)),
                data_dir=str(s.get("data_dir", "")),
            )
        if "notify" in data:
            n = data["notify"]
            cfg.notify = NotifyCfg(
                enabled=bool(n.get("enabled", cfg.notify.enabled)),
                pushover=bool(n.get("pushover", cfg.notify.pushover)),
                pushover_token=str(n.get("pushover_token", "")),
                pushover_user=str(n.get("pushover_user", "")),
                toast=bool(n.get("toast", cfg.notify.toast)),
            )
        cfg.ferry_provider = str(data.get("ferry", {}).get("provider", cfg.ferry_provider))
    validate(cfg, relax_min_gap=relax_min_gap)
    return cfg


def validate(cfg: Config, relax_min_gap: bool = False) -> None:
    problems: list[str] = []
    if cfg.gate_cc not in GATE_MODES:
        problems.append(f"gate.cc_mode 非法: {cfg.gate_cc}（可选 {GATE_MODES}）")
    if cfg.gate_codex not in GATE_MODES:
        problems.append(f"gate.codex_mode 非法: {cfg.gate_codex}")
    for agent in ("cc", "codex"):
        t = cfg.threshold_for(agent)
        if not t.summarize_s < t.block_s:
            problems.append(f"[{agent}] 总结阈值必须严格小于拦截阈值"
                            f"（当前 {t.summarize_s}s / {t.block_s}s）")
        elif not relax_min_gap and t.block_s - t.summarize_s < 120:
            problems.append(f"[{agent}] 阈值差须 ≥120s（当前 {t.block_s - t.summarize_s:.0f}s）")
    if cfg.watch.poll_interval_s <= 0:
        problems.append("watch.poll_interval_s 须 > 0")
    if problems:
        raise ValueError("配置校验失败，拒绝启动：\n  - " + "\n  - ".join(problems))
