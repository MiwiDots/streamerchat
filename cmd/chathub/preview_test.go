package main

import (
	"strings"
	"testing"
	"time"
)

func newPreviewApp() *App {
	return &App{cfg: &HubConfig{}, ytLive: map[string]ytLiveInfo{}}
}

func TestChannelURL(t *testing.T) {
	a := newPreviewApp()
	cases := map[string]string{
		"miwitv":                      "https://www.twitch.tv/miwitv",
		"yt:@DEmiwitv":                "https://www.youtube.com/@DEmiwitv",
		"yt:UCabcdefghijklmnopqrstuv": "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv",
	}
	for in, want := range cases {
		if got := a.channelURL(in); got != want {
			t.Errorf("channelURL(%q) = %q, want %q", in, got, want)
		}
	}
	a.ytLive["yt:@DEmiwitv"] = ytLiveInfo{VideoID: "abc123", Title: "Live!"}
	if got := a.channelURL("yt:@DEmiwitv"); got != "https://www.youtube.com/watch?v=abc123" {
		t.Errorf("live YouTube tab must open the stream, got %q", got)
	}
}

func TestIsWebURL(t *testing.T) {
	for _, ok := range []string{"https://www.twitch.tv/x", "http://example.com"} {
		if !isWebURL(ok) {
			t.Errorf("%q should be allowed", ok)
		}
	}
	for _, bad := range []string{"file:///C:/Windows/System32/calc.exe", "ms-settings:privacy", "javascript:alert(1)", "https://", "", "C:\\evil.exe"} {
		if isWebURL(bad) {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestGetTabPreviewYouTube(t *testing.T) {
	a := newPreviewApp()
	off := a.GetTabPreview("yt:@DEmiwitv")
	if off["live"] != false || off["platform"] != "youtube" {
		t.Fatalf("offline preview wrong: %+v", off)
	}
	a.ytLive["yt:@DEmiwitv"] = ytLiveInfo{VideoID: "vid42", Title: "Stream title", Since: time.Now().Add(-time.Hour)}
	on := a.GetTabPreview("yt:@DEmiwitv")
	thumb, _ := on["thumbnailUrl"].(string)
	if on["live"] != true || on["title"] != "Stream title" || !strings.HasPrefix(thumb, "https://i.ytimg.com/vi/vid42/mqdefault_live.jpg?t=") {
		t.Fatalf("live preview wrong: %+v", on)
	}
	if on["url"] != "https://www.youtube.com/watch?v=vid42" {
		t.Fatalf("url = %v", on["url"])
	}
}
