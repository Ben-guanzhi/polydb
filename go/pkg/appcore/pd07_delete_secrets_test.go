package appcore

import (
	"errors"
	"testing"

	"github.com/polydb/polydb/pkg/keyring"
	"github.com/polydb/polydb/pkg/protocol"
	"github.com/polydb/polydb/pkg/storage"
)

// newPD07TestApp 构造带文件 keyring 的 AppCore，返回 keyring 以便断言凭据是否
// 被摘除（PD-07）。
func newPD07TestApp(t *testing.T) (*AppCore, keyring.Keyring) {
	t.Helper()
	t.Setenv("POLYDB_DATA_DIR", t.TempDir())
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	kr, err := keyring.NewFileKeyring(t.TempDir(), "pd07-test")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return New(db, kr), kr
}

// TestDeleteConnectionRevokesKeyringSecrets 是 PD-07 的回归：删除连接必须把
// keyring 里由 password_ref 指向的凭据一并摘掉。此前 `DeleteConnection` 只删
// `connections` 行，`password_ref` 指向的明文密码留在 keyring 里仍可被复用——
// 删除被做成了「只删元数据的假删除」。
func TestDeleteConnectionRevokesKeyringSecrets(t *testing.T) {
	a, kr := newPD07TestApp(t)

	info, err := a.CreateConnection(&protocol.CreateConnectionRequest{
		Name:     "c1",
		Kind:     protocol.DatabaseKindSQLite,
		Database: ":memory:",
		Username: "u",
		Password: "conn-secret",
		SSHTunnel: &protocol.SshTunnelConfig{
			Host:                 "127.0.0.1",
			Port:                 22,
			Username:             "ssh-user",
			Password:             "ssh-secret",
			PrivateKeyPassphrase: "passphrase-secret",
		},
	})
	if err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	if info.SSHTunnel == nil {
		t.Fatalf("CreateConnection dropped SSHTunnel")
	}

	// 删除前三条凭据都必须可读。
	connRef, err := a.repo.GetPasswordRef(info.ID)
	if err != nil {
		t.Fatalf("GetPasswordRef: %v", err)
	}
	refs := map[string]string{
		"conn":     connRef,
		"ssh":      info.SSHTunnel.PasswordRef,
		"ssh-pass": info.SSHTunnel.PrivateKeyPassphraseRef,
	}
	for name, ref := range refs {
		if ref == "" {
			t.Fatalf("%s ref empty", name)
		}
		if _, err := kr.Get(ref); err != nil {
			t.Fatalf("Get(%s ref) before delete: %v", name, err)
		}
	}

	ok, err := a.DeleteConnection(info.ID)
	if err != nil {
		t.Fatalf("DeleteConnection: %v", err)
	}
	if !ok {
		t.Fatalf("DeleteConnection returned false")
	}

	// 删除后凭据必须不存在（`¬ relied_n(γ)`：行已删，凭据不得残留）。
	for name, ref := range refs {
		if _, err := kr.Get(ref); !errors.Is(err, keyring.ErrNotFound) {
			t.Fatalf("Get(%s ref) after delete = %v; want ErrNotFound（PD-07：凭据必须随行删除）", name, err)
		}
	}
}
