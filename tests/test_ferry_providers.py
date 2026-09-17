"""T39 · provider 去硬编码：不内置任何默认 provider（防作者内网地址随仓库发布），
全部经 ~/ferryman/config.toml 配置。"""

from ferryman.ferry import load_config


def test_no_builtin_providers(tmp_path):
    # 无配置文件 → 无任何可用 provider（曾内置指向内网网关的 "local"，已移除）
    assert load_config(tmp_path / "不存在.toml") == {}


def test_load_config_from_file(tmp_path):
    f = tmp_path / "config.toml"
    f.write_text('[providers.mine]\nbase_url = "http://127.0.0.1:9/v1"\n'
                 'model = "m"\nwindow = 4096\n', encoding="utf-8")
    ps = load_config(f)
    assert set(ps) == {"mine"}
    assert ps["mine"].base_url == "http://127.0.0.1:9/v1"
    assert ps["mine"].window == 4096


def test_eval_run_rejects_unknown_provider():
    # provider 名未定义 → 友好报错返回 2，而非 KeyError 崩栈
    from ferryman.eval import run
    assert run("ghost-nope") == 2
