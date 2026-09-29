# 0.2.4 · Playwright 全套验证（简报 §4 a–f）+ 截图
# 用法：python pw_verify.py  （本地 http.server 伺服 widget/ui，chromium headless）
# 结果打印 PASS/FAIL 逐条；截图与 JSON 存本目录。
import json, subprocess, sys, time, socket
from pathlib import Path
from playwright.sync_api import sync_playwright

# 控制台可能非 UTF-8（GBK 下打印 ¥ 会 UnicodeEncodeError 中断全套断言）——统一按 UTF-8 输出
try:
    sys.stdout.reconfigure(encoding='utf-8', errors='replace')
except Exception:
    pass

ROOT = Path(__file__).resolve().parent
UI = ROOT.parent.parent / 'widget' / 'ui'
BASE = 'index.html'
results = []

def check(name, ok, detail=''):
    results.append((name, bool(ok), detail))
    print(f"[{'PASS' if ok else 'FAIL'}] {name}  {detail}")

def free_port():
    s = socket.socket(); s.bind(('127.0.0.1', 0)); p = s.getsockname()[1]; s.close(); return p

PORT = free_port()
srv = subprocess.Popen([sys.executable, '-m', 'http.server', str(PORT), '-b', '127.0.0.1'],
                       cwd=UI, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
time.sleep(1.0)
url = lambda q: f'http://127.0.0.1:{PORT}/{BASE}?{q}'

try:
    with sync_playwright() as p:
        b = p.chromium.launch()

        # ── a. 完整版七项自测 data-* 全 1 ──
        pg = b.new_page(viewport={'width': 132, 'height': 620})
        pg.goto(url('static=1&dev=1&selftest=1'))
        pg.wait_for_selector('#selftest-results[data-done="1"]', state='attached')
        st = pg.evaluate("() => ({...document.getElementById('selftest-results').dataset})")
        keys = ['layout', 'collapse', 'drag', 'tooltip', 'detail', 'console', 'done']
        bad = [k for k in keys if st.get(k) != '1']
        check('a. 完整版七项自测', not bad, f'data-*={st}' if bad else '7/7 全 1')
        pg.close()

        # ── b. 完整版几何防回归：悬浮四角圆+投影；docked-right 右两角直角+右缘贴窗 ──
        pg = b.new_page(viewport={'width': 132, 'height': 620})
        pg.goto(url('static=1&dev=1'))
        pg.wait_for_selector('.disc')
        geo = pg.evaluate("""() => {
          const w = document.getElementById('widget');
          const cs = getComputedStyle(w, '::before');
          return { br: cs.borderRadius, shadow: cs.boxShadow, right: cs.right };
        }""")
        check('b1. 悬浮态四角 12px 圆+投影',
              geo['br'] == '12px' and geo['shadow'] != 'none', json.dumps(geo, ensure_ascii=False))
        pg.screenshot(path=str(ROOT / 'shot-full-float.png')) # 无详情卡的干净悬浮态
        pg.evaluate("document.getElementById('widget').classList.add('docked-right')")
        geo2 = pg.evaluate("""() => {
          const cs = getComputedStyle(document.getElementById('widget'), '::before');
          return { tr: cs.borderTopRightRadius, br_: cs.borderBottomRightRadius,
                   tl: cs.borderTopLeftRadius, right: cs.right };
        }""")
        check('b2. docked-right 右两角直角+右缘贴窗',
              geo2['tr'] == '0px' and geo2['br_'] == '0px' and geo2['tl'] == '12px'
              and geo2['right'] == '0px', json.dumps(geo2, ensure_ascii=False))
        pg.screenshot(path=str(ROOT / 'shot-full-docked-right.png'))
        pg.close()

        # ── c. 紧凑版（?profile=compact；80×322 视口=Rust 设计尺寸，0.2.5 标签行后重推导） ──
        pg = b.new_page(viewport={'width': 80, 'height': 322})
        pg.goto(url('profile=compact&static=1&dev=1'))
        pg.wait_for_selector('.disc[data-id=handoff]')
        c = pg.evaluate("""() => {
          const w = document.getElementById('widget');
          const svg = w.querySelector('.disc svg').getBoundingClientRect();
          const cap = w.querySelector('.disc .cap');
          const hTxt = [...w.querySelectorAll('.disc[data-id=handoff] svg text')]
            .map(t => t.textContent).join('|');
          return {
            compact: document.body.classList.contains('compact'),
            svgW: svg.width, capShown: cap ? getComputedStyle(cap).display !== 'none' : null,
            scrollOK: w.scrollHeight <= w.clientHeight && w.scrollWidth <= w.clientWidth,
            scrollH: w.scrollHeight, clientH: w.clientHeight,
            handoffCenter: hTxt, discs: w.querySelectorAll('.disc').length,
          };
        }""")
        check('c1. 紧凑档 class 生效 + 4 盘', c['compact'] and c['discs'] == 4, f"discs={c['discs']}")
        check('c2. 盘缩至 48px', abs(c['svgW'] - 48) < 0.6, f"svgW={c['svgW']}")
        check('c3. cap/cdline 全隐', c['capShown'] is False)
        check('c4. 内容不溢出（scrollH ≤ clientH）', c['scrollOK'],
              f"scrollH={c['scrollH']} clientH={c['clientH']}")
        check('c5. 紧凑交接盘圆心=单个大数字 23（0.2.5：删「次 · 本月」行——48px 盘里 ≈6px 物理不可读）',
              '23' in c['handoffCenter'] and '次 · 本月' not in c['handoffCenter'],
              c['handoffCenter'])
        # 0.2.4 紧凑档圆心大数字：每盘圆心单行——套餐盘=最紧环剩余%（色随该环阈值
        # 告警联动），DS=余额；0.2.5 起交接盘同语言=本月次数单行大数字。
        # 期望按 data.js 演示值算出。
        cc = pg.evaluate("""() => {
          const out = {};
          for (const id of ['glm', 'kimi', 'deepseek', 'handoff']) {
            const d = document.querySelector(`.disc[data-id=${id}]`);
            out[id] = [...d.querySelectorAll('svg text')]
              .map(t => ({ cls: t.getAttribute('class'), txt: t.textContent, fill: t.getAttribute('fill') }));
          }
          return out;
        }""")
        check('c6. 紧凑圆心大数字：GLM=8% 红 / Kimi=17% 黄（最紧环+告警色联动）',
              cc['glm'] == [{'cls': 'c-pct', 'txt': '8%', 'fill': '#E85D5D'}]
              and cc['kimi'] == [{'cls': 'c-pct', 'txt': '17%', 'fill': '#E8C33D'}],
              json.dumps({'glm': cc['glm'], 'kimi': cc['kimi']}, ensure_ascii=False))
        check('c7. 紧凑圆心大数字：DS=余额单行（无 CNY/可用 小字行）',
              cc['deepseek'] == [{'cls': 'c-money', 'txt': '87.50', 'fill': None}],
              json.dumps(cc['deepseek'], ensure_ascii=False))
        check('c8. 紧凑圆心无第二行（glm/kimi/deepseek 各只一个 text，无 c-sub/c-label）',
              all(len(cc[i]) == 1 and not any(t['cls'] in ('c-sub', 'c-label') for t in cc[i])
                  for i in ('glm', 'kimi', 'deepseek')),
              json.dumps({i: cc[i] for i in ('glm', 'kimi', 'deepseek')}, ensure_ascii=False))
        # 0.2.5 交接盘紧凑档大数字：恰一个 c-pct=demo 月次数，无 c-sub（中性白=CSS，
        # 无 inline fill）
        check('c9. 紧凑交接盘恰一个 .c-pct=23、无 c-sub/c-label',
              cc['handoff'] == [{'cls': 'c-pct', 'txt': '23', 'fill': None}],
              json.dumps(cc['handoff'], ensure_ascii=False))
        # 0.2.5 迷你标签行（仅紧凑档渲染）：每盘名字+品牌色小点
        mt = pg.evaluate("""() => {
          const out = {};
          for (const id of ['glm', 'kimi', 'deepseek', 'handoff']) {
            const d = document.querySelector(`.disc[data-id=${id}]`);
            const tag = d.querySelector('.mini-tag');
            out[id] = tag ? {
              text: tag.textContent,
              dot: getComputedStyle(tag.querySelector('.dot')).backgroundColor,
            } : null;
          }
          return out;
        }""")
        check('c10. 紧凑档 4 盘 mini-tag 文本=GLM/Kimi/DeepSeek/交接',
              all(mt[i] and mt[i]['text'] == t
                  for i, t in [('glm', 'GLM'), ('kimi', 'Kimi'), ('deepseek', 'DeepSeek'), ('handoff', '交接')]),
              json.dumps({k: v and v['text'] for k, v in mt.items()}, ensure_ascii=False))
        check('c11. mini-tag 品牌色点（kimi #1783FF / glm #4268FA / deepseek #4D6BFE / handoff 紫）',
              mt['kimi']['dot'] == 'rgb(23, 131, 255)'
              and mt['glm']['dot'] == 'rgb(66, 104, 250)'
              and mt['deepseek']['dot'] == 'rgb(77, 107, 254)'
              and mt['handoff']['dot'] == 'rgb(155, 126, 222)',
              json.dumps({k: v and v['dot'] for k, v in mt.items()}, ensure_ascii=False))
        check('c12. 紧凑档内容实测 ≤ 新设计内高（322−12=310）',
              c['scrollH'] <= 310, f"scrollH={c['scrollH']}")
        pg.screenshot(path=str(ROOT / 'shot-compact-float.png'))
        pg.evaluate("document.getElementById('widget').classList.add('docked-right')")
        pg.screenshot(path=str(ROOT / 'shot-compact-docked-right.png'))
        pg.close()

        # ── d. 溢出三层修（完整版） ──
        pg = b.new_page(viewport={'width': 132, 'height': 620})
        pg.goto(url('static=1&dev=1'))
        pg.wait_for_selector('.disc[data-id=handoff]')
        d = pg.evaluate("""() => {
          const w = document.getElementById('widget');
          const ds = w.querySelector('.disc[data-id=deepseek]');
          const dsCaps = [...ds.querySelectorAll('.cap')].map(el => ({
            text: el.textContent, w: el.getBoundingClientRect().width,
            clipped: el.scrollWidth > el.clientWidth + 1,
          }));
          const ho = w.querySelector('.disc[data-id=handoff]');
          const hoCaps = [...ho.querySelectorAll('.cap')].map(el => ({
            text: el.textContent, clipped: el.scrollWidth > el.clientWidth + 1,
          }));
          const hoDiscW = ho.getBoundingClientRect().width;
          return { dsCaps, hoCaps, hoDiscW, dsDiscW: ds.getBoundingClientRect().width,
                   contentW: w.clientWidth - 12 };
        }""")
        ds = d['dsCaps']
        check('d1. DS 盘 cap 单行月花费', len(ds) == 1 and ds[0]['text'].startswith('月'),
              json.dumps(ds, ensure_ascii=False))
        check('d2. DS 单行不超宽不裁切', ds and not ds[0]['clipped'] and ds[0]['w'] <= d['contentW'],
              f"w={ds and ds[0]['w']:.1f} contentW={d['contentW']}")
        ho = d['hoCaps']
        check('d3. 交接盘月/周两行完整无裁切',
              len(ho) == 2 and all(not x['clipped'] for x in ho)
              and '月 23 次' in ho[0]['text'] and '周 5 次' in ho[1]['text'],
              json.dumps(ho, ensure_ascii=False))
        # 高度不变量：内容 ≤ 面板内高且余量 ≥6px
        hgt = pg.evaluate("""() => {
          const w = document.getElementById('widget');
          return { scrollH: w.scrollHeight, panelInner: 620 - 12 };
        }""")
        check('d4. 完整版高度不变量（余量≥6px）',
              hgt['panelInner'] - hgt['scrollH'] >= 6,
              f"内容={hgt['scrollH']} 面板内高={hgt['panelInner']} 余量={hgt['panelInner']-hgt['scrollH']}")
        # 第三层·省略号兜底：注入超长文本验证 ellipsis 而非裸裁
        ell = pg.evaluate("""() => {
          const cap = document.querySelector('.disc[data-id=deepseek] .cap');
          cap.textContent = '月 ¥99999.99 · 超长文本注入用于验证省略号兜底机制是否生效';
          const cs = getComputedStyle(cap);
          const r = cap.getBoundingClientRect();
          return { te: cs.textOverflow, ws: cs.whiteSpace,
                   truncated: cap.scrollWidth > cap.clientWidth, w: r.width };
        }""")
        check('d5. 超长文本 → 省略号兜底（truncate 且限宽）',
              ell['te'] == 'ellipsis' and ell['ws'] == 'nowrap' and ell['truncated']
              and ell['w'] <= d['contentW'], json.dumps(ell, ensure_ascii=False))
        pg.reload()
        pg.wait_for_selector('.disc[data-id=handoff]')
        # DS 溢出修复特写 + 交接盘特写
        pg.locator('.disc[data-id=deepseek]').screenshot(path=str(ROOT / 'shot-ds-overflow-fix.png'))
        pg.locator('.disc[data-id=handoff]').screenshot(path=str(ROOT / 'shot-handoff-closeup.png'))
        pg.close()

        # ── d6. superset 回归不炸 ──
        pg = b.new_page(viewport={'width': 132, 'height': 620})
        errs = []
        pg.on('pageerror', lambda e: errs.append(str(e)))
        pg.goto(url('static=1&dev=1&superset=1'))
        pg.wait_for_selector('.disc[data-id=handoff]')
        n = pg.evaluate("document.querySelectorAll('.disc').length")
        check('d6. ?superset=1 回归不炸', n == 4 and not errs, f'discs={n} errs={errs}')
        pg.close()

        # ── d7. 完整档零变化（0.2.5）：不渲染 mini-tag；交接盘圆心仍是两行（c-money+c-sub） ──
        pg = b.new_page(viewport={'width': 132, 'height': 620})
        pg.goto(url('static=1&dev=1'))
        pg.wait_for_selector('.disc[data-id=handoff]')
        f7 = pg.evaluate("""() => {
          const ho = document.querySelector('.disc[data-id=handoff]');
          return {
            miniTags: document.querySelectorAll('.mini-tag').length,
            hoCenter: [...ho.querySelectorAll('svg text')].map(t => t.getAttribute('class')),
          };
        }""")
        check('d7. 完整档零变化：无 mini-tag，交接盘圆心仍两行（c-money+c-sub）',
              f7['miniTags'] == 0 and f7['hoCenter'] == ['c-money', 'c-sub'],
              json.dumps(f7, ensure_ascii=False))
        pg.close()

        # ── e. 伪造壳：双击 grip / 浮层 radio → set_appearance + profile-changed ──
        pg = b.new_page(viewport={'width': 132, 'height': 620})
        pg.add_init_script("""
          window.__CALLS__ = { invoke: [], emit: [] };
          window.__TAURI_INTERNALS__ = { metadata: { currentWindow: { label: 'widget' } } };
          window.__TAURI__ = {
            core: { invoke: (cmd, args) => {
              window.__CALLS__.invoke.push([cmd, args]);
              if (cmd === 'get_profile') return Promise.resolve({ profile: null, reset_reason: 'missing' });
              return Promise.resolve(null);
            } },
            event: {
              listen: (name, cb) => { (window.__LISTENERS__ ||= {})[name] = cb; return Promise.resolve(() => {}); },
              emit: (name, payload) => { window.__CALLS__.emit.push([name, payload]); return Promise.resolve(); },
            },
          };
        """)
        pg.goto(url('static=1&dev=1'))
        pg.wait_for_selector('.disc')
        pg.wait_for_timeout(400) # 等 get_profile 回落落定，免与双击竞态
        pg.locator('#widget .grip').dispatch_event('dblclick')
        pg.wait_for_timeout(200)
        e1 = pg.evaluate("""() => ({
          invoke: window.__CALLS__.invoke, emit: window.__CALLS__.emit,
          compact: document.body.classList.contains('compact'),
        })""")
        sa = [a for c, a in e1['invoke'] if c == 'set_appearance']
        pc = [pl for n, pl in e1['emit'] if n == 'profile-changed']
        check('e1. 双击 grip → set_appearance(compact) + profile-changed + compact class',
              sa and sa[-1].get('mode') == 'compact' and pc and pc[-1].get('appearance') == 'compact'
              and e1['compact'], json.dumps({'set_appearance': sa, 'pc_appearance': [x.get('appearance') for x in pc]}))
        # e2 · 0.2.4 浮层退役：齿轮 → invoke open_settings_window（独立设置窗）。
        # 紧凑态 gear 按规格隐藏（body.compact .gear{display:none}）→ dispatch_event 直发
        pg.locator('#btnSettings').dispatch_event('click')
        pg.wait_for_timeout(200)
        e2 = pg.evaluate("""() => ({
          calls: window.__CALLS__.invoke.map(x => x[0]),
          errs: document.getElementById('selftest-results') ? null : null,
        })""")
        check('e2. 齿轮点击 → invoke open_settings_window（浮层退役后走独立设置窗）',
              'open_settings_window' in e2['calls'], json.dumps(e2['calls'], ensure_ascii=False))
        # e3 · 倒计时行显隐链路（0.2.4 浮层退役后唯一驱动）：设置窗改 profile →
        # profile-changed 广播 → applyProfile → render（cdlineHTML 吃 show_countdown）
        pg.evaluate("""() => {
          window.__LISTENERS__['profile-changed']({ payload: {
            layout: 'vertical', appearance: 'full', show_countdown: false, objects: {} } });
        }""")
        pg.wait_for_timeout(100)
        cd1 = pg.evaluate("""() => {
          const ls = [...document.querySelectorAll('.cdline')];
          return { n: ls.length, hidden: ls.every(el => el.classList.contains('hidden')) };
        }""")
        pg.evaluate("""() => {
          window.__LISTENERS__['profile-changed']({ payload: {
            layout: 'vertical', appearance: 'full', show_countdown: true, objects: {} } });
        }""")
        pg.wait_for_timeout(100)
        cd2 = pg.evaluate("""() => {
          const ls = [...document.querySelectorAll('.cdline')];
          return { n: ls.length, shown: ls.every(el => !el.classList.contains('hidden')) };
        }""")
        check('e3. profile-changed(show_countdown) → render 驱动倒计时行显隐（浮层退役后唯一链路）',
              cd1['n'] >= 2 and cd1['hidden'] and cd2['shown'],
              json.dumps({'off': cd1, 'on': cd2}, ensure_ascii=False))
        pg.close()

        # ── f. 0.2.4 设置窗卡片式（settings.html；660×640=新设计尺寸） ──
        surl = lambda q: f'http://127.0.0.1:{PORT}/settings.html?{q}'
        pg = b.new_page(viewport={'width': 660, 'height': 640})
        pg.goto(surl('selftest=1'))
        pg.wait_for_selector('#selftest-results[data-done="1"]', state='attached')
        sst = pg.evaluate("() => ({...document.getElementById('selftest-results').dataset})")
        sbad = [k for k in ['rows', 'defaults', 'console', 'done'] if sst.get(k) != '1']
        check('f1. 设置窗自测四项全 1（卡片结构）', not sbad, f'data-*={sst}' if sbad else '4/4 全 1')
        cards = pg.evaluate("""() => {
          const card = (id) => {
            const el = document.querySelector(`.obj-card[data-id=${id}]`);
            return el ? {
              visible: !!el.querySelector('.f-visible'),
              colors: el.querySelectorAll('.f-color').length,
              ths: el.querySelectorAll('.f-th').length,
              mb: !!el.querySelector('.f-mb'),
              dsbOn: !!el.querySelector('.f-dsb-on'), dsbAmt: !!el.querySelector('.f-dsb-amt'),
              up: !!el.querySelector('.f-up'), down: !!el.querySelector('.f-down'),
            } : null;
          };
          return {
            n: document.querySelectorAll('.obj-card[data-id]').length,
            glm: card('glm'), kimi: card('kimi'), deepseek: card('deepseek'), handoff: card('handoff'),
          };
        }""")
        check('f2. 4 卡齐全且控件齐（套餐=3 色+2 阈值+月预算；DS=1 色+2 阈值+预算开关/金额；交接=仅显隐排序）',
              cards['n'] == 4
              and all(cards[i] and cards[i]['visible'] and cards[i]['up'] and cards[i]['down']
                      for i in ('glm', 'kimi', 'deepseek', 'handoff'))
              and all(cards[i]['colors'] == 3 and cards[i]['ths'] == 2 and cards[i]['mb']
                      for i in ('glm', 'kimi'))
              and cards['deepseek']['colors'] == 1 and cards['deepseek']['ths'] == 2
              and cards['deepseek']['dsbOn'] and cards['deepseek']['dsbAmt']
              and cards['handoff']['colors'] == 0 and cards['handoff']['ths'] == 0
              and not cards['handoff']['mb'] and not cards['handoff']['dsbOn'],
              json.dumps(cards, ensure_ascii=False))
        hlp = pg.evaluate("""() => {
          const d = document.querySelector('details.sec-help');
          return { open: d.open, txt: d.textContent.includes('剩余制') };
        }""")
        check('f3. 图例与帮助默认收起（details[open] 为假、内容在）',
              hlp['open'] is False and hlp['txt'], json.dumps(hlp, ensure_ascii=False))
        pg.screenshot(path=str(ROOT / 'shot-settings-cards.png'), full_page=True)
        pg.locator('details.sec-help summary').click()
        hlp2 = pg.evaluate("""() => {
          const d = document.querySelector('details.sec-help');
          const h = d.querySelector('.help').getBoundingClientRect();
          return { open: d.open, visible: h.height > 0 };
        }""")
        check('f4. 帮助点开后内容可见', hlp2['open'] and hlp2['visible'], json.dumps(hlp2))
        pg.screenshot(path=str(ROOT / 'shot-settings-help-open.png'), full_page=True)
        pg.locator('details.sec-help summary').click()  # 收起还原，防干扰后续截图语义
        # collect 往返：改 glm 黄阈值 20→33 → 点 kimi ↑ 触发重渲染 → glm 卡读回 33
        pg.evaluate("""() => {
          const i = document.querySelector('.obj-card[data-id=glm] [data-th=yellow]');
          i.value = '33'; i.dispatchEvent(new Event('change', { bubbles: true }));
        }""")
        pg.locator('.obj-card[data-id=kimi] .f-up').click()
        rt = pg.evaluate("""() => ({
          glmY: document.querySelector('.obj-card[data-id=glm] [data-th=yellow]').value,
          firstCard: document.querySelector('.obj-card[data-id]').dataset.id,
          status: document.getElementById('status').textContent,
        })""")
        check('f5. collect 往返（阈值改动经 collect→profile→重渲染读回 33；排序交换生效）',
              rt['glmY'] == '33' and rt['firstCard'] == 'kimi', json.dumps(rt, ensure_ascii=False))
        pg.close()

        b.close()
finally:
    srv.terminate()

(ROOT / 'verify-result.json').write_text(json.dumps(
    [{'name': n, 'ok': ok, 'detail': d} for n, ok, d in results], ensure_ascii=False, indent=1),
    encoding='utf-8')
fails = [n for n, ok, _ in results if not ok]
print(f"\n合计 {len(results)} 项，失败 {len(fails)} 项" + (f"：{fails}" if fails else ''))
sys.exit(1 if fails else 0)
