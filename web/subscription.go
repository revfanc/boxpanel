package web

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed src/i18n/zh-CN.ts
var localeSource string

// SubscriptionText reads the JSON subset marked in the shared locale module.
// Returning a fresh map keeps consumers from mutating another renderer's text.
func SubscriptionText() map[string]string {
	_, rest, ok := strings.Cut(localeSource, "/* subscription-text:start */")
	block, _, end := strings.Cut(rest, "/* subscription-text:end */")
	var text map[string]string
	if !ok || !end || json.Unmarshal([]byte(block), &text) != nil {
		panic("invalid subscription locale; restore web/src/i18n/zh-CN.ts and rebuild")
	}
	return text
}
