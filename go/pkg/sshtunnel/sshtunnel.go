// Package sshtunnel 实现连接级 SSH 本地端口转发（等价 ssh -L）。
// 对所有数据库驱动通用：驱动连接 127.0.0.1:<本地端口>，隧道转发到目标 host:port。
package sshtunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/polydb/polydb/pkg/protocol"
)

// Tunnel 是到目标 host:port 的本地端口转发隧道。
type Tunnel struct {
	client    *ssh.Client
	listener  net.Listener
	target    string
	localAddr string
	closeOnce sync.Once
}

// Open 按配置建立隧道：连 SSH 服务器，监听 127.0.0.1 随机端口并转发到 target。
// password 为从 keyring 取回的 SSH 密码（未配置私钥时使用）。
// knownHostsPath 为 SSH 主机密钥 known_hosts 文件（TOFU 首用校验，见 knownhosts.go）；
// 传空则不校验主机密钥（M8 原行为）。
func Open(ctx context.Context, cfg *protocol.SshTunnelConfig, targetHost string, targetPort int, password string, knownHostsPath string) (*Tunnel, error) {
	auth, err := authMethods(cfg, password)
	if err != nil {
		return nil, err
	}
	clientCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            auth,
		Timeout:         10 * time.Second,
		HostKeyCallback: HostKeyCallback(knownHostsPath, cfg.Host, cfg.Port),
	}
	sshHost := net.JoinHostPort(cfg.Host, itoa(cfg.Port))
	client, err := ssh.Dial("tcp", sshHost, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", sshHost, err)
	}
	if err := ctx.Err(); err != nil {
		_ = client.Close()
		return nil, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ssh tunnel listen: %w", err)
	}

	t := &Tunnel{
		client:    client,
		listener:  listener,
		target:    net.JoinHostPort(targetHost, itoa(targetPort)),
		localAddr: listener.Addr().String(),
	}
	go t.forwardLoop()
	return t, nil
}

// LocalAddr 返回 "127.0.0.1:<port>"，供驱动 DSN 替换目标地址。
func (t *Tunnel) LocalAddr() string { return t.localAddr }

// Close 关闭隧道与底层 SSH 连接。
func (t *Tunnel) Close() error {
	var err error
	t.closeOnce.Do(func() {
		_ = t.listener.Close()
		err = t.client.Close()
	})
	return err
}

func (t *Tunnel) forwardLoop() {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			return
		}
		go t.forward(conn)
	}
}

func (t *Tunnel) forward(conn net.Conn) {
	target, err := t.client.Dial("tcp", t.target)
	if err != nil {
		_ = conn.Close()
		return
	}
	go func() { _, _ = io.Copy(target, conn); _ = target.Close() }()
	_, _ = io.Copy(conn, target)
	_ = conn.Close()
}

// authMethods 按 private_key_path > password 的优先级构建认证方法。
func authMethods(cfg *protocol.SshTunnelConfig, password string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if cfg.PrivateKeyPath != "" {
		key, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("ssh tunnel read private key: %w", err)
		}
		var signer ssh.Signer
		if cfg.PrivateKeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(cfg.PrivateKeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("ssh tunnel parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if password != "" {
		methods = append(methods, ssh.Password(password))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("ssh tunnel: no auth method (set private_key_path or password)")
	}
	return methods, nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
