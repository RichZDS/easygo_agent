// Package credential 负责用户供应商凭证的静态加密存储。
// 加密密钥由部署环境注入，不来自 MySQL 或应用 YAML 配置。
package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

const (
	AlgorithmAES256GCM = "AES-256-GCM" // 当前唯一支持的加密算法
	NonceSize          = 12            // GCM 标准 nonce 长度（字节）
	KeySize            = 32            // AES-256 密钥长度（字节）
)

// Ciphertext 是凭证在数据库中的安全存储形态。
// Ciphertext 与 Nonce 使用 base64 编码，避免明文或二进制泄漏到日志与 JSON 响应。
type Ciphertext struct {
	Ciphertext string // base64 编码的密文
	Nonce      string // base64 编码的随机 nonce
	Algorithm  string // 加密算法标识，用于密钥轮换时的解密路由
	KeyVersion string // 密钥版本，与部署环境注入的 KEK 版本对应
}

// Cipher 持有一个版本化的 32 字节 KEK（Key Encryption Key）。
// 进程启动时创建，不参与序列化或持久化。
type Cipher struct {
	aead       cipher.AEAD
	keyVersion string
}

// AssociatedData 构造 GCM 关联数据，将密文绑定到 user + provider 上下文。
// 防止数据库行被复制到其他用户或供应商凭证下解密。
func AssociatedData(userID uint64, provider string) []byte {
	return []byte(fmt.Sprintf("user:%d:provider:%s", userID, provider))
}

// NewFromBase64 从 base64 编码的 32 字节密钥创建 Cipher。
func NewFromBase64(encodedKey, keyVersion string) (*Cipher, error) {
	if keyVersion == "" {
		return nil, fmt.Errorf("credential key version is required")
	}
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, fmt.Errorf("decode credential key: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("credential key must decode to %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	if aead.NonceSize() != NonceSize {
		return nil, fmt.Errorf("unexpected GCM nonce size %d", aead.NonceSize())
	}
	return &Cipher{aead: aead, keyVersion: keyVersion}, nil
}

// Encrypt 使用 AES-256-GCM 加密明文，associatedData 参与认证标签计算。
func (c *Cipher) Encrypt(plaintext string, associatedData []byte) (Ciphertext, error) {
	if plaintext == "" {
		return Ciphertext{}, fmt.Errorf("credential must not be empty")
	}
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Ciphertext{}, fmt.Errorf("generate credential nonce: %w", err)
	}
	sealed := c.aead.Seal(nil, nonce, []byte(plaintext), associatedData)
	return Ciphertext{
		Ciphertext: base64.StdEncoding.EncodeToString(sealed),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Algorithm:  AlgorithmAES256GCM,
		KeyVersion: c.keyVersion,
	}, nil
}

// Decrypt 解密 Ciphertext 并校验 associatedData；算法或 nonce 不匹配时返回错误。
func (c *Cipher) Decrypt(value Ciphertext, associatedData []byte) (string, error) {
	if value.Algorithm != AlgorithmAES256GCM {
		return "", fmt.Errorf("unsupported credential algorithm %q", value.Algorithm)
	}
	nonce, err := base64.StdEncoding.DecodeString(value.Nonce)
	if err != nil || len(nonce) != NonceSize {
		return "", fmt.Errorf("invalid credential nonce")
	}
	sealed, err := base64.StdEncoding.DecodeString(value.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode credential ciphertext: %w", err)
	}
	plaintext, err := c.aead.Open(nil, nonce, sealed, associatedData)
	if err != nil {
		return "", fmt.Errorf("decrypt credential: %w", err)
	}
	return string(plaintext), nil
}
