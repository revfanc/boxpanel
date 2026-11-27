package exitlink

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"slices"
	"testing"
)

func TestPortInventoryRoundTripAndBounds(t *testing.T) {
	sparse := EncodePorts([]int{22, PortLast, PortFirst, PortLast, 65535})
	if len(sparse) > 100 {
		t.Fatal("sparse ports should fit into a small heartbeat")
	}
	if result, err := DecodePorts(sparse); err != nil || !slices.Equal(result, []int{PortFirst, PortLast}) {
		t.Fatal(result, err)
	}
	if result, err := DecodePorts(EncodePorts(nil)); err != nil || len(result) != 0 {
		t.Fatal("empty inventory is a valid sample", result, err)
	}
	var dense []int
	for port := PortFirst; port <= PortLast; port++ {
		if port%4 != 0 {
			dense = append(dense, port)
		}
	}
	if result, err := DecodePorts(EncodePorts(dense)); err != nil || !slices.Equal(result, dense) {
		t.Fatal("dense list lost ports", err)
	}
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write(make([]byte, portBitmapSize+2))
	_ = zw.Close()
	for _, invalid := range []string{"", "!", base64.RawStdEncoding.EncodeToString(compressed.Bytes())} {
		if _, err := DecodePorts(invalid); err == nil {
			t.Fatal("accepted malformed or oversize port inventory")
		}
	}
}
