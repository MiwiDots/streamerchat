package platform

import "testing"

func TestStartupApprovedDisabled(t *testing.T) {
	for _, c := range []struct {
		v    []byte
		want bool
	}{
		{nil, false},
		{[]byte{0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, false},
		{[]byte{0x06, 0, 0}, false},
		{[]byte{0x03, 0x12, 0x34}, true},
		{[]byte{0x07}, true},
	} {
		if got := startupApprovedDisabled(c.v); got != c.want {
			t.Errorf("startupApprovedDisabled(% x) = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestCommandExeAndSamePath(t *testing.T) {
	exe := `C:\Tools\StreamerChat\streamerchat-gui.exe`
	if got := commandExe(quoteCommand(exe)); got != exe {
		t.Fatalf("round trip = %q", got)
	}
	if got := commandExe(`"C:\a b\x.exe" --flag`); got != `C:\a b\x.exe` {
		t.Fatalf("args after the quoted path must be dropped, got %q", got)
	}
	if got := commandExe(` C:\x.exe `); got != `C:\x.exe` {
		t.Fatalf("unquoted = %q", got)
	}
	if !samePath(`c:/tools/streamerchat/STREAMERCHAT-GUI.EXE`, exe) {
		t.Fatal("paths differing only in case/slashes must match")
	}
	if samePath(`C:\Users\me\Downloads\streamerchat-gui.exe`, exe) {
		t.Fatal("a moved exe must not match")
	}
}

func TestIsTempPath(t *testing.T) {
	if !isTempPath(`C:\Users\me\AppData\Local\Temp\Rar$EX01\chathub.exe`) {
		t.Fatal("exe started from a zip in Temp must be refused")
	}
	if isTempPath(`C:\Users\me\Desktop\chathub.exe`) {
		t.Fatal("normal folder must be allowed")
	}
}
