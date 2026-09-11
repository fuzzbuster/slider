//go:build linux

package interpreter

import (
	"os"
	"strings"
)

func nativeProcessName() string {
	name, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(name), "\x00\r\n")
}
