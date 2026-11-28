// Package exitlink defines the private, single-hop configuration shared between
// the main panel, its agents and the sing-box configuration generator.
package exitlink

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	Method        = "2022-blake3-aes-256-gcm"
	InboundPrefix = "bp.exit-in."
	ForwardTag    = "bp-exit-forward"
	probeTag      = "bp-exit-probe"
	PortFirst     = 20000
	PortLast      = 29999

	Split        = "split"
	All          = "all"
	Block        = "block"
	Fallback     = "direct"
	ProbeEvery   = 30 * time.Second
	ProbeExpires = 2 * time.Minute
)

var presets = map[string][]string{
	"ai": {
		"openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com",
		"anthropic.com", "claude.ai", "claude.com",
		"gemini.google.com", "aistudio.google.com", "generativelanguage.googleapis.com",
		"ai.google.dev", "notebooklm.google.com",
	},
	"streaming": {
		"netflix.com", "netflix.net", "nflxext.com", "nflximg.com", "nflximg.net",
		"nflxso.net", "nflxvideo.net", "fast.com",
		"disneyplus.com", "disney-plus.net", "bamgrid.com", "dssott.com",
	},
}

type Policy struct {
	Mode    string `json:"mode"`
	Failure string `json:"failure"`
	Rules   Rules  `json:"rules"`
}

type Rules struct {
	Presets        []string `json:"presets"`
	Domains        []string `json:"domains"`
	DomainSuffixes []string `json:"domain_suffixes"`
}

func (p *Policy) Normalize() error {
	if p.Mode == "" {
		p.Mode = Split
	}
	if p.Failure == "" {
		p.Failure = Block
	}
	if p.Mode != Split && p.Mode != All || p.Failure != Block && p.Failure != Fallback {
		return errors.New("select split or all and block or direct")
	}
	p.Rules.Presets = sortedUnique(p.Rules.Presets)
	for _, name := range p.Rules.Presets {
		if _, ok := presets[name]; !ok {
			return errors.New("choose AI or streaming presets")
		}
	}
	for _, list := range []*[]string{&p.Rules.Domains, &p.Rules.DomainSuffixes} {
		if len(*list) > 128 {
			return errors.New("use at most 128 domains per list")
		}
		normalized := make([]string, 0, len(*list))
		for _, raw := range *list {
			value := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
			if !validDomain(value) {
				return errors.New("use DNS names without wildcards, ports or URLs")
			}
			normalized = append(normalized, value)
		}
		*list = sortedUnique(normalized)
	}
	return nil
}

func validDomain(name string) bool {
	if len(name) > 253 || !strings.ContainsRune(name, '.') {
		return false
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func sortedUnique(values []string) []string {
	out := append([]string{}, values...)
	slices.Sort(out)
	return slices.Compact(out)
}

func (r Rules) Suffixes() []string {
	suffixes := slices.Clone(r.DomainSuffixes)
	for _, preset := range r.Presets {
		suffixes = append(suffixes, presets[preset]...)
	}
	return sortedUnique(suffixes)
}

type Revision struct {
	ID      int64 `json:"id"`
	Version int64 `json:"revision"`
}

func (r Revision) ProbeTag() string {
	return probeTag + "-" + strconv.FormatInt(r.ID, 10) + "-" + strconv.FormatInt(r.Version, 10)
}

type Inbound struct {
	ID       int64  `json:"id"`
	Source   string `json:"source"`
	Port     int    `json:"port"`
	Password string `json:"password"`
}

type Outbound struct {
	Revision
	External *Proxy `json:"external_proxy,omitempty"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	Policy
}

// Configuration contains secrets. Only a local apply or the authenticated
// desired state for a participating server may receive it.
type Configuration struct {
	Outgoing *Outbound `json:"outgoing,omitempty"`
	Incoming []Inbound `json:"incoming,omitempty"`
}

func (c Configuration) Empty() bool { return c.Outgoing == nil && len(c.Incoming) == 0 }

func (i Inbound) Validate() error {
	if i.ID <= 0 {
		return errors.New("invalid exit inbound identity")
	}
	return validateEndpoint(i.Source, i.Port, i.Password)
}

func (o Outbound) Validate() error {
	if o.ID <= 0 || o.Version <= 0 {
		return errors.New("invalid exit link revision")
	}
	if o.External != nil {
		if err := o.External.Validate(); err != nil {
			return err
		}
	} else if err := validateEndpoint(o.Address, o.Port, o.Password); err != nil {
		return err
	}
	return o.Policy.Normalize()
}

func validateEndpoint(address string, port int, password string) error {
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.IsValid() || ip.Zone() != "" || ip.Is4In6() || ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("invalid exit link IP address")
	}
	if port < 1 || port > 65535 {
		return errors.New("invalid exit link port")
	}
	value, err := base64.StdEncoding.Strict().DecodeString(password)
	if err != nil || len(value) != 32 || base64.StdEncoding.EncodeToString(value) != password {
		return errors.New("invalid exit link credential; recreate the link")
	}
	return nil
}

func NewPassword() (string, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", fmt.Errorf("generate exit link key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key[:]), nil
}

type Probe struct {
	Revision
	CheckedAt  int64 `json:"checked_at"`
	Failures   int   `json:"failures"`
	Available  *bool `json:"available,omitempty"`
	PolicyDown bool  `json:"policy_down,omitempty"`
}

func (p Probe) DownFor(revision Revision) bool {
	return p.Revision == revision && p.PolicyDown
}

// WithResult waits for two failures before changing the policy. A successful
// probe recovers immediately.
func (p Probe) WithResult(revision Revision, now time.Time, success bool) Probe {
	if p.Revision != revision {
		p = Probe{Revision: revision}
	}
	p.CheckedAt = now.Unix()
	if success {
		p.Failures, p.Available = 0, new(true)
	} else {
		p.Failures = min(2, p.Failures+1)
		if p.Failures == 2 {
			p.Available = new(false)
		}
	}
	return p
}

type Totals struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

func (t Totals) Valid() bool { return t.Up >= 0 && t.Down >= 0 }
