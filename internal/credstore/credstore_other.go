//go:build !windows

package credstore

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// On macOS and Linux the OS keyring stores comfortably-sized blobs (>= 4KB
// in practice on both Keychain and the Secret Service) so we use it directly.

func get(svc Service) (string, error) {
	v, err := keyring.Get(string(svc), defaultAcct)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	return v, nil
}

func set(svc Service, value string) error {
	return keyring.Set(string(svc), defaultAcct, value)
}

func clear(svc Service) error {
	err := keyring.Delete(string(svc), defaultAcct)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}
