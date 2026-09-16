"""T01 · config 校验（DESIGN §4：违例拒启）。"""

import pytest

from ferryman.config import Config, ThresholdCfg, WatchCfg, load, validate


def _cfg(**kw) -> Config:
    c = Config()
    for k, v in kw.items():
        setattr(c, k, v)
    return c


def test_defaults_pass():
    validate(_cfg())


def test_equal_thresholds_rejected():
    c = _cfg(thresholds=ThresholdCfg(summarize_s=100, block_s=100))
    with pytest.raises(ValueError, match="严格小于"):
        validate(c)


def test_summarize_above_block_rejected():
    c = _cfg(thresholds=ThresholdCfg(summarize_s=200, block_s=100))
    with pytest.raises(ValueError):
        validate(c)


def test_gap_below_120s_rejected():
    c = _cfg(thresholds=ThresholdCfg(summarize_s=60, block_s=100))
    with pytest.raises(ValueError, match="120s"):
        validate(c)


def test_relax_min_gap_only_relaxes_gap():
    c = _cfg(thresholds=ThresholdCfg(summarize_s=60, block_s=100))
    validate(c, relax_min_gap=True)          # 差值放宽
    with pytest.raises(ValueError):          # 相等仍拒
        validate(_cfg(thresholds=ThresholdCfg(summarize_s=100, block_s=100)),
                 relax_min_gap=True)


def test_bad_gate_mode_rejected():
    with pytest.raises(ValueError, match="cc_mode"):
        validate(_cfg(gate_cc="bogus"))
    with pytest.raises(ValueError, match="codex_mode"):
        validate(_cfg(gate_codex="always"))


def test_bad_poll_interval_rejected():
    with pytest.raises(ValueError, match="poll"):
        validate(_cfg(watch=WatchCfg(poll_interval_s=0)))


def test_env_config_path(tmp_path, monkeypatch):
    f = tmp_path / "my.toml"
    f.write_text('[server]\nport = 7399\n[thresholds]\nsummarize_s = 10\nblock_s = 200\n',
                 encoding="utf-8")
    monkeypatch.setenv("FERRYMAN_CONFIG", str(f))
    cfg = load()
    assert cfg.server.port == 7399
    assert cfg.thresholds.summarize_s == 10
