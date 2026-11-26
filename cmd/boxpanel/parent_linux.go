package main

import (
	"os"
	"syscall"
)

// stopWithParent asks the kernel to send SIGTERM when the installer that
// started this process dies, even by SIGKILL. It reports false when the parent
// is already gone, so the caller can stop before claiming anything.
func stopWithParent() bool {
	parent := os.Getppid()
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_PDEATHSIG, uintptr(syscall.SIGTERM), 0); errno != 0 {
		return true
	}
	return os.Getppid() == parent
}
