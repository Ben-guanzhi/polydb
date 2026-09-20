// SSH 本地端口转发（等价 ssh -L），与 Go 端 go/pkg/sshtunnel 行为一致：
// 监听 127.0.0.1 随机端口，驱动连接本地端口，隧道经 SSH 转发到目标 host:port。
use std::sync::{Arc, Mutex};

use polydb_core::{CoreError, CoreResult};
use polydb_protocol::common::SshTunnelConfig;
use russh::client::AuthResult;

// known_hosts 读-改-写串行化（TOFU 首用校验，双实现行为一致，见 docs/ssh-tunnel.md）。
static KNOWN_HOSTS_MU: Mutex<()> = Mutex::new(());

/// 客户端 Handler：SSH 主机密钥校验（与 Go 侧 `sshtunnel::HostKeyCallback` 对齐）：
///
/// - `known_hosts` 为 `None`：接受任意 key（M8 原行为）；
/// - `Some`：known_hosts 中已有该 host 条目且 key 一致 → 接受；
///   key 变更 → 拒绝（MITM 信号，`russh::keys::Error::KeyChanged`）；
///   无条目 → 追加条目并接受（首次使用 / OpenSSH accept-new 语义）。
///
/// 记簿（文件写入）失败不阻断连接。
struct TunnelHandler {
    known_hosts: Option<std::path::PathBuf>,
    host: String,
    port: u16,
}

impl russh::client::Handler for TunnelHandler {
    type Error = russh::Error;

    async fn check_server_key(
        &mut self,
        server_public_key: &russh::keys::ssh_key::PublicKey,
    ) -> Result<bool, Self::Error> {
        let Some(path) = &self.known_hosts else {
            return Ok(true);
        };
        let _guard = KNOWN_HOSTS_MU
            .lock()
            .map_err(|_| russh::Error::Inconsistent)?;
        match russh::keys::check_known_hosts_path(&self.host, self.port, server_public_key, path) {
            Ok(true) => Ok(true),
            Ok(false) => {
                record_known_host(path, &self.host, self.port, server_public_key);
                Ok(true)
            }
            Err(e) => Err(e.into()),
        }
    }
}

/// 追加首用条目（OpenSSH known_hosts 格式 `host keytype base64`）。
/// host 字段与 russh known_hosts 匹配规则一致：22 端口用裸 host，其余 `[host]:port`；
/// 第三列 wire blob 的 base64（public_key_base64）与 Go 侧 key.Marshal() 的 base64 同格式，
/// 两端条目可互通（russh 解析时只读 host 与 base64 两列，keytype 列仅供参考）。
fn record_known_host(
    path: &std::path::Path,
    host: &str,
    port: u16,
    key: &russh::keys::ssh_key::PublicKey,
) {
    use russh::keys::PublicKeyBase64;
    let host_spec = if port == 22 {
        host.to_string()
    } else {
        format!("[{host}]:{port}")
    };
    let algo = key.algorithm();
    let key_type = algo.as_str();
    let entry = format!("{host_spec} {key_type} {}\n", key.public_key_base64());
    if let Some(parent) = path.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    let Ok(mut file) = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
    else {
        return;
    };
    use std::io::Write;
    let _ = file.write_all(entry.as_bytes());
}

/// 已建立的隧道。session 供转发与关闭共享；runtime 保活转发任务。
pub struct Tunnel {
    // Handle 含 UnboundedReceiver，非 Sync：Arc<Mutex> 供 forward_loop 与 close 共享。
    session: Arc<tokio::sync::Mutex<russh::client::Handle<TunnelHandler>>>,
    local_addr: String,
    // forward_loop 任务运行在该运行时上；运行时一 drop 任务即被取消，故必须持有。
    #[allow(dead_code)]
    runtime: tokio::runtime::Runtime,
}

impl Tunnel {
    /// 建立隧道。ssh_password / passphrase 为从 keyring 取回的明文（可为空）。
    ///
    /// russh 侧运行在独立线程的独立运行时上：connect() 会从 axum 异步 handler 调用，
    /// 而运行时内 rt.block_on 会 panic（该请求随即 EOF 返回客户端，进程不死）。
    /// 驱动侧仍跑在调用方运行时上，所以只隔离建连过程。
    pub fn open(
        cfg: &SshTunnelConfig,
        target_host: &str,
        target_port: u16,
        ssh_password: &str,
        passphrase: &str,
        known_hosts: Option<&std::path::Path>,
    ) -> CoreResult<Tunnel> {
        let cfg_owned = cfg.clone();
        let target_host = target_host.to_string();
        let ssh_password = ssh_password.to_string();
        let passphrase = passphrase.to_string();
        let known_hosts = known_hosts.map(|p| p.to_path_buf());
        let (tx, rx) = std::sync::mpsc::channel::<CoreResult<Tunnel>>();

        std::thread::spawn(move || {
            let _ = tx.send(Self::setup(
                &cfg_owned,
                &target_host,
                target_port,
                &ssh_password,
                &passphrase,
                known_hosts,
            ));
        });

        rx.recv()
            .map_err(|_| CoreError::SshTunnel("tunnel open thread failed".into()))?
    }

    /// 建连 + 监听 127.0.0.1 随机端口，运行在新建的 current_thread 运行时上。
    fn setup(
        cfg: &SshTunnelConfig,
        target_host: &str,
        target_port: u16,
        ssh_password: &str,
        passphrase: &str,
        known_hosts: Option<std::path::PathBuf>,
    ) -> CoreResult<Tunnel> {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .expect("build tunnel runtime");
        let (session, local_addr) = runtime.block_on(Self::connect(
            cfg,
            target_host,
            target_port,
            ssh_password,
            passphrase,
            known_hosts,
        ))?;
        Ok(Tunnel {
            session,
            local_addr,
            runtime,
        })
    }

    async fn connect(
        cfg: &SshTunnelConfig,
        target_host: &str,
        target_port: u16,
        ssh_password: &str,
        passphrase: &str,
        known_hosts: Option<std::path::PathBuf>,
    ) -> CoreResult<(
        Arc<tokio::sync::Mutex<russh::client::Handle<TunnelHandler>>>,
        String,
    )> {
        // 端口缺省 22（与 Go 侧一致；known_hosts 校验与拨号用同一端口值）。
        let ssh_port = if cfg.port == 0 { 22 } else { cfg.port };
        let ssh_addr = format!("{}:{ssh_port}", cfg.host);
        let config = Arc::new(russh::client::Config::default());
        let handler = TunnelHandler {
            known_hosts,
            host: cfg.host.clone(),
            port: ssh_port,
        };
        let mut session = russh::client::connect(config, &ssh_addr, handler)
            .await
            .map_err(|e| CoreError::SshTunnel(format!("connect {ssh_addr}: {e}")))?;

        let authed = if let Some(key_path) = cfg.private_key_path.as_deref() {
            let pass = if passphrase.is_empty() {
                None
            } else {
                Some(passphrase)
            };
            let key = russh::keys::load_secret_key(key_path, pass)
                .map_err(|e| CoreError::SshTunnel(format!("parse private key {key_path}: {e}")))?;
            // RSA 密钥自动协商 rsa-sha2-256/512（ssh-rsa 已被新版 OpenSSH 默认禁用）。
            let hash = session
                .best_supported_rsa_hash()
                .await
                .ok()
                .flatten()
                .flatten();
            session
                .authenticate_publickey(
                    &cfg.username,
                    russh::keys::PrivateKeyWithHashAlg::new(Arc::new(key), hash),
                )
                .await
                .map_err(|e| CoreError::SshTunnel(format!("publickey auth: {e}")))?
        } else if !ssh_password.is_empty() {
            session
                .authenticate_password(&cfg.username, ssh_password)
                .await
                .map_err(|e| CoreError::SshTunnel(format!("password auth: {e}")))?
        } else {
            return Err(CoreError::SshTunnel(
                "no auth method (set private_key_path or password)".into(),
            ));
        };
        if !matches!(authed, AuthResult::Success) {
            return Err(CoreError::SshTunnel("ssh authentication failed".into()));
        }

        let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
            .await
            .map_err(|e| CoreError::SshTunnel(format!("tunnel listen: {e}")))?;
        let local_port = listener
            .local_addr()
            .map_err(|e| CoreError::SshTunnel(format!("local addr: {e}")))?
            .port();

        let session = Arc::new(tokio::sync::Mutex::new(session));
        tokio::spawn(forward_loop(
            listener,
            session.clone(),
            target_host.to_string(),
            target_port,
        ));

        Ok((session, format!("127.0.0.1:{local_port}")))
    }

    /// 本地地址 "127.0.0.1:<port>"，供驱动 DSN 替换目标地址。
    pub fn local_addr(&self) -> &str {
        &self.local_addr
    }

    /// 关闭底层 SSH 会话（本对象 drop 时会一并停掉转发运行时）。
    pub fn close(&self) {
        let session = self.session.clone();
        std::thread::spawn(move || {
            // 关闭不能 block_on 隧道的运行时（它属于另一个线程且未 enter）：
            // 另起短生命周期运行时即可，Handle 跨运行时可用。
            let rt = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build();
            if let Ok(rt) = rt {
                rt.block_on(async move {
                    let s = session.lock().await;
                    let _ = s.disconnect(russh::Disconnect::ByApplication, "", "").await;
                });
            }
        });
    }
}

impl Drop for Tunnel {
    fn drop(&mut self) {
        self.close();
    }
}

async fn forward_loop(
    listener: tokio::net::TcpListener,
    session: Arc<tokio::sync::Mutex<russh::client::Handle<TunnelHandler>>>,
    target_host: String,
    target_port: u16,
) {
    loop {
        let (mut stream, _) = match listener.accept().await {
            Ok(v) => v,
            Err(_) => return,
        };
        let session = session.clone();
        let target_host = target_host.clone();
        tokio::spawn(async move {
            let ch = match session
                .lock()
                .await
                .channel_open_direct_tcpip(&target_host, target_port as u32, "127.0.0.1", 0)
                .await
            {
                Ok(c) => c,
                Err(_) => return,
            };
            // into_stream 消费 channel 为双向 AsyncRead+AsyncWrite 流，rx 侧 ChannelCloseOnDrop，drop 即关闭
            let mut ch = ch.into_stream();
            let _ = tokio::io::copy_bidirectional(&mut stream, &mut ch).await;
        });
    }
}

#[cfg(test)]
mod tests {
    use super::record_known_host;
    use russh::keys::ssh_key::PublicKey;

    const KEY1: &str =
        "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDyghIykDbRDLDQpRaT38Kyzbz5TU20isF9/W2JHMfVB";
    const KEY2: &str =
        "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEd5R74QHGpKkki6q3Zb2CNFLgzxV87WroYfolztKqxx";

    /// known_hosts 首用（TOFU）回读：record 写入的条目能被 russh 校验器识别
    /// （含 host 字段约定、base64 尾随换行容忍、同 host 异 key → KeyChanged）。
    #[test]
    fn known_hosts_first_use_roundtrip() {
        let dir = tempfile_dir();
        let path = dir.join("known_hosts");
        let k1: PublicKey = KEY1.parse().expect("parse key1");
        let k2: PublicKey = KEY2.parse().expect("parse key2");

        // 首次使用：记录并可用同 key 校验通过
        record_known_host(&path, "ssh.example.com", 2222, &k1);
        let got = russh::keys::check_known_hosts_path("ssh.example.com", 2222, &k1, &path)
            .expect("check key1");
        assert!(got, "recorded key should verify");

        // 同 host 异 key：拒绝（KeyChanged）
        let err = russh::keys::check_known_hosts_path("ssh.example.com", 2222, &k2, &path)
            .expect_err("different key for known host must fail");
        assert!(
            matches!(err, russh::keys::Error::KeyChanged { .. }),
            "expected KeyChanged, got {err:?}"
        );

        // 未记录 host：未知（TOFU 追加前的返回值）
        assert!(
            !russh::keys::check_known_hosts_path("other.example.com", 22, &k1, &path)
                .expect("check unknown host")
        );

        // 22 端口裸 host 记录
        record_known_host(&path, "db.example.com", 22, &k1);
        assert!(
            russh::keys::check_known_hosts_path("db.example.com", 22, &k1, &path)
                .expect("check port22")
        );

        let content = std::fs::read_to_string(&path).expect("read file");
        assert!(
            content.contains("[ssh.example.com]:2222"),
            "non-22 host field:\n{content}"
        );
        assert!(
            content.contains("db.example.com ssh-"),
            "port22 bare host field:\n{content}"
        );
    }

    fn tempfile_dir() -> std::path::PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "polydb-knownhosts-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }
}
