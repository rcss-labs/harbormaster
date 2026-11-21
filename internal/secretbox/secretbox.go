// Package secretbox seals small secrets at rest with AES-256-GCM under a key derived from HM_SESSION_KEY.
// Each box is bound to a purpose string (HKDF info and GCM associated data), so a blob sealed for one
// purpose cannot be opened for another.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

var (
	ErrNoKey         = errors.New("session key is empty")
	ErrUndecryptable = errors.New("cannot decrypt")
)

// PurposeNotifySecret binds sealed notification route secrets.
const PurposeNotifySecret = "harbormaster/notify-secret/v1"

type Box struct {
	aead    cipher.AEAD
	purpose []byte
}

// New derives the key with HKDF-SHA256(sessionKey, info=purpose).
func New(sessionKey, purpose string) (*Box, error) {
	if sessionKey == "" {
		return nil, ErrNoKey
	}
	derived := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, []byte(sessionKey), nil, []byte(purpose)), derived); err != nil {
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
	return &Box{aead: aead, purpose: []byte(purpose)}, nil
}

// Seal returns nonce || ciphertext with a fresh random nonce.
func (b *Box) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plain, b.purpose), nil
}

// Open reverses Seal. Every failure is reported as ErrUndecryptable.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) < ns+b.aead.Overhead() {
		return nil, ErrUndecryptable
	}
	plain, err := b.aead.Open(nil, sealed[:ns], sealed[ns:], b.purpose)
	if err != nil {
		return nil, ErrUndecryptable
	}
	return plain, nil
}
