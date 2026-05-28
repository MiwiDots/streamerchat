package youtube

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// computeSAPISIDHASH builds the Authorization header value YouTube's web
// client sends with authenticated InnerTube requests. Format:
//
//	SAPISIDHASH <timestamp>_<sha1Hex(timestamp + " " + SAPISID + " " + origin)>
//
// The same hash is also valid via the "SAPISIDHASH1" / "SAPISIDHASH3" alias
// headers but a plain SAPISIDHASH is enough for live_chat/send_message.
func computeSAPISIDHASH(sapisid, origin string) string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	h := sha1.New()
	h.Write([]byte(ts + " " + sapisid + " " + origin))
	return "SAPISIDHASH " + ts + "_" + hex.EncodeToString(h.Sum(nil))
}

// cookieHeader joins the relevant cookies into the single Cookie: header
// value the request needs. Empty values are skipped so we don't emit
// trailing semicolons.
func (c LoginCookies) cookieHeader() string {
	var parts []string
	add := func(name, val string) {
		if val != "" {
			parts = append(parts, name+"="+val)
		}
	}
	add("SAPISID", c.SAPISID)
	add("SID", c.SID)
	add("HSID", c.HSID)
	add("SSID", c.SSID)
	add("APISID", c.APISID)
	add("__Secure-3PSID", c.Secure3PSID)
	add("__Secure-3PAPISID", c.Secure3PAPISID)
	add("__Secure-1PSID", c.Secure1PSID)
	add("__Secure-1PAPISID", c.Secure1PAPISID)
	add("LOGIN_INFO", c.LoginInfo)
	add("VISITOR_INFO1_LIVE", c.VisitorInfo1Live)
	return strings.Join(parts, "; ")
}

// SendLiveChatMessage posts `text` to a live chat. chatParams is the
// `sendLiveChatMessageEndpoint.params` value extracted from the InnerTube
// chat continuation payload; see continuation.go (or wherever the reader
// extracts it).
func SendLiveChatMessage(c LoginCookies, chatParams, text string) error {
	if !c.Valid() {
		return fmt.Errorf("youtube: cookies not valid (login first)")
	}
	if chatParams == "" {
		return fmt.Errorf("youtube: empty chat params (stream may not be live)")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("youtube: empty message")
	}

	body := map[string]interface{}{
		"context": map[string]interface{}{
			"client": map[string]interface{}{
				"clientName":     "WEB",
				"clientVersion":  "2.20260101.00.00",
				"hl":             "en",
				"gl":             "US",
				"originalUrl":    "https://www.youtube.com/",
				"platform":       "DESKTOP",
				"visitorData":    c.VisitorInfo1Live,
			},
			"user": map[string]interface{}{
				"lockedSafetyMode": false,
			},
			"request": map[string]interface{}{
				"useSsl": true,
			},
		},
		"params": chatParams,
		"richMessage": map[string]interface{}{
			"textSegments": []map[string]string{{"text": text}},
		},
		"clientMessageId": strconv.FormatInt(time.Now().UnixNano(), 10),
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST",
		"https://www.youtube.com/youtubei/v1/live_chat/send_message?prettyPrint=false",
		bytes.NewReader(buf))
	if err != nil {
		return err
	}
	const origin = "https://www.youtube.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set("X-Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("X-Youtube-Client-Name", "1")
	req.Header.Set("X-Youtube-Client-Version", "2.20260101.00.00")
	req.Header.Set("Cookie", c.cookieHeader())
	req.Header.Set("Authorization", computeSAPISIDHASH(c.SAPISID, origin))
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("youtube: send_message status %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}
