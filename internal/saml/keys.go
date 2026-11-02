package saml

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
)

var (
	ErrNoSessionKey  = errors.New("session key is empty")
	ErrUndecryptable = errors.New("cannot decrypt SP key")
)

// keyInfo is both the HKDF info string and the GCM associated data, so a blob
// sealed for another purpose under the same derived key does not open here.
const keyInfo = "harbormaster/saml-sp-key/v1"

const spKeyValidityYears = 10

// GenerateSPKey creates an RSA-2048 key and a self-signed certificate for host
// valid for ten years from now. The key is returned as PKCS#8 PEM.
func GenerateSPKey(host string, now time.Time) (certPEM string, keyPEM []byte, err error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", nil, errors.New("host is empty")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", nil, fmt.Errorf("generate RSA key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 127)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return "", nil, fmt.Errorf("generate serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		DNSNames:              []string{host},
		NotBefore:             now.Add(-5 * time.Minute).UTC(),
		NotAfter:              now.AddDate(spKeyValidityYears, 0, 0).UTC(),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", nil, fmt.Errorf("marshal private key: %w", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// KeyCipher encrypts SP private keys at rest with AES-256-GCM under a key
// derived from HM_SESSION_KEY.
type KeyCipher struct{ aead cipher.AEAD }

func NewKeyCipher(sessionKey string) (*KeyCipher, error) {
	if sessionKey == "" {
		return nil, ErrNoSessionKey
	}
	derived := make([]byte, 32)
	r := hkdf.New(sha256.New, []byte(sessionKey), nil, []byte(keyInfo))
	if _, err := io.ReadFull(r, derived); err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &KeyCipher{aead: aead}, nil
}

// Seal returns nonce || ciphertext with a fresh random 12-byte nonce.
func (c *KeyCipher) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, plain, []byte(keyInfo)), nil
}

// Open reverses Seal. Every failure is reported as ErrUndecryptable.
func (c *KeyCipher) Open(sealed []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	if len(sealed) < ns+c.aead.Overhead() {
		return nil, ErrUndecryptable
	}
	plain, err := c.aead.Open(nil, sealed[:ns], sealed[ns:], []byte(keyInfo))
	if err != nil {
		return nil, ErrUndecryptable
	}
	return plain, nil
}

// SPKey is a parsed service-provider key pair. RetiredAt is nil for the active key.
type SPKey struct {
	ID        int64
	Cert      *x509.Certificate
	Key       *rsa.PrivateKey
	RetiredAt *time.Time
}

func ParseSPKey(id int64, certPEM string, keyPEM []byte, retiredAt *time.Time) (SPKey, error) {
	cb, _ := pem.Decode([]byte(certPEM))
	if cb == nil || cb.Type != "CERTIFICATE" {
		return SPKey{}, errors.New("SP certificate is not valid PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return SPKey{}, errors.New("SP certificate cannot be parsed")
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return SPKey{}, errors.New("SP private key is not valid PEM")
	}
	var key *rsa.PrivateKey
	switch kb.Type {
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
		if err != nil {
			return SPKey{}, errors.New("SP private key cannot be parsed")
		}
		var ok bool
		if key, ok = parsed.(*rsa.PrivateKey); !ok {
			return SPKey{}, errors.New("SP private key is not RSA")
		}
	case "RSA PRIVATE KEY":
		if key, err = x509.ParsePKCS1PrivateKey(kb.Bytes); err != nil {
			return SPKey{}, errors.New("SP private key cannot be parsed")
		}
	default:
		return SPKey{}, errors.New("SP private key has an unsupported PEM type")
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || pub.N.Cmp(key.N) != 0 || pub.E != key.E {
		return SPKey{}, errors.New("SP certificate does not match the private key")
	}
	return SPKey{ID: id, Cert: cert, Key: key, RetiredAt: retiredAt}, nil
}

// Fingerprint is the SHA-256 of the DER certificate as upper-case,
// colon-separated hex.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}
