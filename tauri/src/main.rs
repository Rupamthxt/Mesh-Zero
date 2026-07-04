#![cfg_attr(
  all(not(debug_assertions), target_os = "windows"),
  windows_subsystem = "windows"
)]

use std::sync::Mutex;
use tauri::State;

// Link the Go shared library compiled via c-shared buildmode
#[link(name = "meshzero", kind = "dylib")]
extern "C" {
    fn StartMeshDaemon();
    fn StopMeshDaemon();
}

struct EngineState {
    is_running: Mutex<bool>,
}

#[tauri::command]
fn start_compute_node(state: State<'_, EngineState>) -> Result<String, String> {
    let mut is_running = state.is_running.lock().unwrap();
    if *is_running {
        return Err("Compute node is already running".to_string());
    }

    unsafe {
        StartMeshDaemon();
    }
    *is_running = true;
    Ok("Compute node daemon successfully started".to_string())
}

#[tauri::command]
fn stop_compute_node(state: State<'_, EngineState>) -> Result<String, String> {
    let mut is_running = state.is_running.lock().unwrap();
    if !*is_running {
        return Err("Compute node is not running".to_string());
    }

    unsafe {
        StopMeshDaemon();
    }
    *is_running = false;
    Ok("Compute node daemon successfully stopped".to_string())
}

#[tauri::command]
fn get_status(state: State<'_, EngineState>) -> bool {
    *state.is_running.lock().unwrap()
}

fn main() {
    tauri::Builder::default()
        .manage(EngineState {
            is_running: Mutex::new(false),
        })
        .invoke_handler(tauri::generate_handler![
            start_compute_node,
            stop_compute_node,
            get_status
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
