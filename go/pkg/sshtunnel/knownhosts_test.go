package sshtunnel

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	_ = pub
	return signer
}

func TestHostKeyCallbackTOFU(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	s1 := testSigner(t)
	s2 := testSigner(t)

	// 1) 首次使用：接受并落盘
	cb := HostKeyCallback(path, "ssh.example.com", 2222)
	if err := cb("ssh.example.com", nil, s1.PublicKey()); err != nil {
		t.Fatalf("first use: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	// OpenSSH host 字段约定：非 22 端口 → [host]:port
	if !strings.Contains(string(data), "[ssh.example.com]:2222 ssh-ed25519 ") {
		t.Fatalf("entry not recorded: %q", data)
	}

	// 2) 同 key 再次连接：接受
	if err := cb("ssh.example.com", nil, s1.PublicKey()); err != nil {
		t.Fatalf("known host same key: %v", err)
	}

	// 3) 同 host 不同 key：拒绝（MITM 信号）
	if err := cb("ssh.example.com", nil, s2.PublicKey()); err == nil ||
		!strings.Contains(err.Error(), "host key mismatch") {
		t.Fatalf("mismatch should reject, got %v", err)
	}

	// 4) 22 端口用裸 host 记录
	path2 := filepath.Join(t.TempDir(), "known_hosts")
	cb22 := HostKeyCallback(path2, "db.example.com", 22)
	if err := cb22("db.example.com", nil, s1.PublicKey()); err != nil {
		t.Fatalf("port22 first use: %v", err)
	}
	data2, _ := os.ReadFile(path2)
	if !strings.Contains(string(data2), "\ndb.example.com ssh-ed25519 ") &&
		!strings.HasPrefix(string(data2), "db.example.com ssh-ed25519 ") {
		t.Fatalf("port22 entry host field wrong: %q", data2)
	}

	// 5) 空路径退化为不校验
	insecure := HostKeyCallback("", "x", 0)
	if err := insecure("x", nil, s2.PublicKey()); err != nil {
		t.Fatalf("insecure fallback should accept: %v", err)
	}
}

func TestHostField(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"db.example.com", 22, "db.example.com"},
		{"db.example.com", 2222, "[db.example.com]:2222"},
	}
	for _, c := range cases {
		if got := hostField(c.host, c.port); got != c.want {
			t.Errorf("hostField(%q,%d) = %q, want %q", c.host, c.port, got, c.want)
		}
	}
}
