//go:build !darwin && !linux

package main

import (
	"fmt"
	"os"
)

func hideTerminal(*os.File) (func(), error) {
	return nil, fmt.Errorf("请使用 --passphrase-file 指定口令文件")
}
