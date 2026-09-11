//go:build windows

package interpreter

import "testing"

func TestNativeProcessNameWindows(t *testing.T) {
	name := nativeProcessName()
	if name == "" {
		t.Fatal("nativeProcessName() returned an empty Windows process name")
	}
	if got := SanitizeProcessName(name); got != name {
		t.Fatalf("nativeProcessName() = %q, sanitized value = %q", name, got)
	}
}
