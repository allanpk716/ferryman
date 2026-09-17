# tests/test_prices.py
"""价格表：TOML 解析、版本选择、缺省 p_cache、版本标签。"""
from datetime import datetime, timezone
from pathlib import Path

import pytest

from ferryman.prices import load_prices, price_tag

TOML = """
[prices.glm]
unit = "智谱积分"
per = 10000

[[prices.glm.versions]]
effective_from = "2026-09-01"
p_in = 6.9
p_cache = 1.7
p_out = 24

[[prices.glm.versions]]
effective_from = "2026-09-17"
p_in = 6.9
p_cache = 1.7
p_out = 24

[prices.nocache]
unit = "元"
per = 1000000

[[prices.nocache.versions]]
effective_from = "2026-09-01"
p_in = 1.0
p_out = 2.0
"""

D16 = datetime(2026, 9, 16, 12, 0, tzinfo=timezone.utc).timestamp()
D17 = datetime(2026, 9, 17, 12, 0, tzinfo=timezone.utc).timestamp()


@pytest.fixture
def books(tmp_path: Path) -> dict:
    p = tmp_path / "config.toml"
    p.write_text(TOML, encoding="utf-8")
    return load_prices(path=p)


def test_missing_file_is_empty(tmp_path):
    assert load_prices(path=tmp_path / "nope.toml") == {}


def test_version_selection(books):
    glm = books["glm"]
    assert glm.per == 10_000 and glm.unit == "智谱积分"
    assert glm.at(D16).effective_from == "2026-09-01"   # 生效日前一天 → 旧版
    assert glm.at(D17).effective_from == "2026-09-17"   # 生效日起 → 新版
    assert glm.at(0) is None                            # 早于一切版本


def test_p_cache_optional(books):
    v = books["nocache"].versions[0]
    assert v.p_cache is None                            # 缺省 = 无缓存经济


def test_price_tag(books):
    assert price_tag("glm", books["glm"].at(D17)) == "glm@2026-09-17"
