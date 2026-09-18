package keyring

import (
	"fmt"

	oskr "github.com/zalando/go-keyring"
)

const osService = "polydb"

// OSKeyring 委托 OS 凭据管理器（Windows 凭据管理器 / macOS Keychain / Linux Secret Service）。
// 无桌面会话的 Linux/Docker 环境不可用，此时应使用 FileKeyring（POLYDB_KEYRING=file，默认）。
type OSKeyring struct{}

func (OSKeyring) Set(key, secret string) error {
	if err := oskr.Set(osService, key, secret); err != nil {
		return fmt.Errorf("keyring: os set %s: %w", key, err)
	}
	return nil
}

func (OSKeyring) Get(key string) (string, error) {
	s, err := oskr.Get(osService, key)
	if err == oskr.ErrNotFound {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("keyring: os get %s: %w", key, err)
	}
	return s, nil
}

func (OSKeyring) Delete(key string) error {
	if err := oskr.Delete(osService, key); err != nil && err != oskr.ErrNotFound {
		return fmt.Errorf("keyring: os delete %s: %w", key, err)
	}
	return nil
}
