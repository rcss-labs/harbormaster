package auth

import (
	"regexp"
	"strings"
	"testing"
)

func TestHashPassword_ArgonParams(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected hash prefix: %s", hash)
	}
	if ok, err := VerifyPassword(hash, "correct horse battery"); err != nil || !ok {
		t.Fatalf("verify right password = %v, %v", ok, err)
	}
	if ok, err := VerifyPassword(hash, "wrong horse battery!"); err != nil || ok {
		t.Fatalf("verify wrong password = %v, %v", ok, err)
	}
}

func TestVerifyPassword_MalformedHash(t *testing.T) {
	if ok, err := VerifyPassword("not-a-hash", "x"); ok || err == nil {
		t.Fatalf("malformed hash = %v, %v; want false and error", ok, err)
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("short"); err == nil || err.Error() != "Use at least 12 characters." {
		t.Fatalf("short password error = %v", err)
	}
	if err := ValidatePassword("twelve chars"); err != nil {
		t.Fatalf("12 chars rejected: %v", err)
	}
}

func TestGenerateTemporaryPassword_Format(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}$`)
	a, b := GenerateTemporaryPassword(), GenerateTemporaryPassword()
	if !re.MatchString(a) || !re.MatchString(b) || a == b {
		t.Fatalf("bad temporary passwords %q %q", a, b)
	}
}
