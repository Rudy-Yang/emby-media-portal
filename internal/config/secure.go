package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func HashAdminPassword(password string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

func VerifyAdminPassword(cfg *Config, password string) bool {
	if cfg == nil {
		return false
	}
	switch {
	case strings.TrimSpace(cfg.Server.AdminPasswordHash) != "":
		return bcrypt.CompareHashAndPassword([]byte(cfg.Server.AdminPasswordHash), []byte(password)) == nil
	case strings.TrimSpace(cfg.Server.AdminPassword) != "":
		return password == cfg.Server.AdminPassword
	default:
		return false
	}
}

func HasConfiguredAdminPassword(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	return strings.TrimSpace(cfg.Server.AdminPasswordHash) != "" || strings.TrimSpace(cfg.Server.AdminPassword) != ""
}

func encryptAPIKey(cfgPath, plaintext string) (string, error) {
	if strings.TrimSpace(plaintext) == "" {
		return "", nil
	}
	key, err := loadOrCreateSecretKey(cfgPath)
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

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decryptAPIKey(cfgPath, encoded string) (string, error) {
	if strings.TrimSpace(encoded) == "" {
		return "", nil
	}
	key, err := loadOrCreateSecretKey(cfgPath)
	if err != nil {
		return "", err
	}

	raw, err := base64.StdEncoding.DecodeString(encoded)
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
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted api key")
	}

	nonce := raw[:gcm.NonceSize()]
	ciphertext := raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func loadOrCreateSecretKey(cfgPath string) ([]byte, error) {
	keyPath := secretKeyPath(cfgPath)
	if data, err := os.ReadFile(keyPath); err == nil {
		return base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key)), 0600); err != nil {
		return nil, err
	}
	return key, nil
}

func secretKeyPath(cfgPath string) string {
	baseDir := filepath.Dir(cfgPath)
	return filepath.Join(baseDir, "data", ".secret.key")
}
