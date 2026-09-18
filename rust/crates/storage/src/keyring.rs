// Keyring：与 Go 端（go/pkg/keyring）文件格式兼容的机密存储。
// 文件格式：magic("POLYDBK1\n") | salt(16) | nonce(12) | AES-256-GCM 密文（明文为 JSON map）。
use std::collections::HashMap;
use std::fs;
use std::path::PathBuf;
use std::sync::Mutex;

use aes_gcm::aead::{Aead, KeyInit, OsRng};
use aes_gcm::{Aes256Gcm, Nonce};
use rand::RngCore;
use scrypt::{scrypt, Params};

use polydb_core::{CoreError, CoreResult};

const MAGIC: &[u8] = b"POLYDBK1\n";
const SALT_LEN: usize = 16;
const NONCE_LEN: usize = 12;
const SCRYPT_LOG_N: u8 = 15; // N = 32768，与 Go 端一致
const SCRYPT_R: u32 = 8;
const SCRYPT_P: u32 = 1;
const KEY_LEN: usize = 32;

#[derive(Debug, thiserror::Error)]
enum KeyringError {
    #[error("keyring: io: {0}")]
    Io(#[from] std::io::Error),
    #[error("keyring: secret not found")]
    NotFound,
}

impl From<KeyringError> for CoreError {
    fn from(e: KeyringError) -> Self {
        CoreError::Keyring(e.to_string())
    }
}

pub trait Keyring: Send + Sync {
    fn set(&self, key: &str, secret: &str) -> CoreResult<()>;
    fn get(&self, key: &str) -> CoreResult<String>;
    fn delete(&self, key: &str) -> CoreResult<()>;
}

/// 生成 password_ref：polydb:<scope>:<uuid>。
pub fn keyring_ref(scope: &str) -> String {
    format!("polydb:{scope}:{}", uuid::Uuid::new_v4())
}

pub struct FileKeyring {
    path: PathBuf,
    salt: [u8; SALT_LEN],
    key: [u8; KEY_LEN],
    mu: Mutex<()>,
}

impl FileKeyring {
    pub fn open(data_dir: &str, master_password: &str) -> CoreResult<Self> {
        let path = PathBuf::from(data_dir).join("keyring.bin");
        let salt: [u8; SALT_LEN] = match fs::read(&path) {
            Ok(data) => {
                if data.len() < MAGIC.len() + SALT_LEN + NONCE_LEN {
                    return Err(CoreError::Keyring(format!(
                        "file {} is corrupt (too short)",
                        path.display()
                    )));
                }
                if &data[..MAGIC.len()] != MAGIC {
                    return Err(CoreError::Keyring(format!(
                        "file {} has wrong magic",
                        path.display()
                    )));
                }
                let mut s = [0u8; SALT_LEN];
                s.copy_from_slice(&data[MAGIC.len()..MAGIC.len() + SALT_LEN]);
                s
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                let mut s = [0u8; SALT_LEN];
                OsRng.fill_bytes(&mut s);
                s
            }
            Err(e) => return Err(CoreError::Keyring(format!("read {}: {e}", path.display()))),
        };
        let key = derive_key(master_password, &salt)?;
        let kr = Self {
            path,
            salt,
            key,
            mu: Mutex::new(()),
        };
        // 已有文件时立即验证主密码（解密失败则报错，避免延迟到首次 get）。
        if kr.path.exists() {
            let _ = kr.load()?;
        }
        Ok(kr)
    }

    fn load(&self) -> CoreResult<HashMap<String, String>> {
        let data = match fs::read(&self.path) {
            Ok(d) => d,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                return Ok(HashMap::new());
            }
            Err(e) => {
                return Err(CoreError::Keyring(format!(
                    "read {}: {e}",
                    self.path.display()
                )))
            }
        };
        let plain = decrypt(&data, &self.key, &self.path)?;
        if plain.is_empty() {
            return Ok(HashMap::new());
        }
        serde_json::from_slice(&plain)
            .map_err(|e| CoreError::Keyring(format!("parse {}: {e}", self.path.display())))
    }

    fn save(&self, m: &HashMap<String, String>) -> CoreResult<()> {
        let plain =
            serde_json::to_vec(m).map_err(|e| CoreError::Keyring(format!("serialize: {e}")))?;
        let data = encrypt(&plain, &self.key, &self.salt)?;
        if let Some(parent) = self.path.parent() {
            fs::create_dir_all(parent)
                .map_err(|e| CoreError::Keyring(format!("mkdir {}: {e}", parent.display())))?;
        }
        fs::write(&self.path, data)
            .map_err(|e| CoreError::Keyring(format!("write {}: {e}", self.path.display())))?;
        Ok(())
    }
}

impl Keyring for FileKeyring {
    fn set(&self, key: &str, secret: &str) -> CoreResult<()> {
        let _g = self.mu.lock().unwrap();
        let mut m = self.load()?;
        m.insert(key.to_string(), secret.to_string());
        self.save(&m)
    }

    fn get(&self, key: &str) -> CoreResult<String> {
        let _g = self.mu.lock().unwrap();
        let m = self.load()?;
        m.get(key).cloned().ok_or(KeyringError::NotFound.into())
    }

    fn delete(&self, key: &str) -> CoreResult<()> {
        let _g = self.mu.lock().unwrap();
        let mut m = self.load()?;
        if m.remove(key).is_none() {
            return Ok(());
        }
        self.save(&m)
    }
}

fn derive_key(master_password: &str, salt: &[u8]) -> CoreResult<[u8; KEY_LEN]> {
    let params = Params::new(SCRYPT_LOG_N, SCRYPT_R, SCRYPT_P, KEY_LEN)
        .map_err(|e| CoreError::Keyring(format!("scrypt params: {e}")))?;
    let mut key = [0u8; KEY_LEN];
    scrypt(master_password.as_bytes(), salt, &params, &mut key)
        .map_err(|e| CoreError::Keyring(format!("scrypt: {e}")))?;
    Ok(key)
}

fn encrypt(plain: &[u8], key: &[u8; KEY_LEN], salt: &[u8; SALT_LEN]) -> CoreResult<Vec<u8>> {
    let cipher = Aes256Gcm::new(key.into());
    let mut nonce = [0u8; NONCE_LEN];
    OsRng.fill_bytes(&mut nonce);
    let ct = cipher
        .encrypt(Nonce::from_slice(&nonce), plain)
        .map_err(|_| CoreError::Keyring("aes-gcm encrypt".into()))?;
    let mut out = Vec::with_capacity(MAGIC.len() + SALT_LEN + NONCE_LEN + ct.len());
    out.extend_from_slice(MAGIC);
    out.extend_from_slice(salt);
    out.extend_from_slice(&nonce);
    out.extend_from_slice(&ct);
    Ok(out)
}

fn decrypt(data: &[u8], key: &[u8; KEY_LEN], path: &std::path::Path) -> CoreResult<Vec<u8>> {
    if data.len() < MAGIC.len() + SALT_LEN + NONCE_LEN {
        return Err(CoreError::Keyring(format!(
            "file {} is corrupt (too short)",
            path.display()
        )));
    }
    if &data[..MAGIC.len()] != MAGIC {
        return Err(CoreError::Keyring(format!(
            "file {} has wrong magic",
            path.display()
        )));
    }
    let cipher = Aes256Gcm::new(key.into());
    cipher
        .decrypt(
            Nonce::from_slice(&data[MAGIC.len() + SALT_LEN..MAGIC.len() + SALT_LEN + NONCE_LEN]),
            &data[MAGIC.len() + SALT_LEN + NONCE_LEN..],
        )
        .map_err(|_| {
            CoreError::Keyring(format!(
                "decrypt {} (wrong master password?)",
                path.display()
            ))
        })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp_dir() -> String {
        std::env::temp_dir()
            .join(format!("polydb-keyring-test-{}", uuid::Uuid::new_v4()))
            .to_string_lossy()
            .to_string()
    }

    #[test]
    fn set_get_delete() {
        let dir = tmp_dir();
        let k = FileKeyring::open(&dir, "pw").unwrap();
        k.set("a", "s1").unwrap();
        k.set("b", "s2").unwrap();
        assert_eq!(k.get("a").unwrap(), "s1");
        assert!(k.get("nope").is_err());
        k.delete("a").unwrap();
        assert!(k.get("a").is_err());
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn persists_across_reopen() {
        let dir = tmp_dir();
        {
            let k = FileKeyring::open(&dir, "pw").unwrap();
            k.set("k", "v").unwrap();
        }
        {
            let k = FileKeyring::open(&dir, "pw").unwrap();
            assert_eq!(k.get("k").unwrap(), "v");
        }
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn wrong_master_password_fails() {
        let dir = tmp_dir();
        let k = FileKeyring::open(&dir, "right").unwrap();
        k.set("k", "v").unwrap();
        assert!(FileKeyring::open(&dir, "wrong").is_err());
        let _ = fs::remove_dir_all(&dir);
    }
}
