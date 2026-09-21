// 零闪窗铁律（windows-no-console-flash + ADR-0012）：release 构建不弹控制台黑窗。
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    ferryman_widget_lib::run()
}
