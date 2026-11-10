package web

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed src/i18n/zh-CN.ts
var localeSource string

//go:embed src/data/clients.json
var clientSource []byte

type SubscriptionClient struct {
	Name           string   `json:"name"`
	Platforms      []string `json:"platforms"`
	Download       string   `json:"download"`
	Source         string   `json:"source"`
	Formats        []string `json:"formats"`
	ImportTemplate string   `json:"importTemplate"`
	ImportSource   string   `json:"importSource"`
}

// SubscriptionClients shares the panel's catalog without requesting resources.
func SubscriptionClients() []SubscriptionClient {
	var catalog struct {
		Clients []SubscriptionClient `json:"clients"`
	}
	if err := json.Unmarshal(clientSource, &catalog); err != nil {
		panic("invalid client catalog; restore web/src/data/clients.json and rebuild")
	}
	return catalog.Clients
}

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
