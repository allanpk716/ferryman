# 0.2.3 · Playwright 全套验证（简报 §4 a–f）+ 截图
# 用法：python pw_verify.py  （本地 http.server 伺服 widget/ui，chromium headless）
# 结果打印 PASS/FAIL 逐条；截图与 JSON 存本目录。
import json, subprocess, sys, time, socket
from pathlib import Path
from playwright.sync_api import sync_playwright

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

        # ── c. 紧凑版（?profile=compact；80×272 视口=Rust 设计尺寸） ──
        pg = b.new_page(viewport={'width': 80, 'height': 272})
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
        check('c5. 交接盘圆心有次数', '23' in c['handoffCenter'] and '次 · 本月' in c['handoffCenter'],
              c['handoffCenter'])
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
              listen: () => Promise.resolve(() => {}),
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
        # 浮层外观 radio 同链（切回完整；紧凑态 gear 按规格隐藏 → dispatch_event 直发）
        pg.locator('#btnSettings').dispatch_event('click')
        pg.locator('input[name=appearance][value=full]').click()
        pg.wait_for_timeout(200)
        e2 = pg.evaluate("""() => ({
          sa: window.__CALLS__.invoke.filter(x => x[0] === 'set_appearance').map(x => x[1]),
          compact: document.body.classList.contains('compact'),
          checked: document.querySelector('input[name=appearance]:checked').value,
        })""")
        check('e2. 浮层外观 radio → set_appearance(full) + 摘 compact class',
              e2['sa'] and e2['sa'][-1].get('mode') == 'full' and not e2['compact']
              and e2['checked'] == 'full', json.dumps(e2, ensure_ascii=False))
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
