// Package share encodes client node URIs, without reading files or secrets itself.
package share

import (
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/revfanc/boxpanel/internal/confgen"
)

// Node contains exportable client material only, never a server private key.
type Node struct {
	ID                       int64
	Name, Host               string
	Port                     int
	UUID, Password           string
	Reality                  *Reality
	Hysteria2                *confgen.Hysteria2Options
	Certificate, Fingerprint string
}
type Reality struct{ ServerName, PublicKey, ShortID, Fingerprint string }

func URI(n Node) (string, error) {
	u := url.URL{Host: net.JoinHostPort(n.Host, strconv.Itoa(n.Port)), Fragment: n.Name}
	q := url.Values{}
	switch {
	case n.Reality != nil:
		o := n.Reality
		u.Scheme = "vless"
		u.User = url.User(n.UUID)
		q = url.Values{"encryption": {"none"}, "flow": {"xtls-rprx-vision"}, "security": {"reality"}, "sni": {o.ServerName}, "fp": {o.Fingerprint}, "pbk": {o.PublicKey}, "sid": {o.ShortID}, "type": {"tcp"}}
	case n.Hysteria2 != nil:
		o := n.Hysteria2
		if n.Fingerprint == "" {
			return "", fmt.Errorf("share Hysteria2: missing certificate fingerprint; apply the inbound first")
		}
		u.Scheme = "hysteria2"
		u.User = url.User(n.Password)
		u.Path = "/"
		q.Set("sni", o.SNI)
		q.Set("insecure", "1")
		q.Set("pinSHA256", n.Fingerprint)
		if o.ObfsPassword != "" {
			q.Set("obfs", "salamander")
			q.Set("obfs-password", o.ObfsPassword)
		}
		if h := o.PortHopping; h.Enabled {
			q.Set("mport", fmt.Sprintf("%d-%d", h.Start, h.End))
		}
	default:
		return "", fmt.Errorf("share node: unsupported protocol; recreate the inbound")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
