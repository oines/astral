package auth

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestPasswordHashesUseSelfDescribingPHCParameters(t *testing.T) {
	encoded, err := passwordHash("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("not a self-describing PHC hash: %q", encoded)
	}
	if !passwordMatches(encoded, "correct-horse-battery") || passwordMatches(encoded, "wrong") {
		t.Fatal("password comparison failed")
	}
	salt := []byte("different-salt-16")
	key := argon2.IDKey([]byte("correct-horse-battery"), salt, 1, 8*1024, 2, 48)
	different := fmt.Sprintf("$argon2id$v=%d$m=8192,t=1,p=2$%s$%s", argon2.Version, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
	if !passwordMatches(different, "correct-horse-battery") {
		t.Fatal("verification ignored the hash's stored parameters")
	}
}
func TestPasswordHashParserRejectsInvalidAndUnboundedCosts(t *testing.T) {
	encoded, err := passwordHash("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"argon2id$salt$hash", strings.Replace(encoded, "argon2id", "argon2i", 1), strings.Replace(encoded, "v=19", "v=16", 1), strings.Replace(encoded, "m=19456", "m=0", 1), strings.Replace(encoded, "m=19456", "m=262145", 1), strings.Replace(encoded, "m=19456", "m=4294967296", 1), strings.Replace(encoded, "t=2", "t=0", 1), strings.Replace(encoded, "t=2", "t=11", 1), strings.Replace(encoded, "p=1", "p=0", 1), strings.Replace(encoded, "p=1", "p=17", 1), strings.Replace(encoded, "m=19456,t=2,p=1", "m=19456,m=2,p=1", 1), strings.Replace(encoded, "m=19456,t=2,p=1", "m=19456,t=2,p=1,p=1", 1), encoded + "$extra", strings.Repeat("a", 513)} {
		if passwordMatches(invalid, "correct-horse-battery") {
			t.Fatalf("invalid cost/format accepted: %q", invalid)
		}
	}
}
