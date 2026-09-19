#!/usr/bin/env python3
"""router-fidelity/diff —— 两份 tap 捕获逐字段差分（L1 golden diff 工具）。

对标 q14s3.py 工程经验：UTF-8 落盘、GBK 控制台防御（stdout 重编码 utf-8），
所有输出 `python -X utf8 diff.py ...` 可跑。

用法:
    python -X utf8 diff.py <A> <B> [--ignore k1,k2]
    python -X utf8 diff.py --selftest

A/B 各为：tap 捕获单文件 JSON，或捕获目录（tap.go 产物：*_req*.json 与
*_resp*.json 按 seq 配对成对）。差异逐行输出（DIFF 前缀）；退出码
0=全同，1=有差异，2=输入错误。

白名单忽略（不可控字段）：默认 ts、seq、session_id、user_id。顶层级与 body
内键名按精确名（不区分大小写）忽略；头名再经 -/_ 归一后按包含匹配，故
X-Claude-Code-Session-Id 头与 body.metadata.user_id 一并被忽略。--ignore 追加，
不改默认集。

diff 维度（票 10）：headers 逐键（多值已由 tap join）、body 顶层键集、
model/messages 结构摘要（逐 message role/blocks type 计数）、全 body 规范化
JSON 深比较、resp 件状态码与 usage 四列。
"""
import argparse
import json
import os
import re
import sys
import tempfile

# GBK 控制台防御：输出重编码 utf-8，不可编码字符替换而非炸栈（-X utf8 下本就是 utf-8）
for _stream in (sys.stdout, sys.stderr):
    if hasattr(_stream, "reconfigure"):
        try:
            _stream.reconfigure(encoding="utf-8", errors="replace")
        except Exception:
            pass

# 默认白名单：不可控字段（ts/seq 是捕获时序产物；session_id/user_id 是会话身份，
# 跨会话/跨路径对比必然不同，差异无意义）
DEFAULT_IGNORE = ("ts", "seq", "session_id", "user_id")


def norm_token(t):
    """白名单键归一：小写 + -/_ 统一（头名用）。"""
    return t.strip().lower().replace("-", "_")


def norm_header_name(name):
    return str(name).lower().replace("-", "_")


def trunc(s, n=120):
    s = str(s)
    return s if len(s) <= n else s[:n] + "...(截断)"


def load_json(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


_SEQ_RE = re.compile(r"_(?:req|resp)(\d+)\.json$")


def seq_from_name(name):
    m = _SEQ_RE.search(name)
    return int(m.group(1)) if m else None


def entry_kind(entry, filename=""):
    """req/resp 件判别：先看文件名（tap 命名权威），否则按内容嗅探。"""
    if "_resp" in filename:
        return "resp"
    if "_req" in filename:
        return "req"
    if "status" in entry and "headers" not in entry and "body" not in entry:
        return "resp"
    return "req"


def load_side(path):
    """路径 → 按 seq 配对的捕获列表 [{"seq","req","resp","src"}]，seq 升序。

    单文件 → 单元素列表（文件是 req 件则挂 req 位，resp 件挂 resp 位）；
    目录 → glob *.json，_reqNNN/_respNNN 按 NNN 配对。
    """
    if os.path.isfile(path):
        e = load_json(path)
        kind = entry_kind(e, os.path.basename(path))
        seq = e.get("seq") if isinstance(e.get("seq"), int) else seq_from_name(os.path.basename(path))
        pair = {"seq": seq, "req": None, "resp": None, "src": {}}
        pair[kind] = e
        pair["src"][kind] = path
        return [pair]
    if not os.path.isdir(path):
        raise FileNotFoundError("捕获路径不存在: %s" % path)
    pairs = {}
    for fn in sorted(os.listdir(path)):
        if not fn.endswith(".json"):
            continue
        fp = os.path.join(path, fn)
        try:
            e = load_json(fp)
        except Exception:
            continue
        seq = seq_from_name(fn)
        if seq is None:
            seq = e.get("seq") if isinstance(e.get("seq"), int) else None
        if seq is None:
            continue
        kind = entry_kind(e, fn)
        p = pairs.setdefault(seq, {"seq": seq, "req": None, "resp": None, "src": {}})
        if p[kind] is None:
            p[kind] = e
            p["src"][kind] = fp
    return [pairs[k] for k in sorted(pairs)]


def find_pair(pairs, seq):
    for p in pairs:
        if p["seq"] == seq:
            return p
    return None


# ---- 差异维度 ----

def diff_headers(ha, hb, ignore):
    """headers 逐键差分（tap 已把多值 join 成单值；键不区分大小写比较）。

    头名白名单按包含匹配：归一后的键名（-/_ 统一、小写）含任一白名单词即忽略，
    故 session_id 词元同时命中 X-Claude-Code-Session-Id。
    """
    ignored = {norm_token(t) for t in ignore if norm_token(t)}

    def is_ignored(norm_name):
        return any(tok and tok in norm_name for tok in ignored)

    na = {norm_header_name(k): v for k, v in (ha or {}).items()}
    nb = {norm_header_name(k): v for k, v in (hb or {}).items()}
    lines = []
    for k in sorted(set(na) | set(nb)):
        if is_ignored(k):
            continue
        if k not in nb:
            lines.append("headers[%s]: 仅在 A（值 %r）" % (k, trunc(na[k])))
        elif k not in na:
            lines.append("headers[%s]: 仅在 B（值 %r）" % (k, trunc(nb[k])))
        elif na[k] != nb[k]:
            lines.append("headers[%s]: A=%r B=%r" % (k, trunc(na[k]), trunc(nb[k])))
    return lines


def msg_summaries(body):
    """messages 结构摘要：逐 message 的 role 与 content blocks type 计数。"""
    out = []
    msgs = body.get("messages")
    if not isinstance(msgs, list):
        return out, None
    for i, m in enumerate(msgs):
        if not isinstance(m, dict):
            out.append("msg[%d]=<非对象>" % i)
            continue
        content = m.get("content")
        if isinstance(content, str):
            blocks = {"<str>": 1}
        elif isinstance(content, list):
            blocks = {}
            for b in content:
                t = b.get("type") if isinstance(b, dict) else "<raw>"
                blocks[t] = blocks.get(t, 0) + 1
        else:
            blocks = {"<%s>" % type(content).__name__: 1}
        out.append("msg[%d] role=%s blocks=%s" % (i, m.get("role"), dict(sorted(blocks.items()))))
    return out, len(msgs)


def deep_diff(a, b, path, ign_keys, out):
    """规范化 JSON 深比较：dict 键集无关顺序，list 按下标（数组顺序参与 diff）。

    ign_keys：精确键名忽略集（不区分大小写）——session_id/user_id 类白名单。
    """
    if isinstance(a, dict) and isinstance(b, dict):
        for k in sorted(set(a) | set(b), key=str):
            if str(k).lower() in ign_keys:
                continue
            kp = "%s.%s" % (path, k)
            if k not in a:
                out.append("%s: 仅在 B（%s）" % (kp, trunc(json.dumps(b[k], ensure_ascii=False))))
            elif k not in b:
                out.append("%s: 仅在 A（%s）" % (kp, trunc(json.dumps(a[k], ensure_ascii=False))))
            else:
                deep_diff(a[k], b[k], kp, ign_keys, out)
    elif isinstance(a, list) and isinstance(b, list):
        if len(a) != len(b):
            out.append("%s: 长度 A=%d B=%d" % (path, len(a), len(b)))
        for i, (x, y) in enumerate(zip(a, b)):
            deep_diff(x, y, "%s[%d]" % (path, i), ign_keys, out)
    else:
        if a != b:
            out.append("%s: A=%s B=%s" % (
                path,
                trunc(json.dumps(a, ensure_ascii=False)),
                trunc(json.dumps(b, ensure_ascii=False)),
            ))


def diff_body(ba, bb, ignore):
    """body 三个维度：顶层键集 / messages 结构摘要 / 全量规范化 JSON 深比较。

    白名单键在键集与深比较两层都生效（精确键名、不区分大小写）——
    body.metadata.user_id（内嵌 session_id 的 JSON 串）因此整键跳过。
    """
    lines = []
    ign_keys = {norm_token(t) for t in ignore}
    if not isinstance(ba, dict) or not isinstance(bb, dict):
        deep_diff(ba, bb, "body", ign_keys, lines)
        return lines
    ka, kb = {k for k in ba if str(k).lower() not in ign_keys}, {k for k in bb if str(k).lower() not in ign_keys}
    only_a, only_b = sorted(ka - kb, key=str), sorted(kb - ka, key=str)
    if only_a or only_b:
        lines.append("body 顶层键集: 仅在 A=%s 仅在 B=%s" % (only_a, only_b))
    sa, na_len = msg_summaries(ba)
    sb, nb_len = msg_summaries(bb)
    for i in range(max(len(sa), len(sb))):
        xa = sa[i] if i < len(sa) else "<缺失>"
        xb = sb[i] if i < len(sb) else "<缺失>"
        if xa != xb:
            lines.append("body.messages 摘要: %s  vs  %s" % (xa, xb))
    if na_len != nb_len:
        lines.append("body.messages 条数: A=%s B=%s" % (na_len, nb_len))
    deep_diff(ba, bb, "body", ign_keys, lines)
    return lines


def diff_entry(ea, eb, ignore):
    """单个捕获件对（req 或 resp）逐字段差分，返回差异行列表。

    顶层除 headers/body 特殊处理外逐键比较；白名单键（ts/seq 等）直接跳过。
    req 件与 resp 件共用本函数——resp 的 status/usage_source/usage 走通用路径。
    """
    lines = []
    ign_top = {norm_token(t) for t in ignore}
    for k in sorted(set(ea) | set(eb), key=str):
        if str(k).lower() in ign_top:
            continue
        if k == "headers":
            lines += diff_headers(ea.get("headers"), eb.get("headers"), ignore)
        elif k == "body":
            lines += diff_body(ea.get("body"), eb.get("body"), ignore)
        elif k not in ea:
            lines.append("顶层[%s]: 仅在 B（%s）" % (k, trunc(json.dumps(eb[k], ensure_ascii=False))))
        elif k not in eb:
            lines.append("顶层[%s]: 仅在 A（%s）" % (k, trunc(json.dumps(ea[k], ensure_ascii=False))))
        else:
            va, vb = ea[k], eb[k]
            if isinstance(va, (dict, list)) or isinstance(vb, (dict, list)):
                sub = []
                deep_diff(va, vb, str(k), ign_top, sub)
                lines += sub
            elif va != vb:
                lines.append("顶层[%s]: A=%s B=%s" % (
                    k, trunc(json.dumps(va, ensure_ascii=False)), trunc(json.dumps(vb, ensure_ascii=False))))
    return lines


# ---- 自测 ----

def selftest():
    here = os.path.dirname(os.path.abspath(__file__))
    fx = os.path.join(here, "fixtures")
    base = load_json(os.path.join(fx, "req_base.json"))
    variant = load_json(os.path.join(fx, "req_variant.json"))
    ign = list(DEFAULT_IGNORE)

    # 用例 1：全等夹具 → 零差异（DIFF=0）
    d = diff_entry(base, base, ign)
    assert not d, "用例1失败：全等夹具应零差异，实得 %r" % d

    # 用例 2：变体夹具 → 逐项报 model / 新增头 / message 差异；
    # session_id（头+metadata.user_id）与 ts/seq 差异被白名单忽略、绝不出现
    d = diff_entry(base, variant, ign)
    assert d, "用例2失败：变体夹具应有差异"
    text = "\n".join(d)
    assert "model" in text, "用例2失败：应报 model 差异，实得 %r" % d
    assert "x_extra_probe" in text, "用例2失败：应报新增头 X-Extra-Probe，实得 %r" % d
    assert "messages" in text, "用例2失败：应报 message 差异，实得 %r" % d
    low = text.lower()
    for bad in ("session_id", "session-id", "user_id"):
        assert bad not in low, "用例2失败：%s 差异应被白名单忽略，实得 %r" % (bad, d)

    # 用例 3：目录模式（tap 命名）+ req/resp 按 seq 配对 + resp usage 差异
    resp_base = load_json(os.path.join(fx, "resp_base.json"))
    resp_variant = load_json(os.path.join(fx, "resp_variant.json"))
    with tempfile.TemporaryDirectory() as td:
        da, db = os.path.join(td, "a"), os.path.join(td, "b")
        os.makedirs(da)
        os.makedirs(db)
        for dpath, rq, rp in ((da, base, resp_base), (db, variant, resp_variant)):
            with open(os.path.join(dpath, "20260919_200000_req001.json"), "w", encoding="utf-8") as f:
                json.dump(rq, f, ensure_ascii=False, indent=2)
            with open(os.path.join(dpath, "20260919_200000_resp001.json"), "w", encoding="utf-8") as f:
                json.dump(rp, f, ensure_ascii=False, indent=2)
        A, B = load_side(da), load_side(db)
        assert len(A) == 1 and len(B) == 1, "用例3失败：目录应各配出 1 对，实得 %d/%d" % (len(A), len(B))
        assert A[0]["req"] is not None and A[0]["resp"] is not None, "用例3失败：req/resp 应都配到"
        lines = diff_entry(A[0]["req"], B[0]["req"], ign) + diff_entry(A[0]["resp"], B[0]["resp"], ign)
        text3 = "\n".join(lines)
        assert "usage" in text3 and "output_tokens" in text3, "用例3失败：resp usage 差异应报出，实得 %r" % lines
        low3 = text3.lower()
        for bad in ("session_id", "user_id"):
            assert bad not in low3, "用例3失败：%s 应被白名单忽略" % bad

    # 用例 4：--ignore 追加白名单生效（追加后不再报该头，其余差异仍在）
    d4 = diff_entry(base, variant, ign + ["x_extra_probe"])
    assert not any("x_extra_probe" in l for l in d4), "用例4失败：追加白名单后不应再报该头，实得 %r" % d4
    assert any("model" in l for l in d4), "用例4失败：其余差异应保留，实得 %r" % d4

    print("diff.py selftest: 4 用例全过（零网络外呼）")
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description="两份 tap 捕获逐字段差分（L1 golden diff）")
    ap.add_argument("a", nargs="?", help="捕获 A：单文件 JSON 或目录")
    ap.add_argument("b", nargs="?", help="捕获 B：单文件 JSON 或目录")
    ap.add_argument("--ignore", default="", help="追加白名单键，逗号分隔（默认 %s 之上追加）" % ",".join(DEFAULT_IGNORE))
    ap.add_argument("--selftest", action="store_true", help="内置自测（零网络外呼）")
    args = ap.parse_args(argv)

    if args.selftest:
        return selftest()
    if not args.a or not args.b:
        ap.error("需要 <A> <B> 两个捕获路径，或 --selftest")

    ignore = list(DEFAULT_IGNORE) + [x.strip() for x in args.ignore.split(",") if x.strip()]
    try:
        A = load_side(args.a)
        B = load_side(args.b)
    except (FileNotFoundError, json.JSONDecodeError) as e:
        print("输入错误: %s" % e, file=sys.stderr)
        return 2

    total = 0
    if len(A) == 1 and len(B) == 1:
        # 单件对单件（文件对文件，或目录各恰一对）：seq 不同是常态
        # （golden 与出站捕获各自独立发号），直接配对，不按 seq 对齐。
        seqs = [(A[0]["seq"] if A[0]["seq"] == B[0]["seq"] else "%s/%s" % (A[0]["seq"], B[0]["seq"]), A[0], B[0])]
    else:
        seqs = [(s, find_pair(A, s), find_pair(B, s))
                for s in sorted({p["seq"] for p in A} | {p["seq"] for p in B},
                                key=lambda s: (s is None, s if s is not None else 0))]
    for s, pa, pb in seqs:
        if pa is None or pb is None:
            missing = "A" if pa is None else "B"
            print("== seq=%s ==" % s)
            print("DIFF 捕获缺失: %s 无此 seq" % missing)
            total += 1
            continue
        lines = []
        for kind in ("req", "resp"):
            xa, xb = pa.get(kind), pb.get(kind)
            if xa is None and xb is None:
                continue
            if xa is None or xb is None:
                side = "A" if xa is None else "B"
                lines.append("%s 件缺失: %s 无（对侧 src=%s）" % (kind, side, pa["src"].get(kind) or pb["src"].get(kind)))
                continue
            lines += diff_entry(xa, xb, ignore)
        if lines:
            total += len(lines)
            print("== seq=%s ==" % s)
            for l in lines:
                print("DIFF " + l)
    print("---- %d 对捕获，共 %d 处差异" % (len(seqs), total))
    return 0 if total == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
