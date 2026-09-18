"""E0C · CLI 子命令 + 报告生成(e0c.py scan_projects / load_recon / render_report / run)。

覆盖票 04 验收标准:
- fixture 会话树端到端:CLI 跑通、报告文件生成、关键数字与单测一致;
- 四节结构齐全(Q1..Q4 + 定型建议),回传耦合注记与方法口径注记固定出现;
- 在跑会话单独成节、不进 Q2 终值汇总,Q1 行标"在跑(非终值)";
- 对账手记按 agentId 配对逐个列残差;无手记时 Q3 记"无样本"不崩;
- 真实 ~/.claude 冒烟(默认跳过,E0C_SMOKE=1 开,--limit 控制项目数)。

fixture 基准数字(每响应 usage = 100/20/300/4000):
- projA/SID_A(终值):主 2 响应 + a(3 响应,spawn b)+ b(1)+ u(1,缺 meta);
  会话总账 = (700,140,2100,28000);
- projB/SID_B(在跑):主 1 响应 + c(1)。
Q2 只算终值:主合计 (200,40,600,8000),子合计 (500,100,1500,20000),
总账 (700,140,2100,28000),子代理 input 占比 500/700 = 71.4%。
"""

import json
import os
import time
from pathlib import Path

import pytest

from ferryman.__main__ import main
from ferryman.e0c import (RUNNING_WINDOW_SECONDS, load_recon, render_report,
                          scan_projects)

SID_A = "aaaa1111-2222-3333-4444-555555555555"
SID_B = "bbbb2222-2222-3333-4444-555555555555"

T0 = "2026-09-18T10:00:00.000Z"
T1 = "2026-09-18T10:00:01.000Z"
T2 = "2026-09-18T10:00:02.000Z"
T3 = "2026-09-18T10:00:03.000Z"

OLD = 1_700_000_000.0
FAR_NOW = OLD + RUNNING_WINDOW_SECONDS + 60


def _u(i=100, o=20, cw=300, cr=4000):
    return {"input_tokens": i, "output_tokens": o,
            "cache_creation_input_tokens": cw, "cache_read_input_tokens": cr}


def _arow(mid, *, ts=T1, stop="end_turn", blocks=None):
    msg = {"role": "assistant", "id": mid, "model": "GLM-5.3", "usage": _u(),
           "content": blocks if blocks is not None else [{"type": "text", "text": "x"}]}
    if stop is not None:
        msg["stop_reason"] = stop
    return {"type": "assistant", "uuid": f"uuid-{mid}", "timestamp": ts, "message": msg}


def _urow(ts=T0):
    return {"type": "user", "timestamp": ts,
            "message": {"role": "user", "content": "做点活"}}


def _block(tid, name="Task"):
    return {"type": "tool_use", "id": tid, "name": name, "input": {"prompt": "x"}}


def _wjsonl(p: Path, rows) -> Path:
    p.write_text("\n".join(json.dumps(r, ensure_ascii=False) for r in rows) + "\n",
                 encoding="utf-8")
    return p


def _wmeta(sub: Path, aid: str, **kw) -> None:
    meta = {"agentType": "general", "description": "干活",
            "toolUseId": f"toolu_{aid}", "spawnDepth": 1, "model": "opus"}
    meta.update(kw)
    (sub / f"agent-{aid}.meta.json").write_text(
        json.dumps(meta, ensure_ascii=False), encoding="utf-8")


def _mk_projects(tmp_path: Path) -> Path:
    """两棵项目目录:projA/SID_A 终值(嵌套+缺 meta),projB/SID_B 在跑。"""
    projects = tmp_path / "projects"
    pa = projects / "projA"
    sub_a = pa / SID_A / "subagents"
    sub_a.mkdir(parents=True)
    _wjsonl(pa / f"{SID_A}.jsonl", [
        _urow(T0),
        _arow("mm1", ts=T1),
        _arow("mm2", ts=T2, stop="tool_use",
              blocks=[_block("toolu_a", "Agent"), _block("toolu_c", "Task")]),
    ])
    _wjsonl(sub_a / "agent-a.jsonl", [
        _arow("ma1"),
        _arow("ma2", ts=T2, stop="tool_use", blocks=[_block("toolu_b", "Task")]),
        _arow("ma3", ts=T3),
    ])
    _wmeta(sub_a, "a", spawnDepth=1)
    _wjsonl(sub_a / "agent-b.jsonl", [_arow("mb1")])
    _wmeta(sub_a, "b", spawnDepth=2)
    _wjsonl(sub_a / "agent-u.jsonl", [_arow("mu1")])  # 缺 meta → 未知桶

    pb = projects / "projB"
    sub_b = pb / SID_B / "subagents"
    sub_b.mkdir(parents=True)
    _wjsonl(pb / f"{SID_B}.jsonl", [
        _urow(T0),
        _arow("mm1", ts=T1, stop="tool_use", blocks=[_block("toolu_c", "Task")]),
    ])
    _wjsonl(sub_b / "agent-c.jsonl", [_arow("mc1")])
    _wmeta(sub_b, "c", spawnDepth=1)

    # 全部拨老脱离在跑窗口,再单独把 projB 拨新 → SID_B 在跑、SID_A 终值
    for p in sorted(projects.rglob("*")):
        if p.is_file():
            os.utime(p, (OLD, OLD))
    fresh = time.time()
    for p in sorted(pb.rglob("*")):
        if p.is_file():
            os.utime(p, (fresh, fresh))
    return projects


def _recon_dict() -> dict:
    """对账手记:a 自报 370(文件 in+out=360,残差 +10 = +2.7%;四列合计 13,260 仅参考);
    zzz 扫描未见。自报语义 ≈ 去重 in+out(评审 R2 裁定口径)。"""
    return {"path": "recon.jsonl", "bad_lines": 0, "entries": [
        {"ts": T1, "agentId": "a", "agentType": "general",
         "self_reported_tokens": 370, "source": "task-notification"},
        {"ts": T2, "agentId": "zzz", "agentType": "general",
         "self_reported_tokens": 999, "source": "task-notification"},
    ]}


def _meta(projects: Path) -> dict:
    return {"generated_at": "2026-09-18 15:30", "projects_dir": str(projects),
            "limit": None}


# ---- 扫描层 ----------------------------------------------------------------------


def test_scan_projects_enumerates_sessions_not_subagents(tmp_path):
    projects = _mk_projects(tmp_path)
    accs = scan_projects(projects, now=FAR_NOW)
    assert {a.session_id for a in accs} == {SID_A, SID_B}
    assert len(accs) == 2  # subagents/ 下的转录不当会话扫
    by_id = {a.session_id: a for a in accs}
    assert by_id[SID_A].running is False
    assert by_id[SID_B].running is True


def test_scan_projects_limit_restricts_project_dirs(tmp_path):
    projects = _mk_projects(tmp_path)
    accs = scan_projects(projects, now=FAR_NOW, limit=1)  # projA 排序在前
    assert [a.session_id for a in accs] == [SID_A]


def test_scan_projects_missing_dir_is_defensive(tmp_path):
    assert scan_projects(tmp_path / "nope", now=FAR_NOW) == []


# ---- 渲染层:结构 -----------------------------------------------------------------


def _render(projects: Path, *, recon=None):
    accs = scan_projects(projects, now=FAR_NOW)
    from ferryman.e0c import lineage_rows
    return render_report(accs, lineage_rows(accs, projects), recon, _meta(projects))


def test_report_structure_four_sections_and_fixed_notes(tmp_path):
    text = _render(_mk_projects(tmp_path), recon=_recon_dict())
    for head in ("## Q1 · 逐会话明细账", "## Q2 · 子代理占比与分布",
                 "## Q3 · 与 CLI 自报数对账", "## Q4 · 长尾清单",
                 "## 定型建议"):
        assert head in text, head
    # 回传耦合注记固定出现(头部 + Q2 至少两处)
    assert text.count("回传耦合注记") >= 2
    # 方法口径注记(票 01 评审要求)
    assert "responses = 计入账目的干净组数" in text
    assert "按 message.id 去重" in text
    # 族系合计行恒标"识别未验证"
    assert "族系合计" in text and "识别未验证" in text


def test_report_running_session_own_section_excluded_from_q2(tmp_path):
    text = _render(_mk_projects(tmp_path))
    assert "## 在跑会话(非终值,不进终值汇总)" in text
    assert "在跑(非终值)" in text
    # Q2 总账 input 700(只算 SID_A);若误算在跑 SID_B 会变 900
    assert "700" in text
    # SID_B 出现在在跑节里(而不是 Q1 终值明细里)


def test_report_key_numbers_match_units(tmp_path):
    text = _render(_mk_projects(tmp_path), recon=_recon_dict())
    # Q2 总账四列:700 / 140 / 2,100 / 28,000(千分位)
    assert "28,000" in text and "2,100" in text
    # 子代理占比:500/700 = 71.4%(四列同率)
    assert "71.4%" in text
    # Q1 SID_A 会话总账 700/140/2,100/28,000;agent 行有类型/深度/终态
    assert f"`{SID_A}`" in text and f"`{SID_B}`" in text
    assert "| `a` | general | 1 | opus | 完成 |" in text
    assert "| `b` | general | 2 | opus | 完成 |" in text
    assert "| `u` | 未知 | 未知 |" in text  # 缺 meta → 未知桶
    # Q2 分布:类型 general(400,80,1,200,16,000)、模型行级 GLM-5.3 全量
    assert "| general | 2 |" in text        # a+b 两行
    assert "| GLM-5.3 | 700 | 140 | 2,100 | 28,000 |" in text
    assert "| 未知 | 1 |" in text           # 深度未知桶单列
    assert "| 2 | 1 |" in text              # depth=2 一行


def test_report_q3_pairs_and_residuals(tmp_path):
    text = _render(_mk_projects(tmp_path), recon=_recon_dict())
    # a:自报 370;文件侧 in+out = 3 响应 × (100+20) = 360;残差 = +10(+2.7%);
    # 四列合计 13,260 照旧并列展示(仅参考,不进残差)
    assert "| `a` | general | 370 | 13,260 | 360 | 10 | +2.7% | |" in text
    assert "残差口径=in+out" in text                 # 口径注记固定出现
    assert "与 CLI 自报语义对齐" in text
    assert "`zzz`" in text and "扫描未见" in text   # 未配对如实列出
    assert "不足 10" in text            # 样本 2 条,如实报告
    assert "无样本" not in text


def test_report_q3_running_session_pair_annotated(tmp_path):
    """配对 agent 属于在跑会话 → 备注列标"在跑会话快照";终值会话配对不标。"""
    recon = _recon_dict()
    recon["entries"].append(
        {"ts": T2, "agentId": "c", "agentType": "general",
         "self_reported_tokens": 120, "source": "task-notification"})
    text = _render(_mk_projects(tmp_path), recon=recon)
    # c 在 SID_B(在跑会话):自报 120,文件 in+out = 1 响应 × (100+20) = 120,
    # 残差 0(+0.0%),四列合计 4,420 仅参考;备注列标"在跑会话快照"
    assert "| `c` | general | 120 | 4,420 | 120 | 0 | +0.0% | 在跑会话快照 |" in text
    # a 在 SID_A(终值会话)→ 备注列留空;全文只此一处标注
    assert "| `a` | general | 370 | 13,260 | 360 | 10 | +2.7% | |" in text
    assert text.count("在跑会话快照") == 1


def test_report_q3_mechanism_note_after_residual_note(tmp_path):
    """Q3 固定注记(残差口径)后追加机制注记一行(终局评审裁定,措辞照抄)。"""
    from ferryman.e0c import Q3_MECHANISM_NOTE
    text = _render(_mk_projects(tmp_path), recon=_recon_dict())
    q3 = text.split("## Q3 · 与 CLI 自报数对账", 1)[1].split("## Q4", 1)[0]
    assert Q3_MECHANISM_NOTE in q3          # 该句在 Q3 节内出现
    # 顺序:固定口径注记在前,机制注记紧随其后
    assert q3.index("残差口径=in+out") < q3.index(Q3_MECHANISM_NOTE)


def test_report_q3_no_recon_says_no_sample(tmp_path):
    text = _render(_mk_projects(tmp_path), recon=None)
    assert "无样本" in text


def test_report_empty_accounts_no_crash(tmp_path):
    text = render_report([], [], None, _meta(tmp_path))
    assert "无终值会话" in text
    assert "无样本" in text
    assert "n/a" in text  # 占比分母为 0 不除崩


def test_report_q4_longtail_categories(tmp_path):
    projects = _mk_projects(tmp_path)
    # 塞两条解析层长尾:坏行 + 组内冲突,进 SID_A 主转录
    mainp = projects / "projA" / f"{SID_A}.jsonl"
    raw = mainp.read_text(encoding="utf-8").rstrip("\n")
    conflict = json.dumps({"type": "assistant", "uuid": "u9", "timestamp": T3,
                           "message": {"role": "assistant", "id": "mm1",
                                       "model": "GLM-5.3",
                                       "usage": _u(111, 22, 333, 4444)}})
    mainp.write_text(f"{raw}\nnot-json\n{conflict}\n", encoding="utf-8")
    os.utime(mainp, (OLD, OLD))
    text = _render(projects)
    assert "中断" in text        # ma2 之后又有 ma3?否——a 终态完成;中断计数 0 也要列行
    assert "组内非零 usage 冲突" in text
    assert "坏行/半行 JSON" in text
    assert "有转录无 meta" in text
    assert "扫描窗口外" in text   # 类目行存在(计数 0 也列出,分布如实)


# ---- 对账手记解析 ------------------------------------------------------------------


def test_load_recon_parses_and_counts_bad_lines(tmp_path):
    p = tmp_path / "recon.jsonl"
    p.write_text(
        json.dumps({"ts": T1, "agentId": "a", "agentType": "general",
                    "self_reported_tokens": 13600, "source": "task-notification"})
        + "\nnot-json\n"
        + json.dumps({"no_agent_id": True}) + "\n"
        + json.dumps({"ts": T2, "agentId": "b", "agentType": "general",
                      "self_reported_tokens": 4200, "source": "task-notification"})
        + "\n", encoding="utf-8")
    info = load_recon(p)
    assert info["bad_lines"] == 2
    assert [e["agentId"] for e in info["entries"]] == ["a", "b"]
    assert info["path"] == str(p)


def test_load_recon_missing_file_returns_none(tmp_path):
    assert load_recon(tmp_path / "nope.jsonl") is None


# ---- CLI 端到端 -------------------------------------------------------------------


def test_cli_end_to_end_fixture_tree(tmp_path):
    projects = _mk_projects(tmp_path)
    out = tmp_path / "reports" / "e0c.md"
    recon = tmp_path / "recon.jsonl"
    recon.write_text(
        json.dumps({"ts": T1, "agentId": "a", "agentType": "general",
                    "self_reported_tokens": 370, "source": "task-notification"})
        + "\n", encoding="utf-8")
    rc = main(["e0c", "--projects", str(projects), "--out", str(out),
               "--recon", str(recon)])
    assert rc == 0
    text = out.read_text(encoding="utf-8")  # UTF-8 可读
    assert "## Q1 · 逐会话明细账" in text
    assert "## Q2 · 子代理占比与分布" in text
    assert "## Q3 · 与 CLI 自报数对账" in text
    assert "## Q4 · 长尾清单" in text
    assert "回传耦合注记" in text
    assert "71.4%" in text and "+2.7%" in text   # 关键数字与单测一致


def test_cli_limit_and_missing_projects_dir(tmp_path):
    projects = _mk_projects(tmp_path)
    out = tmp_path / "r1.md"
    assert main(["e0c", "--projects", str(projects), "--out", str(out),
                 "--limit", "1"]) == 0
    text = out.read_text(encoding="utf-8")
    assert f"`{SID_A}`" in text and f"`{SID_B}`" not in text
    # 项目目录不存在 → 防御出空报告,不崩
    out2 = tmp_path / "r2.md"
    assert main(["e0c", "--projects", str(tmp_path / "nope"),
                 "--out", str(out2)]) == 0
    assert "无终值会话" in out2.read_text(encoding="utf-8")


@pytest.mark.skipif(not os.environ.get("E0C_SMOKE"),
                    reason="真实 ~/.claude 冒烟:默认跳过,设 E0C_SMOKE=1 开(--limit 控制项目数)")
def test_real_smoke_against_home_claude(tmp_path):
    out = tmp_path / "real-e0c.md"
    rc = main(["e0c", "--out", str(out), "--limit", "2"])
    assert rc == 0
    text = out.read_text(encoding="utf-8")
    assert "## Q4 · 长尾清单" in text
