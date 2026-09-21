// 票 02 · 窗口与常驻形态：托盘 / 单实例 / 自启 / 位置记忆 / 关窗收托盘。
// 零闪窗铁律不变：conf visible:false，setup 末尾（位置恢复之后）才 show。
use std::{fs, path::PathBuf, sync::Mutex, time::{Duration, Instant}};

use tauri::{
    menu::{Menu, MenuItem},
    tray::TrayIconBuilder,
    Manager, PhysicalPosition, WindowEvent,
};
use tauri_plugin_autostart::MacosLauncher;

/// 持久化的窗口几何（票 02 只记位置；尺寸不可缩放无需记）。
#[derive(serde::Serialize, serde::Deserialize)]
struct WindowState {
    x: i32,
    y: i32,
}

/// Moved 事件节流：拖动中每 500ms 至多写一次盘。
struct MoveThrottle(Mutex<Option<Instant>>);

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
            }
        }))
        // 自启插件只接线；默认关（不 enable），票 04 设置里给开关
        .plugin(tauri_plugin_autostart::init(MacosLauncher::LaunchAgent, None))
        .manage(MoveThrottle(Mutex::new(None)))
        // 自定义命令（票 04）：get_profile / save_profile（profile.json 读写）
        .invoke_handler(tauri::generate_handler![get_profile, save_profile])
        .setup(|app| {
            let w = app.get_webview_window("widget").expect("conf 未配置 widget 窗口");

            // 位置记忆：恢复上次位置（无记录/损坏=回落 conf 默认，不崩）
            if let Some(p) = state_path(app.handle()) {
                if let Ok(json) = fs::read_to_string(&p) {
                    if let Ok(st) = serde_json::from_str::<WindowState>(&json) {
                        let _ = w.set_position(PhysicalPosition::new(st.x, st.y));
                    }
                }
            }

            // 托盘：常驻图标 + 菜单（检查更新为占位，票 05 接真身）
            let show = MenuItem::with_id(app, "show", "显示悬浮窗", true, None::<&str>)?;
            let hide = MenuItem::with_id(app, "hide", "收起到托盘", true, None::<&str>)?;
            let settings = MenuItem::with_id(app, "settings", "设置…", true, None::<&str>)?;
            let update = MenuItem::with_id(app, "update", "检查更新（票 05）", true, None::<&str>)?;
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
                        }
                    }
                    "hide" => {
                        if let Some(w) = app.get_webview_window("widget") {
                            let _ = w.hide();
                        }
                    }
                    "settings" => open_settings(app), // 票 04：按需创建、关闭即销毁
                    "quit" => app.exit(0),
                    _ => {} // update 占位（票 05）
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
            // 拖动中节流保存位置（≥500ms 一次）；只针对悬浮窗（设置窗位置不记）
            WindowEvent::Moved(pos) => {
                if window.label() != "widget" {
                    return;
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
            _ => {}
        })
        .run(tauri::generate_context!())
        .expect("ferryman-widget 启动失败");
}
