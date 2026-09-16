"""T18 · 大会话 L0 提取（slow 标记，默认 -m 'not slow' 跳过）。

用本机真实最大会话验证：L0 完成时限、材料缩比、内存不炸。
"""

import time
from pathlib import Path

import pytest

from ferryman.extract import extract, material_text, token_estimate


def _biggest_session() -> Path | None:
    root = Path.home() / ".claude" / "projects"
    if not root.exists():
        return None
    files = sorted(root.glob("**/*.jsonl"), key=lambda p: p.stat().st_size,
                   reverse=True)
    return files[0] if files else None


@pytest.mark.slow
def test_t18_big_session_l0():
    f = _biggest_session()
    if f is None or f.stat().st_size < 5 * 1024 * 1024:
        pytest.skip("本机无 >5MB 会话可测")
    t0 = time.time()
    facts, items, turns = extract(f)
    elapsed = time.time() - t0
    mat = material_text(facts, items)
    mat_tokens = token_estimate(mat)
    assert elapsed < 120, f"L0 超时 {elapsed:.0f}s"
    assert facts.peak_ctx > 100_000                       # 大会话确实大
    # 材料相对上下文的缩比 ≥2×（DESIGN §6.4 的 3-10× 假设下界放宽）
    assert mat_tokens * 2 <= facts.peak_ctx, (
        f"缩比不足: material={mat_tokens} ctx={facts.peak_ctx}")
