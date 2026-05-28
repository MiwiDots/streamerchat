//go:build windows

package credstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows Credential Manager caps each entry at 2560 bytes for the
// CredentialBlob field, which is too tight for a full YouTube session
// (Secure-3PSID alone can be 200-400 bytes; the whole cookie set easily
// goes past 4KB). Instead we encrypt the blob ourselves with DPAPI
// (CryptProtectData, scoped to the current Windows user) and write it
// to %APPDATA%\chathub\creds\<service>.bin. Same cryptographic story as
// Credential Manager — that also uses DPAPI internally — but without
// the size limit and we control the on-disk layout.

func credsDir() (string, error) {
	base := os.Getenv("APPDATA")
	if base == "" {
		// Fall back to LOCALAPPDATA which is always set on supported
		// Windows versions; this path matters for things like Wine.
		base = os.Getenv("LOCALAPPDATA")
	}
	if base == "" {
		return "", fmt.Errorf("APPDATA / LOCALAPPDATA not set")
	}
	dir := filepath.Join(base, "chathub", "creds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func filePath(svc Service) (string, error) {
	dir, err := credsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, string(svc)+".bin"), nil
}

func dpapiProtect(plain []byte) ([]byte, error) {
	var in windows.DataBlob
	in.Size = uint32(len(plain))
	if len(plain) > 0 {
		in.Data = &plain[0]
	}
	var out windows.DataBlob
	// flags 0 = current-user scope (re-readable only on the same Windows
	// account, on this machine).
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	cipher := make([]byte, out.Size)
	copy(cipher, unsafe.Slice(out.Data, int(out.Size)))
	return cipher, nil
}

func dpapiUnprotect(cipher []byte) ([]byte, error) {
	var in windows.DataBlob
	in.Size = uint32(len(cipher))
	if len(cipher) > 0 {
		in.Data = &cipher[0]
	}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	plain := make([]byte, out.Size)
	copy(plain, unsafe.Slice(out.Data, int(out.Size)))
	return plain, nil
}

func get(svc Service) (string, error) {
	path, err := filePath(svc)
	if err != nil {
		return "", err
	}
	cipher, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}
	plain, err := dpapiUnprotect(cipher)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func set(svc Service, value string) error {
	path, err := filePath(svc)
	if err != nil {
		return err
	}
	cipher, err := dpapiProtect([]byte(value))
	if err != nil {
		return err
	}
	// Write through a temp file + rename so a crash mid-write can't
	// leave a half-written ciphertext that fails to decrypt on next read.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, cipher, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func clear(svc Service) error {
	path, err := filePath(svc)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
