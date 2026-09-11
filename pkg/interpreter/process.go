package interpreter

import (
	"os"
	"strings"
	"unicode"
)

const maxProcessNameRunes = 128

func currentProcessInfo() ProcessInfo {
	info := ProcessInfo{
		Name: nativeProcessName(),
	}
	if pid := os.Getpid(); pid > 0 && uint64(pid) <= uint64(^uint32(0)) {
		info.PID = uint32(pid)
	}
	return SanitizeProcessInfo(info)
}

// SanitizeProcessInfo makes untrusted process metadata safe for terminal output.
func SanitizeProcessInfo(info ProcessInfo) ProcessInfo {
	info.Name = SanitizeProcessName(info.Name)
	return info
}

// SanitizeProcessName preserves printable Unicode while replacing terminal
// control and formatting characters.
func SanitizeProcessName(name string) string {
	name = strings.TrimSpace(strings.ToValidUTF8(name, "\uFFFD"))
	var result strings.Builder
	count := 0
	for _, value := range name {
		if count == maxProcessNameRunes {
			break
		}
		if unicode.IsGraphic(value) {
			result.WriteRune(value)
		} else {
			result.WriteRune('\uFFFD')
		}
		count++
	}
	return strings.TrimSpace(result.String())
}
