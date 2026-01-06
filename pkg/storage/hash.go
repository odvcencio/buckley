package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func hashSecret(secret string) string {
	trimmed := strings.TrimSpace(secret)
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}
