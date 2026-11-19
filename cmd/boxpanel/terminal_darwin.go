//go:build darwin

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func hideTerminal(f *os.File) (func(), error) {
	var old syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&old))); e != 0 {
		return nil, fmt.Errorf("读取终端失败；请使用 --passphrase-file")
	}
	next := old
	next.Lflag &^= syscall.ECHO | syscall.ECHONL
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCSETA, uintptr(unsafe.Pointer(&next))); e != 0 {
		return nil, fmt.Errorf("关闭终端回显失败；请使用 --passphrase-file")
	}
	return func() {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCSETA, uintptr(unsafe.Pointer(&old)))
		fmt.Fprintln(f)
	}, nil
}
