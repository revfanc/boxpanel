package subscription

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"time"

	"github.com/revfanc/boxpanel/web"
	qrcode "github.com/skip2/go-qrcode"
)

// UI text comes from web/src/i18n/zh-CN.ts, also embedded by the web package.
// No external scripts, QR services or resources receive the subscription token.
var page = template.Must(template.New("subscription").Parse(`<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Name}}</title>
<style>body{font-family:system-ui,sans-serif;max-width:42rem;margin:3rem auto;padding:0 1rem;line-height:1.7;background:#f8fafc;color:#172033}a{color:#1753b5;overflow-wrap:anywhere}img{max-width:100%;height:auto}li{margin:.7rem 0}</style>
<h1>{{.Name}}</h1><p>{{.Text.scan}}</p><img width="256" height="256" alt="{{.Text.qr}}" src="{{.QR}}">
<p><a href="{{.Auto}}">{{.Auto}}</a></p><ul><li><a href="{{.SingboxImport}}">{{.Text.singboxImport}}</a> · <a href="{{.Singbox}}">{{.Text.singbox}}</a></li><li><a href="{{.ClashImport}}">{{.Text.clashImport}}</a> · <a href="{{.Clash}}">{{.Text.clash}}</a></li><li><a href="{{.Base64}}">{{.Text.base64}}</a></li></ul>
<p>{{.Text.upload}}：{{.Up}} {{.Text.bytes}}；{{.Text.download}}：{{.Down}} {{.Text.bytes}}；{{.Text.total}}：{{.Total}}；{{.Text.expiry}}：{{.Expire}}</p>
<p>{{.Text.compatibility}}</p><p>{{.Text.privacy}}</p></html>`))

func Page(d Data) ([]byte, error) {
	l, err := MakeLinks(d)
	if err != nil {
		return nil, err
	}
	png, err := qrcode.Encode(l.Subscription["auto"], qrcode.Medium, 256)
	if err != nil {
		return nil, fmt.Errorf("encode subscription QR: %w", err)
	}
	text := web.SubscriptionText()
	total, expire := fmt.Sprint(d.User.TrafficLimit), time.Unix(d.User.ExpireAt, 0).UTC().Format(time.RFC3339)
	if d.User.TrafficLimit == 0 {
		total = text["unlimited"]
	}
	if d.User.ExpireAt == 0 {
		expire = text["never"]
	}
	// Only locally constructed, known-scheme URLs are trusted for template URL
	// contexts; all names and other content keep html/template escaping.
	v := struct {
		Text                                 map[string]string
		Name                                 string
		QR, Auto, SingboxImport, ClashImport template.URL
		Singbox, Clash, Base64               string
		Up, Down                             int64
		Total, Expire                        string
	}{text, d.PanelName, template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)), template.URL(l.Subscription["auto"]), template.URL(l.Import["singbox"]), template.URL(l.Import["clash"]), l.Subscription["singbox"], l.Subscription["clash"], l.Subscription["base64"], d.User.TrafficUp, d.User.TrafficDown, total, expire}
	var b bytes.Buffer
	if err := page.Execute(&b, v); err != nil {
		return nil, fmt.Errorf("render subscription page: %w", err)
	}
	return b.Bytes(), nil
}
