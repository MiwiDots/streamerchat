package twitch

import (
	"strings"
	"testing"
)

func TestEmotesAreScopedPerChannel(t *testing.T) {
	tpe := NewThirdPartyEmotes()
	tpe.global = emoteSet{"EZ": {Name: "EZ", ID: "global-ez"}, "KEKW": {Name: "KEKW", ID: "g-kekw"}}
	tpe.channels["A"] = emoteSet{"EZ": {Name: "EZ", ID: "a-ez"}, "aOnly": {Name: "aOnly", ID: "a1"}}
	tpe.channels["B"] = emoteSet{}

	if info, _ := tpe.LookupIn("A", "EZ"); info.ID != "a-ez" {
		t.Errorf("channel emote must win over global, got %q", info.ID)
	}
	if info, _ := tpe.LookupIn("B", "EZ"); info.ID != "global-ez" {
		t.Errorf("channel B must fall back to global, got %q", info.ID)
	}
	if _, ok := tpe.LookupIn("B", "aOnly"); ok {
		t.Error("channel A emote leaked into channel B")
	}
	if _, ok := tpe.Lookup("aOnly"); ok {
		t.Error("channel emote visible in global lookup")
	}
	if !tpe.HasChannel("A") || tpe.HasChannel("C") {
		t.Error("HasChannel wrong")
	}
}

func TestLoadAllFixedPrecedence(t *testing.T) {
	ffz := func() emoteSet { return emoteSet{"X": {ID: "ffz"}, "F": {ID: "ffz"}} }
	bttv := func() emoteSet { return emoteSet{"X": {ID: "bttv"}, "B": {ID: "bttv"}} }
	stv := func() emoteSet { return emoteSet{"X": {ID: "7tv"}} }
	for i := 0; i < 20; i++ { // goroutine scheduling must not change the winner
		got := loadAll(ffz, bttv, stv)
		if got["X"].ID != "7tv" || got["F"].ID != "ffz" || got["B"].ID != "bttv" {
			t.Fatalf("precedence broken: %+v", got)
		}
	}
}

func TestEmoteInfoURL(t *testing.T) {
	cases := map[EmoteInfo]string{
		{Provider: EmoteProvider7TV, ID: "abc"}:                 "https://cdn.7tv.app/emote/abc/2x.webp",
		{Provider: EmoteProviderBTTV, ID: "b1", Animated: true}: "https://cdn.betterttv.net/emote/b1/2x.gif",
		{Provider: EmoteProviderBTTV, ID: "b2"}:                 "https://cdn.betterttv.net/emote/b2/2x.png",
		{Provider: EmoteProviderFFZ, ID: "9"}:                   "https://cdn.frankerfacez.com/emote/9/2",
	}
	for in, want := range cases {
		if got := in.URL(); got != want {
			t.Errorf("%+v → %q, want %q", in, got, want)
		}
	}
}

func TestPickerSections(t *testing.T) {
	tpe := NewThirdPartyEmotes()
	tpe.global = emoteSet{
		"EZ":    {Name: "EZ", Provider: EmoteProvider7TV, ID: "g-ez"},
		"zeta":  {Name: "zeta", Provider: EmoteProviderBTTV, ID: "g-z"},
		"Alpha": {Name: "Alpha", Provider: EmoteProviderBTTV, ID: "g-a"},
	}
	tpe.channels["42"] = emoteSet{"EZ": {Name: "EZ", Provider: EmoteProviderFFZ, ID: "c-ez"}}
	got := tpe.PickerSections("42")
	var desc []string
	for _, s := range got {
		names := []string{}
		for _, e := range s.Emotes {
			names = append(names, e.Name)
		}
		desc = append(desc, s.Source+"/"+s.Scope+":"+strings.Join(names, ","))
	}
	// Channel FFZ "EZ" overrides the global 7TV "EZ" (not listed twice);
	// globals sorted case-insensitively and grouped by provider.
	want := "ffz/channel:EZ|bttv/global:Alpha,zeta"
	if strings.Join(desc, "|") != want {
		t.Fatalf("got %q, want %q", strings.Join(desc, "|"), want)
	}
}

func TestBuildEmotePickerWithoutLogin(t *testing.T) {
	tpe := NewThirdPartyEmotes()
	tpe.global = emoteSet{"KEKW": {Name: "KEKW", Provider: EmoteProvider7TV, ID: "k"}}
	got := BuildEmotePicker(NewTwitchEmoteCache(), tpe, "", "", "42")
	if len(got) != 1 || got[0].Source != "7tv" {
		t.Fatalf("without a login only third-party emotes are listed: %+v", got)
	}
}
