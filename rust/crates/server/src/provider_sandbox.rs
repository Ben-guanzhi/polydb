//! 阶段 11：provider 侧沙箱——OS 级执行边界（behavior.md §16.16）。与 Go 侧
//! `go/pkg/server/provider_sandbox*.go` 严格镜像。
//!
//! 承诺（§16.16.1/§16.16.2）：
//!   - 生命周期绑定：宿主死 → provider 必死（Windows Job Object
//!     KILL_ON_JOB_CLOSE；Unix 进程组 + provider 自检父进程）。
//!   - 资源上限：内存（Windows Job 内存限制；Unix provider 自限 RLIMIT_AS）。
//! 不承诺（§16.16.3）：文件系统/网络访问隔离、恶意进程防御、CPU 硬限。

/// provider 进程的默认内存上限（MiB，§16.16.2）。
pub const PROVIDER_DEFAULT_MEM_MB: u64 = 512;

/// 传给 provider 进程的父进程 PID 环境变量名（§16.16.1 Unix 行：父死自检）。
pub const SANDBOX_PARENT_PID_KEY: &str = "POLYDB_PROVIDER_PARENT_PID";

/// 从 `POLYDB_PROVIDER_MEM_MB` 读取内存上限（MiB）；缺省 512。
pub fn provider_mem_limit_mb() -> u64 {
    let raw = std::env::var("POLYDB_PROVIDER_MEM_MB").unwrap_or_default();
    match raw.trim().parse::<u64>() {
        Ok(n) if n > 0 => n,
        _ => PROVIDER_DEFAULT_MEM_MB,
    }
}

/// 给已启动的 provider 进程应用 OS 级沙箱（平台相关）。
///
/// Windows：把进程加入 Job Object（KILL_ON_JOB_CLOSE + 内存上限）。
/// Unix：无需宿主侧操作（进程组由 `Command::process_group` 在 spawn 时设置、
/// 内存由 provider 自限 RLIMIT_AS），返回的 cleanup 是空操作。
pub fn apply_sandbox(child: &mut std::process::Child) -> Result<Box<dyn FnOnce() + Send>, String> {
    apply_sandbox_platform(child)
}

#[cfg(windows)]
mod windows_impl {
    use std::sync::Mutex;
    use windows_sys::Win32::Foundation::{CloseHandle, HANDLE};
    use windows_sys::Win32::System::JobObjects::{
        AssignProcessToJobObject, CreateJobObjectA, JobObjectExtendedLimitInformation,
        SetInformationJobObject, JOBOBJECT_BASIC_LIMIT_INFORMATION,
        JOBOBJECT_EXTENDED_LIMIT_INFORMATION, JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
        JOB_OBJECT_LIMIT_PROCESS_MEMORY,
    };
    use windows_sys::Win32::System::Threading::{OpenProcess, PROCESS_ALL_ACCESS};

    /// 把 provider 进程加入 Windows Job Object：
    ///
    /// - `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`：宿主进程退出（含被杀）→ Job
    ///   句柄关闭 → OS 强制终止 Job 内的全部进程（§16.16.1 生命周期绑定）。
    /// - `JOB_OBJECT_LIMIT_PROCESS_MEMORY`：内存上限（§16.16.2）。
    ///
    /// 注意：windows-sys 0.52 的 `HANDLE` 是 `isize`，判空用 `== 0`；Job 与
    /// 进程句柄都存进互斥保护的槽位——清理只执行一次（双关句柄是未定义
    /// 行为），且 cleanup 需要 `Sync`（端点跨线程共享）。
    pub(super) fn apply_sandbox_platform(
        child: &mut std::process::Child,
    ) -> Result<Box<dyn FnOnce() + Send>, String> {
        // Child 的原始句柄是私有字段：按 PID 打开（沙箱需要 PROCESS_ALL_ACCESS
        // 以便 Assign 与后续 Terminate）。
        let proc_handle: HANDLE = unsafe { OpenProcess(PROCESS_ALL_ACCESS, 0, child.id() as u32) };
        if proc_handle == 0 {
            return Err("open provider process failed".to_string());
        }

        let job: HANDLE = unsafe { CreateJobObjectA(std::ptr::null(), std::ptr::null()) };
        if job == 0 {
            unsafe { CloseHandle(proc_handle) };
            return Err("create job object failed".to_string());
        }
        let ret = unsafe {
            let mut info: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = std::mem::zeroed();
            info.BasicLimitInformation.LimitFlags =
                JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | JOB_OBJECT_LIMIT_PROCESS_MEMORY;
            info.ProcessMemoryLimit = (super::provider_mem_limit_mb() << 20) as usize;
            SetInformationJobObject(
                job,
                JobObjectExtendedLimitInformation,
                &info as *const _ as *const core::ffi::c_void,
                std::mem::size_of::<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>() as u32,
            )
        };
        if ret == 0 {
            unsafe {
                CloseHandle(job);
                CloseHandle(proc_handle);
            }
            return Err("set job limits failed".to_string());
        }
        if unsafe { AssignProcessToJobObject(job, proc_handle) } == 0 {
            unsafe {
                CloseHandle(job);
                CloseHandle(proc_handle);
            }
            return Err("assign to job object failed".to_string());
        }

        let job_slot = Mutex::new(Some(job));
        let proc_slot = Mutex::new(Some(proc_handle));
        Ok(Box::new(move || {
            if let Some(job) = job_slot.lock().expect("job slot").take() {
                unsafe { CloseHandle(job) };
            }
            if let Some(ph) = proc_slot.lock().expect("proc slot").take() {
                unsafe { CloseHandle(ph) };
            }
        }))
    }
}

#[cfg(windows)]
fn apply_sandbox_platform(
    child: &mut std::process::Child,
) -> Result<Box<dyn FnOnce() + Send>, String> {
    windows_impl::apply_sandbox_platform(child)
}

#[cfg(unix)]
fn apply_sandbox_platform(
    child: &mut std::process::Child,
) -> Result<Box<dyn FnOnce() + Send>, String> {
    // Unix：进程组在 spawn 时已设置（Command::process_group）；宿主侧无
    // rlimit 可设（由 provider 自限）。生命周期绑定由 provider 的父进程
    // 自检补齐（§16.16.1 Unix 行）。
    let _ = child;
    Ok(Box::new(|| {}))
}
