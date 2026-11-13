package applog

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupRotatesPreviousLog(t *testing.T) {
	defer log.SetOutput(os.Stderr)
	dir := t.TempDir()
	p := Setup("app", dir)
	if p != filepath.Join(dir, "app.log") {
		t.Fatalf("path = %q", p)
	}
	log.Print("first run")
	Setup("app", dir) // second start
	log.Print("second run")
	prev, _ := os.ReadFile(filepath.Join(dir, "app.prev.log"))
	cur, _ := os.ReadFile(filepath.Join(dir, "app.log"))
	if !strings.Contains(string(prev), "first run") || strings.Contains(string(cur), "first run") {
		t.Fatalf("rotation wrong:\nprev=%q\ncur=%q", prev, cur)
	}
}

func TestSetupFallsBackWhenDirUnwritable(t *testing.T) {
	defer log.SetOutput(os.Stderr)
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0644) // a FILE where a dir is expected
	good := t.TempDir()
	if p := Setup("app", filepath.Join(blocker, "sub"), good); p != filepath.Join(good, "app.log") {
		t.Fatalf("fallback not used: %q", p)
	}
}
