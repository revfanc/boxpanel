package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/revfanc/boxpanel/internal/db"
	"github.com/revfanc/boxpanel/internal/exitlink"
)

const ruleRefreshInterval = 24 * time.Hour

// Reserve each preset's attempt durably before going online. Downloads run
// outside SQLite and Apply locks. Apply all successful presets together.
func (m *ExitMonitor) refreshExitRules(ctx context.Context) error {
	config, err := db.ReadLocalExit(ctx, m.Reader)
	if err != nil {
		return err
	}
	now := m.currentTime()
	if m.nextRuleRefresh == nil {
		m.nextRuleRefresh = make(map[string]time.Time)
	}
	var due []db.ExitPresetSettings
	for _, preset := range db.ExitPresets {
		if config.Outgoing.UsesPreset(preset.Name) && !now.Before(m.nextRuleRefresh[preset.Name]) {
			due = append(due, preset)
		}
	}
	if len(due) == 0 {
		return nil
	}
	var reserved []db.ExitPresetSettings
	next := make(map[string]time.Time)
	if err := m.update(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, preset := range due {
			previous, err := db.GetSetting(ctx, tx, preset.LastAttempt)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && now.Before(time.Unix(previous, 0).Add(ruleRefreshInterval)) {
				next[preset.Name] = time.Unix(previous, 0).Add(ruleRefreshInterval)
				continue
			}
			if err := db.SetSetting(ctx, tx, preset.LastAttempt, now.Unix()); err != nil {
				return err
			}
			next[preset.Name] = now.Add(ruleRefreshInterval)
			reserved = append(reserved, preset)
		}
		return nil
	}); err != nil {
		return err
	}
	for name, when := range next {
		m.nextRuleRefresh[name] = when
	}
	fetch := m.fetchRules
	if fetch == nil {
		fetch = downloadPresetRules
	}
	var failures error
	updates := make(map[string]exitlink.DestinationRules)
	for _, preset := range reserved {
		rules, err := fetch(ctx, preset.Name)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("refresh %s rules: %w", preset.Name, err))
			continue
		}
		updates[preset.Name] = rules
	}
	if len(updates) == 0 {
		return failures
	}
	err = m.Manager.Apply(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, preset := range reserved {
			if rules, ok := updates[preset.Name]; ok {
				if err := db.SetSetting(ctx, tx, preset.Rules, rules); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return errors.Join(failures, err)
}

func downloadPresetRules(ctx context.Context, preset string) (exitlink.DestinationRules, error) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	switch preset {
	case exitlink.AIPreset:
		return downloadAIRules(ctx, client, exitlink.AIDomainURL, exitlink.VoicePrefixURL)
	case exitlink.StreamingPreset:
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return exitlink.LoadStreamingRules(func(_, url string) ([]byte, error) { return readRuleSource(ctx, client, url) })
	default:
		return exitlink.DestinationRules{}, fmt.Errorf("unknown exit rule preset")
	}
}

func downloadAIRules(ctx context.Context, client *http.Client, domainURL, voiceURL string) (exitlink.DestinationRules, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	domains, err := readRuleSource(ctx, client, domainURL)
	if err != nil {
		return exitlink.DestinationRules{}, fmt.Errorf("download AI domains: %w", err)
	}
	voice, err := readRuleSource(ctx, client, voiceURL)
	if err != nil {
		return exitlink.DestinationRules{}, fmt.Errorf("download ChatGPT Voice ranges: %w", err)
	}
	return exitlink.ParseAIRules(domains, voice)
}

func readRuleSource(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	const maxBytes = 1 << 20
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read rule source: %w", err)
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("rule source exceeds %d bytes", maxBytes)
	}
	return data, nil
}
