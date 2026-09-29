// 票 02 · 窗口与常驻形态：托盘 / 单实例 / 自启 / 位置记忆 / 关窗收托盘。
// 零闪窗铁律不变：conf visible:false，setup 末尾（位置恢复之后）才 show。
use std::{
    fs, path::PathBuf, sync::mpsc, sync::Mutex,
    time::{Duration, Instant},
};

use tauri::{
    menu::{Menu, MenuItem},
    tray::TrayIconBuilder,
    Emitter, Manager, PhysicalPosition, WindowEvent,
};
use tauri_plugin_autostart::MacosLauncher;
use tauri_plugin_updater::UpdaterExt;

/// 持久化的窗口几何（票 02 只记位置；尺寸不可缩放无需记——自愈只断言设计
/// 值 132×620 逻辑 px，同样无需记忆）。
#[derive(serde::Serialize, serde::Deserialize)]
struct WindowState {
    x: i32,
    y: i32,
}

/// Moved 事件节流：拖动中每 500ms 至多写一次盘。
struct MoveThrottle(Mutex<Option<Instant>>);

/// 几何事件通道句柄：Moved/Resized/ScaleFactorChanged 每发必喂（磁吸防抖
/// 要全量 Moved 事件流，不走 500ms 节流；自愈断言同样要全量几何事件流）；
/// worker 线程持有接收端。Sender 包 Mutex 进 manage（Sender 是 Send 非 Sync）。
struct GeomFeed(Mutex<mpsc::Sender<GeomEvent>>);

/// 喂给几何 worker 的事件：移动（磁吸原料）与尺寸/缩放触碰（自愈原料）。
/// RDP 断开重连时两类事件都会来且种类不保证（2026-09-29 实测塌缩时 Moved
/// 必发、Resized 未必发），worker 按「事件流静止即全面断言」处理，不押注
/// 单一事件种类。
enum GeomEvent {
    Moved(PhysicalPosition<i32>),
    SizeTouched,
}

/// 磁吸参数：松手判定静止时长 / 吸附距离（16 逻辑 px，×scale 换物理）/
/// 启动静默窗（位置恢复的 Moved 不得吸走用户存的边距）。
const SNAP_SETTLE_MS: u64 = 250;
const SNAP_NEAR_LOGICAL: f64 = 16.0;
const SNAP_ARM_DELAY: Duration = Duration::from_secs(2);

/// 几何自愈（2026-09-29 RDP 塌缩事故，ADR-0016）：设计尺寸=tauri.conf.json
/// 的 132×620 逻辑 px——窗口不可缩放，「内容完整最小尺寸」即唯一尺寸。
/// RDP 断开瞬间会话 DPI 150%→100%，Windows 把窗口物理尺寸 ×2/3 而重连不
/// 还原（实测 930×(2/3)³=275），widget 代码从不写尺寸、防不了系统；改为
/// 事件流静止后断言：物理尺寸偏离设计值超过容忍即拉回，位置夹回工作区。
const DESIGN_W_LOGICAL: f64 = 132.0;
const DESIGN_H_LOGICAL: f64 = 620.0;
/// 物理尺寸比较容忍（px）：吸收 DPI 换算取整抖动。
const SIZE_TOLERANCE_PX: i32 = 2;
/// 自愈兜底轮询间隔：极端场景事件全漏时，塌缩到发现的时限仍有界（≤此值）。
const GEOM_POLL: Duration = Duration::from_secs(5);

/// 贴边求值（worker 线程调）：窗口左上角落在哪块屏就贴哪块屏的工作区
/// （工作区=扣任务栏，任务栏在哪侧都不吸到屏外）；四边独立判定，角落
/// 两边同吸。已在目标位（x,y 未变）不 set——避免 set→Moved→再评估的
/// 自激循环。贴齐位立即落盘（不等 500ms 节流）。
/// 左上角落点定屏：窗口左上角落在哪块显示器内就算哪块的（磁吸、自愈夹回
/// 与位置恢复三处同口径）。
fn monitor_containing(mons: &[tauri::Monitor], pos: PhysicalPosition<i32>) -> Option<&tauri::Monitor> {
    mons.iter().find(|m| {
        let p = m.position();
        let s = m.size();
        pos.x >= p.x && pos.x < p.x + s.width as i32 && pos.y >= p.y && pos.y < p.y + s.height as i32
    })
}

fn snap_if_near(app: &tauri::AppHandle, pos: PhysicalPosition<i32>) {
    let Some(w) = app.get_webview_window("widget") else { return };
    let Ok(ws) = w.outer_size() else { return };
    let Ok(mons) = app.available_monitors() else { return };
    let Some(mon) = monitor_containing(&mons, pos) else {
        return;
    };
    let wa = mon.work_area();
    let (wx, wy, ww, wh) = (
        wa.position.x,
        wa.position.y,
        wa.size.width as i32,
        wa.size.height as i32,
    );
    let thr = (SNAP_NEAR_LOGICAL * mon.scale_factor()).round() as i32;
    let (cw, ch) = (ws.width as i32, ws.height as i32);
    let (mut x, mut y, mut snapped) = (pos.x, pos.y, false);
    if (pos.x - wx).abs() <= thr {
        x = wx;
        snapped = true;
    } else if (wx + ww - (pos.x + cw)).abs() <= thr {
        x = wx + ww - cw;
        snapped = true;
    }
    if (pos.y - wy).abs() <= thr {
        y = wy;
        snapped = true;
    } else if (wy + wh - (pos.y + ch)).abs() <= thr {
        y = wy + wh - ch;
        snapped = true;
    }
    if !snapped || (x == pos.x && y == pos.y) {
        return;
    }
    let _ = w.set_position(PhysicalPosition::new(x, y));
    save_state(app, x, y);
}

/// 几何自愈断言（worker 线程调）：物理尺寸偏离设计值（按当前 DPI 换算）超
/// 容忍即 set_size 拉回，并把位置夹回所在屏工作区——60px 窄条恢复成 132
/// 逻辑宽后右/下缘可能压出屏外（2026-09-29 实测：x=4830 + 198 宽 > 竖屏
/// 右缘 4920）。位置被夹动才落盘（未动不写，与磁吸同习）。对任何原因的
/// 塌缩都收敛，不限 RDP DPI 路径。
fn assert_geometry(app: &tauri::AppHandle) {
    let Some(w) = app.get_webview_window("widget") else { return };
    let Ok(cur) = w.outer_size() else { return };
    let sf = w.scale_factor().unwrap_or(1.0);
    let (want_w, want_h) = (
        (DESIGN_W_LOGICAL * sf).round() as i32,
        (DESIGN_H_LOGICAL * sf).round() as i32,
    );
    if (cur.width as i32 - want_w).abs() <= SIZE_TOLERANCE_PX
        && (cur.height as i32 - want_h).abs() <= SIZE_TOLERANCE_PX
    {
        return; // 尺寸无恙：断言零成本通过，不碰窗口
    }
    let _ = w.set_size(tauri::LogicalSize::new(DESIGN_W_LOGICAL, DESIGN_H_LOGICAL));
    if let Ok(pos) = w.outer_position() {
        if let Ok(mons) = app.available_monitors() {
            if let Some(mon) = monitor_containing(&mons, pos) {
                let wa = mon.work_area();
                let (x, y) = clamp_into_work_area(
                    pos,
                    (wa.position.x, wa.position.y),
                    (wa.size.width as i32, wa.size.height as i32),
                    (want_w, want_h), // 夹回用恢复后的尺寸，不用塌缩值
                );
                if x != pos.x || y != pos.y {
                    let _ = w.set_position(PhysicalPosition::new(x, y));
                    save_state(app, x, y);
                }
            }
        }
    }
}

/// 把窗口左上角 (x,y) 夹进工作区（纯函数，可测）：右/下溢出优先收回，工作
/// 区装不下窗口时左/上对齐。手写 min/max 而非 i32::clamp——后者在 min>max
/// （工作区小于窗口）会 panic。
fn clamp_into_work_area(
    pos: PhysicalPosition<i32>,
    wa_pos: (i32, i32),
    wa_size: (i32, i32),
    win_size: (i32, i32),
) -> (i32, i32) {
    let max_x = wa_pos.0 + (wa_size.0 - win_size.0).max(0);
    let max_y = wa_pos.1 + (wa_size.1 - win_size.1).max(0);
    (pos.x.min(max_x).max(wa_pos.0), pos.y.min(max_y).max(wa_pos.1))
}

/// 几何 worker：常驻线程。事件流静止 SNAP_SETTLE_MS 即视为松手，此刻做两
/// 件事：先几何自愈断言（不区分事件种类），再对静止前最后的 Moved 贴边。
/// 松手判定用「吸干-超时」软防抖：拖动进行中每个新事件都重置 250ms 计时，
/// 不需要在 Moved 里区分拖动/程序性移动。启动 SNAP_ARM_DELAY 内不动作
/// （恢复位/默认位自身的 Moved 不得触发吸附——用户存了 12px 边距，开机不
/// 该被吸走；自愈也不在启动窗内抢跑，此窗内的塌缩由轮询兜底）。
/// 平级 GEOM_POLL 轮询断言：RDP 转换期间事件种类/时序无保证，全漏时发现
/// 时限仍有界。
fn spawn_geom_worker(app: tauri::AppHandle, rx: mpsc::Receiver<GeomEvent>) {
    std::thread::spawn(move || {
        let arm_at = Instant::now() + SNAP_ARM_DELAY;
        loop {
            let mut last_pos: Option<PhysicalPosition<i32>> = None;
            match rx.recv_timeout(GEOM_POLL) {
                Ok(ev) => {
                    let mut ev = Some(ev);
                    loop {
                        match ev.take() {
                            Some(GeomEvent::Moved(p)) => last_pos = Some(p),
                            Some(GeomEvent::SizeTouched) | None => {}
                        }
                        match rx.recv_timeout(Duration::from_millis(SNAP_SETTLE_MS)) {
                            Ok(e) => ev = Some(e),
                            Err(mpsc::RecvTimeoutError::Timeout) => break,
                            Err(mpsc::RecvTimeoutError::Disconnected) => return,
                        }
                    }
                }
                Err(mpsc::RecvTimeoutError::Timeout) => {}
                Err(mpsc::RecvTimeoutError::Disconnected) => return,
            }
            if Instant::now() >= arm_at {
                assert_geometry(&app);
                if let Some(p) = last_pos {
                    snap_if_near(&app, p);
                }
            }
        }
    });
}

fn state_path(app: &tauri::AppHandle) -> Option<PathBuf> {
    app.path().app_data_dir().ok().map(|d| d.join("window-state.json"))
}

fn save_state(app: &tauri::AppHandle, x: i32, y: i32) {
    let Some(p) = state_path(app) else { return };
    if let Some(dir) = p.parent() {
        let _ = fs::create_dir_all(dir); // 首次运行 $APPDATA 目录可能不存在
    }
    if let Ok(json) = serde_json::to_string(&WindowState { x, y }) {
        let _ = fs::write(p, json); // 位置记忆是尽力而为：写失败不打扰用户
    }
}

// ── 票 04 · 显示配置（display profile）持久化与设置窗 ──

/// 配置文件路径：app_data_dir()/profile.json（=%APPDATA%/<identifier>/，与
/// window-state.json 同目录；票面「$APPDATA/ferryman-widget/profile.json」即此目录的泛称，
/// 走 tauri path API 而非硬编码目录名）。
fn profile_path(app: &tauri::AppHandle) -> Option<PathBuf> {
    app.path().app_data_dir().ok().map(|d| d.join("profile.json"))
}

/// 读显示配置。文件缺失/损坏 → profile=null + reset_reason（missing|corrupted），
/// 由前端回落默认并如实提示——配置文件问题永不崩程序。
#[tauri::command]
fn get_profile(app: tauri::AppHandle) -> Result<serde_json::Value, String> {
    let Some(p) = profile_path(&app) else {
        return Ok(serde_json::json!({ "profile": null, "reset_reason": "no-dir" }));
    };
    match fs::read_to_string(&p) {
        Ok(text) => match serde_json::from_str::<serde_json::Value>(&text) {
            Ok(v) => Ok(serde_json::json!({ "profile": v, "reset_reason": null })),
            Err(_) => Ok(serde_json::json!({ "profile": null, "reset_reason": "corrupted" })),
        },
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            Ok(serde_json::json!({ "profile": null, "reset_reason": "missing" }))
        }
        Err(e) => Err(format!("读取显示配置失败: {e}")),
    }
}

/// 写显示配置（目录不存在则建；失败返回 Err，前端如实提示）。
#[tauri::command]
fn save_profile(app: tauri::AppHandle, profile: serde_json::Value) -> Result<(), String> {
    let Some(p) = profile_path(&app) else { return Err("无法定位配置目录".into()) };
    if let Some(dir) = p.parent() {
        fs::create_dir_all(dir).map_err(|e| format!("创建配置目录失败: {e}"))?;
    }
    let text =
        serde_json::to_string_pretty(&profile).map_err(|e| format!("序列化显示配置失败: {e}"))?;
    fs::write(&p, text).map_err(|e| format!("写入显示配置失败: {e}"))
}

// ── 票 05 · 独立升级线：托盘菜单手动检查更新（唯一触发点，无后台轮询、不自动下载） ──

/// 更新检查结果载荷（emit 给前端通知条如实显示；前端=app.js wireUpdateNotice）。
#[derive(serde::Serialize, Clone)]
struct UpdateNotice {
    /// available=有新版可下载 / up-to-date=已是最新 / error=检查失败
    status: &'static str,
    version: Option<String>,
    /// 失败原因等补充说明（照实转述，不吞不美化；密钥未配=占位 pubkey 时如实报错）
    message: Option<String>,
}

/// 检查命中后暂存的更新包：只由前端「下载安装」按钮显式取走（update_install），绝不自动下载。
struct PendingUpdate(Mutex<Option<tauri_plugin_updater::Update>>);

/// 托盘「检查更新」：异步查 latest.json，结果一律 emit 到前端如实显示（通知条）。
/// 密钥未配/端点 404/网络失败都是 error 路径——诚实报错，不弹系统对话框、不崩程序。
fn check_for_updates(app: tauri::AppHandle) {
    tauri::async_runtime::spawn(async move {
        let notice = match app.updater() {
            Ok(u) => match u.check().await {
                Ok(Some(update)) => {
                    let version = update.version.clone();
                    if let Some(st) = app.try_state::<PendingUpdate>() {
                        if let Ok(mut slot) = st.0.lock() {
                            *slot = Some(update);
                        }
                    }
                    UpdateNotice { status: "available", version: Some(version), message: None }
                }
                Ok(None) => UpdateNotice { status: "up-to-date", version: None, message: None },
                Err(e) => UpdateNotice { status: "error", version: None, message: Some(format!("{e}")) },
            },
            Err(e) => UpdateNotice {
                status: "error",
                version: None,
                message: Some(format!("更新器初始化失败: {e}")),
            },
        };
        let _ = app.emit("update-check-result", notice); // 主窗不在也不碍事
    });
}

/// 下载并安装更新（前端按钮显式触发；Windows 下 nsis 安装器接管并重启应用）。
#[tauri::command]
async fn update_install(app: tauri::AppHandle) -> Result<(), String> {
    let update = {
        let Some(st) = app.try_state::<PendingUpdate>() else {
            return Err("更新器未就绪".into());
        };
        let mut slot = st.0.lock().map_err(|_| "更新状态锁失败".to_string())?;
        slot.take().ok_or_else(|| "请先在托盘菜单执行「检查更新」".to_string())?
    };
    let _ = app.emit("update-install-progress", "downloading");
    let bytes = update
        .download(|_, _| {}, || {})
        .await
        .map_err(|e| format!("下载更新失败: {e}"))?;
    let _ = app.emit("update-install-progress", "installing");
    update.install(bytes).map_err(|e| format!("安装更新失败: {e}"))?;
    Ok(()) // 到不了这行也无妨：Windows 上 install 会拉起安装器退出本进程
}

// ── 票 09 · daemon 端点发现：读 ~/ferryman/daemon.token（daemon EnsureToken
// 落盘）；地址钦定 http://127.0.0.1:7311/widget/summary（票 09 票面原文；
// [server].port 缺省即 7311，读 config.toml 属越界解析，不做）。token 每次
// 现读：daemon 重启换 token 后下次轮询自动生效。失败返回 Err，前端如实灰化
// （不假造可达）。悬浮窗进程内永不出现服务商凭据（只有 daemon 的本机 token）。 ──
#[tauri::command]
fn get_daemon_config() -> Result<serde_json::Value, String> {
    let home =
        std::env::var("USERPROFILE").map_err(|_| "无法定位用户目录（USERPROFILE）".to_string())?;
    let path = std::path::Path::new(&home).join("ferryman").join("daemon.token");
    let text = fs::read_to_string(&path).map_err(|e| format!("读取 daemon.token 失败: {e}"))?;
    let token = text.trim();
    if token.is_empty() {
        return Err("daemon.token 为空".into());
    }
    Ok(serde_json::json!({ "url": "http://127.0.0.1:7311/widget/summary", "token": token }))
}

/// 设置窗：按需创建、关闭即销毁（防双 WebView 常驻内存）；已开则只聚焦不重复建。
fn open_settings(app: &tauri::AppHandle) {
    if let Some(w) = app.get_webview_window("settings") {
        let _ = w.show();
        let _ = w.set_focus();
        return;
    }
    let built = tauri::WebviewWindowBuilder::new(
        app,
        "settings",
        tauri::WebviewUrl::App("settings.html".into()),
    )
    .title("显示配置")
    .inner_size(560.0, 520.0)
    .min_inner_size(460.0, 380.0)
    .center()
    .resizable(true)
    .visible(false) // 零闪窗：建好再显
    .build();
    match built {
        Ok(w) => {
            let _ = w.show();
            let _ = w.set_focus();
        }
        Err(e) => eprintln!("设置窗创建失败: {e}"), // 建窗失败不崩主程序，托盘仍可用
    }
}

pub fn run() {
    tauri::Builder::default()
        // 单实例必须最前：二次启动不出现第二个窗口，拉起既有窗口后由 main 退出
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
            if let Some(w) = app.get_webview_window("widget") {
                let _ = w.show();
                let _ = w.set_focus();
                // 评审 M2 · 恢复即拉：二次启动拉起既有窗口后，通知前端立即刷新一轮
                let _ = app.emit("widget-restored", ());
            }
        }))
        // 自启插件只接线；默认关（不 enable），票 04 设置里给开关
        .plugin(tauri_plugin_autostart::init(MacosLauncher::LaunchAgent, None))
        // 自升级（票 05）：插件接线。检查只走托盘菜单手动触发（check_for_updates），
        // 无后台轮询；pubkey 占位期间 check 会失败并经前端通知条如实显示（预期行为）
        .plugin(tauri_plugin_updater::Builder::new().build())
        .manage(MoveThrottle(Mutex::new(None)))
        .manage(PendingUpdate(Mutex::new(None)))
        // 自定义命令（票 04/05/09）：get_profile / save_profile / update_install /
        // get_daemon_config（live 取数目标：url+token）
        .invoke_handler(tauri::generate_handler![
            get_profile,
            save_profile,
            update_install,
            get_daemon_config
        ])
        .setup(|app| {
            let w = app.get_webview_window("widget").expect("conf 未配置 widget 窗口");

            // 磁吸贴边（2026-09-28 用户需求）：通道先建、worker 先起（带 2s 启动
            // 静默），下面位置恢复的 set_position 触发的 Moved 落在静默窗内，天然
            // 不吸附——用户存的边距不被开机吸走。
            let (geom_tx, geom_rx) = mpsc::channel::<GeomEvent>();
            app.manage(GeomFeed(Mutex::new(geom_tx)));
            spawn_geom_worker(app.handle().clone(), geom_rx);

            // 位置记忆：恢复上次位置；无记录（首启）= 贴主屏右缘竖排默认位
            // （spec UI 定稿：默认竖排贴右缘）。恢复位必须落在某块显示器内——
            // 拔屏/换布局后旧坐标可能在所有屏外（2026-09-25 真机：09-21 存的
            // x=3357 在单屏 1920 布局下不可见），屏外即弃用、走默认位。
            let mut restored = false;
            if let Some(p) = state_path(app.handle()) {
                if let Ok(json) = fs::read_to_string(&p) {
                    if let Ok(st) = serde_json::from_str::<WindowState>(&json) {
                        let _ = w.set_position(PhysicalPosition::new(st.x, st.y));
                        if let Ok(mons) = app.available_monitors() {
                            restored =
                                monitor_containing(&mons, PhysicalPosition::new(st.x, st.y))
                                    .is_some();
                        }
                    }
                }
            }
            if !restored {
                // 多显示器：锚主显示器右缘（current_monitor 会跟着 OS 初放走——
                // 初放落在副屏时 widget 会钉在用户看不到的屏上；主屏缺探测不到
                // 再回落 current）。
                let monitor = app.primary_monitor().ok().flatten().or_else(|| {
                    w.current_monitor().ok().flatten()
                });
                if let Some(monitor) = monitor {
                    if let Ok(ws) = w.outer_size() {
                        let size = monitor.size();
                        let pos = monitor.position();
                        let (ww, wh) = (ws.width as i32, ws.height as i32);
                        let x = pos.x + (size.width as i32 - ww - 12).max(0);
                        // 垂直大致居中，底部让开任务栏余量
                        let y = pos.y + (size.height as i32 - wh - 56).max(0) / 2;
                        let _ = w.set_position(PhysicalPosition::new(x, y));
                    }
                }
            }

            // 托盘：常驻图标 + 菜单（检查更新=票 05 真身：手动触发，结果经通知条如实显示）
            let show = MenuItem::with_id(app, "show", "显示悬浮窗", true, None::<&str>)?;
            let hide = MenuItem::with_id(app, "hide", "收起到托盘", true, None::<&str>)?;
            let settings = MenuItem::with_id(app, "settings", "设置…", true, None::<&str>)?;
            let update = MenuItem::with_id(app, "update", "检查更新", true, None::<&str>)?;
            let quit = MenuItem::with_id(app, "quit", "退出", true, None::<&str>)?;
            let menu = Menu::with_items(app, &[&show, &hide, &settings, &update, &quit])?;
            TrayIconBuilder::with_id("main")
                .icon(app.default_window_icon().expect("无内置图标").clone())
                .tooltip("Ferryman 用量悬浮窗")
                .menu(&menu)
                .show_menu_on_left_click(true)
                .on_menu_event(|app, event| match event.id.as_ref() {
                    "show" => {
                        if let Some(w) = app.get_webview_window("widget") {
                            let _ = w.show();
                            let _ = w.set_focus();
                            // 评审 M2 · 恢复即拉：托盘恢复显示后，通知前端立即刷新一轮
                            let _ = app.emit("widget-restored", ());
                        }
                    }
                    "hide" => {
                        if let Some(w) = app.get_webview_window("widget") {
                            let _ = w.hide();
                        }
                    }
                    "settings" => open_settings(app), // 票 04：按需创建、关闭即销毁
                    "quit" => app.exit(0),
                    "update" => check_for_updates(app.clone()), // 票 05：手动检查，异步
                    _ => {}
                })
                .build(app)?;

            // dev 构建旗标（票 03）：debug 构建把窗口导航到带 ?dev=1 的自身地址，
            // 前端据此显示“演示数据”角标并走演示数据层；release 不带参数（角标不显示、
            // 走 live 取数，daemon 端点未接线时如实整体灰化）。
            // 不用 eval 注入：窗口来自 conf 挂不了 initialization script，eval 有输给
            // 页面脚本的竞态；navigate 在 show 之前发生，用户只看到最终页面（零闪窗不破）。
            if cfg!(debug_assertions) {
                if let Ok(mut url) = w.url() {
                    url.set_query(Some("dev=1"));
                    let _ = w.navigate(url);
                }
            }

            let _ = w.show(); // 零闪窗：先载（含位置恢复）后显
            Ok(())
        })
        .on_window_event(|window, event| match event {
            // 悬浮窗：关窗=收起到托盘，退出只走托盘菜单；
            // 设置窗（票 04）：不拦默认关闭流程 → 窗口与 WebView 一并销毁（关闭即销毁验收）
            WindowEvent::CloseRequested { api, .. } => {
                if window.label() != "settings" {
                    api.prevent_close();
                    if let Ok(pos) = window.outer_position() {
                        save_state(window.app_handle(), pos.x, pos.y);
                    }
                    let _ = window.hide();
                }
            }
            // 拖动中节流保存位置（≥500ms 一次）；只针对悬浮窗（设置窗位置不记）。
            // 磁吸喂流不走节流：防抖要全量 Moved 事件流（250ms 静止=松手）。
            WindowEvent::Moved(pos) => {
                if window.label() != "widget" {
                    return;
                }
                if let Some(feed) = window.try_state::<GeomFeed>() {
                    if let Ok(tx) = feed.0.lock() {
                        let _ = tx.send(GeomEvent::Moved(*pos)); // 接收端亡=worker 已退，丢事件无碍
                    }
                }
                let throttle = window.state::<MoveThrottle>();
                let due = throttle
                    .0
                    .lock()
                    .map(|mut last| {
                        let due = last.map_or(true, |t| t.elapsed() >= Duration::from_millis(500));
                        if due {
                            *last = Some(Instant::now());
                        }
                        due
                    })
                    .unwrap_or(false);
                if due {
                    save_state(window.app_handle(), pos.x, pos.y);
                }
            }
            // 几何自愈原料：尺寸/DPI 被触碰（RDP 显示切换等）也喂 worker。断言
            // 不区分事件种类，这里只是多一路触发；设置窗可缩放，不参与。
            WindowEvent::Resized(_) | WindowEvent::ScaleFactorChanged { .. } => {
                if window.label() == "widget" {
                    if let Some(feed) = window.try_state::<GeomFeed>() {
                        if let Ok(tx) = feed.0.lock() {
                            let _ = tx.send(GeomEvent::SizeTouched);
                        }
                    }
                }
            }
            _ => {}
        })
        .run(tauri::generate_context!())
        .expect("ferryman-widget 启动失败");
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 塌缩恢复后的右缘压屏：4830 + 198 宽越过竖屏工作区右缘 → 收回到缘内
    /// （2026-09-29 真机实测坐标）。
    #[test]
    fn clamp_right_overflow_pulls_back_to_edge() {
        let (x, y) = clamp_into_work_area(
            PhysicalPosition::new(4830, 592),
            (3840, 0),
            (1080, 1920),
            (198, 930),
        );
        assert_eq!((x, y), (3840 + 1080 - 198, 592));
    }

    /// 工作区装不下窗口：左/上对齐而不是 panic（i32::clamp 的 min>max 陷阱）。
    #[test]
    fn clamp_work_area_smaller_than_window_aligns_top_left() {
        let (x, y) = clamp_into_work_area(
            PhysicalPosition::new(500, 500),
            (0, 0),
            (100, 200),
            (198, 930),
        );
        assert_eq!((x, y), (0, 0));
    }

    /// 屏内原位：不动（x=1000 在工作区左边界外会被夹回，那是另一条用例的
    /// 职责——本例取屏内点 4000,700）。
    #[test]
    fn clamp_onscreen_untouched() {
        let pos = PhysicalPosition::new(4000, 700);
        let (x, y) = clamp_into_work_area(pos, (3840, 0), (1080, 1920), (198, 930));
        assert_eq!((x, y), (pos.x, pos.y));
    }
}
