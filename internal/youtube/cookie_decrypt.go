package youtube

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"errors"
	"fmt"
)

// Platform-independent part of reading Chromium cookies (the DPAPI key
// unwrap and SQLite access live in cookie_read_windows.go). Kept separate
// so it can be unit-tested on any OS.

// ErrAppBoundEncryption means the browser stored the cookies with Chrome's
// "App-Bound Encryption" (v20 prefix, Chrome 127+ on Windows). Its key can
// only be unwrapped by the browser's own elevated service, so a third-party
// app cannot read these cookies at all — the login has to use a browser
// that still writes v10 cookies (Edge).
var ErrAppBoundEncryption = errors.New("browser uses app-bound cookie encryption (v20) which other apps cannot read")

// hostKeyHashLen is the size of the SHA-256(host_key) prefix Chromium puts
// in front of the plaintext since cookie DB version 24 (Chrome/Edge ~130).
const hostKeyHashLen = sha256.Size

// decryptChromeValue decrypts one `encrypted_value` blob:
// "v10"/"v11" + 12-byte nonce + AES-256-GCM ciphertext + 16-byte tag.
//
// Since cookie DB version 24 the plaintext starts with SHA-256(host_key).
// That prefix used to be kept as part of the value: the cookie then had 32
// bytes of garbage in front (the "invalid byte 'ï'" errors), and
// SAPISIDHASH was computed over the wrong value, so every YouTube send was
// rejected. The prefix is stripped only when it really is the hash of this
// row's host_key, which is safe for every DB version.
func decryptChromeValue(blob, key []byte, hostKey string) ([]byte, error) {
	if len(blob) < 3 {
		return nil, fmt.Errorf("blob too short")
	}
	switch string(blob[:3]) {
	case "v10", "v11":
	case "v20":
		return nil, ErrAppBoundEncryption
	default:
		return nil, fmt.Errorf("not an AES-GCM blob (might be legacy DPAPI)")
	}
	if len(blob) < 3+12+16 {
		return nil, fmt.Errorf("blob too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, blob[3:15], blob[15:], nil)
	if err != nil {
		return nil, err
	}
	if len(plain) >= hostKeyHashLen {
		h := sha256.Sum256([]byte(hostKey))
		if bytes.Equal(plain[:hostKeyHashLen], h[:]) {
			plain = plain[hostKeyHashLen:]
		}
	}
	if !validCookieValue(plain) {
		return nil, fmt.Errorf("decrypted value is not a valid cookie value")
	}
	return plain, nil
}

// validCookieValue reports whether v consists only of RFC 6265
// cookie-octets (what net/http accepts in a Cookie header).
func validCookieValue(v []byte) bool {
	for _, c := range v {
		if c < 0x21 || c > 0x7E || c == '"' || c == ',' || c == ';' || c == '\\' {
			return false
		}
	}
	return true
}
