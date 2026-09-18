// Package keyring 提供机密存储：数据库密码、SSH 密码、私钥口令。
// 引用者（connections 表）只存 password_ref，明文绝不落库、绝不随响应返回。
package keyring

import (
	"errors"
	"os"

	"github.com/google/uuid"
)

// ErrNotFound 表示指定键不存在。
var ErrNotFound = errors.New("keyring: secret not found")

// Keyring 是机密存取接口。
type Keyring interface {
	// Set 写入或覆盖 key 对应的机密。
	Set(key, secret string) error
	// Get 返回 key 对应的机密；不存在返回 ErrNotFound。
	Get(key string) (string, error)
	// Delete 删除 key；不存在时不报错。
	Delete(key string) error
}

// Ref 生成 password_ref：polydb:<scope>:<uuid>。scope 取 conn / ssh / ssh-pass。
func Ref(scope string) string {
	return "polydb:" + scope + ":" + uuid.NewString()
}

// Open 按配置打开 keyring：POLYDB_KEYRING=os 用 OS 凭据管理器，否则本地加密文件。
// 文件后端的主密码取 POLYDB_MASTER_PASSWORD（缺省为空字符串，即开发模式）。
func Open(dataDir string) (Keyring, error) {
	if os.Getenv("POLYDB_KEYRING") == "os" {
		return OSKeyring{}, nil
	}
	return NewFileKeyring(dataDir, os.Getenv("POLYDB_MASTER_PASSWORD"))
}
