// Fairy — Tauri 2 桌面壳
// 职责:
//   - debug 模式: 假设 pnpm dev 已被外部拉起 (cargo tauri dev 走 beforeDevCommand)
//   - release 模式: 自动 spawn pnpm dev 作为 sidecar,关窗时 taskkill 整棵进程树
//   - 始终挂托盘 + 加载 pnpm dev 写入的实际前端地址

use std::fs;
use std::io::{Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::path::{Path, PathBuf};
use std::process::Child;
#[cfg(not(debug_assertions))]
use std::process::{Command, Stdio};
use std::sync::{Mutex, OnceLock};
use std::time::{Duration, Instant};

use serde::Deserialize;
use tauri::{
    menu::{Menu, MenuItem, PredefinedMenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    Manager, RunEvent, WindowEvent,
};
use tauri_plugin_opener::OpenerExt;

const FRONTEND_HOST: &str = "127.0.0.1";
const FRONTEND_READY_TIMEOUT_MS: u64 = 90_000;
const DEFAULT_FRONTEND_URL: &str = "about:blank";

static FRONTEND_URL: OnceLock<String> = OnceLock::new();
#[cfg(windows)]
static SINGLE_INSTANCE_HANDLE: OnceLock<usize> = OnceLock::new();

#[derive(Deserialize)]
struct ServiceStateFile {
    instance_id: Option<String>,
    repository_root: Option<String>,
    frontend: Option<FrontendServiceState>,
}

#[derive(Deserialize)]
struct FrontendServiceState {
    url: Option<String>,
    port: Option<u16>,
}

#[derive(Deserialize)]
struct FrontendIdentity {
    service: Option<String>,
    instance_id: Option<String>,
    repository_root: Option<String>,
}

// 把 Tauri 窗口可见性变化注入到前端: Tauri 2 的 hide()/show() 不会触发
// document.visibilityState, 也不会发 visibilitychange —— 但 WebView2 失/获焦
// 时 RAF + microtask 会被冻结, 前端 batched updater 会堆积在 messageBatchRef
// 拿不到出圈时机. 这里在每个窗口被用户带回前台的路径上 eval 一段 JS,
// 前端 addEventListener('fairy:focus') 收到后立刻 flushBatchNow 把堆积的
// 流式事件推到 UI.
// Focused(false) 也注入一次: 窗口失焦时把残留 updater 推到 UI, 避免下次
// show 回来时 React 状态序列错乱.
const DISPATCH_FOCUS_JS: &str = "window.dispatchEvent(new CustomEvent('fairy:focus'))";

#[cfg(all(windows, not(debug_assertions)))]
const CREATE_NO_WINDOW: u32 = 0x0800_0000;
#[cfg(all(windows, not(debug_assertions)))]
const CREATE_NEW_PROCESS_GROUP: u32 = 0x0000_0200;

#[tauri::command]
fn ping() -> &'static str {
    "pong"
}

struct PnpmSidecar(pub Mutex<Option<Child>>);

// ---------- sidecar (仅 release 编译进二进制) ----------

// Locate the project root regardless of how the shell was launched: a
// double-clicked fairy.exe starts with the exe's own folder as cwd, which is
// not the repo, so fall back to walking up from the executable path.
fn looks_like_repo_root(dir: &std::path::Path) -> bool {
    dir.join("package.json").is_file() && dir.join("scripts").join("dev.mjs").is_file()
}

fn resolve_repo_root() -> Option<PathBuf> {
    if let Ok(cwd) = std::env::current_dir() {
        if looks_like_repo_root(&cwd) {
            return Some(cwd);
        }
    }
    let exe = std::env::current_exe().ok()?;
    let mut dir = exe.parent()?.to_path_buf();
    for _ in 0..8 {
        if looks_like_repo_root(&dir) {
            return Some(dir);
        }
        dir = dir.parent()?.to_path_buf();
    }
    None
}

fn service_state_path(repo_root: &Path) -> PathBuf {
    repo_root.join(".tools").join("dev-service-state.json")
}

fn frontend_url() -> &'static str {
    FRONTEND_URL
        .get()
        .map(String::as_str)
        .unwrap_or(DEFAULT_FRONTEND_URL)
}

#[cfg(windows)]
fn acquire_single_instance() -> bool {
    use windows::core::PCWSTR;
    use windows::Win32::Foundation::{CloseHandle, GetLastError, ERROR_ALREADY_EXISTS};
    use windows::Win32::System::Threading::CreateMutexW;

    let name = "Local\\Fairy-com.fairy.workbench"
        .encode_utf16()
        .chain(std::iter::once(0))
        .collect::<Vec<u16>>();
    match unsafe { CreateMutexW(None, false, PCWSTR(name.as_ptr())) } {
        Ok(handle) => {
            if unsafe { GetLastError() } == ERROR_ALREADY_EXISTS {
                let _ = unsafe { CloseHandle(handle) };
                false
            } else {
                let _ = SINGLE_INSTANCE_HANDLE.set(handle.0 as usize);
                true
            }
        }
        Err(error) => {
            eprintln!("[fairy] single-instance mutex failed: {error}");
            true
        }
    }
}

#[cfg(all(test, windows))]
#[test]
fn single_instance_mutex_rejects_duplicate() {
    assert!(acquire_single_instance());
    assert!(!acquire_single_instance());
}

#[test]
fn frontend_identity_requires_instance_and_repo_match() {
    let root = Path::new(r"D:\Fairy");
    let matching = FrontendIdentity {
        service: Some("fairy-frontend".to_string()),
        instance_id: Some("instance-123".to_string()),
        repository_root: Some(r"d:/fairy/".to_string()),
    };
    assert!(frontend_identity_matches(&matching, "instance-123", root));
    assert!(!frontend_identity_matches(
        &matching,
        "instance-other",
        root
    ));

    let wrong_repo = FrontendIdentity {
        service: Some("fairy-frontend".to_string()),
        instance_id: Some("instance-123".to_string()),
        repository_root: Some(r"D:\Other".to_string()),
    };
    assert!(!frontend_identity_matches(
        &wrong_repo,
        "instance-123",
        root
    ));
}

#[cfg(not(windows))]
fn acquire_single_instance() -> bool {
    true
}

#[cfg(not(debug_assertions))]
fn spawn_pnpm_dev(cwd: &Path) -> Option<Child> {
    #[cfg(windows)]
    use std::os::windows::process::CommandExt;
    let tools_dir = cwd.join(".tools");
    let _ = fs::create_dir_all(&tools_dir);
    let stdout_log = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(tools_dir.join("fairy-launcher.stdout.log"))
        .ok();
    let stderr_log = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(tools_dir.join("fairy-launcher.stderr.log"))
        .ok();

    let bundled_node = tools_dir
        .join("node")
        .join(if cfg!(windows) { "node.exe" } else { "node" });
    let bundled_pnpm = tools_dir
        .join("node")
        .join("node_modules")
        .join("pnpm")
        .join("bin")
        .join("pnpm.cjs");

    let mut cmd = if bundled_node.is_file() && bundled_pnpm.is_file() {
        eprintln!(
            "[fairy] spawning bundled pnpm dev via {}",
            bundled_node.display()
        );
        let mut command = Command::new(&bundled_node);
        command.arg(&bundled_pnpm).arg("dev");
        command
    } else {
        eprintln!("[fairy] bundled pnpm missing; falling back to PATH");
        let mut command = Command::new("cmd");
        command.args(&["/D", "/S", "/C", "pnpm dev"]);
        command
    };

    cmd.current_dir(cwd).stdin(Stdio::null());
    if let Some(log) = stdout_log {
        cmd.stdout(Stdio::from(log));
    } else {
        cmd.stdout(Stdio::null());
    }
    if let Some(log) = stderr_log {
        cmd.stderr(Stdio::from(log));
    } else {
        cmd.stderr(Stdio::null());
    }
    #[cfg(windows)]
    cmd.creation_flags(CREATE_NO_WINDOW);
    match cmd.spawn() {
        Ok(child) => Some(child),
        Err(error) => {
            eprintln!("[fairy] failed to spawn pnpm dev: {error}");
            if let Ok(mut log) = fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(tools_dir.join("fairy-launcher.stderr.log"))
            {
                let _ = writeln!(log, "[fairy] failed to spawn pnpm dev: {error}");
            }
            None
        }
    }
}

fn normalized_path_for_compare(value: &str) -> String {
    value
        .replace('\\', "/")
        .trim_end_matches('/')
        .to_lowercase()
}

fn frontend_identity_matches(
    identity: &FrontendIdentity,
    expected_instance_id: &str,
    expected_repo_root: &Path,
) -> bool {
    if identity.service.as_deref() != Some("fairy-frontend") {
        return false;
    }
    if identity.instance_id.as_deref() != Some(expected_instance_id) {
        return false;
    }
    let Some(actual_root) = identity.repository_root.as_deref() else {
        return false;
    };
    normalized_path_for_compare(actual_root)
        == normalized_path_for_compare(&expected_repo_root.to_string_lossy())
}

fn probe_frontend_identity(port: u16, instance_id: &str) -> Option<FrontendIdentity> {
    let address: SocketAddr = format!("{FRONTEND_HOST}:{port}").parse().ok()?;
    let mut stream = TcpStream::connect_timeout(&address, Duration::from_millis(750)).ok()?;
    let timeout = Some(Duration::from_millis(1_000));
    let _ = stream.set_read_timeout(timeout);
    let _ = stream.set_write_timeout(timeout);
    let request = format!(
        "GET /__fairy_identity?token={instance_id} HTTP/1.1\r\nHost: {FRONTEND_HOST}:{port}\r\nConnection: close\r\n\r\n"
    );
    stream.write_all(request.as_bytes()).ok()?;

    let mut response = String::new();
    stream.read_to_string(&mut response).ok()?;
    let status = response.lines().next()?.split_whitespace().nth(1)?;
    if status != "200" {
        return None;
    }
    let (_, body) = response.split_once("\r\n\r\n")?;
    serde_json::from_str(body).ok()
}

fn wait_for_frontend_url(repo_root: &Path, timeout_ms: u64) -> Option<String> {
    let deadline = Instant::now() + Duration::from_millis(timeout_ms);
    while Instant::now() < deadline {
        if let Ok(raw) = fs::read_to_string(service_state_path(repo_root)) {
            if let Ok(state) = serde_json::from_str::<ServiceStateFile>(&raw) {
                let state_root = state.repository_root.as_deref().unwrap_or_default();
                let state_root_matches = !state_root.is_empty()
                    && normalized_path_for_compare(state_root)
                        == normalized_path_for_compare(&repo_root.to_string_lossy());
                let instance_id = state.instance_id.as_deref().unwrap_or_default();
                if state_root_matches && !instance_id.is_empty() {
                    if let Some(frontend) = state.frontend {
                        if let (Some(url), Some(port)) = (frontend.url, frontend.port) {
                            let expected_url = format!("http://{FRONTEND_HOST}:{port}");
                            if url == expected_url {
                                if let Some(identity) = probe_frontend_identity(port, instance_id) {
                                    if frontend_identity_matches(&identity, instance_id, repo_root)
                                    {
                                        eprintln!(
                                            "[fairy] verified frontend instance {instance_id} at {url}"
                                        );
                                        return Some(url);
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
        std::thread::sleep(Duration::from_millis(200));
    }
    eprintln!("[fairy] frontend state not verified after {timeout_ms}ms");
    None
}

#[cfg(not(debug_assertions))]
fn kill_tree(id: u32) {
    let _ = Command::new("taskkill")
        .args(&["/F", "/T", "/PID", &id.to_string()])
        .output();
}

// ---------- 构建并重启 ----------

// 托盘「重启服务」：只重启 vite + API + Go agent + voice，不重编译桌面壳。
// 改前端/后端代码时重新编译 Rust 纯属浪费 —— 那才是主人要的「简单重启」。
// 真正的编排在 scripts/restart-service.mjs：纯 Node 实现，各平台同一份代码，
// 校验服务状态文件（拒绝杀无关进程）、停端口、重建 Go agent、重新拉起、健康检查。
const RESTART_SERVICE_SCRIPT: &str = "scripts/restart-service.mjs";

// 防连点：重启要几十秒，重复触发会互相抢端口和 .tools 里的服务状态文件。
static RESTART_IN_FLIGHT: std::sync::atomic::AtomicBool =
    std::sync::atomic::AtomicBool::new(false);

// 重启会把服务交给一个独立进程，此后本应用手上的 child 句柄就管不着它了。
// 只有真的重启过，退出时才需要让脚本按状态文件补收一次。
static RESTART_HAPPENED: std::sync::atomic::AtomicBool =
    std::sync::atomic::AtomicBool::new(false);

// 重启期间盖一层遮罩。vite 一停，页面仍然渲染着、看着一切正常，但其实
// 已经连不上后端 —— 不提示的话主人只会以为「点了没反应」。重载时它随页面消失。
const RESTART_OVERLAY_JS: &str = r#"
(() => {
  if (document.getElementById('__fairy_restart_overlay')) return;
  const el = document.createElement('div');
  el.id = '__fairy_restart_overlay';
  el.textContent = '正在重启前后端服务…';
  el.style.cssText = 'position:fixed;inset:0;z-index:2147483647;display:flex;align-items:center;justify-content:center;background:rgba(12,14,20,.82);color:#e8ecf4;font:14px/1.6 system-ui,-apple-system,"Segoe UI",sans-serif';
  document.body.appendChild(el);
})()
"#;

const RESTART_OVERLAY_CLEAR_JS: &str =
    "document.getElementById('__fairy_restart_overlay')?.remove()";

#[cfg(all(test, windows))]
#[test]
fn restart_service_script_exists_in_repo() {
    // 菜单只按路径拉起脚本；脚本一旦被改名或挪走，点「重启服务」会静默失败。
    let root = resolve_repo_root().expect("解析仓库根目录");
    let script = root.join(RESTART_SERVICE_SCRIPT);
    assert!(
        script.exists(),
        "重启脚本缺失，托盘「重启服务」会失效: {}",
        script.display()
    );
}

// 同步执行重启脚本。调用方在后台线程上用它，避免卡住 UI 线程。
fn run_service_restart() -> Result<(), String> {
    let repo_root = resolve_repo_root().ok_or_else(|| "找不到仓库根目录".to_string())?;
    let script = repo_root.join(RESTART_SERVICE_SCRIPT);
    if !script.exists() {
        return Err(format!("重启脚本不存在: {}", script.display()));
    }

    // 各平台同一份 Node 脚本，不再分叉到 PowerShell：
    // .ps1 只在 Windows 上跑得动，而重启逻辑本身没有任何 Windows 专属的东西。
    let mut command = std::process::Command::new("node");
    command.arg(&script).current_dir(&repo_root);
    command.stdin(std::process::Stdio::null());

    // The tray action must not inherit the shell/app console. On Windows a
    // console close (STATUS_CONTROL_C_EXIT / 0xC000013A) is delivered to the
    // whole attached process group, which was killing the newly started voice
    // process right after a successful restart. Keep the restart orchestration
    // in its own no-window process group and persist its output for diagnosis.
    let tools_dir = repo_root.join(".tools");
    let _ = fs::create_dir_all(&tools_dir);
    match fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(tools_dir.join("restart-service.stdout.log"))
    {
        Ok(log) => {
            command.stdout(std::process::Stdio::from(log));
        }
        Err(_) => {
            command.stdout(std::process::Stdio::null());
        }
    }
    match fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(tools_dir.join("restart-service.stderr.log"))
    {
        Ok(log) => {
            command.stderr(std::process::Stdio::from(log));
        }
        Err(_) => {
            command.stderr(std::process::Stdio::null());
        }
    }
    #[cfg(all(windows, not(debug_assertions)))]
    {
        use std::os::windows::process::CommandExt;
        command.creation_flags(CREATE_NO_WINDOW | CREATE_NEW_PROCESS_GROUP);
    }

    let status = command.status();

    let status = status.map_err(|error| format!("无法启动重启脚本: {error}"))?;
    if status.success() {
        Ok(())
    } else {
        Err(format!("重启脚本退出码 {:?}", status.code()))
    }
}

// 从状态里取走 pnpm dev 子进程：take 之后状态变 None，
// 正常退出与重启两条路径靠它避免重复 kill 同一棵进程树。
fn take_pnpm_child(app: &tauri::AppHandle) -> Option<Child> {
    let state = app.state::<PnpmSidecar>();
    // 不能把 lock() 的结果直接当函数尾表达式：守卫会活过 state，触发 E0597
    let taken = match state.0.lock() {
        Ok(mut guard) => guard.take(),
        Err(_) => None,
    };
    taken
}

// ---------- 入口 ----------

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    if !acquire_single_instance() {
        eprintln!("[fairy] another Fairy instance is already running");
        return;
    }

    let repo_root = resolve_repo_root();
    #[cfg(not(debug_assertions))]
    let pnpm_child = repo_root.as_deref().and_then(spawn_pnpm_dev);
    #[cfg(debug_assertions)]
    let pnpm_child: Option<Child> = None;

    let app = tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_opener::init())
        .manage(PnpmSidecar(Mutex::new(pnpm_child)))
        .setup(move |app| {
            let endpoint = repo_root
                .as_deref()
                .and_then(|root| wait_for_frontend_url(root, FRONTEND_READY_TIMEOUT_MS))
                .unwrap_or_else(|| DEFAULT_FRONTEND_URL.to_string());
            let _ = FRONTEND_URL.set(endpoint.clone());

            // 窗口 ready 后再显示 (避免 race)
            if let Some(win) = app.get_webview_window("main") {
                if let Ok(url) = tauri::Url::parse(&endpoint) {
                    let _ = win.navigate(url);
                }
                let _ = win.show();
                let _ = win.eval(DISPATCH_FOCUS_JS);
            }

            // ---- 托盘菜单 ----
            let show_i = MenuItem::with_id(app, "show", "显示 Fairy", true, None::<&str>)?;
            let hide_i = MenuItem::with_id(app, "hide", "隐藏到托盘", true, None::<&str>)?;
            let sep1 = PredefinedMenuItem::separator(app)?;
            let open_i =
                MenuItem::with_id(app, "open_browser", "在外部浏览器打开", true, None::<&str>)?;
            let sep2 = PredefinedMenuItem::separator(app)?;
            let restart_i = MenuItem::with_id(app, "restart", "重启服务", true, None::<&str>)?;
            let quit_i = MenuItem::with_id(app, "quit", "退出 Fairy", true, None::<&str>)?;
            let menu = Menu::with_items(
                app,
                &[&show_i, &hide_i, &sep1, &open_i, &sep2, &restart_i, &quit_i],
            )?;

            // ---- 托盘 ----
            let icon = app
                .default_window_icon()
                .cloned()
                .ok_or("default window icon missing")?;

            TrayIconBuilder::with_id("main-tray")
                .icon(icon)
                .tooltip("Fairy — 本地 Agent 工作台")
                .menu(&menu)
                .show_menu_on_left_click(false)
                .on_menu_event(|app, event| {
                    // 「重启服务」先于窗口查找处理，窗口异常时也能自救。
                    if event.id.as_ref() == "restart" {
                        use std::sync::atomic::Ordering;
                        if RESTART_IN_FLIGHT.swap(true, Ordering::SeqCst) {
                            eprintln!("[fairy] restart: 上一次重启还在进行中，忽略");
                            return;
                        }
                        // 立刻给视觉反馈，再把耗时的部分丢到后台线程 ——
                        // 重启要几十秒，占用 UI 线程会让整个应用看起来卡死。
                        if let Some(win) = app.get_webview_window("main") {
                            let _ = win.eval(RESTART_OVERLAY_JS);
                        }
                        let app_handle = app.clone();
                        let repo_root = resolve_repo_root();
                        std::thread::spawn(move || {
                            match run_service_restart() {
                                Ok(()) => {
                                    RESTART_HAPPENED.store(true, Ordering::SeqCst);
                                    // 服务已换成新进程，webview 还停在旧页面上。
                                    // 不重新指向就等于「重启了，但看到的还是旧代码」，
                                    // 那正是主人点这个菜单想避免的事。
                                    // 这里重新解析而不是简单 reload：新起的 vite 未必
                                    // 拿到同一个端口，reload 一个已经没人监听的地址只会白屏。
                                    let endpoint = repo_root
                                        .as_deref()
                                        .and_then(|root| wait_for_frontend_url(root, FRONTEND_READY_TIMEOUT_MS));
                                    eprintln!("[fairy] restart: services back at {endpoint:?}");
                                    if let Some(win) = app_handle.get_webview_window("main") {
                                        match endpoint.as_deref().and_then(|url| tauri::Url::parse(url).ok()) {
                                            Some(url) => {
                                                let _ = win.navigate(url);
                                            }
                                            // 认不出新地址时退回重载当前页：端口没变时同样有效。
                                            None => {
                                                let _ = win.eval("window.location.reload()");
                                            }
                                        }
                                    }
                                }
                                Err(error) => {
                                    eprintln!("[fairy] restart: {error}");
                                    // 失败时不能让遮罩永远盖着，否则应用等于报废。
                                    if let Some(win) = app_handle.get_webview_window("main") {
                                        let _ = win.eval(RESTART_OVERLAY_CLEAR_JS);
                                    }
                                }
                            }
                            RESTART_IN_FLIGHT.store(false, Ordering::SeqCst);
                        });
                        return;
                    }
                    let win = match app.get_webview_window("main") {
                        Some(w) => w,
                        None => return,
                    };
                    match event.id.as_ref() {
                        "show" => {
                            let _ = win.show();
                            let _ = win.set_focus();
                            let _ = win.eval(DISPATCH_FOCUS_JS);
                        }
                        "hide" => {
                            let _ = win.hide();
                            let _ = win.eval(DISPATCH_FOCUS_JS); // 落盘残余 updater
                        }
                        "open_browser" => {
                            let _ = app.opener().open_url(frontend_url(), None::<&str>);
                        }
                        "quit" => {
                            app.exit(0);
                        }
                        _ => {}
                    }
                })
                .on_tray_icon_event(|tray, event| {
                    if let TrayIconEvent::Click {
                        button: MouseButton::Left,
                        button_state: MouseButtonState::Up,
                        ..
                    } = event
                    {
                        let app = tray.app_handle();
                        if let Some(win) = app.get_webview_window("main") {
                            let visible = win.is_visible().unwrap_or(false);
                            if visible {
                                let _ = win.hide();
                                let _ = win.eval(DISPATCH_FOCUS_JS); // 落盘残余 updater
                            } else {
                                let _ = win.show();
                                let _ = win.set_focus();
                                let _ = win.eval(DISPATCH_FOCUS_JS);
                            }
                        }
                    }
                })
                .build(app)?;
            Ok(())
        })
        .on_window_event(|window, event| {
            // 注: 闭包拿到的是 &Window, 不是 &WebviewWindow —— eval 是 webview 的
            // 方法, 必须走 window.get_webview_window("main") 拿 WebviewWindow.
            let dispatch = |w: &tauri::Window| {
                if let Some(webview) = w.get_webview_window("main") {
                    let _ = webview.eval(DISPATCH_FOCUS_JS);
                }
            };
            // 关闭窗口 -> 隐藏到托盘 (而不是退出进程)
            if let WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
                dispatch(window); // 落盘残余 updater
            }
            // 获/失焦: Tauri hide()/show() 之外的纯失焦路径(用户 Alt-Tab 切走)
            // 也会冻 WebView2 的 RAF + microtask. 拿到 Focused 事件时强制把
            // batched updater 推到 UI, 避免下次回到前台时 React state 序列错乱.
            if let WindowEvent::Focused(focused) = event {
                dispatch(window);
                let _ = focused; // 显式消费, 避免编译警告
            }
        })
        .invoke_handler(tauri::generate_handler![ping])
        .build(tauri::generate_context!())
        .expect("error while building Fairy");

    app.run(|app_handle, event| {
        // App 真正退出时清理 pnpm dev 子进程
        if let RunEvent::Exit = event {
            // take 出 child，避免持锁跨 kill 调用；重启路径已先 take 过，这里拿到 None
            if let Some(mut c) = take_pnpm_child(app_handle) {
                eprintln!("[fairy] killing pnpm dev tree (pid {})", c.id());
                #[cfg(not(debug_assertions))]
                kill_tree(c.id());
                let _ = c.wait();
            }
            // 走过托盘重启的话，当前服务是一个独立进程拉起来的，不在上面的 child
            // 句柄里。让重启脚本按服务状态文件补收一次 —— 复用同一套校验逻辑，
            // 而不是在 Rust 里重写一遍「哪些进程才该被停」。
            #[cfg(not(debug_assertions))]
            if RESTART_HAPPENED.load(std::sync::atomic::Ordering::SeqCst) {
                stop_services_in_background();
            }
        }
    });
}

// 退出兜底：让重启脚本按服务状态文件停掉服务。detached 且不等待，
// 应用能立刻退出，收尾在后台跑完。
#[cfg(not(debug_assertions))]
fn stop_services_in_background() {
    let Some(repo_root) = resolve_repo_root() else {
        return;
    };
    let script = repo_root.join(RESTART_SERVICE_SCRIPT);
    if !script.exists() {
        return;
    }
    let _ = std::process::Command::new("node")
        .arg(&script)
        .arg("--stop")
        .current_dir(&repo_root)
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn();
}
