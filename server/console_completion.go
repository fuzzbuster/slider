package server

import (
	"os"
	"strings"

	"slider/pkg/completion"
	"slider/pkg/interpreter"
)

func (c *Console) setConsoleAutoComplete(
	registry *CommandRegistry,
	serverInterpreter *interpreter.Interpreter,
) {
	c.Term.AutoCompleteCallback = func(line string, pos int, key rune) (string, int, bool) {
		if key != 9 {
			return line, pos, false
		}

		line = strings.TrimSpace(line)
		if len(line) == 0 {
			return line, pos, false
		}

		args := strings.Fields(line)
		if len(args) == 0 {
			return line, pos, false
		}

		if len(args) == 1 && !strings.HasSuffix(line, " ") {
			newLine, newPos := registry.Autocomplete(line)
			return newLine, newPos, true
		}

		currentArg := ""
		if !strings.HasSuffix(line, " ") {
			currentArg = args[len(args)-1]
		}

		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}

		var completer completion.PathCompleter = completion.NewLocalPathCompleter()
		matches, commonPrefix, err := completer.Complete(
			currentArg,
			cwd,
			serverInterpreter.System,
			serverInterpreter.HomeDir,
		)
		if err != nil || len(matches) == 0 {
			return line, pos, false
		}

		completedPath := buildCompletedPath(
			currentArg,
			commonPrefix,
			serverInterpreter.System,
			serverInterpreter.HomeDir,
		)
		newLine := buildCompletedLine(
			line,
			currentArg,
			completedPath,
			strings.HasSuffix(line, " "),
		)
		return newLine, len(newLine), true
	}
}

// buildCompletedPath reconstructs the full path from the original input and completion
// Expands ~ to the full home directory path
func buildCompletedPath(originalInput, completion, system, homeDir string) string {
	if completion == "" {
		return originalInput
	}
	if completion == originalInput {
		return originalInput
	}

	expandedInput := originalInput
	if len(originalInput) >= 1 && originalInput[0] == '~' {
		if homeDir != "" {
			if originalInput == "~" {
				expandedInput = homeDir
			} else if len(originalInput) >= 2 &&
				(originalInput[1] == '/' || originalInput[1] == '\\') {
				expandedInput = homeDir + originalInput[1:]
			}
		}
	}

	var lastSep int
	if system == "windows" {
		lastBackslash := strings.LastIndex(expandedInput, "\\")
		lastSlash := strings.LastIndex(expandedInput, "/")
		if lastBackslash > lastSlash {
			lastSep = lastBackslash
		} else {
			lastSep = lastSlash
		}
	} else {
		lastSep = strings.LastIndex(expandedInput, "/")
	}

	if lastSep == -1 {
		return completion
	}
	dirPart := expandedInput[:lastSep+1]
	return dirPart + completion
}

// buildCompletedLine constructs a new command line with the completed argument
func buildCompletedLine(line string, currentArg string, completion string, trailingSpace bool) string {
	if completion == "" || completion == currentArg {
		return line
	}

	needsQuoting := strings.Contains(completion, " ")
	if trailingSpace {
		if needsQuoting {
			return line + `"` + completion + `"`
		}
		return line + completion
	}

	lastArgStart := strings.LastIndex(line, currentArg)
	if lastArgStart == -1 {
		return line
	}

	prefix := line[:lastArgStart]
	if needsQuoting {
		return prefix + `"` + completion + `"`
	}
	return prefix + completion
}

func autocompleteCommand(input string, cmdList []string) (string, int) {
	var matches []string
	for _, cmd := range cmdList {
		if strings.HasPrefix(cmd, input) {
			matches = append(matches, cmd)
		}
	}

	if len(matches) == 1 {
		return matches[0], len(matches[0])
	}
	if len(matches) > 1 {
		commonPrefix := matches[0]
		for _, match := range matches[1:] {
			for !strings.HasPrefix(match, commonPrefix) {
				commonPrefix = commonPrefix[:len(commonPrefix)-1]
			}
		}
		return commonPrefix, len(commonPrefix)
	}

	return input, len(input)
}
