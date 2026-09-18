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
QWATCH_MODES = ("off", "observe", "enforce")
QWATCH_MIN_LEAD_S = 60.0        # ferry_deadline_lead_s 下限（spec 决策 3 夹取区间）
FERRY_WALL_TIMEOUT_S = 480      # 摆渡墙钟总时限 8min（DESIGN §4；daemon 同名再导出）
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
    harvest_usage: bool = True           # 用量采集（usage 科目）：30 天清理后的审计地基，隐私敏感可关


@dataclass
class ThresholdCfg:
    summarize_s: float = 25 * 60
    block_s: float = 35 * 60             # E0a 实测拐点+5min（reports/e0a-cc-glm.md）
    min_ctx_tokens: int = 20_000
    cache_warn_s: float = 720.0        # 12min：缓存死线纯提醒（信息条，不拦不触发摆渡；0=关）


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
class HeartbeatCfg:
    enabled: bool = False        # T41 仅预留：执行器未实装（设计 §0 授权边界）
    ttl_s: float = 0.0           # 0 = 未实测/未配置（report 策略对比跳过）
    ttl_measured_at: str = ""
    ttl_source: str = ""


@dataclass
class QuestionWatchCfg:
    """T51 问询守望（spec 决策 9）：提问潮等答复窗口＋心跳保温，默认 off。"""

    mode: str = "off"                    # off | observe | enforce
    min_questions: int = 5               # 提问潮阈值（unit_count ≥ 此值命中）
    beat_interval_s: float = 420.0       # 0.7×GLM 实测 TTL 600s（口径统一 0.7×）
    max_beats: int = 2                   # 每窗最多心跳跳数
    ferry_deadline_lead_s: float = 480.0 # 摆渡死线提前量（校验见 validate）


@dataclass
class Config:
    gate_cc: str = "observe"             # 验证期默认 observe（DESIGN §6.2）
    gate_codex: str = "off"              # E0b 后再议
    thresholds: ThresholdCfg = field(default_factory=ThresholdCfg)
    watch: WatchCfg = field(default_factory=WatchCfg)
    server: ServerCfg = field(default_factory=ServerCfg)
    notify: NotifyCfg = field(default_factory=NotifyCfg)
    heartbeat: HeartbeatCfg = field(default_factory=HeartbeatCfg)
    question_watch: QuestionWatchCfg = field(default_factory=QuestionWatchCfg)
    ferry_provider: str = ""              # 空=未配置：摆渡降级骨架（worker 警告，doctor 提示）

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
                cache_warn_s=float(t.get("cache_warn_s", cfg.thresholds.cache_warn_s)),
            )
        if "watch" in data:
            w = data["watch"]
            cfg.watch = WatchCfg(
                poll_interval_s=float(w.get("poll_interval_s", cfg.watch.poll_interval_s)),
                cc_projects_dir=str(w.get("cc_projects_dir", "")),
                codex_sessions_dir=str(w.get("codex_sessions_dir", "")),
                codex_extra_dirs=[str(d) for d in w.get("codex_extra_dirs", [])],
                harvest_usage=bool(w.get("harvest_usage", True)),
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
        if "heartbeat" in data:
            hb = data["heartbeat"]
            cfg.heartbeat = HeartbeatCfg(
                enabled=bool(hb.get("enabled", False)),
                ttl_s=float(hb.get("ttl_s", 0.0)),
                ttl_measured_at=str(hb.get("ttl_measured_at", "")),
                ttl_source=str(hb.get("ttl_source", "")))
        if "question_watch" in data:
            q = data["question_watch"]
            cfg.question_watch = QuestionWatchCfg(
                mode=str(q.get("mode", cfg.question_watch.mode)),
                min_questions=int(q.get("min_questions",
                                        cfg.question_watch.min_questions)),
                beat_interval_s=float(q.get("beat_interval_s",
                                            cfg.question_watch.beat_interval_s)),
                max_beats=int(q.get("max_beats", cfg.question_watch.max_beats)),
                ferry_deadline_lead_s=float(q.get(
                    "ferry_deadline_lead_s",
                    cfg.question_watch.ferry_deadline_lead_s)))
        cfg.ferry_provider = str(data.get("ferry", {}).get("provider", cfg.ferry_provider))
    validate(cfg, relax_min_gap=relax_min_gap)
    return cfg


def validate(cfg: Config, relax_min_gap: bool = False) -> None:
    """违例拒启。附带 T51 就地修正：question_watch 开启时 ferry_deadline_lead_s
    低于下限则夹取为 QWATCH_MIN_LEAD_S（打印告警）；上限不夹取——summarize_s+lead
    超过 block_s 直接拒绝（摆渡死线必须赶在闸门拦截之前，spec 决策 3）。"""
    problems: list[str] = []
    if cfg.gate_cc not in GATE_MODES:
        problems.append(f"gate.cc_mode 非法: {cfg.gate_cc}（可选 {GATE_MODES}）")
    if cfg.gate_codex not in GATE_MODES:
        problems.append(f"gate.codex_mode 非法: {cfg.gate_codex}")
    qw = cfg.question_watch
    if qw.mode not in QWATCH_MODES:
        problems.append(f"question_watch.mode 非法: {qw.mode}（可选 {QWATCH_MODES}）")
    if qw.beat_interval_s <= 0:     # 票04 M5：≤0 排出的计划全是过去跳（开窗即狂跳）
        problems.append(f"question_watch.beat_interval_s 须 > 0"
                        f"（当前 {qw.beat_interval_s:g}s）")
    if qw.mode != "off":        # 功能关闭时不校验 lead（存量小阈值配置零影响）
        t = cfg.threshold_for("cc")
        if qw.ferry_deadline_lead_s < QWATCH_MIN_LEAD_S:
            print(f"[config] ⚠ question_watch.ferry_deadline_lead_s "
                  f"{qw.ferry_deadline_lead_s:g}s 低于下限，已夹取为 "
                  f"{QWATCH_MIN_LEAD_S:g}s", flush=True)
            qw.ferry_deadline_lead_s = QWATCH_MIN_LEAD_S
        if qw.ferry_deadline_lead_s + t.summarize_s > t.block_s:
            problems.append(
                f"question_watch.ferry_deadline_lead_s 过大："
                f"summarize_s + lead（{t.summarize_s:g} + "
                f"{qw.ferry_deadline_lead_s:g}s）须 ≤ block_s（{t.block_s:g}s）"
                f"——摆渡死线必须赶在闸门拦截之前")
        elif qw.ferry_deadline_lead_s <= FERRY_WALL_TIMEOUT_S:
            # 票02 评审转来的配置补强：死线余量不大于摆渡墙钟，摆渡可能贴线
            # 被墙钟砍成骨架。只告警不改值（默认 480 恰在贴线位，属已知取舍）。
            print(f"[config] ⚠ question_watch.ferry_deadline_lead_s "
                  f"{qw.ferry_deadline_lead_s:g}s 不大于摆渡墙钟 "
                  f"{FERRY_WALL_TIMEOUT_S}s——死线余量不足，骨架可能贴线",
                  flush=True)
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
