package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/miwi/streamerchat/internal/config"
)

func TestParseIgnoredUsers(t *testing.T) {
	got, err := parseIgnoredUsers(" Nightbot, @StreamElements;stammtischbot\nnightbot  ")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nightbot", "streamelements", "stammtischbot"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, _ := parseIgnoredUsers("  ,  "); got != nil {
		t.Fatalf("blank input must mean defaults (nil), got %v", got)
	}
	if _, err := parseIgnoredUsers(manyNames(config.MaxStatsIgnoredUsers + 1)); err == nil {
		t.Fatal("more than the max must be rejected")
	}
}

func manyNames(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "user%d,", i)
	}
	return b.String()
}
