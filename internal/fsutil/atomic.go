// Package fsutil holds small filesystem helpers shared by both apps.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteFileAtomic replaces path with data so that readers (and the next app
// start after a crash or power loss) see either the complete old file or the
// complete new one, never a truncated mix.
//
// How: write into a temp file in the SAME directory (rename is only atomic
// within one filesystem), fsync it, then rename it over the target. On
// Windows os.Rename uses MoveFileEx(MOVEFILE_REPLACE_EXISTING), so replacing
// an existing file works there too.
//
// perm is applied to the temp file before the rename, so the final file
// never exists with looser permissions, even briefly.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Any failure below must not leave a stray temp file behind.
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if err = tmp.Chmod(perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	return nil
}

// BackupCorrupt copies an unreadable file aside as
// "<path>.corrupt-<timestamp>" so the user's data survives even if the app
// later saves a fresh default config over the original. Returns the backup
// path. The copy keeps 0600 because config files can contain tokens.
func BackupCorrupt(path string, data []byte) (string, error) {
	backup := fmt.Sprintf("%s.corrupt-%s", path, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(backup, data, 0600); err != nil {
		return "", fmt.Errorf("backup corrupt file: %w", err)
	}
	return backup, nil
}
