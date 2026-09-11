//go:build darwin

package interpreter

import (
	"os"

	"golang.org/x/sys/unix"
)

func nativeProcessName() string {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid())
	if err != nil {
		return ""
	}
	return unix.ByteSliceToString(process.Proc.P_comm[:])
}
