// Package crypto 提供上游凭据的应用层信封加密（AES-256-GCM）。
//
// 密钥通过配置 security.encryption_key（base64 编码的 32 字节）提供；
// 未配置密钥时以明文透传（仅限本地开发），生产环境启动时会输出安全告警。
// 兼容历史明文数据：非 "enc:v1:" 前缀的存储值原样返回。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// prefix 密文前缀（同时标记格式版本 v1）
	prefix = "enc:v1:"
	// KeySize AES-256 密钥长度
	KeySize = 32
)

// DecodeKey 解析 base64 编码的 32 字节 AES 密钥。
func DecodeKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, errors.New("empty encryption key")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decrypt encryption_key: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("encryption_key must decode to exactly %d bytes, got %d", KeySize, len(key))
	}
	return key, nil
}

// Encrypt 加密明文。明文为空或未配置密钥时原样返回（空字符串不加密）。
func Encrypt(plain, encodedKey string) (string, error) {
	if plain == "" || encodedKey == "" {
		return plain, nil
	}
	key, err := DecodeKey(encodedKey)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt 解密存储值。未加密（无前缀）或未配置密钥时原样返回，
// 保证历史明文数据在未启用加密的部署上不受影响。
func Decrypt(stored, encodedKey string) (string, error) {
	if !strings.HasPrefix(stored, prefix) {
		return stored, nil
	}
	if encodedKey == "" {
		return "", errors.New("encrypted credential found but no encryption_key configured")
	}
	key, err := DecodeKey(encodedKey)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, prefix))
	if err != nil {
		return "", fmt.Errorf("decrypt credential: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("decrypt credential: ciphertext too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt credential: %w", err)
	}
	return string(plain), nil
}

// MaskSuffix 返回脱敏尾缀：只显示末尾 4 个字符（不足 8 位全打码）。
func MaskSuffix(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	return "****" + s[len(s)-4:]
}
