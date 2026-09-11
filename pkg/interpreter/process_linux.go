//go:build linux

package interpreter

func nativeProcessName() string {
	return executableProcessName()
}
