package twitch

import (
	"fmt"
	"strconv"
	"strings"
)

// handleBroadcasterCommand handles the broadcaster / moderator tools:
// warnings, ban by id, shield mode, polls, predictions, clips, markers,
// title and category. line is the full input (needed for the free-text
// arguments of /poll, /prediction, /settitle, /setgame, /marker, /warn).
// Returns ok=false for commands it doesn't know.
func handleBroadcasterCommand(h *HelixClient, cmd string, args []string, line string) (string, bool) {
	rest := commandRest(line)
	switch cmd {
	case "warn":
		if len(args) < 2 {
			return "/warn <user> <reason>", true
		}
		login := strings.TrimPrefix(args[0], "@")
		userID, err := h.ResolveLogin(login)
		if err != nil {
			return fmt.Sprintf("/warn: %v", err), true
		}
		reason := strings.TrimSpace(strings.TrimPrefix(rest, args[0]))
		if err := h.WarnUser(userID, reason); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("%s has been warned: %s", login, reason), true

	case "banid":
		if len(args) < 1 {
			return "/banid <user-id> [reason]", true
		}
		if _, err := strconv.ParseUint(args[0], 10, 64); err != nil {
			return "/banid: the user id must be numeric", true
		}
		if err := h.BanUser(args[0], strings.Join(args[1:], " ")); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("User id %s has been banned.", args[0]), true

	case "shield", "shieldoff":
		on := cmd == "shield"
		if err := h.SetShieldMode(on); err != nil {
			return err.Error(), true
		}
		if on {
			return "Shield mode on.", true
		}
		return "Shield mode off.", true

	case "poll":
		title, options, secs, err := parseChoiceCommand(rest, pollDefaultDuration)
		if err != nil {
			return "/poll " + err.Error() + " — e.g. /poll Which map? | Ascent | Bind | 2m", true
		}
		if err := h.CreatePoll(title, options, secs); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("Poll started (%ds): %s", secs, title), true
	case "endpoll":
		if err := h.EndActivePoll(false); err != nil {
			return err.Error(), true
		}
		return "Poll ended.", true

	case "prediction":
		title, outcomes, secs, err := parseChoiceCommand(rest, predDefaultWindow)
		if err != nil {
			return "/prediction " + err.Error() + " — e.g. /prediction Do I win? | Yes | No | 2m", true
		}
		if err := h.CreatePrediction(title, outcomes, secs); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("Prediction started (%ds): %s", secs, title), true
	case "lockprediction":
		if err := h.LockPrediction(); err != nil {
			return err.Error(), true
		}
		return "Prediction locked.", true
	case "cancelprediction":
		if err := h.CancelPrediction(); err != nil {
			return err.Error(), true
		}
		return "Prediction cancelled — points refunded.", true
	case "resolveprediction":
		if len(args) < 1 {
			return "/resolveprediction <outcome number, e.g. 1>", true
		}
		n, err := strconv.Atoi(args[0])
		if err != nil {
			return "/resolveprediction: give the number of the winning outcome (1, 2, …)", true
		}
		if err := h.ResolvePrediction(n); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("Prediction resolved — outcome %d won.", n), true

	case "clip":
		url, err := h.CreateClip()
		if err != nil {
			return err.Error(), true
		}
		return "Clip created: " + url, true
	case "marker":
		if err := h.CreateMarker(rest); err != nil {
			return err.Error(), true
		}
		return "Stream marker added.", true
	case "settitle":
		if rest == "" {
			return "/settitle <title>", true
		}
		if err := h.SetTitle(rest); err != nil {
			return err.Error(), true
		}
		return "Title changed: " + rest, true
	case "setgame":
		if rest == "" {
			return "/setgame <category>", true
		}
		name, err := h.SetCategory(rest)
		if err != nil {
			return err.Error(), true
		}
		return "Category changed: " + name, true
	}
	return "", false
}

// hasDurationUnit reports whether s looks like "<digits><s|m|h|d>".
func hasDurationUnit(s string) bool {
	if len(s) < 2 {
		return false
	}
	switch s[len(s)-1] {
	case 's', 'S', 'm', 'M', 'h', 'H', 'd', 'D':
	default:
		return false
	}
	for _, r := range s[:len(s)-1] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// commandRest returns everything after the command word, trimmed.
func commandRest(line string) string {
	line = strings.TrimSpace(line)
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return ""
}

// parseChoiceCommand parses "question | option | option [| …] [| 2m]".
// The last segment is the duration only if it has a unit (90s, 2m, 1h):
// a bare number stays an option, so "| 2019 | 2020 | 2021" works.
// Without a duration defaultSecs is used.
func parseChoiceCommand(rest string, defaultSecs int) (title string, options []string, secs int, err error) {
	parts := strings.Split(rest, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	secs = defaultSecs
	if n := len(parts); n >= 2 && hasDurationUnit(parts[n-1]) {
		if d, derr := parseDuration(parts[n-1]); derr == nil {
			secs = d
			parts = parts[:n-1]
		}
	}
	if len(parts) < 3 || parts[0] == "" {
		return "", nil, 0, fmt.Errorf("needs a question and at least two options separated by |")
	}
	return parts[0], parts[1:], secs, nil
}
