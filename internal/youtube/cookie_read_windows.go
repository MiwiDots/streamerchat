//go:build windows

package youtube

import (
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	_ "modernc.org/sqlite"

	"golang.org/x/sys/windows"
)

// readBrowserProfileCookies reads + decrypts cookies from a Chrome-family
// browser's user-data-dir on Windows. Returns a map keyed by cookie name.
//
// Steps:
//  1. Pull os_crypt.encrypted_key from `<dir>/Local State` (a JSON file).
//  2. Strip the 5-byte "DPAPI" prefix, decrypt the rest with CryptUnprotectData.
//     The plaintext is the 32-byte AES-256 master key Chrome uses for cookie
//     value encryption (since Chrome 80).
//  3. Open `<dir>/Default/Network/Cookies` (SQLite) in read-only mode. Browser
//     may have it open via WAL — that's fine, SQLite supports concurrent
//     readers. We make a temp copy first as a safety net for older browsers
//     that hold an exclusive lock.
//  4. SELECT rows whose host_key matches .google.com or .youtube.com.
//  5. For each, decrypt the BLOB: "v10" / "v11" header + 12-byte AES-GCM
//     nonce + ciphertext + 16-byte tag.
func readBrowserProfileCookies(profileDir string) (map[string]string, error) {
	masterKey, err := readChromeMasterKey(profileDir)
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}

	// Cookies file. Newer Chrome/Edge keeps it under Default/Network/.
	candidates := []string{
		filepath.Join(profileDir, "Default", "Network", "Cookies"),
		filepath.Join(profileDir, "Default", "Cookies"),
	}
	var src string
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			src = p
			break
		}
	}
	if src == "" {
		return nil, fmt.Errorf("no Cookies file found under %s", profileDir)
	}

	// Copy to a temp path so we don't fight the browser's open handle.
	tmpCopy, err := copyToTemp(src)
	if err != nil {
		return nil, fmt.Errorf("copy cookies db: %w", err)
	}
	defer os.Remove(tmpCopy)

	// modernc/sqlite's URI form needs forward slashes even on Windows.
	dsn := "file:" + filepath.ToSlash(tmpCopy) + "?mode=ro&_pragma=journal_mode(off)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open cookies db: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT name, host_key, encrypted_value
		FROM cookies
		WHERE host_key LIKE '%.google.com'
		   OR host_key LIKE '%.youtube.com'
		   OR host_key = '.google.com'
		   OR host_key = '.youtube.com'
	`)
	if err != nil {
		return nil, fmt.Errorf("query cookies: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var name, host string
		var enc []byte
		if err := rows.Scan(&name, &host, &enc); err != nil {
			continue
		}
		plain, derr := decryptChromeValue(enc, masterKey)
		if derr != nil {
			// Skip unreadable rows (e.g. very old v11-only entries we don't
			// handle, or DPAPI fallback rows from before Chrome 80).
			continue
		}
		// Last writer wins; .google.com and .youtube.com may have separate
		// cookies with the same name and we want whichever was set later.
		out[name] = string(plain)
		_ = host
	}
	return out, nil
}

func readChromeMasterKey(profileDir string) ([]byte, error) {
	localStatePath := filepath.Join(profileDir, "Local State")
	raw, err := os.ReadFile(localStatePath)
	if err != nil {
		return nil, err
	}
	var ls struct {
		OSCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if err := json.Unmarshal(raw, &ls); err != nil {
		return nil, err
	}
	enc, err := base64.StdEncoding.DecodeString(ls.OSCrypt.EncryptedKey)
	if err != nil {
		return nil, err
	}
	if len(enc) <= 5 || string(enc[:5]) != "DPAPI" {
		return nil, fmt.Errorf("os_crypt key missing DPAPI prefix")
	}
	return dpapiUnprotect(enc[5:])
}

func dpapiUnprotect(blob []byte) ([]byte, error) {
	var input windows.DataBlob
	input.Size = uint32(len(blob))
	input.Data = &blob[0]
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, nil, 0, nil, 0, &output); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	result := make([]byte, output.Size)
	copy(result, unsafe.Slice(output.Data, int(output.Size)))
	return result, nil
}

func decryptChromeValue(blob, key []byte) ([]byte, error) {
	if len(blob) < 3+12+16 {
		return nil, fmt.Errorf("blob too short")
	}
	if !strings.HasPrefix(string(blob[:3]), "v") {
		return nil, fmt.Errorf("not an AES-GCM blob (might be legacy DPAPI)")
	}
	nonce := blob[3:15]
	ct := blob[15:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ct, nil)
}

func copyToTemp(src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp, err := os.CreateTemp("", "chathub-cookies-*.sqlite")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := io.Copy(tmp, in); err != nil {
		return "", err
	}
	return tmp.Name(), nil
}
