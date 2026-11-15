package twitch

import (
	"strings"
	"testing"
)

func TestParseChoiceCommand(t *testing.T) {
	title, opts, secs, err := parseChoiceCommand("Which map? | Ascent | Bind | 2m", pollDefaultDuration)
	if err != nil || title != "Which map?" || strings.Join(opts, ",") != "Ascent,Bind" || secs != 120 {
		t.Fatalf("got %q %v %d %v", title, opts, secs, err)
	}
	// A bare number at the end is an option, not a duration.
	_, opts, secs, err = parseChoiceCommand("Best year? | 2019 | 2020 | 2021", pollDefaultDuration)
	if err != nil || strings.Join(opts, ",") != "2019,2020,2021" || secs != pollDefaultDuration {
		t.Fatalf("numeric options: opts=%v secs=%d err=%v", opts, secs, err)
	}
	// Option text that merely ends in a letter isn't a duration either.
	_, opts, _, _ = parseChoiceCommand("Q | Team A | 5em", 60)
	if strings.Join(opts, ",") != "Team A,5em" {
		t.Fatalf("5em must stay an option: %v", opts)
	}
	for _, bad := range []string{"", "only question", "Q | one", "| A | B"} {
		if _, _, _, err := parseChoiceCommand(bad, 60); err == nil {
			t.Errorf("parseChoiceCommand(%q): want error", bad)
		}
	}
}

func TestValidateChoices(t *testing.T) {
	if err := validateChoices("poll", "Q?", pollTitleMax, []string{"a", "b"}, pollChoicesMin, pollChoicesMax); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"too few":      {"a"},
		"too many":     {"a", "b", "c", "d", "e", "f"},
		"long option":  {"a", strings.Repeat("x", choiceTitleMax+1)},
		"empty option": {"a", ""},
	}
	for name, opts := range cases {
		if err := validateChoices("poll", "Q?", pollTitleMax, opts, pollChoicesMin, pollChoicesMax); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if err := validateChoices("poll", strings.Repeat("q", pollTitleMax+1), pollTitleMax, []string{"a", "b"}, 2, 5); err == nil {
		t.Error("long title: want error")
	}
}

func TestBroadcasterCommandValidationBeforeAPI(t *testing.T) {
	h := newOfflineHelix(t)
	for line, want := range map[string]string{
		"/warn":                 "/warn <user> <reason>",
		"/banid abc":            "must be numeric",
		"/poll just a question": "needs a question",
		"/prediction Q | only":  "needs a question",
		"/resolveprediction x":  "number of the winning outcome",
		"/settitle":             "/settitle <title>",
		"/setgame":              "/setgame <category>",
	} {
		status, handled := HandleSlashCommand(h, line)
		if !handled || !strings.Contains(status, want) {
			t.Errorf("%q → %q, want it to contain %q", line, status, want)
		}
	}
}
