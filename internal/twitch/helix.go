package twitch

import (
	"fmt"
	"log"
	"time"

	"github.com/nicklaw5/helix/v2"
)

// HelixClient wraps the Twitch Helix API for mod actions.
type HelixClient struct {
	client        *helix.Client
	broadcasterID string
	moderatorID   string
}

// NewHelixClient creates a new Helix API client.
func NewHelixClient(clientID, accessToken, broadcasterID, moderatorID string) (*HelixClient, error) {
	client, err := helix.NewClient(&helix.Options{
		ClientID:       clientID,
		UserAccessToken: accessToken,
	})
	if err != nil {
		return nil, fmt.Errorf("create helix client: %w", err)
	}

	return &HelixClient{
		client:        client,
		broadcasterID: broadcasterID,
		moderatorID:   moderatorID,
	}, nil
}

// BanUser permanently bans a user from chat.
func (h *HelixClient) BanUser(userID, reason string) error {
	_, err := h.client.BanUser(&helix.BanUserParams{
		BroadcasterID: h.broadcasterID,
		ModeratorId:   h.moderatorID,
		Body: helix.BanUserRequestBody{
			UserId: userID,
			Reason: reason,
		},
	})
	if err != nil {
		return fmt.Errorf("ban user: %w", err)
	}
	return nil
}

// TimeoutUser temporarily bans a user from chat.
func (h *HelixClient) TimeoutUser(userID string, durationSeconds int, reason string) error {
	_, err := h.client.BanUser(&helix.BanUserParams{
		BroadcasterID: h.broadcasterID,
		ModeratorId:   h.moderatorID,
		Body: helix.BanUserRequestBody{
			UserId:   userID,
			Reason:   reason,
			Duration: durationSeconds,
		},
	})
	if err != nil {
		return fmt.Errorf("timeout user: %w", err)
	}
	return nil
}

// UnbanUser removes a ban or timeout from a user.
func (h *HelixClient) UnbanUser(userID string) error {
	_, err := h.client.UnbanUser(&helix.UnbanUserParams{
		BroadcasterID: h.broadcasterID,
		ModeratorID:   h.moderatorID,
		UserID:        userID,
	})
	if err != nil {
		return fmt.Errorf("unban user: %w", err)
	}
	return nil
}

// DeleteMessage deletes a single chat message.
func (h *HelixClient) DeleteMessage(messageID string) error {
	_, err := h.client.DeleteChatMessage(&helix.DeleteChatMessageParams{
		BroadcasterID: h.broadcasterID,
		ModeratorID:   h.moderatorID,
		MessageID:     messageID,
	})
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	return nil
}

// ClearChat clears all messages in the chat.
//
// The nicklaw5/helix v2 client returns ErrorMessage on the response
// struct for 4xx / 5xx Twitch answers — `err` is only set for
// transport failures. Before this change we silently returned nil
// when Twitch said "401 missing scope" so /clear appeared to do
// nothing. Now we surface both.
func (h *HelixClient) ClearChat() error {
	resp, err := h.client.DeleteChatMessage(&helix.DeleteChatMessageParams{
		BroadcasterID: h.broadcasterID,
		ModeratorID:   h.moderatorID,
	})
	if err != nil {
		return fmt.Errorf("clear chat: %w", err)
	}
	if resp.ErrorMessage != "" {
		return fmt.Errorf("clear chat: %d %s (%s)", resp.StatusCode, resp.Error, resp.ErrorMessage)
	}
	return nil
}

// SetSlowMode enables or disables slow mode.
func (h *HelixClient) SetSlowMode(enabled bool, waitSeconds int) error {
	params := &helix.UpdateChatSettingsParams{
		BroadcasterID: h.broadcasterID,
		ModeratorID:   h.moderatorID,
		SlowMode:      &enabled,
	}
	if enabled && waitSeconds > 0 {
		params.SlowModeWaitTime = &waitSeconds
	}
	_, err := h.client.UpdateChatSettings(params)
	if err != nil {
		return fmt.Errorf("set slow mode: %w", err)
	}
	return nil
}

// SetSubOnly enables or disables subscriber-only mode.
func (h *HelixClient) SetSubOnly(enabled bool) error {
	_, err := h.client.UpdateChatSettings(&helix.UpdateChatSettingsParams{
		BroadcasterID:  h.broadcasterID,
		ModeratorID:    h.moderatorID,
		SubscriberMode: &enabled,
	})
	if err != nil {
		return fmt.Errorf("set sub-only: %w", err)
	}
	return nil
}

// SetEmoteOnly enables or disables emote-only mode.
func (h *HelixClient) SetEmoteOnly(enabled bool) error {
	_, err := h.client.UpdateChatSettings(&helix.UpdateChatSettingsParams{
		BroadcasterID: h.broadcasterID,
		ModeratorID:   h.moderatorID,
		EmoteMode:     &enabled,
	})
	if err != nil {
		return fmt.Errorf("set emote-only: %w", err)
	}
	return nil
}

// SetFollowerOnly enables or disables follower-only mode.
func (h *HelixClient) SetFollowerOnly(enabled bool, minMinutes int) error {
	_, err := h.client.UpdateChatSettings(&helix.UpdateChatSettingsParams{
		BroadcasterID:      h.broadcasterID,
		ModeratorID:        h.moderatorID,
		FollowerMode:       &enabled,
		FollowerModeDuration: &minMinutes,
	})
	if err != nil {
		return fmt.Errorf("set follower-only: %w", err)
	}
	return nil
}

// GetChatSettings retrieves current chat settings.
func (h *HelixClient) GetChatSettings() (*helix.GetChatSettingsResponse, error) {
	resp, err := h.client.GetChatSettings(&helix.GetChatSettingsParams{
		BroadcasterID: h.broadcasterID,
	})
	if err != nil {
		return nil, fmt.Errorf("get chat settings: %w", err)
	}
	return resp, nil
}

// GetChatters returns all users currently in the chat.
func (h *HelixClient) GetChatters() ([]helix.ChatChatter, error) {
	var all []helix.ChatChatter
	cursor := ""

	for {
		resp, err := h.client.GetChannelChatChatters(&helix.GetChatChattersParams{
			BroadcasterID: h.broadcasterID,
			ModeratorID:   h.moderatorID,
			First:         "1000",
			After:         cursor,
		})
		if err != nil {
			return nil, fmt.Errorf("get chatters: %w", err)
		}

		all = append(all, resp.Data.Chatters...)

		if resp.Data.Pagination.Cursor == "" {
			break
		}
		cursor = resp.Data.Pagination.Cursor
	}

	return all, nil
}

// GetModerators returns all moderators for the channel.
func (h *HelixClient) GetModerators() ([]string, error) {
	var mods []string
	cursor := ""
	for {
		resp, err := h.client.GetModerators(&helix.GetModeratorsParams{
			BroadcasterID: h.broadcasterID,
			First:         100,
			After:         cursor,
		})
		if err != nil {
			return nil, err
		}
		for _, m := range resp.Data.Moderators {
			mods = append(mods, m.UserLogin)
		}
		if resp.Data.Pagination.Cursor == "" {
			break
		}
		cursor = resp.Data.Pagination.Cursor
	}
	return mods, nil
}

// GetVIPs returns all VIPs for the channel.
func (h *HelixClient) GetVIPs() ([]string, error) {
	var vips []string
	cursor := ""
	for {
		resp, err := h.client.GetChannelVips(&helix.GetChannelVipsParams{
			BroadcasterID: h.broadcasterID,
			First:         100,
			After:         cursor,
		})
		if err != nil {
			return nil, err
		}
		for _, v := range resp.Data.ChannelsVips {
			vips = append(vips, v.UserLogin)
		}
		if resp.Data.Pagination.Cursor == "" {
			break
		}
		cursor = resp.Data.Pagination.Cursor
	}
	return vips, nil
}

// ResolveLogin turns a Twitch username (no leading @ or #) into its
// numeric user id. Many slash commands take a target user by login;
// the Helix moderation/VIP endpoints all want the id, so we resolve
// here once per call.
func (h *HelixClient) ResolveLogin(login string) (string, error) {
	resp, err := h.client.GetUsers(&helix.UsersParams{
		Logins: []string{login},
	})
	if err != nil {
		return "", err
	}
	if resp.ErrorMessage != "" {
		return "", fmt.Errorf("%s", resp.ErrorMessage)
	}
	if len(resp.Data.Users) == 0 {
		return "", fmt.Errorf("user %q not found", login)
	}
	return resp.Data.Users[0].ID, nil
}

// AddMod adds a moderator to the broadcaster's channel.
func (h *HelixClient) AddMod(userID string) error {
	_, err := h.client.AddChannelModerator(&helix.AddChannelModeratorParams{
		BroadcasterID: h.broadcasterID,
		UserID:        userID,
	})
	if err != nil {
		return fmt.Errorf("add mod: %w", err)
	}
	return nil
}

// RemoveMod removes a moderator from the broadcaster's channel.
func (h *HelixClient) RemoveMod(userID string) error {
	_, err := h.client.RemoveChannelModerator(&helix.RemoveChannelModeratorParams{
		BroadcasterID: h.broadcasterID,
		UserID:        userID,
	})
	if err != nil {
		return fmt.Errorf("remove mod: %w", err)
	}
	return nil
}

// AddVIP grants a user the VIP role.
func (h *HelixClient) AddVIP(userID string) error {
	_, err := h.client.AddChannelVip(&helix.AddChannelVipParams{
		BroadcasterID: h.broadcasterID,
		UserID:        userID,
	})
	if err != nil {
		return fmt.Errorf("add vip: %w", err)
	}
	return nil
}

// RemoveVIP revokes the VIP role.
func (h *HelixClient) RemoveVIP(userID string) error {
	_, err := h.client.RemoveChannelVip(&helix.RemoveChannelVipParams{
		BroadcasterID: h.broadcasterID,
		UserID:        userID,
	})
	if err != nil {
		return fmt.Errorf("remove vip: %w", err)
	}
	return nil
}

// SendAnnouncement posts an announcement in chat. color may be one of
// "primary" (default channel color), "blue", "green", "orange", "purple".
func (h *HelixClient) SendAnnouncement(message, color string) error {
	_, err := h.client.SendChatAnnouncement(&helix.SendChatAnnouncementParams{
		BroadcasterID: h.broadcasterID,
		ModeratorID:   h.moderatorID,
		Message:       message,
		Color:         color,
	})
	if err != nil {
		return fmt.Errorf("announce: %w", err)
	}
	return nil
}

// SendShoutout fires a /shoutout to the given user in the broadcaster's chat.
func (h *HelixClient) SendShoutout(toBroadcasterID string) error {
	_, err := h.client.SendShoutout(&helix.SendShoutoutParams{
		FromBroadcasterID: h.broadcasterID,
		ToBroadcasterID:   toBroadcasterID,
		ModeratorID:       h.moderatorID,
	})
	if err != nil {
		return fmt.Errorf("shoutout: %w", err)
	}
	return nil
}

// StartRaid initiates a raid from this channel to toBroadcasterID.
func (h *HelixClient) StartRaid(toBroadcasterID string) error {
	_, err := h.client.StartRaid(&helix.StartRaidParams{
		FromBroadcasterID: h.broadcasterID,
		ToBroadcasterID:   toBroadcasterID,
	})
	if err != nil {
		return fmt.Errorf("raid: %w", err)
	}
	return nil
}

// CancelRaid cancels a pending raid started from this channel.
func (h *HelixClient) CancelRaid() error {
	_, err := h.client.CancelRaid(&helix.CancelRaidParams{
		BroadcasterID: h.broadcasterID,
	})
	if err != nil {
		return fmt.Errorf("unraid: %w", err)
	}
	return nil
}

// GetChannelName resolves a room/user ID to a login name.
func (h *HelixClient) GetChannelName(userID string) (string, error) {
	resp, err := h.client.GetUsers(&helix.UsersParams{
		IDs: []string{userID},
	})
	if err != nil {
		return "", err
	}
	if len(resp.Data.Users) > 0 {
		return resp.Data.Users[0].Login, nil
	}
	return "", fmt.Errorf("user not found: %s", userID)
}

// UserInfo contains combined user + follow info.
type UserInfo struct {
	UserID      string
	Login       string
	DisplayName string
	Description string
	CreatedAt   string
	IsFollower  bool
	FollowedAt  string
	AccountAge  string
	FollowAge   string
}

// GetUserInfo fetches user details and follow status.
func (h *HelixClient) GetUserInfo(userID string) (*UserInfo, error) {
	info := &UserInfo{UserID: userID}

	usersResp, err := h.client.GetUsers(&helix.UsersParams{
		IDs: []string{userID},
	})
	if err != nil {
		log.Printf("[HELIX] GetUsers failed for %s: %v", userID, err)
	} else if usersResp.ErrorMessage != "" {
		log.Printf("[HELIX] GetUsers error for %s: %s", userID, usersResp.ErrorMessage)
	} else if len(usersResp.Data.Users) > 0 {
		user := usersResp.Data.Users[0]
		info.Login = user.Login
		info.DisplayName = user.DisplayName
		info.Description = user.Description
		info.CreatedAt = user.CreatedAt.Format("2006-01-02")
		info.AccountAge = formatAge(user.CreatedAt.Time)
	}

	followResp, err := h.client.GetChannelFollows(&helix.GetChannelFollowsParams{
		BroadcasterID: h.broadcasterID,
		UserID:        userID,
	})
	if err != nil {
		log.Printf("[HELIX] GetChannelFollows failed for broadcaster=%s user=%s: %v", h.broadcasterID, userID, err)
	} else {
		log.Printf("[HELIX] GetChannelFollows broadcaster=%s user=%s status=%d errMsg=%q totalChannels=%d total=%d",
			h.broadcasterID, userID, followResp.StatusCode, followResp.ErrorMessage,
			len(followResp.Data.Channels), followResp.Data.Total)
		if len(followResp.Data.Channels) > 0 {
			follow := followResp.Data.Channels[0]
			info.IsFollower = true
			info.FollowedAt = follow.Followed.Format("2006-01-02")
			info.FollowAge = formatAge(follow.Followed.Time)
		}
	}

	return info, nil
}

func formatAge(t time.Time) string {
	diff := time.Since(t)
	days := int(diff.Hours() / 24)
	if days < 1 {
		return "today"
	}
	if days < 30 {
		return fmt.Sprintf("%dd", days)
	}
	months := days / 30
	if months < 12 {
		return fmt.Sprintf("%dmo", months)
	}
	years := months / 12
	remainMonths := months % 12
	if remainMonths == 0 {
		return fmt.Sprintf("%dy", years)
	}
	return fmt.Sprintf("%dy %dmo", years, remainMonths)
}
