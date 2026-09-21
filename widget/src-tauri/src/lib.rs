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

            // 托盘：常驻图标 + 菜单（设置/检查更新为占位，票 04/05 接真身）
            let show = MenuItem::with_id(app, "show", "显示悬浮窗", true, None::<&str>)?;
            let hide = MenuItem::with_id(app, "hide", "收起到托盘", true, None::<&str>)?;
            let settings = MenuItem::with_id(app, "settings", "设置…（票 04）", true, None::<&str>)?;
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
                    "quit" => app.exit(0),
                    _ => {} // settings/update 占位
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
            // 关窗=收起到托盘；退出只走托盘菜单
            WindowEvent::CloseRequested { api, .. } => {
                api.prevent_close();
                if let Ok(pos) = window.outer_position() {
                    save_state(window.app_handle(), pos.x, pos.y);
                }
                let _ = window.hide();
            }
            // 拖动中节流保存位置（≥500ms 一次）
            WindowEvent::Moved(pos) => {
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
