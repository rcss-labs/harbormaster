package secretbox

import (
	"bytes"
	"errors"
	"testing"
)

func newBox(t *testing.T, key, purpose string) *Box {
	t.Helper()
	b, err := New(key, purpose)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBox_RoundTrip(t *testing.T) {
	b := newBox(t, "session-key", PurposeNotifySecret)
	sealed, err := b.Seal([]byte("https://hooks.example/T123"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hooks.example")) {
		t.Fatal("sealed bytes contain the plaintext")
	}
	plain, err := b.Open(sealed)
	if err != nil || string(plain) != "https://hooks.example/T123" {
		t.Fatalf("Open = %q, %v", plain, err)
	}
}

func TestBox_EmptyKeyIsErrNoKey(t *testing.T) {
	if _, err := New("", PurposeNotifySecret); !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
}

func TestBox_OtherPurposeCannotOpen(t *testing.T) {
	sealed, _ := newBox(t, "k", "purpose-a").Seal([]byte("x"))
	if _, err := newBox(t, "k", "purpose-b").Open(sealed); !errors.Is(err, ErrUndecryptable) {
		t.Fatalf("err = %v, want ErrUndecryptable", err)
	}
}

func TestBox_OtherSessionKeyCannotOpen(t *testing.T) {
	sealed, _ := newBox(t, "k1", PurposeNotifySecret).Seal([]byte("x"))
	if _, err := newBox(t, "k2", PurposeNotifySecret).Open(sealed); !errors.Is(err, ErrUndecryptable) {
		t.Fatalf("err = %v, want ErrUndecryptable", err)
	}
}

func TestBox_TamperedOrShortIsUndecryptable(t *testing.T) {
	b := newBox(t, "k", PurposeNotifySecret)
	sealed, _ := b.Seal([]byte("secret"))
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0xff
	for name, in := range map[string][]byte{"tampered": tampered, "short": sealed[:5], "nil": nil} {
		if _, err := b.Open(in); !errors.Is(err, ErrUndecryptable) {
			t.Errorf("%s: err = %v, want ErrUndecryptable", name, err)
		}
	}
}

func TestBox_SealUsesAFreshNonce(t *testing.T) {
	b := newBox(t, "k", PurposeNotifySecret)
	a, _ := b.Seal([]byte("same"))
	c, _ := b.Seal([]byte("same"))
	if bytes.Equal(a, c) {
		t.Fatal("two seals of one plaintext are identical")
	}
}
