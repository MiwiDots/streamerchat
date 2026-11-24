package platform

import "testing"

func TestIsWebURL(t *testing.T) {
	for _, ok := range []string{"https://www.twitch.tv/x", "http://example.com/a?b=c"} {
		if !IsWebURL(ok) {
			t.Errorf("%q must be allowed", ok)
		}
	}
	for _, bad := range []string{"file:///C:/Windows/system32/calc.exe", "ms-settings:", "javascript:alert(1)", "https://", "/relative", ""} {
		if IsWebURL(bad) {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestIsTwitchLogin(t *testing.T) {
	for _, ok := range []string{"miwitv", "a", "user_name_123", "abcdefghijklmnopqrstuvwxy"} {
		if !IsTwitchLogin(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "Upper", "has space", "x&login=y", "abcdefghijklmnopqrstuvwxyz", "@handle"} {
		if IsTwitchLogin(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}
