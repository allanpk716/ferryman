"""T08 · eval.verify_handoff：匹配合法情形与幻觉捕获。"""

from ferryman.eval import verify_handoff

TRUTH = [
    "C:/repo/src/a.ts",
    "C:/repo/docs",
    "C:/repo/docs/2026-09-02-报告.md",
]


def test_suffix_and_windows_forms_pass():
    r = verify_handoff("改了 `src/a.ts` 和 `C:\\repo\\docs` 下东西", TRUTH)
    assert r["pass"] and r["n_ok"] >= 2


def test_truncated_relative_prefix_passes():
    r = verify_handoff("详见 docs/2026-09-02-...md", TRUTH)
    assert r["pass"], r["hallucinated"]


def test_host_like_token_skipped():
    r = verify_handoff("连到 server/127.0.0.1 和 db/10.0.0.2", TRUTH)
    assert r["pass"]


def test_real_hallucination_caught():
    r = verify_handoff("更新了 C:/repo2/b.ts", TRUTH)
    assert not r["pass"] and "C:/repo2/b.ts" in r["hallucinated"]


def test_parent_directory_legit():
    r = verify_handoff("在 C:/repo/src 里加了文件", TRUTH)
    assert r["pass"]
