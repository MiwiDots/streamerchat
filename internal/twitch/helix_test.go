package twitch

import (
	"strings"
	"testing"

	"github.com/nicklaw5/helix/v2"
)

func TestStatusErr(t *testing.T) {
	tests := []struct {
		name    string
		rc      helix.ResponseCommon
		wantErr string // "" = expect nil
	}{
		{"2xx ok", helix.ResponseCommon{StatusCode: 200}, ""},
		{"204 ok", helix.ResponseCommon{StatusCode: 204}, ""},
		{"401 with message", helix.ResponseCommon{StatusCode: 401, Error: "Unauthorized", ErrorMessage: "Invalid OAuth token"}, "op: 401 Invalid OAuth token"},
		{"400 already banned", helix.ResponseCommon{StatusCode: 400, ErrorMessage: "The user specified in the user_id field is already banned."}, "already banned"},
		// 5xx frequently arrives with no JSON body — StatusCode alone must fail it.
		{"500 no body", helix.ResponseCommon{StatusCode: 500}, "op: 500 request failed"},
		{"503 error only", helix.ResponseCommon{StatusCode: 503, Error: "Service Unavailable"}, "op: 503 Service Unavailable"},
		// Defensive: a message without an error status still counts as failure.
		{"message on 200", helix.ResponseCommon{StatusCode: 200, ErrorMessage: "weird"}, "weird"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := statusErr("op", tt.rc)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// TimeoutUser must refuse out-of-range durations BEFORE any request is made:
// a 0 is dropped by the library's `omitempty` and Twitch would ban permanently.
func TestTimeoutUserRejectsInvalidDuration(t *testing.T) {
	h := newOfflineHelix(t)
	for _, d := range []int{0, -5, MaxTimeoutSeconds + 1} {
		if err := h.TimeoutUser("123", d, ""); err == nil {
			t.Errorf("duration %d: want error, got nil", d)
		}
	}
}

func TestSlashTimeoutDuration(t *testing.T) {
	h := newOfflineHelix(t)
	for _, line := range []string{"/timeout troll 0", "/timeout troll 0s", "/timeout troll 0m", "/timeout troll -10", "/timeout troll 15d"} {
		status, handled := HandleSlashCommand(h, line)
		if !handled || !strings.Contains(status, "duration must be between") {
			t.Errorf("%q: want duration rejection, got handled=%v status=%q", line, handled, status)
		}
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]int{"30": 30, "30s": 30, "5m": 300, "2h": 7200, "1d": 86400}
	for in, want := range cases {
		got, err := parseDuration(in)
		if err != nil || got != want {
			t.Errorf("parseDuration(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "spamming", "m"} {
		if _, err := parseDuration(in); err == nil {
			t.Errorf("parseDuration(%q): want error", in)
		}
	}
}

// newOfflineHelix builds a client that is never expected to reach the
// network — the tests above only exercise paths that fail before a request.
func newOfflineHelix(t *testing.T) *HelixClient {
	t.Helper()
	h, err := NewHelixClient("client-id", "token", "1", "1")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestParseFollowerMinutes(t *testing.T) {
	ok := map[string]int{"0": 0, "10": 10, "10m": 10, "30s": 1, "1h": 60, "1d": 1440, "90d": 129600}
	for in, want := range ok {
		got, err := parseFollowerMinutes(in)
		if err != nil || got != want {
			t.Errorf("parseFollowerMinutes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"-1", "91d", "abc"} {
		if _, err := parseFollowerMinutes(in); err == nil {
			t.Errorf("parseFollowerMinutes(%q): want error", in)
		}
	}
}

func TestSlashSlowRange(t *testing.T) {
	h := newOfflineHelix(t)
	for _, line := range []string{"/slow 0", "/slow 2", "/slow 121", "/slow 5m", "/slow abc"} {
		status, handled := HandleSlashCommand(h, line)
		if !handled || !strings.Contains(status, "wait time must be") {
			t.Errorf("%q: want range rejection, got %q", line, status)
		}
	}
}
