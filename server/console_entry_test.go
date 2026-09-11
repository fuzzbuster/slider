package server

import (
	"bytes"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"slider/pkg/conf"
	"slider/pkg/interpreter"

	"golang.org/x/term"
)

func TestConfiguredServerInitializesConsoleEntries(t *testing.T) {
	cfg := &Config{
		Verbose:    "off",
		Address:    "127.0.0.1",
		Port:       8080,
		Keepalive:  conf.Keepalive,
		StatusCode: http.StatusOK,
		Headless:   true,
	}

	s := newConfiguredServer(cfg)
	if !s.httpConsoleOn {
		t.Fatal("headless server did not enable HTTP console")
	}
	if s.commandRegistry == nil {
		t.Fatal("command registry was not initialized")
	}
	for _, name := range []string{bgCmd, clearCmd, exitCmd, helpCmd, sessionsCmd} {
		if _, ok := s.commandRegistry.Get(name); !ok {
			t.Fatalf("command %q was not registered", name)
		}
	}
}

func TestExecuteConsoleInputReturnsControlActions(t *testing.T) {
	s := &server{commandRegistry: newServerCommandRegistry(false)}
	ui, _ := newBufferedConsole()

	tests := []struct {
		input string
		want  consoleAction
	}{
		{input: "", want: consoleActionContinue},
		{input: "bg", want: consoleActionBackground},
		{input: "exit", want: consoleActionExit},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := s.executeConsoleInput(ui, test.input)
			if err != nil {
				t.Fatalf("executeConsoleInput() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("executeConsoleInput() action = %d, want %d", got, test.want)
			}
		})
	}
}

func TestExecuteConsoleInputRoutesLocalCommandToCurrentConsole(t *testing.T) {
	shell, shellArgs := testShell()
	s := &server{
		commandRegistry: newServerCommandRegistry(false),
		serverInterpreter: &interpreter.Interpreter{
			Shell:         shell,
			ShellExecArgs: shellArgs,
		},
	}
	localConsole, localOutput := newBufferedConsole()
	currentConsole, currentOutput := newBufferedConsole()
	s.console = *localConsole

	action, err := s.executeConsoleInput(currentConsole, "!echo routed-output")
	if err != nil {
		t.Fatalf("executeConsoleInput() error = %v", err)
	}
	if action != consoleActionContinue {
		t.Fatalf("executeConsoleInput() action = %d, want %d", action, consoleActionContinue)
	}
	if !strings.Contains(currentOutput.String(), "routed-output") {
		t.Fatalf("current console output = %q, want routed command output", currentOutput.String())
	}
	if localOutput.Len() != 0 {
		t.Fatalf("local console received remote output: %q", localOutput.String())
	}
}

func TestLocalCommandWithDirUsesProvidedConsole(t *testing.T) {
	shell, shellArgs := testShell()
	s := &server{
		serverInterpreter: &interpreter.Interpreter{
			Shell:         shell,
			ShellExecArgs: shellArgs,
		},
	}
	localConsole, localOutput := newBufferedConsole()
	currentConsole, currentOutput := newBufferedConsole()
	s.console = *localConsole

	s.notConsoleCommandWithDir(
		currentConsole,
		[]string{"echo", "sftp-output"},
		t.TempDir(),
	)

	if !strings.Contains(currentOutput.String(), "sftp-output") {
		t.Fatalf("current console output = %q, want SFTP command output", currentOutput.String())
	}
	if localOutput.Len() != 0 {
		t.Fatalf("local console received SFTP output: %q", localOutput.String())
	}
}

func newBufferedConsole() (*Console, *bytes.Buffer) {
	output := new(bytes.Buffer)
	return &Console{
		Term:       term.NewTerminal(output, ""),
		ReadWriter: output,
	}, output
}

func testShell() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/C"}
	}
	return "/bin/sh", []string{"-c"}
}
