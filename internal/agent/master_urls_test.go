package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/revfanc/boxpanel/internal/certs"
)

func TestMasterURLSignatureAndLegacyHeartbeat(t *testing.T) {
	key, err := certs.GenerateTLSKey()
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	pin := certs.SPKIPin(spki)
	nonce := strings.Repeat("A", 22)
	urls := []string{"https://9.9.9.9:2053/panel/", "https://old.example.com:2053/panel/"}
	reply := HeartbeatReply{DesiredGeneration: 7, Protocol: ProtocolVersion, ReadOnly: true}
	if err := SignMasterURLs(&reply, urls, nonce, key); err != nil || !VerifyMasterURLs(reply, pin, nonce) {
		t.Fatal("valid ordered addresses failed verification", err)
	}
	if VerifyMasterURLs(reply, pin, "BQ"+nonce[2:]) || VerifyMasterURLs(reply, "", nonce) {
		t.Fatal("accepted a replay or verified without a pinned signing key")
	}
	forged := reply
	forged.MasterURLs = slices.Clone(reply.MasterURLs)
	slices.Reverse(forged.MasterURLs)
	if VerifyMasterURLs(forged, pin, nonce) {
		t.Fatal("accepted a reordered address list")
	}
	forged.MasterURLs[0] = "https://forged.example.com/"
	if VerifyMasterURLs(forged, pin, nonce) {
		t.Fatal("accepted a tampered address")
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignMasterURLs(&forged, urls, nonce, other); err != nil || VerifyMasterURLs(forged, pin, nonce) {
		t.Fatal("accepted the wrong signing key", err)
	}
	blob, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Generation int64 `json:"desired_generation"`
		Protocol   int   `json:"protocol"`
	}
	if err := json.Unmarshal(blob, &legacy); err != nil || legacy.Generation != 7 || legacy.Protocol != ProtocolVersion {
		t.Fatal("old heartbeat decoder cannot read the response", err)
	}
}
