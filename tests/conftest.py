"""共享 fixture。"""

import pytest


@pytest.fixture
def h(tmp_path, monkeypatch):
    from helpers import Harness

    harness = Harness(tmp_path, monkeypatch)
    yield harness
    harness.stop()
