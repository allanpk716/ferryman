// 零闪窗铁律（windows-no-console-flash / ADR-0012）：无条件 GUI 子系统，
// 任何构建（含 debug）双击都不弹黑色控制台——本仓纪律优先于"debug 留控制台看日志"的惯例。
#![windows_subsystem = "windows"]

fn main() {
    ferryman_widget_lib::run()
}
