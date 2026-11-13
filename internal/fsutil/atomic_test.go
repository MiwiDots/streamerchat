package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFileAtomicCreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := WriteFileAtomic(path, []byte(`{"v":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte(`{"v":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"v":2}` {
		t.Fatalf("content = %s", got)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(path)
		if st.Mode().Perm() != 0600 {
			t.Fatalf("perm = %v, want 0600", st.Mode().Perm())
		}
	}
	// No temp files may be left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file %s", e.Name())
		}
	}
}

func TestWriteFileAtomicMissingDirFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "config.json")
	if err := WriteFileAtomic(path, []byte("x"), 0600); err == nil {
		t.Fatal("want error for missing directory")
	}
}

func TestBackupCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	backup, err := BackupCorrupt(path, []byte("{broken"))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(backup)
	if string(got) != "{broken" || !strings.HasPrefix(filepath.Base(backup), "config.json.corrupt-") {
		t.Fatalf("backup %s = %q", backup, got)
	}
}
