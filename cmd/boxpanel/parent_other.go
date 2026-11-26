//go:build !linux

package main

// Migration runs only on Linux; other systems have no parent-death signal.
func stopWithParent() bool { return true }
