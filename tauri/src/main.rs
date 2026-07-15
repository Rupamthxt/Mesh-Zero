#![cfg_attr(
  all(not(debug_assertions), target_os = "windows"),
  windows_subsystem = "windows"
)]

use std::sync::Mutex;
use tauri::State;
use tauri::api::process::{Command, CommandChild, CommandEvent};

struct EngineState {
    child_process: Mutex<Option<CommandChild>>,
}

#[tauri::command]
fn start_compute_node(
    state: State<'_, EngineState>,
    port: String,
    price: String,
    broker: String,
) -> Result<String, String> {
    let mut child_lock = state.child_process.lock().unwrap();
    if child_lock.is_some() {
        return Err("Compute node daemon is already running".to_string());
    }

    // Prepare sidecar execution arguments:
    // ./mesh-zero worker start <port> <price>
    let mut cmd = Command::new_sidecar("mesh-zero")
        .map_err(|e| format!("Failed to locate worker sidecar binary: {}", e))?
        .args(vec!["worker", "start", &port, &price]);

    // Feed environment variables if a custom broker destination is provided
    if !broker.is_empty() {
        cmd = cmd.env("MESH_BROKER_ADDR".to_string(), broker);
    }

    // Spawn the worker sidecar background process
    let (mut rx, child) = cmd.spawn()
        .map_err(|e| format!("Failed to spawn worker process: {}", e))?;

    // Pipe process stdout logs to our Tauri debug output console
    tauri::async_runtime::spawn(async move {
        while let Some(event) = rx.recv().await {
            if let CommandEvent::Stdout(line) = event {
                println!("[SIDECAR STDOUT] {}", line);
            } else if let CommandEvent::Stderr(line) = event {
                eprintln!("[SIDECAR STDERR] {}", line);
            }
        }
    });

    *child_lock = Some(child);
    Ok("Compute node daemon successfully started".to_string())
}

#[tauri::command]
fn stop_compute_node(state: State<'_, EngineState>) -> Result<String, String> {
    let mut child_lock = state.child_process.lock().unwrap();
    if let Some(child) = child_lock.take() {
        child.kill().map_err(|e| format!("Failed to stop worker process: {}", e))?;
        Ok("Compute node daemon successfully stopped".to_string())
    } else {
        Err("Compute node daemon is not running".to_string())
    }
}

#[tauri::command]
fn get_status(state: State<'_, EngineState>) -> bool {
    state.child_process.lock().unwrap().is_some()
}

fn main() {
    tauri::Builder::default()
        .manage(EngineState {
            child_process: Mutex::new(None),
        })
        .invoke_handler(tauri::generate_handler![
            start_compute_node,
            stop_compute_node,
            get_status
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
