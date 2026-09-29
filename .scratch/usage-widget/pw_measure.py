# 0.2.3 · 紧凑设计尺寸实测 + 完整版高度不变量实测（简报 §3-B / §3-E 的定值依据）
# 用法：python pw_measure.py   （本地 http.server 伺服 widget/ui，chromium headless）
import json, subprocess, sys, time, socket
from pathlib import Path
from playwright.sync_api import sync_playwright

# 控制台可能非 UTF-8（GBK 下部分字符会炸）——统一按 UTF-8 输出
try:
    sys.stdout.reconfigure(encoding='utf-8', errors='replace')
except Exception:
    pass

ROOT = Path(__file__).resolve().parent
UI = ROOT.parent.parent / 'widget' / 'ui'

def free_port():
    s = socket.socket(); s.bind(('127.0.0.1', 0)); p = s.getsockname()[1]; s.close(); return p

PORT = free_port()
srv = subprocess.Popen([sys.executable, '-m', 'http.server', str(PORT), '-b', '127.0.0.1'],
                       cwd=UI, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
time.sleep(1.0)

MEASURE_JS = """
() => {
  const w = document.getElementById('widget');
  const discs = [...w.querySelectorAll('.disc')].map(d => {
    const r = d.getBoundingClientRect();
    return { id: d.dataset.id, w: r.width, h: r.height };
  });
  const grip = w.querySelector('.grip').getBoundingClientRect();
  const kids = [...w.children].filter(el => getComputedStyle(el).display !== 'none')
    .map(el => { const r = el.getBoundingClientRect(); return { cls: el.className || el.id, w: r.width, h: r.height }; });
  return {
    scrollH: w.scrollHeight, scrollW: w.scrollWidth,
    clientH: w.clientHeight, clientW: w.clientWidth,
    discs, grip: { w: grip.width, h: grip.height }, kids,
    compact: document.body.classList.contains('compact'),
  };
}
"""

out = {}
try:
    with sync_playwright() as p:
        b = p.chromium.launch()

        # ── 完整版高度不变量：132×620 视口，演示满载 4 盘 ──
        pg = b.new_page(viewport={'width': 132, 'height': 620})
        pg.goto(f'http://127.0.0.1:{PORT}/index.html?static=1&dev=1')
        pg.wait_for_selector('.disc[data-id=handoff]')
        m = pg.evaluate(MEASURE_JS)
        out['full'] = m
        print('== 完整版 132×620 ==')
        print(f"  widget scrollH={m['scrollH']} clientH={m['clientH']} scrollW={m['scrollW']} clientW={m['clientW']}")
        print(f"  面板内高=608  余量={608 - m['scrollH']}px")
        for k in m['kids']:
            print(f"    {k['cls']!r}: {k['w']:.1f}×{k['h']:.1f}")
        pg.close()

        # ── 紧凑版内容实测：大视口（不裁切），?profile=compact ──
        pg = b.new_page(viewport={'width': 400, 'height': 900})
        pg.goto(f'http://127.0.0.1:{PORT}/index.html?profile=compact&static=1&dev=1')
        pg.wait_for_selector('.disc[data-id=handoff]')
        m = pg.evaluate(MEASURE_JS)
        out['compact'] = m
        print('== 紧凑版（大视口实测内容） ==')
        print(f"  compact class={m['compact']} scrollH={m['scrollH']} scrollW={m['scrollW']}")
        for k in m['kids']:
            print(f"    {k['cls']!r}: {k['w']:.1f}×{k['h']:.1f}")
        pg.close()

        # ── 紧凑横排（0.2.4 浮层退役：页内 layout radio 已无，直接切 class 等效 setLayout） ──
        pg = b.new_page(viewport={'width': 1366, 'height': 400})
        pg.goto(f'http://127.0.0.1:{PORT}/index.html?profile=compact&static=1&dev=1')
        pg.wait_for_selector('.disc[data-id=handoff]')
        pg.evaluate("""() => {
          const w = document.getElementById('widget');
          w.classList.remove('vertical'); w.classList.add('horizontal');
        }""")
        m = pg.evaluate(MEASURE_JS)
        out['compact_h'] = m
        print('== 紧凑×横排 ==')
        print(f"  scrollW={m['scrollW']} scrollH={m['scrollH']}（一行迷你盘，要求总宽 ≤1366）")
        pg.close()
        b.close()
finally:
    srv.terminate()

(ROOT / 'measure-result.json').write_text(json.dumps(out, ensure_ascii=False, indent=1))
print('已存 measure-result.json')
