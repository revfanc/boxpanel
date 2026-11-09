package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/revfanc/boxpanel/internal/confgen"
	"github.com/revfanc/boxpanel/internal/reality"
)

func defaultInboundOptions(protocol, target string) (json.RawMessage, error) {
	switch protocol {
	case "vless_reality":
		pair, err := reality.GenerateKeyPair()
		if err != nil {
			return nil, err
		}
		id, err := reality.GenerateShortID()
		if err != nil {
			return nil, err
		}
		return json.Marshal(confgen.RealityOptions{ServerName: target, HandshakeServer: target, HandshakePort: 443, PrivateKey: pair.PrivateKey, PublicKey: pair.PublicKey, ShortIDs: []string{id}, Fingerprint: "chrome"})
	case "hysteria2":
		return json.Marshal(confgen.Hysteria2Options{SNI: "www.bing.com", CertMode: "self_signed", PortHopping: confgen.PortHopping{Start: 20000, End: 30000}})
	default:
		return nil, invalid("protocol", "请选择 vless_reality 或 hysteria2。")
	}
}

func optionFields(raw json.RawMessage) map[string]json.RawMessage {
	var values map[string]json.RawMessage
	_ = json.Unmarshal(raw, &values)
	result := map[string]json.RawMessage{}
	for key, value := range values {
		result[strings.ToLower(key)] = value
	}
	return result
}
func mergeInboundOptions(protocol string, old, patch json.RawMessage) (json.RawMessage, error) {
	if len(patch) == 0 {
		return old, nil
	}
	var result any
	switch protocol {
	case "vless_reality":
		var options confgen.RealityOptions
		if json.Unmarshal(old, &options) != nil {
			return nil, fmt.Errorf("decode stored Reality options")
		}
		if strictObject(patch, &options) != nil {
			return nil, invalid("options", "请按 CONFIG.md 的 Reality 字段填写参数，不要使用 null 或未知字段。")
		}
		fields := optionFields(patch)
		if _, ok := fields["private_key"]; ok {
			public, err := reality.PublicKey(options.PrivateKey)
			if err != nil {
				return nil, invalid("options.private_key", "请提供有效的 X25519 私钥，或在创建时省略密钥让服务器生成。")
			}
			if _, supplied := fields["public_key"]; !supplied {
				options.PublicKey = public
			}
		}
		result = options
	case "hysteria2":
		var options confgen.Hysteria2Options
		if json.Unmarshal(old, &options) != nil {
			return nil, fmt.Errorf("decode stored Hysteria2 options")
		}
		if strictObject(patch, &options) != nil {
			return nil, invalid("options", "请按 CONFIG.md 的 Hysteria2 字段填写参数，不要使用 null 或未知字段。")
		}
		result = options
	default:
		return nil, invalid("protocol", "请选择 vless_reality 或 hysteria2。")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode inbound options: %w", err)
	}
	return encoded, nil
}

func validateInbound(in inboundRecord) error {
	if in.Protocol != "vless_reality" && in.Protocol != "hysteria2" {
		return invalid("protocol", "请选择 vless_reality 或 hysteria2。")
	}
	if in.Port < 1 || in.Port > 65535 {
		return invalid("port", "请使用 1–65535 之间的整数端口。")
	}
	if !validTag(in.Tag) {
		return invalid("tag", "标识限 1–64 位字母、数字、下划线或连字符。")
	}
	if len(in.Remark) > 1024 {
		return invalid("remark", "请将备注缩短到 1024 字节以内。")
	}
	if in.Protocol == "vless_reality" {
		var options confgen.RealityOptions
		if json.Unmarshal(in.Options, &options) != nil {
			return invalid("options", "请重新填写 Reality 参数。")
		}
		if len(options.ShortIDs) > 16 || !validTag(options.Fingerprint) || !reality.ValidServer(options.ServerName) || !reality.ValidServer(options.HandshakeServer) {
			return invalid("options", "请使用公网域名或 IP、1–16 个 short_ids，并填写有效的客户端指纹名称。")
		}
	} else {
		var options confgen.Hysteria2Options
		if json.Unmarshal(in.Options, &options) != nil {
			return invalid("options", "请重新填写 Hysteria2 参数。")
		}
		if len(options.ObfsPassword) > 1024 {
			return invalid("options.obfs_password", "请将混淆密码缩短到 1024 字节以内。")
		}
		h := options.PortHopping
		if h.Start < 1 || h.End > 65535 || h.Start > h.End {
			return invalid("options.port_hopping", "请填写 1–65535 范围内从小到大的端口范围。")
		}
	}
	// Reuse the generator's protocol validation, even for disabled inbounds.
	// No files, certificates or system commands are involved here.
	control := 1
	if in.Port == control {
		control = 2
	}
	_, err := confgen.Build(confgen.Snapshot{Inbounds: []confgen.Inbound{{ID: 1, Tag: in.Tag, Protocol: in.Protocol, Port: in.Port, Enabled: true, Options: in.Options}}}, confgen.Options{Now: time.Unix(1, 0), ClashAPIPort: control, ClashAPISecret: "validation", Hysteria2TLS: map[int64]confgen.CertificateFiles{1: {CertificatePath: "/validation/cert", KeyPath: "/validation/key"}}})
	if err != nil {
		return invalid("options", "请检查协议参数、Reality 公私钥对应关系和 short_ids，或 Hysteria2 的 SNI、自签证书模式和非负带宽。")
	}
	return nil
}
func validTag(tag string) bool {
	if len(tag) == 0 || len(tag) > 64 {
		return false
	}
	for _, c := range tag {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func publicInbound(in inboundRecord) (inboundRecord, error) {
	var options map[string]json.RawMessage
	if json.Unmarshal(in.Options, &options) != nil {
		return in, fmt.Errorf("decode inbound response options")
	}
	// Only protocol-defined public fields may leave the API; unknown fields in
	// a damaged DB must not accidentally expose additional credentials.
	allowed := "server_name handshake_server handshake_port public_key short_ids fingerprint"
	if in.Protocol == "hysteria2" {
		allowed = "sni cert_mode up_mbps down_mbps port_hopping"
	}
	clean := map[string]json.RawMessage{}
	for _, name := range strings.Fields(allowed) {
		if value, ok := options[name]; ok {
			clean[name] = value
		}
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return in, fmt.Errorf("encode inbound response: %w", err)
	}
	in.Options = raw
	return in, nil
}
