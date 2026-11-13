package main

import "testing"

func TestCutCommand(t *testing.T) {
	cases := []struct {
		in, rest string
		ok       bool
	}{
		{"/me waves", "waves", true},
		{"/ME  waves  ", "waves", true},
		{"/me", "", true},
		{"/mention x", "", false},
		{"hello /me", "", false},
		{"/timeout x", "", false},
	}
	for _, c := range cases {
		rest, ok := cutCommand(c.in, "/me")
		if ok != c.ok || rest != c.rest {
			t.Errorf("cutCommand(%q) = %q,%v; want %q,%v", c.in, rest, ok, c.rest, c.ok)
		}
	}
}
