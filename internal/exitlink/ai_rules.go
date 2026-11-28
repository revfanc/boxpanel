package exitlink

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"sync"
)

// These are destination rules. In particular, crawler or API-client source
// ranges published by AI providers do not belong in the exit rule set.
const (
	AIDomainURL    = "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/category-ai-%21cn.list"
	VoicePrefixURL = "https://openai.com/chatgpt-voice.json"
)

//go:embed seed/category-ai.list
var seedDomains []byte

//go:embed seed/chatgpt-voice.json
var seedVoice []byte

// BundledAIRules supplies the first configuration without network access.
// The returned slices are shared and must be treated as read-only.
var BundledAIRules = sync.OnceValues(func() (DestinationRules, error) { return ParseAIRules(seedDomains, seedVoice) })

type DestinationRules struct {
	Domains  []string `json:"domain,omitempty"`
	Suffixes []string `json:"domain_suffix,omitempty"`
	Prefixes []string `json:"ip_cidr,omitempty"`
}

func ParseAIRules(domains, voice []byte) (DestinationRules, error) {
	var rules DestinationRules
	for line := range strings.Lines(string(domains)) {
		value := strings.TrimSpace(line)
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		name, suffix := strings.CutPrefix(strings.ToLower(value), "+.")
		if !validDomain(name) {
			return DestinationRules{}, fmt.Errorf("invalid domain in AI rule source")
		}
		if suffix {
			rules.Suffixes = append(rules.Suffixes, name)
		} else {
			rules.Domains = append(rules.Domains, name)
		}
	}
	var feed struct {
		Prefixes []struct {
			IPv4 string `json:"ipv4Prefix"`
			IPv6 string `json:"ipv6Prefix"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(voice, &feed); err != nil {
		return DestinationRules{}, fmt.Errorf("decode ChatGPT Voice ranges: %w", err)
	}
	for _, row := range feed.Prefixes {
		if row.IPv4 == "" && row.IPv6 == "" {
			return DestinationRules{}, fmt.Errorf("missing IP range in ChatGPT Voice rule source")
		}
		for i, value := range []string{row.IPv4, row.IPv6} {
			if value == "" {
				continue
			}
			p, err := netip.ParsePrefix(value)
			if err != nil || p.Bits() == 0 || !p.Addr().IsGlobalUnicast() || p.Addr().IsPrivate() || p.Addr().Is4In6() || p.Addr().Is4() != (i == 0) || p != p.Masked() {
				return DestinationRules{}, fmt.Errorf("invalid public CIDR in ChatGPT Voice rule source")
			}
			rules.Prefixes = append(rules.Prefixes, p.String())
		}
	}
	if len(rules.Domains)+len(rules.Suffixes) == 0 || len(rules.Prefixes) == 0 {
		return DestinationRules{}, fmt.Errorf("empty AI domain or voice IP rule source")
	}
	rules.Domains = sortedUnique(rules.Domains)
	rules.Suffixes = sortedUnique(rules.Suffixes)
	rules.Prefixes = sortedUnique(rules.Prefixes)
	return rules, nil
}
