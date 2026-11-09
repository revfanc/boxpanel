package subscription

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func Redact(data []byte, format Format, secrets []string) ([]byte, error) {
	var v any
	var err error
	if format == Singbox {
		err = json.Unmarshal(data, &v)
	} else {
		err = yaml.Unmarshal(data, &v)
	}
	if err != nil {
		return nil, fmt.Errorf("decode preview: %w", err)
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, v := range x {
				switch k {
				case "uuid", "password", "obfs-password", "private_key", "sub_token", "token":
					out[k] = "[REDACTED]"
				default:
					out[walk(k).(string)] = walk(v)
				}
			}
			return out
		case []any:
			for i, v := range x {
				x[i] = walk(v)
			}
			return x
		case string:
			for _, secret := range secrets {
				if secret != "" {
					x = strings.ReplaceAll(x, secret, "[REDACTED]")
				}
			}
			return x
		}
		return v
	}
	v = walk(v)
	if format == Singbox {
		return json.Marshal(v)
	}
	return yaml.Marshal(v)
}
