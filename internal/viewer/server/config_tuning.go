// config_tuning.go — 票08:配置与调参页(只读;D11/D12)。
//
// 两个路由,都注册在 Routes()(panelMux 首块调用,cmd 装配层零改动即挂上):
//   - GET /api/config-tuning 数据代理:服务端读 <dataDir>/daemon.token 持
//     Bearer 调守护 GET /config_tuning(127.0.0.1:[server].port,缺省 7311),
//     原样中转 JSON。token 只活在服务端,永不出现在给页面的任何字节里。
//     守护不可达/token 缺席 → 200 {ok:false, note} 如实降级(页面渲染说明,
//     不编造数据)。
//   - GET /config-tuning 自包含页面:调参三列(配置值/生效值/建议值,按上游
//     分列,三列独立表头禁止混排——widget 查询值/估算值分列规矩同款)+
//     状态徽章(待审/已接受/已拒绝/已自动应用)+ 全配置总览(密钥掩码照
//     daemon 返回呈现)。纯只读:整页无按钮、无表单、无任何 POST——写操作
//     唯一路径在 CLI(D11),页面文案也钉死这句话。

package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// ctDaemonDefaultPort 守护默认端口(config.toml 缺 [server].port 时)。
const ctDaemonDefaultPort = 7311

// ctProxyClient 代理出站客户端:短超时——守护没起时页面要快拿降级说明。
var ctProxyClient = &http.Client{Timeout: 4 * time.Second}

// daemonTarget 解析守护地址与 token。端口取 config.toml [server].port(路径
// 沿用 ConfigPath 惯例:与账本同根;解析失败回落默认口)。返回
// (baseURL, token);note 非空 = 不能发起查询的原因(人话,页面如实展示)。
func (s *Server) daemonTarget() (string, string, string) {
	port := ctDaemonDefaultPort
	if p := ConfigPath(s.dataDir); p != "" {
		if raw, err := os.ReadFile(p); err == nil {
			var doc struct {
				Server struct {
					Port int
				}
			}
			if toml.Unmarshal(raw, &doc) == nil && doc.Server.Port > 0 {
				port = doc.Server.Port
			}
		}
	}
	tokenPath := filepath.Join(s.dataDir, "daemon.token")
	b, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", "", fmt.Sprintf("未读到守护 token(%s)——守护进程可能未运行;配置与调参数据缺席,守护在跑时刷新即得", tokenPath)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), strings.TrimSpace(string(b)), ""
}

// handleConfigTuningAPI GET /api/config-tuning:服务端持 token 代理守护。
func (s *Server) handleConfigTuningAPI(w http.ResponseWriter, _ *http.Request) {
	base, token, note := s.daemonTarget()
	if note != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "note": note})
		return
	}
	req, err := http.NewRequest(http.MethodGet, base+"/config_tuning", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false,
			"note": "构造守护请求失败: " + err.Error()})
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := ctProxyClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false,
			"note": fmt.Sprintf("守护进程不可达(%s):%v——配置与调参数据缺席,守护在跑时刷新即得", base, err)})
		return
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false,
			"note": "读取守护响应失败: " + err.Error()})
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false,
			"note": fmt.Sprintf("守护返回 %d(需 Bearer token)——配置与调参数据缺席", resp.StatusCode)})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// handleConfigTuningPage GET /config-tuning:自包含只读页。
func (s *Server) handleConfigTuningPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, ctPageHTML)
}

// ctPageHTML 页面本体(内联 CSS/JS,零外部依赖、零构建步骤;静态资产在
// cmd/ferryman/web——路径外,故本页自包含,经面板口 /config-tuning 直达)。
// 只读铁律的三重落实:无任何表单/按钮元素;JS 只有 GET fetch;页面文案钉
// "写操作只在 CLI"。
const ctPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>配置与调参 · Ferryman</title>
<style>
  :root { --bd:#e2e2e6; --mut:#6b6b74; --bg:#f7f7f9; --ok:#1a7f37; --warn:#9a6700; --bad:#b42318; --info:#0550ae; }
  * { box-sizing: border-box; }
  body { font: 14px/1.6 system-ui, "Segoe UI", "Microsoft YaHei", sans-serif; color:#1c1c21; margin:0; background:var(--bg); }
  main { max-width: 1080px; margin: 0 auto; padding: 24px 16px 48px; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  h2 { font-size: 16px; margin: 28px 0 8px; border-bottom: 1px solid var(--bd); padding-bottom: 6px; }
  h3 { font-size: 13px; margin: 18px 0 4px; color: var(--mut); }
  .sub { color: var(--mut); margin: 0 0 16px; }
  .note { background:#fff8e1; border:1px solid #eadfa8; border-radius:8px; padding:10px 12px; margin:12px 0; }
  .card { background:#fff; border:1px solid var(--bd); border-radius:10px; padding:12px 14px; margin:10px 0; overflow-x:auto; }
  table { border-collapse: collapse; width: 100%; }
  th, td { text-align: left; padding: 7px 10px; border-bottom: 1px solid var(--bd); vertical-align: top; }
  th { color: var(--mut); font-weight: 600; white-space: nowrap; background:#fafafc; }
  tr:last-child td { border-bottom: none; }
  td.num { font-variant-numeric: tabular-nums; white-space: nowrap; }
  .badge { display:inline-block; border-radius:999px; padding:1px 10px; font-size:12px; border:1px solid; white-space:nowrap; }
  .b-pending   { color:var(--warn); border-color:var(--warn); background:#fff8e1; }
  .b-accepted  { color:var(--ok);  border-color:var(--ok);  background:#eaf5ec; }
  .b-rejected  { color:var(--bad); border-color:var(--bad); background:#fdeeee; }
  .b-auto      { color:var(--info); border-color:var(--info); background:#e8f1fc; }
  .b-none      { color:var(--mut); border-color:var(--bd); background:#f4f4f6; }
  .mut { color: var(--mut); }
  .small { font-size: 12px; color: var(--mut); }
  .mask { font-family: ui-monospace, Consolas, monospace; letter-spacing: 1px; color:#7a5c00; }
  .err { color: var(--bad); }
  code { font-family: ui-monospace, Consolas, monospace; font-size: 13px; background:#f1f1f4; border-radius:4px; padding:1px 5px; }
  a { color: var(--info); }
</style>
</head>
<body>
<main>
  <h1>配置与调参（只读）</h1>
  <p class="sub">全配置总览（密钥一律打码）与调参三列对比。<a href="/">← 返回时间线</a></p>
  <p class="small">建议状态徽章图例：
    <span class="badge b-pending">待审</span>
    <span class="badge b-accepted">已接受</span>
    <span class="badge b-rejected">已拒绝</span>
    <span class="badge b-auto">已自动应用</span>
  </p>
  <div id="note"></div>
  <div id="content"></div>
  <p class="small">本页纯只读：无按钮、无表单。写操作唯一路径在 CLI——
  <code>ferryman tuning status / apply / reject / rollback</code>；改配置请手编
  <code>config.toml</code> 后重启守护生效。</p>
</main>
<script>
"use strict";
function esc(s) {
  return String(s).replace(/[&<>"']/g, function (c) {
    return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
  });
}
function f1(v) {
  if (v === null || v === undefined) return "—";
  return Number(v).toFixed(1);
}
function badge(status, text) {
  var cls = { pending: "b-pending", accepted: "b-accepted",
              rejected: "b-rejected", auto_applied: "b-auto" }[status] || "b-none";
  return '<span class="badge ' + cls + '">' + esc(text) + "</span>";
}
function renderTuning(d) {
  var ups = d.upstreams || [];
  var h = '<h2>调参三列（按上游分列）</h2>';
  h += '<p class="small">配置值 = config.toml 手编（manual 档下即生效值，其余档下为生效值上限）；' +
       '生效值 = 策略计算器现算（实际在跑）；建议值 = 扫参最新建议（写操作只在 CLI）。当前档位：' +
       '<code>' + esc(d.mode || "—") + '</code>（永不自动升档）</p>';
  if (!ups.length) {
    h += '<div class="note">同模型白名单为空（[ferry.same_model].upstreams）——无上游可显示三列。</div>';
    return h;
  }
  h += '<div class="card"><table><thead><tr>' +
       '<th>上游</th><th>配置值（分钟）</th><th>生效值（分钟）</th><th>建议值（分钟）</th><th>状态</th>' +
       '</tr></thead><tbody>';
  ups.forEach(function (u) {
    h += '<tr><td>' + esc(u.upstream) + '</td>';
    h += '<td class="num">' + f1(u.configured_min) + '</td>';
    if (u.effective_ok) {
      h += '<td class="num">' + f1(u.effective_min);
      var det = u.effective_detail || {};
      if (det.econ_floor_min && det.econ_floor_min > u.effective_min) {
        h += '<div class="small">低于最早不亏点 ' + f1(det.econ_floor_min) +
             ' 分钟（被配置上限压低，经济面从紧）</div>';
      }
      if (det.seed_fallback) {
        h += '<div class="small">种子基线（无 TTL 观测）</div>';
      }
      h += '</td>';
    } else {
      h += '<td class="num err" title="' + esc(u.effective_err_text || u.effective_err || "") + '">' +
           '拒算（' + esc(u.effective_err || "error") + '）</td>';
    }
    var sug = u.suggestion;
    if (sug) {
      h += '<td class="num">' + f1(sug.suggest_min) +
           '<div class="small">现值 ' + f1(sug.current_min) + ' · 净省 ' + f1(sug.net_savings) +
           ' · 窗内事件 ' + esc(sug.ferry_events) + "/" + esc(sug.min_events) + '</div></td>';
      h += '<td>' + badge(sug.status, sug.status_text || sug.status) + '</td>';
    } else {
      h += '<td class="num mut">' + esc(u.suggestion_text || "无建议") + '</td><td><span class="badge b-none">—</span></td>';
    }
    h += '</tr>';
  });
  h += '</tbody></table></div>';
  return h;
}
function renderConfig(c) {
  var h = '<h2>全配置总览</h2>';
  if (!c || !c.found) {
    h += '<div class="note">' + esc((c && c.note) || "配置文件不可读——全配置总览缺席。") + '</div>';
    return h;
  }
  h += '<p class="small">' + esc(c.path || "") +
       (c.mtime ? ' · 修改于 ' + esc(c.mtime) : '') +
       (c.note ? ' · ' + esc(c.note) : '') + '</p>';
  (c.sections || []).forEach(function (sec) {
    h += '<h3>' + esc(sec.name) + '</h3><div class="card"><table><tbody>';
    (sec.rows || []).forEach(function (r) {
      var val = r.redact ? '<span class="mask">' + esc(r.value) + '</span>' : esc(r.value);
      h += '<tr><th style="width:280px">' + esc(r.key) + '</th><td>' + val + '</td></tr>';
    });
    h += '</tbody></table></div>';
  });
  return h;
}
function render(d) {
  var note = document.getElementById("note");
  var content = document.getElementById("content");
  if (d && d.ok === false) {
    note.innerHTML = '<div class="note">' + esc(d.note || "数据缺席") + '</div>';
    content.innerHTML = "";
    return;
  }
  note.innerHTML = "";
  content.innerHTML = renderTuning(d) + renderConfig(d.config);
}
fetch("/api/config-tuning")
  .then(function (r) { return r.json(); })
  .then(render)
  .catch(function (e) {
    document.getElementById("note").innerHTML =
      '<div class="note">面板数据代理异常：' + esc(e) + '</div>';
  });
</script>
</body>
</html>
`
