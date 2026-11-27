package exitlink

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"io"
)

const portBitmapSize = (PortLast - PortFirst + 1 + 7) / 8

// The bounded bitmap covers every possible internal port even on dense hosts.
func EncodePorts(ports []int) string {
	var bitmap [portBitmapSize]byte
	for _, port := range ports {
		if port >= PortFirst && port <= PortLast {
			index := port - PortFirst
			bitmap[index/8] |= 1 << (index % 8)
		}
	}
	// Most hosts listen on few ports in this range, so a compressed bitmap
	// keeps heartbeat size proportional to the number of occupied ports.
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write(bitmap[:]) // bytes.Buffer writes cannot fail
	_ = zw.Close()
	return base64.RawStdEncoding.EncodeToString(compressed.Bytes())
}

func DecodePorts(encoded string) ([]int, error) {
	if len(encoded) > base64.RawStdEncoding.EncodedLen(portBitmapSize+32) {
		return nil, errors.New("invalid exit port bitmap size")
	}
	compressed, err := base64.RawStdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, errors.New("invalid exit port bitmap encoding")
	}
	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, errors.New("invalid compressed exit port bitmap")
	}
	defer zr.Close()
	bitmap, err := io.ReadAll(io.LimitReader(zr, portBitmapSize+1))
	if err != nil || len(bitmap) != portBitmapSize {
		return nil, errors.New("invalid exit port bitmap")
	}
	var ports []int
	for index := 0; index <= PortLast-PortFirst; index++ {
		if bitmap[index/8]&(1<<(index%8)) != 0 {
			ports = append(ports, PortFirst+index)
		}
	}
	return ports, nil
}
