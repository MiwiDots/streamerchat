package twitch

import (
	"reflect"
	"testing"

	"github.com/miwi/streamerchat/internal/chat"
)

func TestBadgesFromTagKeepsOrderAndPredictionVersions(t *testing.T) {
	got := badgesFromTag("broadcaster/1,subscriber/3012,predictions/blue-1", map[string]int{"predictions": 0})
	want := []chat.Badge{{Name: "broadcaster", Version: "1"}, {Name: "subscriber", Version: "3012"}, {Name: "predictions", Version: "blue-1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// Without the raw tag: parsed map, in a stable (sorted) order.
	for i := 0; i < 20; i++ {
		got = badgesFromTag("", map[string]int{"vip": 1, "moderator": 1, "bits": 100})
		if got[0].Name != "bits" || got[1].Name != "moderator" || got[2].Name != "vip" {
			t.Fatalf("fallback order not stable: %+v", got)
		}
	}
	if got := badgesFromTag("bad,/1,ok/2", nil); len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("malformed entries must be skipped, got %+v", got)
	}
}
