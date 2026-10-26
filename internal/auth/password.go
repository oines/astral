package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Password hashes use PHC encoding so each record keeps the cost parameters
// needed for verification when the default work factor changes in the future.
// The defaults meet the OWASP Argon2id minimum: 19 MiB, two passes, one lane.
const passwordMemory uint32 = 19 * 1024
const passwordIterations uint32 = 2
const passwordParallelism uint8 = 1

func passwordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 1024 {
		return "", problem(400, "invalid", "Use a password between 12 and 1024 bytes.")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemory, passwordParallelism, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, passwordMemory, passwordIterations, passwordParallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}
func passwordMatches(encoded, password string) bool {
	if len(password) > 1024 || len(encoded) > 512 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return false
	}
	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 {
		return false
	}
	values := make([]uint64, 3)
	for i, name := range []string{"m=", "t=", "p="} {
		if !strings.HasPrefix(parameters[i], name) {
			return false
		}
		value, err := strconv.ParseUint(strings.TrimPrefix(parameters[i], name), 10, 32)
		if err != nil {
			return false
		}
		values[i] = value
	}
	memory, iterations, parallelism := values[0], values[1], values[2]
	// Reject corrupt or unreasonable costs before allocating memory or starting
	// work. Argon2's minimum memory is eight KiB per lane; cap verification at
	// 256 MiB, ten passes and sixteen lanes. No mutable database value is allowed
	// to request unbounded resources or cause Argon2 to panic.
	if parallelism < 1 || parallelism > 16 || iterations < 1 || iterations > 10 || memory < 8*parallelism || memory > 256*1024 {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[4])
	expected, e2 := base64.RawStdEncoding.DecodeString(parts[5])
	if e1 != nil || e2 != nil || len(salt) < 16 || len(salt) > 64 || len(expected) < 16 || len(expected) > 64 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, uint32(iterations), uint32(memory), uint8(parallelism), uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
func randomToken() string {
	v := make([]byte, 32)
	if _, err := rand.Read(v); err != nil {
		panic(fmt.Errorf("secure random source: %w", err))
	}
	return base64.RawURLEncoding.EncodeToString(v)
}
func digest(token string) string {
	v := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(v[:])
}
func validateUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 128 || strings.ContainsAny(username, "\r\n\x00") {
		return "", problem(400, "invalid", "Enter a username of at most 128 bytes.")
	}
	return username, nil
}
