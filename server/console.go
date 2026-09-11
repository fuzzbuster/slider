package server

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"

	"slider/pkg/conf"
	"slider/pkg/escseq"
	"slider/pkg/session"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/term"
)

type Console struct {
	Term       *term.Terminal
	InitState  *term.State
	FirstRun   bool
	History    *session.CustomHistory
	ReadWriter io.ReadWriter
	ResizeChan chan types.TermDimensions
}

type screenIO struct {
	io.Reader
	io.Writer
}

type consoleAction uint8

const (
	consoleActionContinue consoleAction = iota
	consoleActionBackground
	consoleActionExit
)

func (sIO screenIO) Fd() uintptr {
	if f, ok := sIO.Reader.(*os.File); ok {
		return f.Fd()
	}
	if f, ok := sIO.Reader.(interface{ Fd() uintptr }); ok {
		return f.Fd()
	}
	return 0
}

func (s *server) consoleBanner(ui *Console) {
	ui.ScreenAlignment(true)
	ui.Printf("%s\n\n", escseq.GreyBoldText(conf.Banner))
	ui.PrintInfo("Type \"bg\" to leave the console without stopping the Server.")
	ui.PrintInfo("Type \"help\" to see available commands.")
	ui.PrintInfo("Type \"exit\" to exit the console.\n")
}

func (s *server) newTerminal(screen screenIO, registry *CommandRegistry) error {
	_, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}

	s.console.Term = term.NewTerminal(screen, getPrompt())
	s.console.ReadWriter = screen
	s.console.setConsoleAutoComplete(registry, s.serverInterpreter)
	s.console.Term.History = s.console.History

	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return err
	}
	if err := s.console.Term.SetSize(width, height); err != nil {
		return err
	}

	if s.console.FirstRun {
		s.consoleBanner(&s.console)
		s.console.FirstRun = false
	}

	return nil
}

func (s *server) NewConsole() string {
	var out string

	if err := s.serverInterpreter.EnableProcessedInputOutput(); err != nil {
		s.ErrorWith("Failed to enable Processed Input/Output", slog.F("error", err))
		escseq.SetColors(s.serverInterpreter.ColorOn)
	}
	defer func() {
		if err := s.serverInterpreter.ResetInputOutputModes(); err != nil {
			s.ErrorWith("Failed to reset Input/Output modes", slog.F("error", err))
		}
	}()

	var err error
	s.console.InitState, err = term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		s.Fatalf("Failed to read terminal size: %v", err)
	}
	s.console.ResizeChan = make(chan types.TermDimensions, 10)
	defer func() {
		_ = term.Restore(int(os.Stdin.Fd()), s.console.InitState)
	}()
	screen := screenIO{os.Stdin, os.Stdout}
	if err := s.newTerminal(screen, s.commandRegistry); err != nil {
		s.Fatalf("Failed to initialize terminal: %s", err)
	}

	for consoleInput := true; consoleInput; {
		input, err := s.console.Term.ReadLine()
		if err != nil {
			if err != io.EOF {
				s.console.TermPrintf("\rFailed to read input: %s\r\n", err)
			}
			if err := s.newTerminal(screen, s.commandRegistry); err != nil {
				s.Fatalf("Failed to recover terminal: %s", err)
			}
			_, _ = s.console.Term.Write([]byte{'\n'})
			continue
		}

		action, err := s.executeConsoleInput(&s.console, input)
		if err != nil {
			s.console.PrintError("Error: %v", err)
			continue
		}
		switch action {
		case consoleActionExit:
			out = exitCmd
			consoleInput = false
		case consoleActionBackground:
			s.console.PrintlnGreyOut("Logging...")
			out = bgCmd
			consoleInput = false
		default:
			s.console.Term.SetPrompt(getPrompt())
		}
	}

	return out
}

func (s *server) executeConsoleInput(ui *Console, input string) (consoleAction, error) {
	args := strings.Fields(input)
	if len(args) == 0 {
		return consoleActionContinue, nil
	}

	if localCommand, ok := strings.CutPrefix(args[0], "!"); ok && localCommand != "" {
		command := append([]string{localCommand}, args[1:]...)
		s.notConsoleCommand(ui, command)
		return consoleActionContinue, nil
	}

	ctx := &ExecutionContext{server: s, ui: ui}
	err := s.commandRegistry.Execute(ctx, strings.ToLower(args[0]), args[1:])
	switch {
	case errors.Is(err, ErrExitConsole):
		return consoleActionExit, nil
	case errors.Is(err, ErrBackgroundConsole):
		return consoleActionBackground, nil
	default:
		return consoleActionContinue, err
	}
}

func (s *server) notConsoleCommand(ui *Console, command []string) {
	s.notConsoleCommandWithDir(ui, command, "")
}

func (s *server) notConsoleCommandWithDir(ui *Console, command []string, workingDir string) {
	if s.serverInterpreter.Shell == "" {
		ui.PrintError("No Shell set")
		return
	}

	ui.PrintWarn("Executing local Command: %s", command)
	shellArgs := append([]string(nil), s.serverInterpreter.ShellExecArgs...)
	shellArgs = append(shellArgs, strings.Join(command, " "))

	ctx, cancel := context.WithTimeout(context.Background(), conf.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.serverInterpreter.Shell, shellArgs...)
	cmd.Dir = workingDir
	cmd.Stdout = ui.Term
	cmd.Stderr = ui.Term
	if err := cmd.Run(); err != nil {
		ui.PrintError("%v", err)
	}
	ui.Println("")
}
