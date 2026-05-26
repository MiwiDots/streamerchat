# StreamerChat & ChatHub

Two Wails-based desktop apps for following Twitch (and YouTube) chats from your desktop without keeping a browser tab open.

| App         | Purpose                                                                                                                |
| ----------- | ---------------------------------------------------------------------------------------------------------------------- |
| **ChatHub** | Multi-channel **viewer**. Join many Twitch channels at once, each as its own tab, with tab-reorder and live-status dots. Anonymous read-only by default; log in via Twitch device-code flow to send. |
| **StreamerChat** | Single-channel **streamer dashboard**. One Twitch channel plus optional YouTube live chat side-by-side, with a user list, mod actions, and per-message detail panel. Targets the broadcaster, not the lurker. |

Both apps share the same Go core under `internal/` (IRC client, Helix client, badge registry, third-party emote loader, history writer). The frontends are vanilla HTML + JS — no framework — and live in `cmd/<app>/frontend/dist/`.

---

## Features

### ChatHub

- Multi-channel tabs with drag-to-reorder (persisted to `hubconfig.json`)
- Tabs wrap onto multiple rows; scrollbars hidden globally
- Live status dot per tab (Helix `/streams` poll every 60 s, plus immediate check on `AddChannel`)
- Optional sound on offline → live transition (reuses the mention sound)
- Real Twitch badge images **per channel** — each broadcaster's custom sub-tier artwork is kept in its own namespace so `subscriber/24` in channel A never overwrites channel B (`internal/twitch/badges.go`)
- Inline emote rendering: Twitch native emotes (via IRC `emotes=` tag) + 7TV / BetterTTV / FrankerFaceZ
- Username colors from IRC tags
- Batched join/part events ("X, Y, Z joined" every 1.5 s)
- `@username` autocomplete: tab/enter completes, recency-ranked from the active channel's last 500 chatters
- Click any username for a Chatterino-style **user card** (Helix avatar, account created, follower count, following-since, sub tier, plus the last ~15 local messages from that user)
- DE/EN UI with live language switch (no restart)
- Windows autostart toggle (HKCU `\Software\Microsoft\Windows\CurrentVersion\Run`, visible in Settings → Apps → Startup)
- Self-echo on send (chathub's authenticated send client is separate from the anonymous read client)
- Per-channel JSONL history (`~/.config/chathub/logs/<channel>.jsonl`) replayed on tab open with an "earlier / new" separator

### StreamerChat

- Twitch + YouTube unified into one feed with a `TW` / `YT` platform badge
- Real Twitch badge images in chat **and** in the right-hand user list sidebar (broadcaster / mod / VIP / sub icons resolved via the BadgeRegistry once per session)
- Clickable usernames open a mod-action bar (Ban, Timeout 10m/1h/24h, delete message, unban) wired to Helix
- Bot detection (`internal/twitch/bots.go`)
- YouTube auto-detect: polls a channel's `/live` page every 30 s and attaches/detaches the YT chat automatically when the stream starts/ends — no API key needed
- Token refresh loop with explicit logging on validate/refresh failures
- Channel-specific badge loading triggered after a successful token validate (no more race where badges silently load with a stale token)

### Shared

- `internal/twitch/multi_irc.go` — anonymous read client with exponential-backoff auto-reconnect (2 s → 60 s cap) and channel re-join after a netsplit
- `internal/twitch/ircclient.go` — authenticated send client (gempir/go-twitch-irc) used for IRC PRIVMSG
- `internal/twitch/badges.go` — global + per-channel badge URLs, channel scope wins over global on lookup
- `internal/twitch/thirdparty_emotes.go` — 7TV / BTTV / FFZ global + channel emote sets
- `internal/youtube/` — YouTube live-chat polling + live detection

---

## Build

Requires:

- Go 1.22+
- Node (only because Wails runs `npm install` during `wails build`; no JS framework, but Wails wants the lock file to exist)
- The Wails CLI: `go install github.com/wailsapp/wails/v2/cmd/wails@latest` (the CLI lives in `$GOPATH/bin/wails`)

### macOS (Apple Silicon)

```bash
# ChatHub
cd cmd/chathub
CGO_LDFLAGS="-framework UniformTypeIdentifiers" \
  go build -tags desktop,production -ldflags "-w -s" \
  -o ../../chathub.app/Contents/MacOS/chathub .

# StreamerChat
cd ../wails
CGO_LDFLAGS="-framework UniformTypeIdentifiers" \
  go build -tags desktop,production -ldflags "-w -s" \
  -o ../../streamerchat.app/Contents/MacOS/streamerchat .
```

The `UniformTypeIdentifiers` framework is required because Apple's recent SDK split that symbol out from `AppKit`.

### Windows (cross-compiled from macOS)

```bash
cd cmd/chathub
$GOPATH/bin/wails build -platform windows/amd64 -o chathub.exe

cd ../wails
$GOPATH/bin/wails build -platform windows/amd64 -o streamerchat-gui.exe
```

The `wails` CLI handles the cross-toolchain, embed step, and WebView2 loader.

---

## Configuration

Both apps store their config under `~/.config/<app>/config.json` (or `%APPDATA%\<app>\config.json` on Windows). The config holds the Twitch client ID, OAuth tokens, channel list, highlights, and UI preferences. It is **excluded from the repo** — see `.gitignore`.

Login uses Twitch's **device code flow**. The Client ID is embedded into the build via `-ldflags "-X main.defaultClientID=…"` if you want to ship a build with a baked-in app identity; otherwise the user can supply their own.

---

## Status

This is a personal project. Code is pragmatic, not enterprise-tidy — comments explain the *why* of tricky bits (badge race conditions, IRC dual-connection echo, etc.) rather than restating *what* the code does. Patches welcome.

See `changelog.txt` for the chronological log of changes (newest first).
