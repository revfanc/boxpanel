// Package fwmark defines the reserved Linux socket/conntrack mark range.
package fwmark

import "fmt"

const Base uint32 = 0xb7000000
const MaxUserID int64 = 0x00ffffff

func User(id int64) (uint32, error) {
	if id <= 0 || id > MaxUserID {
		return 0, fmt.Errorf("user ID must be within 1–%d for traffic accounting; restore a supported user ID before retrying", MaxUserID)
	}
	return Base + uint32(id), nil
}
