"""心跳调度的纯类型与纯逻辑（T51 票 03；spec 决策 5/6）。

调度本体挂在 Watcher（daemon.py，风格对齐 _maybe_qwatch）；本模块只放：
beat 请求/结果形状与可注入发送接口（决策 5——本票仅接口占位，真实 HTTP
发送属 Q14 段二/三，未授权前不实现）、三态分类（HIT/MISS/ERROR）与熔断
计数器。全部无 I/O、无消息内容（隐私不变量：字段只有元数据与金额）。
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol

HIT_RATIO_THRESHOLD = 0.5   # cache_read 占比 ≥ 此值记 HIT；Q14 段二实测后校准

# 三态 + 演练（observe 未真发，单独一态入账；决策 6/7）
OUT_HIT = "hit"
OUT_MISS = "miss"
OUT_ERROR = "error"
OUT_OBSERVE = "observe"


@dataclass(frozen=True)
class BeatPlan:
    """一跳的计划快照（决策 5 请求形状的字段载体；本票不构造真实请求）。

    只传转录路径不传内容——前缀重构（BeatBuilder）由 Q14 按 jsonl 现读。"""

    agent: str
    session_id: str
    transcript_path: str    # 转录绝对路径（Q14 前缀重构用）
    opened_ts: float        # 窗口开启时刻
    last_write: float       # 计划基线（开窗快照 mtime）
    size: int               # 开窗快照 size
    beat_index: int         # 第几跳（1 起）
    beat_ts: float          # 本跳计划时刻


@dataclass(frozen=True)
class BeatResult:
    """一跳的结果。sent=False = 未真发（observe 演练）；ok=False = 重试后仍败。"""

    sent: bool = False
    ok: bool = False
    input_tokens: int = 0       # 实收 input（未命中前缀部分）
    cache_read_tokens: int = 0  # 实收缓存读
    output_tokens: int = 0
    cost_pred: float = 0.0
    cost_actual: float = 0.0
    provider: str = ""
    model: str = ""
    err: str = ""               # 错误类别摘要（不含消息内容）


class BeatSender(Protocol):
    """可注入发送接口（决策 5）。

    真实实现方职责（Q14 段二/三，本票不做）：直打本地代理 127.0.0.1:15721、
    发 CC 别名、重放前缀 [system+tools+u1..uN]（不含末轮 assistant 输出）、
    max_tokens=1 封顶输出、429/5xx/超时指数退避重试 1 次（重试语义归 sender，
    调度器只看最终 BeatResult）、与摆渡路由零共用。"""

    def send(self, plan: BeatPlan) -> BeatResult: ...


class NoopSender:
    """observe 演练发送器：零网络，恒返回未真发结果（入账标 observe）。"""

    def send(self, plan: BeatPlan) -> BeatResult:
        return BeatResult(sent=False)


class HttpBeatSender:
    """enforce 真实发送占位——接口落地、实现留白（Q14 段二/三之后）。

    TODO(Q14): 按上 BeatSender 协议注释实现；请求形状锁定回归测试
    （前缀字段逐字段 diff 全等）随段二接入。"""

    def send(self, plan: BeatPlan) -> BeatResult:
        raise NotImplementedError(
            "真实心跳发送未授权（Q14 段二/三之后）；observe 模式请用 NoopSender")


def classify(r: BeatResult) -> str:
    """三态判定（决策 6）：未真发=observe；重试后仍败=error；
    成功按 cache_read 占比 ≥ 阈值记 hit，否则 miss（含 ≈0 全 miss——
    单跳 MISS = 全前缀按全价重付，故部分命中也按 miss 计入熔断连击）。"""
    if not r.sent:
        return OUT_OBSERVE
    if not r.ok:
        return OUT_ERROR
    total = r.cache_read_tokens + r.input_tokens
    ratio = r.cache_read_tokens / total if total else 0.0
    return OUT_HIT if ratio >= HIT_RATIO_THRESHOLD else OUT_MISS


class BeatBreaker:
    """熔断计数器（决策 6）：连续 2 MISS → 降级 enforce→observe；
    连续 3 ERROR → 暂停当前窗口剩余跳。

    - ERROR 不计入也不重置 MISS 连击（两枚 MISS 之间夹 ERROR 视作仍连续
      ——ERROR 期间缓存状态未知，不作洗白）；能拿到成败结论（hit/miss）
      即代理可达，清 ERROR 连击；HIT 清 MISS 连击；
    - observe 演练无真实信号，不动任何连击；
    - record() 返回动作："demote" | "pause" | ""（触发后对应连击清零，
      下个窗口从头计）。"""

    MISS_LIMIT = 2
    ERROR_LIMIT = 3

    def __init__(self) -> None:
        self.miss_streak = 0
        self.error_streak = 0

    def record(self, outcome: str) -> str:
        if outcome == OUT_OBSERVE:
            return ""
        if outcome == OUT_ERROR:
            self.error_streak += 1
            if self.error_streak >= self.ERROR_LIMIT:
                self.error_streak = 0
                return "pause"
            return ""
        self.error_streak = 0
        if outcome == OUT_MISS:
            self.miss_streak += 1
            if self.miss_streak >= self.MISS_LIMIT:
                self.miss_streak = 0
                return "demote"
            return ""
        self.miss_streak = 0
        return ""
