//go:build !linux && !darwin

package singbox

import "context"

func lockApply(context.Context, string) (func(), error) { return nil, ErrUnsupported }
