package youtube

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
)

// encryptLikeChrome builds an encrypted_value blob the way Chromium does.
func encryptLikeChrome(t *testing.T, key []byte, prefix string, plain []byte) []byte {
	t.Helper()
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	return append(append([]byte(prefix), nonce...), gcm.Seal(nil, nonce, plain, nil)...)
}

func TestDecryptChromeValue(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	const host = ".google.com"
	const value = "AbCdEf123/xyz-_.ABC"

	// DB version < 24: plain value.
	got, err := decryptChromeValue(encryptLikeChrome(t, key, "v10", []byte(value)), key, host)
	if err != nil || string(got) != value {
		t.Fatalf("v10 plain: %q, %v", got, err)
	}

	// DB version >= 24: SHA-256(host_key) prefix must be stripped.
	h := sha256.Sum256([]byte(host))
	withHash := append(h[:], []byte(value)...)
	got, err = decryptChromeValue(encryptLikeChrome(t, key, "v10", withHash), key, host)
	if err != nil || string(got) != value {
		t.Fatalf("host-hash prefix not stripped: %q, %v", got, err)
	}

	// A hash of a DIFFERENT host is not stripped → binary bytes → rejected
	// instead of producing a garbage cookie.
	other := sha256.Sum256([]byte(".youtube.com"))
	if _, err := decryptChromeValue(encryptLikeChrome(t, key, "v10", append(other[:], value...)), key, host); err == nil {
		t.Fatal("garbage prefix should be rejected")
	}

	// v20 app-bound → dedicated error.
	if _, err := decryptChromeValue(encryptLikeChrome(t, key, "v20", []byte(value)), key, host); !errors.Is(err, ErrAppBoundEncryption) {
		t.Fatalf("v20: want ErrAppBoundEncryption, got %v", err)
	}

	// Wrong key → error, not garbage.
	wrong := make([]byte, 32)
	if _, err := decryptChromeValue(encryptLikeChrome(t, key, "v10", []byte(value)), wrong, host); err == nil {
		t.Fatal("wrong key must fail")
	}
}

func TestLoginCookiesValidRejectsCorruptSAPISID(t *testing.T) {
	good := LoginCookies{SAPISID: "AbC/def-123", SID: "x"}
	if !good.Valid() {
		t.Fatal("clean session rejected")
	}
	bad := good
	bad.SAPISID = "\xef\x01garbage" + good.SAPISID // pre-v0.5.7 Windows capture
	if bad.Valid() {
		t.Fatal("corrupt SAPISID must make the session invalid")
	}
}
