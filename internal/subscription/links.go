package subscription

import (
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/revfanc/boxpanel/internal/share"
)

type Links struct {
	Subscription map[string]string `json:"subscription"`
	Import       map[string]string `json:"import"`
	Nodes        []NodeLink        `json:"nodes"`
}
type NodeLink struct {
	InboundID int64  `json:"inbound_id"`
	Remark    string `json:"remark"`
	URI       string `json:"uri"`
}

func MakeLinks(d Data) (Links, error) {
	scheme := "https"
	if d.TLSMode == "http" {
		scheme = "http"
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(d.PublicHost, strconv.Itoa(d.WebPort)), Path: "/" + d.SubPath + "/" + d.User.Token}
	l := Links{Subscription: map[string]string{"auto": u.String()}, Import: map[string]string{}, Nodes: []NodeLink{}}
	for _, format := range []string{"singbox", "clash", "base64"} {
		u.RawQuery = url.Values{"format": {format}}.Encode()
		l.Subscription[format] = u.String()
	}
	for _, format := range []string{"singbox", "clash"} {
		scheme, host := "sing-box", "import-remote-profile"
		q := url.Values{"url": {l.Subscription[format]}}
		if format == "clash" {
			scheme, host = "clash", "install-config"
			q.Set("name", d.PanelName)
		}
		deep := url.URL{Scheme: scheme, Host: host, RawQuery: q.Encode()}
		if format == "singbox" {
			deep.Fragment = d.PanelName
		}
		l.Import[format] = deep.String()
	}
	for _, n := range d.Nodes {
		uri, err := share.URI(n)
		if err != nil {
			return l, fmt.Errorf("create subscription links: %w", err)
		}
		l.Nodes = append(l.Nodes, NodeLink{n.ID, n.Name, uri})
	}
	return l, nil
}
