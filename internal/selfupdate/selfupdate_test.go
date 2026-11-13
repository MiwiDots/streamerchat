package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current, latest string
		want            bool
	}{
		{"0.5.0", "v0.5.1", true},
		{"0.5.1", "v0.5.0", false},
		{"0.5.1", "v0.5.1", false},
		{"0.9.0", "v0.10.0", true}, // numeric, not string, compare
		// Pre-release handling — the old parser treated these as equal.
		{"0.3.9-alpha", "v0.3.9", true},
		{"0.3.9", "v0.3.9-alpha", false},
		{"0.5.1-beta.1", "v0.5.1-beta.2", true},
		{"0.5.1-beta.2", "v0.5.1-beta.10", true}, // numeric identifier
		{"0.5.1-alpha", "v0.5.1-beta", true},
		{"0.5.1-beta", "v0.5.1-beta.1", true}, // longer set ranks higher
		{"0.5.1-1", "v0.5.1-alpha", true},     // numeric < alphanumeric
		{"0.5.1+build7", "v0.5.1", false},     // build metadata ignored
		// Garbage must never trigger an install.
		{"0.5.0", "nightly", false},
		{"0.5.0", "v1.2", false},
		{"0.5.0", "v1.2.3-", false},
		{"bogus", "v9.9.9", false},
	}
	for _, tt := range tests {
		if got := IsNewer(tt.current, tt.latest); got != tt.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

const validDigest = "sha256:2b18021baba5f705f136b10c90b6e29407f8fea122e2a621cdb4bf5155c89524"

func TestValidateAsset(t *testing.T) {
	good := Asset{
		Name:        "chathub.exe",
		DownloadURL: "https://github.com/MiwiDots/streamerchat/releases/download/v0.5.0/chathub.exe",
		Size:        100,
		Digest:      validDigest,
	}
	if _, err := validateAsset("MiwiDots", "streamerchat", &good); err != nil {
		t.Fatalf("good asset rejected: %v", err)
	}

	bad := map[string]func(a *Asset){
		"other host":       func(a *Asset) { a.DownloadURL = "https://evil.example/chathub.exe" },
		"other repo":       func(a *Asset) { a.DownloadURL = "https://github.com/someone/else/releases/download/v1/chathub.exe" },
		"http":             func(a *Asset) { a.DownloadURL = strings.Replace(a.DownloadURL, "https", "http", 1) },
		"prefix trick":     func(a *Asset) { a.DownloadURL = "https://github.com/MiwiDots/streamerchat.evil/releases/download/x" },
		"no digest":        func(a *Asset) { a.Digest = "" },
		"wrong algo":       func(a *Asset) { a.Digest = "md5:abc" },
		"malformed digest": func(a *Asset) { a.Digest = "sha256:zz" },
		"no size":          func(a *Asset) { a.Size = 0 },
	}
	for name, mutate := range bad {
		a := good
		mutate(&a)
		if _, err := validateAsset("MiwiDots", "streamerchat", &a); err == nil {
			t.Errorf("%s: want rejection", name)
		}
	}
	if _, err := validateAsset("o", "r", nil); err == nil {
		t.Error("nil asset: want rejection")
	}
}

func TestDownloadVerifiesSizeAndHash(t *testing.T) {
	payload := []byte("fake exe contents")
	sum := sha256.Sum256(payload)
	wantHash := hex.EncodeToString(sum[:])

	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "app.exe.new")

	body = payload
	if err := download(srv.URL, dst, int64(len(payload)), wantHash); err != nil {
		t.Fatalf("valid download failed: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != string(payload) {
		t.Fatalf("written content = %q", got)
	}

	body = []byte("tampered exe contents")[:len(payload)] // same size, different bytes
	if err := download(srv.URL, dst, int64(len(payload)), wantHash); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered download: want checksum error, got %v", err)
	}

	body = payload[:5] // truncated
	if err := download(srv.URL, dst, int64(len(payload)), wantHash); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("truncated download: want size error, got %v", err)
	}

	body = append(append([]byte{}, payload...), 'X') // oversized
	if err := download(srv.URL, dst, int64(len(payload)), wantHash); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("oversized download: want size error, got %v", err)
	}
}
