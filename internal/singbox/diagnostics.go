package singbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

func sensitiveKey(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "password") || strings.Contains(key, "private_key") || strings.Contains(key, "token") || key == "secret" || key == "uuid"
}
func jsonStrings(data []byte) []string {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return nil
	}
	var out []string
	var walk func(any, bool)
	walk = func(v any, sensitive bool) {
		switch v := v.(type) {
		case map[string]any:
			for key, item := range v {
				walk(item, sensitive || sensitiveKey(key))
			}
		case []any:
			for _, item := range v {
				walk(item, sensitive)
			}
		case string:
			if sensitive {
				out = append(out, v)
			}
		}
	}
	walk(value, false)
	return out
}

var uuidPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
var opaquePattern = regexp.MustCompile(`[A-Za-z0-9_+/=-]{32,}`)
var credentialLine = regexp.MustCompile(`(?i)(password|private[_ -]?key|secret|token|authorization|vless://|hysteria2://)`)

// Do not pass raw output to errors/loggers. Known current, candidate and backup
// credentials are replaced before truncation; escaped/URL forms are included.
func redactText(value string, secrets []string) string {
	secrets = slices.Clone(secrets)
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		encoded, _ := json.Marshal(secret)
		for _, form := range []string{secret, string(encoded[1 : len(encoded)-1]), strings.NewReplacer(`\u0026`, "&", `\u003c`, "<", `\u003e`, ">").Replace(string(encoded[1 : len(encoded)-1])), url.QueryEscape(secret), url.PathEscape(secret)} {
			value = strings.ReplaceAll(value, form, "[REDACTED]")
		}
	}
	value = uuidPattern.ReplaceAllString(value, "[REDACTED]")
	value = opaquePattern.ReplaceAllString(value, "[REDACTED]")
	lines := strings.Split(value, "\n")
	pem := false
	for i, line := range lines {
		if strings.Contains(line, "-----BEGIN") {
			pem = true
		}
		if pem || credentialLine.MatchString(line) {
			lines[i] = "[REDACTED credential-bearing line]"
		}
		if strings.Contains(line, "-----END") {
			pem = false
		}
	}
	value = strings.Join(lines, "\n")
	if len(value) > 64*1024 {
		value = value[:64*1024] + "\n[truncated]"
	}
	return value
}

func (m *Manager) journal(ctx context.Context, lines int) (string, error) {
	output, err := m.runner.Run(ctx, "journalctl", "--unit="+SingboxUnit, "--lines="+strconv.Itoa(lines), "--no-pager", "--output=cat", "--quiet")
	if err != nil {
		return "", fmt.Errorf("read core journal; check journal permissions")
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

func (m *Manager) Logs(ctx context.Context, lines int) ([]string, error) {
	if m.unsupported {
		return nil, ErrUnsupported
	}
	if lines < 1 || lines > 1000 {
		return nil, fmt.Errorf("log lines must be between 1 and 1000")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := lockApply(ctx, m.config+".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("read diagnostic credentials: %w", err)
	}
	defer tx.Rollback()
	_, secrets, err := snapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", ".bak"} {
		data, e := m.files.ReadFile(m.config + suffix)
		if e != nil && !errors.Is(e, fs.ErrNotExist) {
			return nil, fmt.Errorf("read diagnostic configuration: %w", e)
		}
		secrets = append(secrets, jsonStrings(data)...)
	}
	output, err := m.journal(ctx, lines)
	if err != nil {
		return nil, err
	}
	if output == "" {
		return []string{}, nil
	}
	result := strings.Split(redactText(output, secrets), "\n")
	if len(result) > lines {
		result = result[len(result)-lines:]
	}
	return result, nil
}

func (m *Manager) Config(ctx context.Context) (json.RawMessage, error) {
	if m.unsupported {
		return nil, ErrUnsupported
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := lockApply(ctx, m.config+".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	data, err := m.files.ReadFile(m.config)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoConfig
	}
	if err != nil {
		return nil, fmt.Errorf("read current config: %w", err)
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return nil, fmt.Errorf("decode current config; run boxpanel apply to regenerate it")
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("read preview credentials: %w", err)
	}
	defer tx.Rollback()
	_, secrets, err := snapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	secrets = append(secrets, jsonStrings(data)...)
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("current config must be a JSON object; run boxpanel apply")
	}
	maskText := func(text string) string {
		for _, secret := range secrets {
			if secret != "" {
				text = strings.ReplaceAll(text, secret, "[REDACTED]")
			}
		}
		return text
	}

	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for key, item := range v {
				if sensitiveKey(key) {
					text, ok := item.(string)
					if key == "uuid" && ok && len(text) == 36 {
						v[key] = text[:4] + "…" + text[len(text)-4:]
					} else {
						v[key] = "[REDACTED]"
					}
				} else if text, ok := item.(string); ok {
					v[key] = maskText(text)
				} else {
					walk(item)
				}
			}
		case []any:
			for i, item := range v {
				if text, ok := item.(string); ok {
					v[i] = maskText(text)
				} else {
					walk(item)
				}
			}
		}
	}
	walk(value)
	return json.Marshal(value)
}
