package twitch

import (
	"fmt"
	"strconv"
	"strings"
)

// HandleSlashCommand dispatches Twitch-style /commands through the
// matching Helix endpoint. Returns:
//
//   - handled=true means the line was a recognised command and we
//     called the API. The status message is what the UI should show
//     in chat (success or error).
//   - handled=false means line wasn't a slash command at all — caller
//     should send it as a normal chat message.
//
// IRC /mod /vip /ban etc. used to be sent as PRIVMSG body and the
// Twitch server interpreted them; Twitch deprecated that path in
// early 2023 and these all go through Helix now.
func HandleSlashCommand(h *HelixClient, line string) (status string, handled bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return "", false
	}
	if h == nil {
		return "Slash commands require Twitch login + Helix client.", true
	}

	parts := strings.Fields(line)
	cmd := strings.ToLower(strings.TrimPrefix(parts[0], "/"))
	args := parts[1:]

	switch cmd {
	case "help", "commands":
		return helpText(), true

	case "mod", "unmod", "vip", "unvip", "ban", "unban", "untimeout", "shoutout", "so":
		if len(args) < 1 {
			return fmt.Sprintf("/%s needs a username", cmd), true
		}
		login := strings.TrimPrefix(args[0], "@")
		userID, err := h.ResolveLogin(login)
		if err != nil {
			return fmt.Sprintf("/%s: %v", cmd, err), true
		}
		switch cmd {
		case "mod":
			if err := h.AddMod(userID); err != nil {
				return err.Error(), true
			}
			return fmt.Sprintf("%s is now a moderator.", login), true
		case "unmod":
			if err := h.RemoveMod(userID); err != nil {
				return err.Error(), true
			}
			return fmt.Sprintf("%s is no longer a moderator.", login), true
		case "vip":
			if err := h.AddVIP(userID); err != nil {
				return err.Error(), true
			}
			return fmt.Sprintf("%s is now a VIP.", login), true
		case "unvip":
			if err := h.RemoveVIP(userID); err != nil {
				return err.Error(), true
			}
			return fmt.Sprintf("%s is no longer a VIP.", login), true
		case "ban":
			reason := strings.Join(args[1:], " ")
			if err := h.BanUser(userID, reason); err != nil {
				return err.Error(), true
			}
			if reason == "" {
				return fmt.Sprintf("%s has been banned.", login), true
			}
			return fmt.Sprintf("%s has been banned (%s).", login, reason), true
		case "unban", "untimeout":
			if err := h.UnbanUser(userID); err != nil {
				return err.Error(), true
			}
			return fmt.Sprintf("%s has been unbanned.", login), true
		case "shoutout", "so":
			if err := h.SendShoutout(userID); err != nil {
				return err.Error(), true
			}
			return fmt.Sprintf("Shoutout sent to %s.", login), true
		}

	case "timeout":
		if len(args) < 1 {
			return "/timeout <user> [seconds=600] [reason]", true
		}
		login := strings.TrimPrefix(args[0], "@")
		seconds := 600
		if len(args) >= 2 {
			if n, err := parseDuration(args[1]); err == nil {
				seconds = n
			}
		}
		reason := ""
		if len(args) >= 3 {
			reason = strings.Join(args[2:], " ")
		}
		userID, err := h.ResolveLogin(login)
		if err != nil {
			return fmt.Sprintf("/timeout: %v", err), true
		}
		if err := h.TimeoutUser(userID, seconds, reason); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("%s timed out for %ds.", login, seconds), true

	case "clear":
		// Server-side chat clear via Helix — wipes the whole
		// channel chat for everyone. The broadcaster is implicitly a
		// mod so this works in streamerchat-gui (streamer dashboard)
		// with the moderator:manage:chat_messages scope. Chathub is
		// a viewer app and intercepts /clear client-side instead.
		if err := h.ClearChat(); err != nil {
			return err.Error(), true
		}
		return "Chat cleared.", true

	case "slow":
		seconds := 30
		if len(args) >= 1 {
			if n, err := parseDuration(args[0]); err == nil {
				seconds = n
			}
		}
		if err := h.SetSlowMode(true, seconds); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("Slow mode on (%ds).", seconds), true
	case "slowoff":
		if err := h.SetSlowMode(false, 0); err != nil {
			return err.Error(), true
		}
		return "Slow mode off.", true

	case "followers":
		minutes := 0
		if len(args) >= 1 {
			if n, err := parseDuration(args[0]); err == nil {
				minutes = n / 60
				if minutes == 0 {
					minutes = 1
				}
			}
		}
		if err := h.SetFollowerOnly(true, minutes); err != nil {
			return err.Error(), true
		}
		if minutes == 0 {
			return "Follower-only mode on.", true
		}
		return fmt.Sprintf("Follower-only mode on (min %d min).", minutes), true
	case "followersoff":
		if err := h.SetFollowerOnly(false, 0); err != nil {
			return err.Error(), true
		}
		return "Follower-only mode off.", true

	case "subscribers", "subs":
		if err := h.SetSubOnly(true); err != nil {
			return err.Error(), true
		}
		return "Sub-only mode on.", true
	case "subscribersoff", "subsoff":
		if err := h.SetSubOnly(false); err != nil {
			return err.Error(), true
		}
		return "Sub-only mode off.", true

	case "emoteonly":
		if err := h.SetEmoteOnly(true); err != nil {
			return err.Error(), true
		}
		return "Emote-only mode on.", true
	case "emoteonlyoff":
		if err := h.SetEmoteOnly(false); err != nil {
			return err.Error(), true
		}
		return "Emote-only mode off.", true

	case "announce", "announcement":
		if len(args) < 1 {
			return "/announce <message>", true
		}
		if err := h.SendAnnouncement(strings.Join(args, " "), ""); err != nil {
			return err.Error(), true
		}
		return "Announcement sent.", true
	case "announceblue", "announcegreen", "announceorange", "announcepurple":
		color := strings.TrimPrefix(cmd, "announce")
		if len(args) < 1 {
			return "/" + cmd + " <message>", true
		}
		if err := h.SendAnnouncement(strings.Join(args, " "), color); err != nil {
			return err.Error(), true
		}
		return "Announcement sent.", true

	case "raid":
		if len(args) < 1 {
			return "/raid <channel>", true
		}
		login := strings.TrimPrefix(args[0], "@")
		toID, err := h.ResolveLogin(login)
		if err != nil {
			return fmt.Sprintf("/raid: %v", err), true
		}
		if err := h.StartRaid(toID); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("Raid started to %s.", login), true
	case "unraid":
		if err := h.CancelRaid(); err != nil {
			return err.Error(), true
		}
		return "Raid cancelled.", true
	}

	return fmt.Sprintf("Unknown command: /%s — try /help", cmd), true
}

// parseDuration accepts plain seconds ("60"), Twitch-style suffix
// notation ("5m", "1h", "1d") or anything strconv can take.
func parseDuration(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	mult := 1
	last := s[len(s)-1]
	switch last {
	case 's', 'S':
		s = s[:len(s)-1]
	case 'm', 'M':
		mult = 60
		s = s[:len(s)-1]
	case 'h', 'H':
		mult = 3600
		s = s[:len(s)-1]
	case 'd', 'D':
		mult = 86400
		s = s[:len(s)-1]
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}

func helpText() string {
	return strings.Join([]string{
		"Available commands:",
		"/mod <user>, /unmod <user>",
		"/vip <user>, /unvip <user>",
		"/ban <user> [reason], /unban <user>",
		"/timeout <user> [60s|5m|1h|1d] [reason], /untimeout <user>",
		"/clear",
		"/slow [seconds], /slowoff",
		"/followers [duration], /followersoff",
		"/subscribers, /subscribersoff",
		"/emoteonly, /emoteonlyoff",
		"/announce <text>  (/announceblue /announcegreen /announceorange /announcepurple)",
		"/raid <channel>, /unraid",
		"/shoutout <user>  (/so)",
	}, "\n")
}
