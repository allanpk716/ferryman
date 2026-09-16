"""T25 · 拦截通知：block 发生时 Pushover（手机）+ Windows Toast（桌面），文案带交接路径。

原则（DESIGN §6）：
- 通知是尽力而为的旁路——任何故障只吞掉（返回 False / 记日志），绝不影响 gate 决策；
- 异步线程发送（gate 返回不等通知；Pushover 慢网不拖闸门）；
- 默认 enabled=False（未配置不响；也保证测试套件不弹真 toast / 不出网）；
- Pushover 凭据：config [notify] 优先，缺省回落环境变量 PUSHOVER_TOKEN/PUSHOVER_USER
  （与 claude-notify 插件共用同一对凭据）。
"""

from __future__ import annotations

import os
import subprocess
import urllib.parse
import urllib.request
from xml.sax.saxutils import escape

PUSHOVER_URL = "https://api.pushover.net/1/messages.json"


def send_pushover(title: str, message: str, *, token: str, user: str,
                  timeout: float = 4.0) -> bool:
    data = urllib.parse.urlencode({
        "token": token, "user": user, "title": title,
        "message": message, "priority": 1}).encode("utf-8")
    try:
        with urllib.request.urlopen(
                urllib.request.Request(PUSHOVER_URL, data=data), timeout=timeout) as r:
            return 200 <= r.status < 300
    except Exception:  # noqa: BLE001 — 旁路：网络/解析任何故障一律吞
        return False


def send_toast(title: str, message: str, *, timeout: float = 5.0) -> bool:
    """Win10 WinRT Toast（无第三方模块；claude-notify 插件同款机制，本机已验证）。"""
    xml = (f'<toast><visual><binding template="ToastText02">'
           f'<text id="1">{escape(title)}</text>'
           f'<text id="2">{escape(message)}</text>'
           f'</binding></visual></toast>')
    ps = (
        "[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications,"
        " ContentType = WindowsRuntime] | Out-Null\n"
        "[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument,"
        " ContentType = WindowsRuntime] | Out-Null\n"
        "$xml = New-Object Windows.Data.Xml.Dom.XmlDocument\n"
        f"$xml.LoadXml(@'\n{xml}\n'@)\n"
        "$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)\n"
        "[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Ferryman')"
        ".Show($toast)")
    try:
        r = subprocess.run(["powershell", "-NoProfile", "-Command", ps],
                           capture_output=True, timeout=timeout)
        return r.returncode == 0
    except Exception:  # noqa: BLE001 — 旁路：超时/PowerShell 任何故障一律吞
        return False


def notify_block(handoff_path: str, agent: str, session_id: str, cfg) -> None:
    """拦截发生：双通道通知（文案带交接路径）。绝不抛出。"""
    try:
        n = cfg.notify
        if not n.enabled:
            return
        title = "Ferryman 拦截"
        message = (f"会话 {session_id[:8]} 闲置被拦，交接已生成：\n{handoff_path}\n"
                   "新会话发任意字即可取回上下文。")
        if n.pushover:
            token = n.pushover_token or os.environ.get("PUSHOVER_TOKEN", "")
            user = n.pushover_user or os.environ.get("PUSHOVER_USER", "")
            if token and user:
                send_pushover(title, message, token=token, user=user)
        if n.toast:
            send_toast(title, message)
    except Exception as e:  # noqa: BLE001 — 双保险（通道内部已吞，这里兜组装层）
        print(f"[notify] 通知失败（忽略）: {e}", flush=True)
