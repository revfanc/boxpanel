// Package subscription renders client subscriptions from a consistent snapshot.
package subscription

import (
	"fmt"
	"net/url"
	"strings"
)

type Format string

const (
	Singbox Format = "singbox"
	Clash   Format = "clash"
	Base64  Format = "base64"
	HTML    Format = "html"
)

func Detect(query url.Values, ua, accept string) (Format, error) {
	if values, ok := query["format"]; ok {
		if len(values) != 1 {
			return "", fmt.Errorf("format: provide one supported format")
		}
		f := Format(values[0])
		if f != Singbox && f != Clash && f != Base64 {
			return "", fmt.Errorf("format: use singbox, clash or base64")
		}
		return f, nil
	}
	ua = strings.ToLower(ua)
	for _, s := range []string{"sing-box", "sfa", "sfi", "sfm", "sft"} {
		if strings.Contains(ua, s) {
			return Singbox, nil
		}
	}
	for _, s := range []string{"clash", "mihomo", "stash", "verge", "flclash", "nyanpasu"} {
		if strings.Contains(ua, s) {
			return Clash, nil
		}
	}
	if strings.Contains(ua, "mozilla") && strings.Contains(strings.ToLower(accept), "text/html") {
		return HTML, nil
	}
	return Base64, nil
}
