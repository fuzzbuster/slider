package interpreter

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestSanitizeProcessName(t *testing.T) {
	t.Run("preserves printable Unicode", func(t *testing.T) {
		const name = "Slider 客户端.exe"
		if got := SanitizeProcessName(name); got != name {
			t.Fatalf("SanitizeProcessName() = %q, want %q", got, name)
		}
	})

	t.Run("replaces terminal control characters", func(t *testing.T) {
		got := SanitizeProcessName("slider\x1b[2J\n\t\x00\u007f\u0085\u202eclient")
		if !strings.ContainsRune(got, '\uFFFD') {
			t.Fatalf("SanitizeProcessName() = %q, want replacement characters", got)
		}
		for _, value := range got {
			if !unicode.IsGraphic(value) {
				t.Fatalf("SanitizeProcessName() retained unsafe rune %U in %q", value, got)
			}
		}
	})

	t.Run("repairs invalid UTF-8", func(t *testing.T) {
		got := SanitizeProcessName(string([]byte{'a', 0xff, 'b'}))
		if !utf8.ValidString(got) || got != "a\uFFFDb" {
			t.Fatalf("SanitizeProcessName() = %q, want %q", got, "a\uFFFDb")
		}
	})

	t.Run("truncates by rune", func(t *testing.T) {
		got := SanitizeProcessName(strings.Repeat("界", maxProcessNameRunes+1))
		if count := utf8.RuneCountInString(got); count != maxProcessNameRunes {
			t.Fatalf("SanitizeProcessName() rune count = %d, want %d", count, maxProcessNameRunes)
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		once := SanitizeProcessName("bad\x1b\n\u202ename")
		if twice := SanitizeProcessName(once); twice != once {
			t.Fatalf("second sanitization = %q, want %q", twice, once)
		}
	})
}

func TestCurrentProcessInfo(t *testing.T) {
	info := currentProcessInfo()
	if info.PID != uint32(os.Getpid()) {
		t.Fatalf("currentProcessInfo().PID = %d, want %d", info.PID, os.Getpid())
	}
	supported := runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if supported && info.Name == "" {
		t.Fatal("currentProcessInfo().Name is empty on a supported platform")
	}
	if !supported && info.Name != "" {
		t.Fatalf("currentProcessInfo().Name = %q on unsupported platform", info.Name)
	}
	if sanitized := SanitizeProcessInfo(info); sanitized != info {
		t.Fatalf("currentProcessInfo() is not sanitized: got %#v, want %#v", info, sanitized)
	}
}
