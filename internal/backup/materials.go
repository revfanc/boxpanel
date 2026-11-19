package backup

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"

	"github.com/revfanc/boxpanel/internal/db"
)

// Refuse to regenerate pinned materials that a damaged archive omitted. Expiry
// is left to the normal activation check; no new Hysteria2 identity is invented.
func validateMaterials(ctx context.Context, conn *sql.DB, files map[string][]byte) error {
	mode, err := db.GetSetting(ctx, conn, db.PanelTLSMode)
	if err != nil {
		return ErrInvalid
	}
	if mode != "http" {
		if _, err = tls.X509KeyPair(files["certs/panel.crt"], files["certs/panel.key"]); err != nil {
			return ErrInvalid
		}
	}
	raw, err := db.GetSetting(ctx, conn, db.PanelCertificate)
	if err == nil {
		var state struct {
			File string `json:"file"`
		}
		if json.Unmarshal(raw, &state) != nil {
			return ErrInvalid
		}
		if state.File != "" {
			if path.Base(state.File) != state.File {
				return ErrInvalid
			}
			b := files["certs/"+state.File]
			if _, err = tls.X509KeyPair(b, b); err != nil {
				return ErrInvalid
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ErrInvalid
	}
	fingerprints, err := db.GetSetting(ctx, conn, db.Hysteria2Fingerprints)
	if errors.Is(err, sql.ErrNoRows) {
		fingerprints = map[string]string{}
	} else if err != nil {
		return ErrInvalid
	}
	rows, err := conn.QueryContext(ctx, "SELECT id,options FROM inbounds WHERE protocol='hysteria2' AND enabled=1 AND server_id=(SELECT id FROM servers WHERE is_local=1)")
	if err != nil {
		return ErrInvalid
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw string
		if rows.Scan(&id, &raw) != nil {
			return ErrInvalid
		}
		expected := fingerprints[strconv.FormatInt(id, 10)]
		if expected == "" {
			continue
		}
		var opts struct {
			SNI string `json:"sni"`
		}
		if json.Unmarshal([]byte(raw), &opts) != nil {
			return ErrInvalid
		}
		hash := sha256.Sum256([]byte(opts.SNI))
		base := fmt.Sprintf("certs/hy2-%d-%x", id, hash[:8])
		pair, e := tls.X509KeyPair(files[base+".crt"], files[base+".key"])
		if e != nil {
			return ErrInvalid
		}
		leaf, e := x509.ParseCertificate(pair.Certificate[0])
		if e != nil || leaf.VerifyHostname(opts.SNI) != nil {
			return ErrInvalid
		}
		sum := sha256.Sum256(pair.Certificate[0])
		if hex.EncodeToString(sum[:]) != expected {
			return ErrInvalid
		}
	}
	if rows.Err() != nil {
		return ErrInvalid
	}
	return nil
}
