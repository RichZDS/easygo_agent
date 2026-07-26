// Package credential encrypts user-supplied provider credentials at rest.
// The key is deliberately supplied by the deployment environment, never by
// MySQL or application YAML.
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
	AlgorithmAES256GCM = "AES-256-GCM"
	NonceSize          = 12
	KeySize            = 32
)

// Ciphertext is the database-safe representation of an encrypted credential.
// Ciphertext and Nonce are base64 so they can be transported without leaking
// the plaintext into logs or JSON responses.
type Ciphertext struct {
	Ciphertext string
	Nonce      string
	Algorithm  string
	KeyVersion string
}

// Cipher owns one versioned 32-byte KEK. A Cipher is intentionally created at
// process startup and is never serialized.
type Cipher struct {
	aead       cipher.AEAD
	keyVersion string
}

func AssociatedData(userID uint64, provider string) []byte {
	return []byte(fmt.Sprintf("user:%d:provider:%s", userID, provider))
}

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

// Encrypt binds ciphertext to its user, credential and provider. This prevents
// a copied database row from being decrypted in another credential's context.
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
