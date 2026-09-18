// SSH 本地端口转发（等价 ssh -L），与 Go 端 go/pkg/sshtunnel 行为一致：
// 监听 127.0.0.1 随机端口，驱动连接本地端口，隧道经 SSH 转发到目标 host:port。
use std::sync::Arc;

use polydb_core::{CoreError, CoreResult};
use polydb_protocol::common::SshTunnelConfig;
use russh::client::AuthResult;

/// 客户端 Handler：接受任意服务器主机密钥。与 Go 侧 ssh.InsecureIgnoreHostKey
/// 对齐；first-use known_hosts 验证留待 M9。
#[derive(Default)]
struct TunnelHandler;

impl russh::client::Handler for TunnelHandler {
    type Error = russh::Error;

    async fn check_server_key(
        &mut self,
        _server_public_key: &russh::keys::ssh_key::PublicKey,
    ) -> Result<bool, Self::Error> {
        Ok(true)
    }
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
    ) -> CoreResult<Tunnel> {
        let cfg_owned = cfg.clone();
        let target_host = target_host.to_string();
        let ssh_password = ssh_password.to_string();
        let passphrase = passphrase.to_string();
        let (tx, rx) = std::sync::mpsc::channel::<CoreResult<Tunnel>>();

        std::thread::spawn(move || {
            let _ = tx.send(Self::setup(
                &cfg_owned,
                &target_host,
                target_port,
                &ssh_password,
                &passphrase,
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
    ) -> CoreResult<(
        Arc<tokio::sync::Mutex<russh::client::Handle<TunnelHandler>>>,
        String,
    )> {
        let ssh_addr = format!("{}:{}", cfg.host, cfg.port);
        let config = Arc::new(russh::client::Config::default());
        let mut session = russh::client::connect(config, &ssh_addr, TunnelHandler)
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
