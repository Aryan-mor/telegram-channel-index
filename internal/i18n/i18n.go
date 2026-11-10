// Package i18n holds the short button labels. Search lines and vote lines stay in English.
package i18n

import "strings"

// Lang picks fa or en from a Telegram language code.
func Lang(code string) string {
	if strings.HasPrefix(strings.ToLower(code), "fa") {
		return "fa"
	}
	return "en"
}

var text = map[string]map[string]string{
	"en": {
		"prev": "Prev", "next": "Next", "up": "Up", "down": "Down",
		"cleared": "cleared",
	},
	"fa": {
		"prev": "قبلی", "next": "بعدی", "up": "موافق", "down": "مخالف",
		"cleared": "برداشته شد",
	},
}

// T returns a label. Missing keys come back as the key.
func T(lang, key string) string {
	if lang != "fa" {
		lang = "en"
	}
	if v := text[lang][key]; v != "" {
		return v
	}
	return key
}
