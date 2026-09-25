package adminauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      uint32 = 19 * 1024
	argonIterations  uint32 = 2
	argonParallelism uint8  = 1
)

var ErrWeakPassword = errors.New("admin password must be 12 to 256 bytes")

func HashPassword(password string) (string, error) {
	if len(password) < 12 || len(password) > 256 {
		return "", ErrWeakPassword
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil || memory < argonMemory || memory > 256*1024 || iterations < argonIterations || iterations > 10 || parallelism < 1 || parallelism > 4 {
		return false
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[4])
	want, hashErr := base64.RawStdEncoding.DecodeString(parts[5])
	if saltErr != nil || hashErr != nil || len(salt) < 16 || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
