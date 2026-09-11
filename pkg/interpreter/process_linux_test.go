//go:build linux

package interpreter

import "testing"

func TestNativeProcessNameLinux(t *testing.T) {
	name := nativeProcessName()
	if name == "" {
		t.Fatal("nativeProcessName() returned an empty Linux process name")
	}
	if got := SanitizeProcessName(name); got != name {
		t.Fatalf("nativeProcessName() = %q, sanitized value = %q", name, got)
	}
}
