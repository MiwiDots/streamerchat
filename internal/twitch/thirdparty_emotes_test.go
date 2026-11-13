package twitch

import "testing"

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
