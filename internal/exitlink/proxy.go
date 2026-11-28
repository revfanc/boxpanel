package exitlink

import (
	"errors"
	"strings"
	"unicode"

	"github.com/revfanc/boxpanel/internal/reality"
)

// Proxy is private to the entry server. Panel responses use a separate view.
type Proxy struct {
	Type     string `json:"type"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func (p Proxy) Validate() error {
	if p.Type != "socks5" && p.Type != "http" && p.Type != "https" {
		return errors.New("invalid proxy protocol")
	}
	if !reality.ValidServer(p.Host) || p.Port < 1 || p.Port > 65535 {
		return errors.New("invalid proxy endpoint")
	}
	if len(p.Username) > 255 || len(p.Password) > 255 || strings.IndexFunc(p.Username+p.Password, unicode.IsControl) >= 0 ||
		(p.Username == "") != (p.Password == "") || p.Type != "socks5" && strings.Contains(p.Username, ":") {
		return errors.New("invalid proxy credentials")
	}
	return nil
}
