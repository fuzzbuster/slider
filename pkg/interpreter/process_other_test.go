//go:build !linux && !darwin && !windows

package interpreter

import "testing"

func TestNativeProcessNameUnsupported(t *testing.T) {
	if name := nativeProcessName(); name != "" {
		t.Fatalf("nativeProcessName() = %q, want unavailable", name)
	}
}
