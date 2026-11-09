package share

import (
	"net/url"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/confgen"
)

func TestURI(t *testing.T) {
	for _, host := range []string{"node.example", "192.0.2.1", "2001:db8::1"} {
		for _, hy := range []bool{false, true} {
			n := Node{Name: "特殊 #&? +/", Host: host, Port: 443, UUID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Reality: &Reality{"www.example.com", "public", "01234567", "chrome"}}
			if hy {
				n.Reality = nil
				n.Password = "p@ss:/?# +中文"
				n.Fingerprint = strings.Repeat("ab", 32)
				n.Hysteria2 = &confgen.Hysteria2Options{SNI: "www.bing.com", ObfsPassword: "o&?# +", PortHopping: confgen.PortHopping{Enabled: true, Start: 20000, End: 30000}}
			}
			raw, err := URI(n)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(raw)
			if err != nil || u.Hostname() != host || u.Port() != "443" || u.Fragment != n.Name {
				t.Fatal(raw, err)
			}
			if hy {
				if u.User.Username() != n.Password || u.Query().Get("obfs-password") != n.Hysteria2.ObfsPassword || u.Query().Get("pinSHA256") != n.Fingerprint || u.Query().Get("mport") != "20000-30000" {
					t.Fatal(raw)
				}
			} else if u.Query().Get("security") != "reality" || u.Query().Get("flow") != "xtls-rprx-vision" {
				t.Fatal(raw)
			}
		}
	}
}
