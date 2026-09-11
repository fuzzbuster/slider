//go:build darwin

package interpreter

func nativeProcessName() string {
	return executableProcessName()
}
