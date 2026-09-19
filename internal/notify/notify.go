// Package notify T25 · 拦截通知：block 发生时 Pushover（手机）+ Windows Toast
// （桌面），文案带交接路径（规格 ferryman/notify.py 1:1）。
//
// 原则（DESIGN §6，notify.py docstring 逐字搬运）：
//   - 通知是尽力而为的旁路——任何故障只吞掉（返回 false / 记日志），绝不影响
//     gate 决策；
//   - 异步线程发送（gate 返回不等通知；Pushover 慢网不拖闸门）——由调用方起
//     goroutine，本包只供同步原语；
//   - 默认 enabled=False（未配置不响；也保证测试套件不弹真 toast / 不出网）；
//   - Pushover 凭据：config [notify] 优先，缺省回落环境变量
//     PUSHOVER_TOKEN/PUSHOVER_USER（与 claude-notify 插件共用同一对凭据）。
package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"ferryman/internal/config"
)

// PushoverURL 测试可换（httptest 假端点；Python PUSHOVER_URL 同位）。
var PushoverURL = "https://api.pushover.net/1/messages.json"

const (
	// pushoverTimeout Python timeout=4.0 同值（测试钉死）。
	pushoverTimeout = 4 * time.Second
	// toastTimeout Python timeout=5.0 同值（测试钉死）。
	toastTimeout = 5 * time.Second
)

// SendPushover 优先级 1 表单推送；任何故障（网络/构造/非 2xx 语义外）只回 false。
func SendPushover(title, message, token, user string) bool {
	form := url.Values{}
	form.Set("token", token)
	form.Set("user", user)
	form.Set("title", title)
	form.Set("message", message)
	form.Set("priority", "1")
	req, err := http.NewRequest(http.MethodPost, PushoverURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: pushoverTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false // 旁路：网络/解析任何故障一律吞
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// xmlEscape 对应 xml.sax.saxutils.escape：只转 & < >（Go xml.EscapeText 额外转
// 引号/换行——为逐字平移 XML 模板自写三连替换；& 必须最先）。
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// toastXML WinRT Toast 载荷（ToastText02 模板，与 claude-notify 插件同款）。
func toastXML(title, message string) string {
	return `<toast><visual><binding template="ToastText02">` +
		`<text id="1">` + xmlEscape(title) + `</text>` +
		`<text id="2">` + xmlEscape(message) + `</text>` +
		`</binding></visual></toast>`
}

// toastScript powershell 命令串（notify.py f-string 逐字平移）。
func toastScript(title, message string) string {
	xmlBody := toastXML(title, message)
	return "[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications," +
		" ContentType = WindowsRuntime] | Out-Null\n" +
		"[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument," +
		" ContentType = WindowsRuntime] | Out-Null\n" +
		"$xml = New-Object Windows.Data.Xml.Dom.XmlDocument\n" +
		"$xml.LoadXml(@'\n" + xmlBody + "\n'@)\n" +
		"$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)\n" +
		"[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Ferryman')" +
		".Show($toast)"
}

// toastArgs powershell argv 单源（subprocess.run 参数 1:1）。
func toastArgs(ps string) []string {
	return []string{"powershell", "-NoProfile", "-Command", ps}
}

// toastCmd 组装 powershell 命令。提为独立函数供测试断言（Python 测试断言
// args[0]=="powershell"——Go 侧 mock 掉 runToast 后该断言转测本函数，不真执行）。
func toastCmd(ps string) *exec.Cmd {
	args := toastArgs(ps)
	return exec.Command(args[0], args[1:]...)
}

// runToast 命令执行 seam——包级 var 供 mock（Python monkeypatch subprocess.run
// 同位）。默认：powershell 执行，toastTimeout 到点杀进程（≡ TimeoutExpired）。
var runToast = func(ps string) error {
	ctx, cancel := context.WithTimeout(context.Background(), toastTimeout)
	defer cancel()
	args := toastArgs(ps)
	return exec.CommandContext(ctx, args[0], args[1:]...).Run() // 退出码 0 ↔ nil（≡ returncode == 0）
}

// SendToast Win10 WinRT Toast（无第三方模块）；超时/PowerShell 任何故障一律 false。
func SendToast(title, message string) bool {
	if err := runToast(toastScript(title, message)); err != nil {
		return false
	}
	return true
}

// NotifyAlert T51 通用告警（问询守望熔断等）：双通道 best-effort，只发元信息
// 文案。同步发送、绝不抛出（recover 双保险：通道内部已吞，这里兜组装层）；
// 是否异步由调用方决定（与 NotifyBlock 同纪律）。
func NotifyAlert(title, message string, cfg *config.Config) {
	defer func() { // Python except Exception → print 兜底同位
		if r := recover(); r != nil {
			fmt.Printf("[notify] 通知失败（忽略）: %v\n", r)
		}
	}()
	n := cfg.Notify
	if !n.Enabled {
		return
	}
	if n.Pushover {
		token := n.PushoverToken
		if token == "" {
			token = os.Getenv("PUSHOVER_TOKEN")
		}
		user := n.PushoverUser
		if user == "" {
			user = os.Getenv("PUSHOVER_USER")
		}
		if token != "" && user != "" {
			SendPushover(title, message, token, user)
		}
	}
	if n.Toast {
		SendToast(title, message)
	}
}

// NotifyBlock 拦截发生：双通道通知（文案带交接路径）。绝不抛出。
// agent 形参保留（Python 签名对齐，文案不带 agent）。
// 票08：标题走 BuildTitle 降级链（项目名＋会话标题——用户痛点：旧文案只有
// 裸数字 sid 无法分辨哪个项目哪个会话）；project/sessionTitle 皆空时回落
// 旧标题（直调/旧测试兼容）。session id 移正文尾部小字。
func NotifyBlock(handoffPath, agent, sessionID, project, sessionTitle string, cfg *config.Config) {
	_ = agent
	sid := sessionID
	if rs := []rune(sid); len(rs) > 8 { // Python session_id[:8] 按码点切
		sid = string(rs[:8])
	}
	message := "会话闲置被拦，交接已生成：\n" + handoffPath + "\n" +
		"新会话发任意字即可取回上下文。"
	if sid != "" {
		message += "\n(sid=" + sid + ")"
	}
	title := "Ferryman 拦截"
	if project != "" || sessionTitle != "" {
		title = BuildTitle(project, sessionTitle, "")
	}
	NotifyAlert(title, message, cfg)
}
