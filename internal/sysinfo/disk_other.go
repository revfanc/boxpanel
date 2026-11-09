//go:build !linux

package sysinfo

func diskUsage(string) (Usage, error) { return Usage{}, ErrUnsupported }
