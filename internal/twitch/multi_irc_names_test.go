package twitch

import (
	"reflect"
	"testing"
)

func TestRealChattersDropsAnonymousConnections(t *testing.T) {
	got := realChatters([]string{"alice", "justinfan48213", "", "JustinFan9", "bob"})
	want := []string{"alice", "bob"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if isAnonymousLogin("justin") || !isAnonymousLogin("justinfan1") {
		t.Error("isAnonymousLogin wrong")
	}
}
