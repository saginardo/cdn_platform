package control

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

type Cipher struct {
	key []byte
}

func NewCipher(encodedKey string) (*Cipher, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, fmt.Errorf("decode CONTROL_ENCRYPTION_KEY: %w", err)
	}
	if len(decoded) != 32 {
		return nil, errors.New("CONTROL_ENCRYPTION_KEY must decode to exactly 32 bytes")
	}
	return &Cipher{key: decoded}, nil
}

func NewEncryptionKey() (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(key), nil
}

func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (c *Cipher) Decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext is too short")
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
}

func CertificateFingerprint(certPEM []byte) string {
	sum := sha256.Sum256(certPEM)
	return fmt.Sprintf("sha256:%x", sum[:])
}
