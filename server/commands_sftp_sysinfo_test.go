package server

import (
	"bytes"
	"strings"
	"testing"

	"slider/pkg/interpreter"

	"golang.org/x/term"
)

func TestSftpSysInfoDisplaysSanitizedProcessDiagnostics(t *testing.T) {
	output := new(bytes.Buffer)
	console := &Console{Term: term.NewTerminal(output, "")}
	remoteCwd := "/tmp"
	ctx := &ExecutionContext{
		ui: console,
		sftpCtx: &SftpCommandContext{
			remoteCwd: &remoteCwd,
			remoteInfo: interpreter.BaseInfo{
				System: "linux",
				Arch:   "amd64",
				User:   "tester",
			},
			processInfo: interpreter.ProcessInfo{
				Name: "slider\x1b[2J\n\tclient",
				PID:  4321,
			},
		},
	}

	if err := (&SftpSysInfoCommand{}).Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{
		"Process Name",
		"slider\uFFFD[2J\uFFFD\uFFFDclient",
		"PID",
		"4321",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sysinfo output does not contain %q:\n%s", want, got)
		}
	}
	if strings.ContainsRune(got, '\x1b') || strings.Contains(got, "\n\tclient") {
		t.Fatalf("sysinfo output retained terminal injection characters:\n%q", got)
	}
}

func TestSftpSysInfoDisplaysUnavailableProcessDiagnostics(t *testing.T) {
	output := new(bytes.Buffer)
	console := &Console{Term: term.NewTerminal(output, "")}
	remoteCwd := "/"
	ctx := &ExecutionContext{
		ui: console,
		sftpCtx: &SftpCommandContext{
			remoteCwd: &remoteCwd,
		},
	}

	if err := (&SftpSysInfoCommand{}).Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.Contains(got, "Process Name") || !strings.Contains(got, "PID") {
		t.Fatalf("sysinfo output is missing process fields:\n%s", got)
	}
	if count := strings.Count(got, "--"); count < 2 {
		t.Fatalf("sysinfo output has %d unavailable markers, want at least 2:\n%s", count, got)
	}
}
