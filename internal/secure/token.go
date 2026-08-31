package secure

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

func GenerateToken(size int) (plain, hash string, err error) {
	raw := make([]byte, size)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	plain = base64.RawURLEncoding.EncodeToString(raw)
	return plain, HashToken(plain), nil
}

func HashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func GenerateKey() ([]byte, error) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	return key, err
}
