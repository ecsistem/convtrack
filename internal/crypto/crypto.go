// Package crypto fornece AES-256-GCM para proteger credenciais de integração
// (Pixel ID, Access Token) em repouso no banco de dados.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
)

// masterKey retorna a chave de 32 bytes carregada de ENCRYPTION_KEY (base64).
// Panics se a chave não estiver configurada ou tiver tamanho errado.
func masterKey() ([]byte, error) {
	raw := os.Getenv("ENCRYPTION_KEY")
	if raw == "" {
		return nil, errors.New("ENCRYPTION_KEY not set")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("ENCRYPTION_KEY must be base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("ENCRYPTION_KEY must decode to 32 bytes, got %d", len(key))
	}
	return key, nil
}

// Encrypt cifra plaintext com AES-256-GCM.
// Retorna uma string base64 no formato: base64(nonce[12] + ciphertext + tag[16]).
func Encrypt(plaintext []byte) (string, error) {
	key, err := masterKey()
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("aes gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize()) // 12 bytes
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("rand nonce: %w", err)
	}

	// Seal: nonce + ciphertext + tag (GCM appends tag automaticamente)
	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt decifra uma string produzida por Encrypt.
func Decrypt(cipherB64 string) ([]byte, error) {
	key, err := masterKey()
	if err != nil {
		return nil, err
	}

	data, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm open: %w", err)
	}
	return plaintext, nil
}

// EncryptString é um wrapper conveniente para strings.
func EncryptString(s string) (string, error) {
	return Encrypt([]byte(s))
}

// DecryptString é um wrapper conveniente que retorna string.
func DecryptString(s string) (string, error) {
	b, err := Decrypt(s)
	return string(b), err
}

// GenerateKey gera uma nova chave AES-256 aleatória e retorna em base64.
// Use isto uma vez para gerar o valor de ENCRYPTION_KEY.
func GenerateKey() (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}
