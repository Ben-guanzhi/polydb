package sshtunnel

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// known_hosts 首用校验（M9 还债，双实现行为一致，见 docs/ssh-tunnel.md）：
//
// 文件格式（OpenSSH known_hosts 兼容：`hostport keytype base64`，hostport = host:port）：
//   - hostport 已有条目且 key 一致  → 接受；
//   - hostport 已有条目但 key 不同  → 拒绝（主机密钥变更 = MITM 信号）；
//   - hostport 无条目（首次使用）    → 追加条目并接受（TOFU / OpenSSH accept-new 语义）。
//
// 记簿失败（目录不可写等）不阻断连接：校验只对「已知 host」有约束力。
// path 为空时退化为 ssh.InsecureIgnoreHostKey（M8 原行为，供无存储环境使用）。

// knownHostsMu 串行化 known_hosts 的读-改-写，避免并发连接互踩。
var knownHostsMu sync.Mutex

// keyBase64 返回 OpenSSH known_hosts 第三列：wire 编码 blob 的 std base64。
// Go 与 Rust（public_key_base64）同格式，两端条目可互通。
func keyBase64(key ssh.PublicKey) string {
	return base64.StdEncoding.EncodeToString(key.Marshal())
}

// hostField 返回 OpenSSH known_hosts 的 host 字段约定（与 Rust 侧
// russh::keys::known_hosts 的匹配规则一致）：22 端口用裸 host，其余 [host]:port。
func hostField(host string, port int) string {
	if port == 22 {
		return host
	}
	return fmt.Sprintf("[%s]:%d", host, port)
}

// HostKeyCallback 返回绑定 <host:port> 与 known_hosts 文件的 HostKeyCallback。
// port 为 0 时按 OpenSSH 语义视为 22。
func HostKeyCallback(path, host string, port int) ssh.HostKeyCallback {
	if path == "" {
		return ssh.InsecureIgnoreHostKey()
	}
	if port == 0 {
		port = 22
	}
	hostSpec := hostField(host, port)
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		knownHostsMu.Lock()
		defer knownHostsMu.Unlock()

		blob := keyBase64(key)
		data, err := os.ReadFile(path)
		if err == nil {
			mismatch := false
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 3 || fields[0] != hostSpec {
					continue
				}
				if fields[2] == blob {
					return nil
				}
				// 同 host 不同 key：拒绝（既有条目失配，上面继续找同名条目）。
				mismatch = true
			}
			if mismatch {
				return fmt.Errorf("ssh host key mismatch for %s: known_hosts 条目与服务器密钥不一致（可能 MITM，或服务器更换了密钥）", hostSpec)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("ssh known_hosts 读取失败 %s: %w", path, err)
		}

		// 首次使用：追加条目并接受（TOFU）。
		entry := fmt.Sprintf("%s %s %s", hostSpec, key.Type(), blob)
		if merr := os.MkdirAll(filepath.Dir(path), 0o700); merr != nil {
			return nil // 记簿失败不阻断连接
		}
		f, aerr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if aerr != nil {
			return nil
		}
		if _, werr := fmt.Fprintf(f, "%s\n", entry); werr != nil {
			_ = f.Close()
			return nil
		}
		return f.Close()
	}
}
