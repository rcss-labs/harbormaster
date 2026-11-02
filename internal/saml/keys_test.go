package saml

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func TestGenerateSPKey_SelfSignedTenYears(t *testing.T) {
	certPEM, keyPEM, err := GenerateSPKey("harbormaster.example.test", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ParseSPKey(7, certPEM, keyPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key.ID != 7 || key.RetiredAt != nil {
		t.Fatalf("unexpected SPKey fields: %+v", key)
	}
	cert := key.Cert
	if cert.Subject.CommonName != "harbormaster.example.test" {
		t.Errorf("CN = %q", cert.Subject.CommonName)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "harbormaster.example.test" {
		t.Errorf("SAN = %v", cert.DNSNames)
	}
	if got := key.Key.N.BitLen(); got != 2048 {
		t.Errorf("RSA bits = %d, want 2048", got)
	}
	if !cert.NotBefore.Equal(fixedNow) && !cert.NotBefore.Before(fixedNow) {
		t.Errorf("NotBefore %v is after now", cert.NotBefore)
	}
	if want := fixedNow.AddDate(10, 0, 0); cert.NotAfter.Sub(want).Abs() > 24*time.Hour {
		t.Errorf("NotAfter = %v, want about %v", cert.NotAfter, want)
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		t.Errorf("not self-signed: %v", err)
	}
	if pub, ok := cert.PublicKey.(*rsa.PublicKey); !ok || pub.N.Cmp(key.Key.N) != 0 {
		t.Error("certificate public key does not match private key")
	}
	if !strings.Contains(string(keyPEM), "PRIVATE KEY") {
		t.Error("key PEM has no private key block")
	}
}

func TestGenerateSPKey_RejectsEmptyHost(t *testing.T) {
	if _, _, err := GenerateSPKey("", fixedNow); err == nil {
		t.Fatal("expected error for empty host")
	}
}

func TestParseSPKey_RejectsMismatchedKey(t *testing.T) {
	certA, _, _ := GenerateSPKey("a.example.test", fixedNow)
	_, keyB, _ := GenerateSPKey("b.example.test", fixedNow)
	if _, err := ParseSPKey(1, certA, keyB, nil); err == nil {
		t.Fatal("expected error for cert/key mismatch")
	}
}

func TestFingerprint_Format(t *testing.T) {
	certPEM, _, _ := GenerateSPKey("fp.example.test", fixedNow)
	block, _ := pem.Decode([]byte(certPEM))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint(cert)
	parts := strings.Split(fp, ":")
	if len(parts) != 32 || fp != strings.ToUpper(fp) {
		t.Fatalf("fingerprint %q is not 32 colon-separated upper-hex bytes", fp)
	}
}

func TestKeyCipher_RoundTrip_WrongKeyFails(t *testing.T) {
	a, err := NewKeyCipher("session-key-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewKeyCipher("session-key-b")
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("-----BEGIN PRIVATE KEY-----secret")
	sealed, err := a.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "secret") {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := a.Open(sealed)
	if err != nil || string(got) != string(plain) {
		t.Fatalf("round trip: %q, %v", got, err)
	}
	if _, err := b.Open(sealed); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("wrong key: %v, want ErrUndecryptable", err)
	}
	if _, err := a.Open(sealed[:5]); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("truncated: %v, want ErrUndecryptable", err)
	}
	if _, err := a.Open(nil); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("nil: %v, want ErrUndecryptable", err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := a.Open(tampered); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("tampered: %v, want ErrUndecryptable", err)
	}
	if _, err := NewKeyCipher(""); !errors.Is(err, ErrNoSessionKey) {
		t.Errorf("empty key: %v, want ErrNoSessionKey", err)
	}
}

func TestKeyCipher_FreshNoncePerSeal(t *testing.T) {
	c, _ := NewKeyCipher("k")
	one, _ := c.Seal([]byte("same"))
	two, _ := c.Seal([]byte("same"))
	if string(one) == string(two) {
		t.Fatal("two seals of the same plaintext are identical (nonce reuse)")
	}
	if len(one[:12]) != 12 {
		t.Fatal("nonce prefix is not 12 bytes")
	}
}

func TestKeyCipher_BindsToPurpose(t *testing.T) {
	// A blob sealed with the same derived key but without the package's
	// associated data must not open.
	c, _ := NewKeyCipher("k")
	nonce := make([]byte, c.aead.NonceSize())
	foreign := append(nonce, c.aead.Seal(nil, nonce, []byte("x"), nil)...)
	if _, err := c.Open(foreign); !errors.Is(err, ErrUndecryptable) {
		t.Fatalf("blob sealed without AAD opened: %v", err)
	}
}
