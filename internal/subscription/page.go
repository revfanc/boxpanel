package subscription

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/web"
	qrcode "github.com/skip2/go-qrcode"
)

// UI text and the client catalog are embedded from the same sources as the SPA.
// No external scripts, QR services or resources receive the subscription token.
//
//go:embed page.html
var pageSource string

var page = template.Must(template.New("subscription").Funcs(template.FuncMap{"join": strings.Join}).Parse(pageSource))

type pageAddress struct{ ID, Label, URL string }
type pageData struct {
	Text                                   map[string]string
	Name, User, Nonce                      string
	QR, SingboxImport, ClashImport         template.URL
	Auto                                   pageAddress
	Formats                                []pageAddress
	Clients                                []web.SubscriptionClient
	Used, Total, Expire, Remaining, Status string
	Progress                               float64
}

// Page takes the response nonce and clock explicitly so golden output is stable.
// The public handler still rejects inactive users before rendering any page.
func Page(d Data, nonce string, now time.Time) ([]byte, error) {
	l, err := MakeLinks(d)
	if err != nil {
		return nil, err
	}
	png, err := qrcode.Encode(l.Subscription["auto"], qrcode.Medium, 256)
	if err != nil {
		return nil, fmt.Errorf("encode subscription QR: %w", err)
	}
	text := web.SubscriptionText()
	// Each counter fits int64, but their sum need not. uint64 holds both.
	used := uint64(max(0, d.User.TrafficUp)) + uint64(max(0, d.User.TrafficDown))
	v := pageData{
		Text: text, Name: d.PanelName, User: d.User.Name, Nonce: nonce,
		QR: template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)),
		// These are locally constructed, known-scheme URLs, never arbitrary input.
		SingboxImport: template.URL(l.Import["singbox"]), ClashImport: template.URL(l.Import["clash"]),
		Auto:    pageAddress{"auto", text["subscription"], l.Subscription["auto"]},
		Clients: web.SubscriptionClients(), Used: pageBytes(used), Total: text["unlimited"],
		Expire: text["never"], Status: "active",
	}
	for _, format := range []string{"singbox", "clash", "base64"} {
		v.Formats = append(v.Formats, pageAddress{format, text[format], l.Subscription[format]})
	}
	if d.User.TrafficLimit > 0 {
		v.Total = pageBytes(uint64(d.User.TrafficLimit))
		v.Progress = math.Min(100, float64(used)/float64(d.User.TrafficLimit)*100)
	}
	if d.User.ExpireAt > 0 {
		v.Expire = fmt.Sprintf(text["expiresOn"], time.Unix(d.User.ExpireAt, 0).UTC().Format("2006-01-02"))
		seconds := max(int64(0), d.User.ExpireAt-now.Unix())
		days := seconds / 86400
		if seconds%86400 > 0 {
			days++
		}
		v.Remaining = fmt.Sprintf(text["remainingDays"], days)
	}
	switch {
	case !d.User.Enabled:
		v.Status = "disabled"
	case d.User.ExpireAt > 0 && d.User.ExpireAt <= now.Unix():
		v.Status = "expired"
	case d.User.TrafficLimit > 0 && used >= uint64(d.User.TrafficLimit):
		v.Status = "exhausted"
	}
	var b bytes.Buffer
	if err := page.Execute(&b, v); err != nil {
		return nil, fmt.Errorf("render subscription page: %w", err)
	}
	return b.Bytes(), nil
}

func pageBytes(n uint64) string {
	units := [...]string{"B", "KB", "MB", "GB", "TB"}
	value, unit := float64(n), 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", n) // whole bytes: "0 B", not "0.00 B"
	}
	return fmt.Sprintf("%.2f %s", value, units[unit])
}
