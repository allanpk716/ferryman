use tauri::Manager;

/// 悬浮窗入口。W1 骨架：只做"visible:false 先载后显"（零闪窗铁律）。
/// 后续票：02 托盘/单实例/自启/位置记忆；03 UI 正式移植；05 updater。
pub fn run() {
    tauri::Builder::default()
        .setup(|app| {
            if let Some(w) = app.get_webview_window("widget") {
                let _ = w.show();
            }
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("ferryman-widget 启动失败");
}
