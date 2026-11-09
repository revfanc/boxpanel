package confgen

// These intentionally small output types track only the supported 1.14 fields.
// They prevent stored panel options from leaking into sing-box's schema.
type config struct {
	Log          logConfig          `json:"log"`
	DNS          dnsConfig          `json:"dns"`
	Inbounds     []inboundConfig    `json:"inbounds"`
	Outbounds    []outboundConfig   `json:"outbounds"`
	Route        routeConfig        `json:"route"`
	Experimental experimentalConfig `json:"experimental"`
}

type logConfig struct {
	Level     string `json:"level"`
	Timestamp bool   `json:"timestamp"`
}

type dnsConfig struct {
	Servers []dnsServer `json:"servers"`
	Final   string      `json:"final"`
}

type dnsServer struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

type inboundConfig struct {
	Type                  string       `json:"type"`
	Tag                   string       `json:"tag"`
	Listen                string       `json:"listen"`
	ListenPort            int          `json:"listen_port"`
	Users                 []serverUser `json:"users"`
	UpMbps                int          `json:"up_mbps,omitempty"`
	DownMbps              int          `json:"down_mbps,omitempty"`
	IgnoreClientBandwidth bool         `json:"ignore_client_bandwidth,omitempty"`
	Obfs                  *obfsConfig  `json:"obfs,omitempty"`
	TLS                   tlsConfig    `json:"tls"`
}

type serverUser struct {
	Name     string `json:"name"`
	UUID     string `json:"uuid,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Password string `json:"password,omitempty"`
}

type obfsConfig struct {
	Type     string `json:"type"`
	Password string `json:"password"`
}

type tlsConfig struct {
	Enabled         bool           `json:"enabled"`
	ServerName      string         `json:"server_name,omitempty"`
	ALPN            []string       `json:"alpn,omitempty"`
	CertificatePath string         `json:"certificate_path,omitempty"`
	KeyPath         string         `json:"key_path,omitempty"`
	Reality         *realityConfig `json:"reality,omitempty"`
}

type realityConfig struct {
	Enabled    bool            `json:"enabled"`
	Handshake  handshakeConfig `json:"handshake"`
	PrivateKey string          `json:"private_key"`
	ShortID    []string        `json:"short_id"`
}

type handshakeConfig struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
}

type outboundConfig struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

type routeConfig struct {
	Rules                 []routeRule `json:"rules"`
	Final                 string      `json:"final"`
	DefaultDomainResolver string      `json:"default_domain_resolver"`
}

type routeRule struct {
	Protocol    string `json:"protocol,omitempty"`
	IPIsPrivate bool   `json:"ip_is_private,omitempty"`
	Action      string `json:"action"`
	Server      string `json:"server,omitempty"`
}

type experimentalConfig struct {
	ClashAPI clashAPIConfig `json:"clash_api"`
}

type clashAPIConfig struct {
	ExternalController string `json:"external_controller"`
	Secret             string `json:"secret"`
}
