package contract

import "testing"

// M8 契约：一次性密码 + keyring + SSH 隧道。
// 两条红线必须双端一致：
//  1. 明文密码/私钥口令绝不回显（响应里不存在 password / private_key_passphrase，
//     连接顶层也不带 password_ref；引用仅存在于 ssh_tunnel 内部）。
//  2. SSH 隧道不可达时 /test 返回 connected=false + 非空 error（不区分驱动错误路径）。

func TestContractSecretsNeverReturned(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			c := NewClient(b.base)
			body := map[string]any{
				"name":     "m8-secrets",
				"kind":     "postgres",
				"host":     "127.0.0.1",
				"port":     5432,
				"database": "postgres",
				"username": "postgres",
				"password": "s3cr3t-db-pw",
				"ssh_tunnel": map[string]any{
					"host":                   "127.0.0.1",
					"port":                   22,
					"username":               "nobody",
					"password":               "s3cr3t-ssh-pw",
					"private_key_path":       "/nonexistent/id_ed25519",
					"private_key_passphrase": "s3cr3t-passphrase",
				},
			}
			status, created, err := c.Do("POST", "/api/connections", body)
			if err != nil || status != 201 {
				t.Fatalf("%s: create with one-time secrets failed: status=%d err=%v", b.name, status, err)
			}
			id, ok := firstConnID(created)
			if !ok || id == "" {
				t.Fatalf("%s: no id in create response: %v", b.name, created)
			}
			assertNoSecret(t, "create response", created)

			// update 携带新密码：同样只写 keyring，不回显。
			status, updated, err := c.Do("PUT", "/api/connections/"+id, map[string]any{"password": "new-pw"})
			if err != nil || status != 200 {
				t.Fatalf("%s: update with password failed: status=%d err=%v", b.name, status, err)
			}
			assertNoSecret(t, "update response", updated)

			// 列表与单查同样不得泄漏。
			status, list, err := c.Do("GET", "/api/connections", nil)
			if err != nil || status != 200 {
				t.Fatalf("%s: list failed: status=%d err=%v", b.name, status, err)
			}
			assertNoSecret(t, "list response", list)

			status, got, err := c.Do("GET", "/api/connections/"+id, nil)
			if err != nil || status != 200 {
				t.Fatalf("%s: get failed: status=%d err=%v", b.name, status, err)
			}
			assertNoSecret(t, "get response", got)

			if _, _, err := c.Do("DELETE", "/api/connections/"+id, nil); err != nil {
				t.Logf("%s: cleanup delete failed: %v", b.name, err)
			}
		})
	}
}

func TestContractSSHTunnelFailure(t *testing.T) {
	bs := backends(t)
	for _, b := range bs {
		b := b
		t.Run(b.name, func(t *testing.T) {
			c := NewClient(b.base)
			// 隧道指向无 SSH 服务的本地端口：connect 必然失败，且失败发生在隧道阶段
			// （驱动目标地址已被重写为隧道本地端口，故不会走驱动错误路径）。
			body := map[string]any{
				"name":     "m8-ssh",
				"kind":     "postgres",
				"host":     "127.0.0.1",
				"port":     5432,
				"database": "postgres",
				"username": "postgres",
				"ssh_tunnel": map[string]any{
					"host":     "127.0.0.1",
					"port":     1,
					"username": "nobody",
					"password": "wrong",
				},
			}
			status, created, err := c.Do("POST", "/api/connections", body)
			if err != nil || status != 201 {
				t.Fatalf("%s: create failed: status=%d err=%v", b.name, status, err)
			}
			id, _ := firstConnID(created)

			// 连接失败必须返回 ConnectionStatus（connected=false + error），而不是 5xx。
			status, got, err := c.Do("POST", "/api/connections/"+id+"/test", nil)
			if err != nil || status != 200 {
				t.Fatalf("%s: test endpoint should return status body, got status=%d err=%v", b.name, status, err)
			}
			m, ok := got.(map[string]any)
			if !ok {
				t.Fatalf("%s: unexpected test response: %v", b.name, got)
			}
			if connected, _ := m["connected"].(bool); connected {
				t.Errorf("%s: connected=true despite unreachable SSH tunnel", b.name)
			}
			if err_, ok := m["error"].(string); !ok || err_ == "" {
				t.Errorf("%s: expected non-empty error in ConnectionStatus, got %v", b.name, m["error"])
			}
			assertNoSecret(t, "test response", got)

			if _, _, err := c.Do("DELETE", "/api/connections/"+id, nil); err != nil {
				t.Logf("%s: cleanup delete failed: %v", b.name, err)
			}
		})
	}
}

// assertNoSecret 递归断言响应中不含明文字段。
// 顶层 password_ref 判定：含 "kind" 的对象即 ConnectionInfo，不得带 password_ref；
// ssh_tunnel 内部的 password_ref / private_key_passphrase_ref 是合法引用，允许存在。
func assertNoSecret(t *testing.T, label string, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		if _, hasKind := x["kind"]; hasKind {
			if _, has := x["password_ref"]; has {
				t.Errorf("%s: ConnectionInfo leaks top-level password_ref", label)
			}
		}
		for k, val := range x {
			switch k {
			case "password", "private_key_passphrase":
				t.Errorf("%s: leaks plaintext secret field %q", label, k)
			}
			assertNoSecret(t, label, val)
		}
	case []any:
		for _, item := range x {
			assertNoSecret(t, label, item)
		}
	}
}
