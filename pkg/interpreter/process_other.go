//go:build !linux && !darwin && !windows

package interpreter

func nativeProcessName() string {
	return ""
}
