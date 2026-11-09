package reality

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func checkKeyPair(t *testing.T, pair KeyPair) {
	t.Helper()
	for _, encoded := range []string{pair.PrivateKey, pair.PublicKey} {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
		if err != nil || len(raw) != 32 || len(encoded) != 43 {
			t.Fatal("expected 32-byte key in unpadded base64url")
		}
	}
	raw, _ := base64.RawURLEncoding.DecodeString(pair.PrivateKey)
	if raw[0]&7 != 0 || raw[31]&192 != 64 {
		t.Fatal("private scalar is not clamped as in sing-box")
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	if base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()) != pair.PublicKey {
		t.Fatal("public key does not correspond to private key")
	}
	public, err := PublicKey(pair.PrivateKey)
	if err != nil || public != pair.PublicKey {
		t.Fatal("public key derivation failed")
	}
}

func TestCredentials(t *testing.T) {
	seenKeys, seenIDs := map[string]bool{}, map[string]bool{}
	for range 32 {
		pair, err := GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}
		checkKeyPair(t, pair)
		id, err := GenerateShortID()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := hex.DecodeString(id)
		if err != nil || len(raw) != 8 || id != strings.ToLower(id) {
			t.Fatal("invalid short ID")
		}
		if seenKeys[pair.PrivateKey] || seenIDs[id] {
			t.Fatal("duplicate random credential")
		}
		seenKeys[pair.PrivateKey], seenIDs[id] = true, true
	}
	for _, key := range []string{"", "private-secret!", strings.Repeat("A", 42), strings.Repeat("A", 43) + "=", strings.Repeat("A", 42) + "B"} {
		if _, err := PublicKey(key); err == nil || key != "" && strings.Contains(err.Error(), key) {
			t.Fatal("invalid private key accepted or exposed in error")
		}
	}
}

func TestSingBoxKeyPairFormat(t *testing.T) {
	binary, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("sing-box not in PATH; run make tools and make test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "generate", "reality-keypair").Output()
	if err != nil {
		t.Fatal("sing-box generate reality-keypair failed:", err)
	}
	var official KeyPair
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		label, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatal("unexpected key pair output format")
		}
		switch label {
		case "PrivateKey":
			official.PrivateKey = strings.TrimSpace(value)
		case "PublicKey":
			official.PublicKey = strings.TrimSpace(value)
		default:
			t.Fatal("unexpected key pair output label")
		}
	}
	checkKeyPair(t, official)
	generated, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	checkKeyPair(t, generated)
	if len(generated.PrivateKey) != len(official.PrivateKey) || len(generated.PublicKey) != len(official.PublicKey) {
		t.Fatal("local and sing-box key formats differ")
	}
}
