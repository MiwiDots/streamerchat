package chat

import "time"

// Platform identifies where a message originated.
type Platform string

const (
	PlatformTwitch  Platform = "twitch"
	PlatformYouTube Platform = "youtube"
)

// MessageType categorizes the kind of chat event.
type MessageType int

const (
	MessageTypeChat MessageType = iota
	MessageTypeJoin
	MessageTypePart
	MessageTypeSub
	MessageTypeGiftSub
	MessageTypeRaid
	MessageTypeSuperChat
	MessageTypeMembership
	MessageTypeBan
	MessageTypeTimeout
	MessageTypeDeletedMessage
	MessageTypeClearChat
	MessageTypeAnnouncement
	MessageTypeSystem
	// MessageTypeWatchStreak: a viewer shared their Twitch watch streak
	// (USERNOTICE msg-id=viewermilestone, category watch-streak).
	MessageTypeWatchStreak
)

// Badge represents a user badge (mod, sub, vip, etc.).
type Badge struct {
	Name    string // e.g. "moderator", "subscriber", "vip"
	Version string // badge version/tier
}

// EmoteRange represents a native Twitch emote position in a message.
// Start and End are rune-indexed, End is inclusive.
type EmoteRange struct {
	ID    string
	Name  string
	Start int
	End   int
}

// Message is the unified message type for all platforms.
type Message struct {
	ID        string
	Platform  Platform
	Type      MessageType
	Channel   string
	Timestamp time.Time

	// User info
	UserID      string
	Username    string
	DisplayName string
	Color       string // hex color for username
	Badges      []Badge
	IsMod       bool
	IsVIP       bool
	IsSub       bool
	IsBroadcaster bool

	// Message content
	Text string

	// Shared Chat (Twitch "Chat Together")
	SourceChannel string // channel the message originated from (empty = local)
	IsSharedChat  bool   // true if from another channel's shared chat

	// IsFirstMessage is Twitch's "first-msg" tag: this is the user's very
	// first message in this channel. Twitch highlights these for the
	// broadcaster and mods ("First-time chat").
	IsFirstMessage bool

	// IsReturningChatter is Twitch's "returning-chatter" tag: a viewer who
	// has chatted in this channel before and is coming back. Shown more
	// subtly than a first message.
	IsReturningChatter bool

	// StreakCount is the number of consecutive streams watched, set on
	// MessageTypeWatchStreak messages.
	StreakCount int

	// Platform-specific metadata
	TwitchEmotes     string         // raw emote tag from IRC
	TwitchEmoteRanges []EmoteRange  // parsed emote positions (rune-based)
	Bits             int            // Twitch bits

	// YouTube-specific
	SuperChatAmount  string // formatted amount e.g. "$5.00"
	SuperChatCurrency string
	MembershipMonths int

	// Mod action metadata
	TargetUserID   string
	TargetUsername string
	BanDuration    int // seconds, 0 = permanent
}

// UserJoinPart is a lightweight event for join/part tracking.
type UserJoinPart struct {
	Platform Platform
	Channel  string
	Username string
	IsJoin   bool // true = join, false = part
	Time     time.Time
}

// ChatSettings represents current chat mode settings.
type ChatSettings struct {
	SlowMode         bool
	SlowModeWait     int // seconds
	SubOnly          bool
	EmoteOnly        bool
	FollowerOnly     bool
	FollowerMinutes  int
	UniqueChat       bool
}
