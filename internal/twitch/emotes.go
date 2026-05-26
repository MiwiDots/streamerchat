package twitch

import (
	"fmt"
	"strings"
)

// Common Twitch text emotes mapped to Unicode equivalents.
var textEmotes = map[string]string{
	"<3":   "❤",
	":)":   "😊",
	":(":   "😞",
	":D":   "😃",
	":O":   "😮",
	"B)":   "😎",
	":/":   "😕",
	";)":   "😉",
	":P":   "😛",
	">(":   "😠",
	"O_o":  "😳",
	"R)":   "🏴‍☠️",
	";P":   "😜",
	":Z":   "😴",
	"#/":   "🤔",
}

// ReplaceTextEmotes replaces common Twitch text emotes with Unicode.
func ReplaceTextEmotes(text string) string {
	for emote, unicode := range textEmotes {
		if strings.Contains(text, emote) {
			text = strings.ReplaceAll(text, emote, unicode)
		}
	}
	return text
}

// CollapseSpam shortens repeated words/emotes.
// "Kappa Kappa Kappa Kappa Kappa" → "Kappa x5"
func CollapseSpam(text string) string {
	words := strings.Fields(text)
	if len(words) < 4 {
		return text
	}

	var result []string
	i := 0
	for i < len(words) {
		word := words[i]
		count := 1
		for i+count < len(words) && words[i+count] == word {
			count++
		}
		if count >= 3 {
			result = append(result, fmt.Sprintf("%s x%d", word, count))
		} else {
			for j := 0; j < count; j++ {
				result = append(result, word)
			}
		}
		i += count
	}
	return strings.Join(result, " ")
}
