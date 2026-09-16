"""T05 · ledger：lineage 继承、路径归一、lookback=0。"""

from ferryman.ledger import Ledger


def test_lineage_same_path_new_session_id():
    led = Ledger()
    st1 = led.touch("cc", "sid-1", "C:\\proj\\a.jsonl", mtime=1000, size=10,
                    cwd="C:\\proj", title="旧标题", daemon_started_at=0)
    assert st1.observed_active                       # mtime ≥ started
    st2 = led.touch("cc", "sid-2", "C:/proj/A.jsonl", mtime=900, size=20,
                    daemon_started_at=0)             # 同路径（大小写/斜杠不同）+ 新 sid
    assert st2.session_id == "sid-2"
    assert st2.last_write == 1000                    # 继承闲置史
    assert st2.cwd == "C:\\proj" and st2.title == "旧标题"
    assert led.get_by_path("c:/PROJ\\a.jsonl") is st2  # 归一化键指向最新


def test_lookback_zero_inactive_before_start():
    led = Ledger()
    st = led.touch("cc", "old", "C:\\proj\\old.jsonl", mtime=10, size=1,
                   daemon_started_at=50)
    assert st.observed_active is False               # mtime < started → 不算启动后活动
    st2 = led.touch("cc", "old", "C:\\proj\\old.jsonl", mtime=60, size=1,
                    daemon_started_at=50)
    assert st2.observed_active                       # 之后有写入才激活


def test_last_write_monotonic_and_last_transcript_write():
    led = Ledger()
    led.touch("cc", "a", "C:\\p\\a.jsonl", mtime=100, size=1, daemon_started_at=0)
    led.touch("cc", "a", "C:\\p\\a.jsonl", mtime=50, size=1, daemon_started_at=0)   # 旧 mtime 不回退
    assert led.get("cc", "a").last_write == 100
    led.touch("cc", "b", "C:\\p\\b.jsonl", mtime=200, size=1, daemon_started_at=0)
    assert led.last_transcript_write == 200
