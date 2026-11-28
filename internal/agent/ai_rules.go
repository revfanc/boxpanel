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

const aiRefreshInterval = 24 * time.Hour

// Reserve each day's attempt before going online. All networking happens
// outside SQLite and Apply locks. A failed attempt leaves the last snapshot
// and its running configuration untouched, including across process restarts.
func (m *ExitMonitor) refreshAIRules(ctx context.Context) error {
	now := m.currentTime()
	if now.Before(m.nextAIRefresh) {
		return nil
	}
	config, err := db.ReadLocalExit(ctx, m.Reader)
	if err != nil || !config.Outgoing.UsesAIRules() {
		return err
	}
	var due bool
	err = m.update(ctx, func(ctx context.Context, tx *sql.Tx) error {
		previous, err := db.GetSetting(ctx, tx, db.ExitAILastAttempt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && now.Before(time.Unix(previous, 0).Add(aiRefreshInterval)) {
			m.nextAIRefresh = time.Unix(previous, 0).Add(aiRefreshInterval)
			return nil
		}
		due = true
		return db.SetSetting(ctx, tx, db.ExitAILastAttempt, now.Unix())
	})
	if err != nil || !due {
		return err
	}
	m.nextAIRefresh = now.Add(aiRefreshInterval)
	fetch := m.fetchAIRules
	if fetch == nil {
		fetch = func(ctx context.Context) (exitlink.DestinationRules, error) {
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			return downloadAIRules(ctx, client, exitlink.AIDomainURL, exitlink.VoicePrefixURL)
		}
	}
	rules, err := fetch(ctx)
	if err != nil {
		return err
	}
	return m.Manager.Apply(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// If the exit was disabled during the download the cached data is
		// still usable on the next activation. Snapshot selects it only for AI.
		return db.SetSetting(ctx, tx, db.ExitAIRules, rules)
	})
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
