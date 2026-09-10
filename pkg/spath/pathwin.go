package spath

import (
	"strings"
)

// Windows reserved file names
var windowsReservedNames = []string{
	"CON", "PRN", "AUX", "NUL",
	"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
	"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
}

// winIsPathSeparator checks if the byte is a Windows path separator
func winIsPathSeparator(b byte) bool {
	return b == '\\' || b == '/'
}

// winFromSlash replaces forward slashes with Windows path separators
func winFromSlash(path string) string {
	return strings.ReplaceAll(path, string(UnixSeparator), string(WindowsSeparator))
}

// winIsReservedName checks if the path is a Windows reserved name
func winIsReservedName(path string) bool {
	if path == "" {
		return false
	}

	// Check if the path (ignoring case) matches any reserved name
	upperPath := strings.ToUpper(path)
	for _, reserved := range windowsReservedNames {
		if upperPath == reserved {
			return true
		}
	}

	return false
}

// winVolumeNameLength returns the length of the volume name in a Windows path
func winVolumeNameLength(path string) int {
	// Handle paths that are too short for a volume name
	if len(path) < 2 {
		return 0
	}

	// Check for drive letter (e.g., "C:")
	if path[1] == ':' {
		c := path[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return 2
		}
	}

	// Check for UNC path (e.g., "\\server\share")
	if len(path) >= 2 && winIsPathSeparator(path[0]) && winIsPathSeparator(path[1]) {
		// Skip leading backslashes
		i := 2
		if i >= len(path) {
			return 0
		}

		// Skip server name
		for i < len(path) && !winIsPathSeparator(path[i]) {
			i++
		}

		if i >= len(path) {
			return 0
		}

		// Skip separator after server name
		i++
		if i >= len(path) {
			return 0
		}

		// Skip share name
		start := i
		for i < len(path) && !winIsPathSeparator(path[i]) {
			i++
		}

		if start < i {
			return i
		}
	}

	return 0
}

// winVolumeName returns the volume name from a Windows path
func winVolumeName(path string) string {
	volLen := winVolumeNameLength(path)
	if volLen == 0 {
		return ""
	}
	return winFromSlash(path[:volLen])
}

// winIsAbs reports whether a Windows path is absolute
func winIsAbs(path string) bool {
	if winIsReservedName(path) {
		return true
	}

	volLen := winVolumeNameLength(path)
	if volLen == 0 {
		return false
	}

	// Check for UNC path (which is always absolute)
	if len(path) >= 2 && winIsPathSeparator(path[0]) && winIsPathSeparator(path[1]) {
		return true
	}

	// Path has a volume name, check if it starts with a separator after the volume
	path = path[volLen:]
	return len(path) > 0 && winIsPathSeparator(path[0])
}

// winJoin joins Windows path elements
func winJoin(elem []string) string {
	firstIdx := 0
	for i, e := range elem {
		if e != "" {
			firstIdx = i
			break
		}
	}

	if firstIdx >= len(elem) {
		return ""
	}

	first := elem[firstIdx]
	isDriveLetter := len(first) == 2 && first[1] == ':'

	if isDriveLetter {
		var parts []string
		parts = append(parts, first)

		for _, e := range elem[firstIdx+1:] {
			if e != "" {
				parts = append(parts, e)
			}
		}

		return winClean(strings.Join(parts, string(WindowsSeparator)))
	}

	isUNC := len(first) > 0 && winIsPathSeparator(first[0]) &&
		(len(first) > 1 && winIsPathSeparator(first[1]))

	var parts []string
	for _, e := range elem[firstIdx:] {
		if e != "" {
			parts = append(parts, e)
		}
	}

	joined := winClean(strings.Join(parts, string(WindowsSeparator)))

	// Preserve UNC path status
	if isUNC && !strings.HasPrefix(joined, string(WindowsSeparator)+string(WindowsSeparator)) {
		if strings.HasPrefix(joined, string(WindowsSeparator)) {
			return string(WindowsSeparator) + joined
		}
		return string(WindowsSeparator) + string(WindowsSeparator) + joined

	}

	return joined
}

// winDir returns the directory portion of a Windows path
func winDir(path string) string {
	// Get volume name
	vol := winVolumeName(path)

	// Find the last separator
	i := len(path) - 1
	for i >= len(vol) && !winIsPathSeparator(path[i]) {
		i--
	}

	// Get the directory part (everything up to and including the last separator)
	dir := winClean(path[len(vol) : i+1])

	// Handle UNC root paths
	if dir == "." && len(vol) > 2 {
		// must be UNC
		return vol
	}

	return vol + dir
}

// winBase returns the last element of a Windows path
func winBase(path string) string {
	// Handle empty path
	if path == "" {
		return "."
	}

	// Strip trailing separators
	end := len(path)
	for end > 0 && winIsPathSeparator(path[end-1]) {
		end--
	}

	if end == 0 {
		return string(WindowsSeparator)
	}

	// Skip volume name
	volLen := winVolumeNameLength(path)
	path = path[volLen:end]

	// Find the last separator
	i := len(path) - 1
	for i >= 0 && !winIsPathSeparator(path[i]) {
		i--
	}

	// Extract the base name (everything after the last separator)
	if i >= 0 {
		path = path[i+1:]
	}

	// If empty (had only separators), return a single separator
	if path == "" {
		return string(WindowsSeparator)
	}

	return path
}

// winClean cleans a Windows path
func winClean(path string) string {
	if path == "" {
		return "."
	}

	volLen := winVolumeNameLength(path)
	vol := ""
	if volLen > 0 {
		vol = path[:volLen]
		path = path[volLen:]
	}

	rooted := len(path) > 0 && winIsPathSeparator(path[0])

	var components []string

	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || winIsPathSeparator(path[i]) {
			component := path[start:i]

			switch component {
			case "", ".":
			case "..":
				if len(components) > 0 && components[len(components)-1] != ".." {
					components = components[:len(components)-1]
				} else if !rooted {
					components = append(components, "..")
				}
			default:
				components = append(components, component)
			}

			start = i + 1
		}
	}

	if !rooted && len(components) == 0 {
		return "."
	}

	var result strings.Builder

	result.WriteString(winFromSlash(vol))

	if rooted {
		result.WriteByte(WindowsSeparator)
	}

	for i, component := range components {
		if i > 0 {
			result.WriteByte(WindowsSeparator)
		}
		result.WriteString(component)
	}

	return result.String()
}
