package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/scrypt"
)

const (
	magic        = "POLYDBK1\n"
	saltLen      = 16
	nonceLen     = 12
	scryptN      = 32768
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
)

// FileKeyring 将整个机密映射加密为单个文件（AES-256-GCM）。
// 密钥由 scrypt(masterPassword, salt) 派生；masterPassword 为空即开发模式（仍加密落盘，但无口令保护）。
// 文件格式：magic | salt(16) | nonce(12) | ciphertext(GCM，含 tag)，明文为 JSON map[string]string。
type FileKeyring struct {
	path string
	key  [scryptKeyLen]byte
	salt []byte
	mu   sync.Mutex
}

func NewFileKeyring(dataDir, masterPassword string) (*FileKeyring, error) {
	path := filepath.Join(dataDir, "keyring.bin")
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("keyring: generate salt: %w", err)
	}
	key, err := scrypt.Key([]byte(masterPassword), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return nil, fmt.Errorf("keyring: derive key: %w", err)
	}
	k := &FileKeyring{path: path, mu: sync.Mutex{}}
	copy(k.key[:], key)
	k.salt = salt

	// 已存在文件：校验 magic 并保留原 salt；否则创建空文件。
	if data, err := os.ReadFile(path); err == nil {
		if len(data) < len(magic)+saltLen+nonceLen {
			return nil, fmt.Errorf("keyring: file %s is corrupt (too short)", path)
		}
		if string(data[:len(magic)]) != magic {
			return nil, fmt.Errorf("keyring: file %s has wrong magic", path)
		}
		copy(k.salt, data[len(magic):len(magic)+saltLen])
		key, err := scrypt.Key([]byte(masterPassword), k.salt, scryptN, scryptR, scryptP, scryptKeyLen)
		if err != nil {
			return nil, fmt.Errorf("keyring: derive key: %w", err)
		}
		copy(k.key[:], key)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("keyring: read %s: %w", path, err)
	}
	// 新文件：salt 已生成，稍后首次 save 时落盘。
	// 已有文件时立即验证主密码（解密失败则报错，避免延迟到首次 Get）。
	if _, err := k.load(); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *FileKeyring) Set(key, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	m[key] = secret
	return k.save(m)
}

func (k *FileKeyring) Get(key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return "", err
	}
	s, ok := m[key]
	if !ok {
		return "", ErrNotFound
	}
	return s, nil
}

func (k *FileKeyring) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return k.save(m)
}

func (k *FileKeyring) load() (map[string]string, error) {
	data, err := os.ReadFile(k.path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("keyring: read %s: %w", k.path, err)
	}
	plain, err := k.decrypt(data)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if len(plain) > 0 {
		if err := json.Unmarshal(plain, &m); err != nil {
			return nil, fmt.Errorf("keyring: parse %s: %w", k.path, err)
		}
	}
	return m, nil
}

func (k *FileKeyring) save(m map[string]string) error {
	plain, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("keyring: marshal: %w", err)
	}
	data, err := k.encrypt(plain)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return fmt.Errorf("keyring: mkdir: %w", err)
	}
	if err := os.WriteFile(k.path, data, 0o600); err != nil {
		return fmt.Errorf("keyring: write %s: %w", k.path, err)
	}
	return nil
}

func (k *FileKeyring) encrypt(plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(k.key[:])
	if err != nil {
		return nil, fmt.Errorf("keyring: aes: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("keyring: nonce: %w", err)
	}
	out := make([]byte, 0, len(magic)+saltLen+nonceLen+len(plain)+aes.BlockSize)
	out = append(out, magic...)
	out = append(out, k.salt...)
	out = append(out, nonce...)
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keyring: gcm: %w", err)
	}
	return gcm.Seal(out, nonce, plain, nil), nil
}

func (k *FileKeyring) decrypt(data []byte) ([]byte, error) {
	if len(data) < len(magic)+saltLen+nonceLen {
		return nil, fmt.Errorf("keyring: file %s is corrupt (too short)", k.path)
	}
	if string(data[:len(magic)]) != magic {
		return nil, fmt.Errorf("keyring: file %s has wrong magic", k.path)
	}
	body := data[len(magic)+saltLen+nonceLen:]
	block, err := aes.NewCipher(k.key[:])
	if err != nil {
		return nil, fmt.Errorf("keyring: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keyring: gcm: %w", err)
	}
	nonce := data[len(magic)+saltLen : len(magic)+saltLen+nonceLen]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("keyring: decrypt %s (wrong master password?): %w", k.path, err)
	}
	return plain, nil
}
