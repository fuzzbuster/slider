//go:build darwin

package interpreter

import "testing"

func TestNativeProcessNameDarwin(t *testing.T) {
	name := nativeProcessName()
	if name == "" {
		t.Fatal("nativeProcessName() returned an empty Darwin process name")
	}
	if got := SanitizeProcessName(name); got != name {
		t.Fatalf("nativeProcessName() = %q, sanitized value = %q", name, got)
	}
}
