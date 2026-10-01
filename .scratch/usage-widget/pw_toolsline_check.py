# 0.2.3 评审补测：完整版高度预算在「Kimi 工具额度兜底行」(真机 V2+ 有、demo 无)下是否仍达标。
# 达标线：内容高(scrollHeight) ≤ 面板内高608 − 余量6 = 602。
import threading, http.server, socketserver, functools, json, sys, io
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding='utf-8')
from playwright.sync_api import sync_playwright

UI = r'C:\Users\allan716\orca\workspaces\Ferryman\悬浮窗修复显示问题\widget\ui'
srv = socketserver.TCPServer(('127.0.0.1', 0), functools.partial(http.server.SimpleHTTPRequestHandler, directory=UI))
port = srv.server_address[1]
threading.Thread(target=srv.serve_forever, daemon=True).start()

with sync_playwright() as p:
    b = p.chromium.launch()
    pg = b.new_page(viewport={'width': 132, 'height': 620})
    pg.goto(f'http://127.0.0.1:{port}/index.html?static=1&dev=1')
    pg.wait_for_timeout(800)
    m = pg.evaluate("""() => {
      const w = document.getElementById('widget');
      const h1 = w.scrollHeight;
      // 模拟 app.js 的 V2+ 工具兜底行（metricOf(week)&&tq 才渲染，demo 数据无 tools → demo 量不到这行）
      const kimi = [...document.querySelectorAll('.disc')].find(d => d.dataset.id === 'kimi');
      const cap = document.createElement('div'); cap.className = 'cap';
      cap.textContent = '工具 剩 42% · 1200/1500';
      kimi.appendChild(cap);
      return { demoH: h1, toolsH: w.scrollHeight, clientH: w.clientHeight, panelInner: 608, budget: 602 };
    }""")
    print(json.dumps(m, ensure_ascii=False))
    ok = m['toolsH'] <= m['budget']
    print('PASS' if ok else f"FAIL 超预算 {m['toolsH'] - m['budget']}px（demo 基线 {m['demoH']}）")
    b.close()
srv.shutdown()
