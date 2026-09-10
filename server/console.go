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
	ui.PrintInfo("Type \"bg\" to return to logging.")
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

	s.initRegistry()

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
		var commandName string
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
		args := append([]string(nil), strings.Fields(input)...)
		if len(args) > 0 {
			commandName = args[0]
		}
		if commandName == "" {
			continue
		}

		if localCommand, ok := strings.CutPrefix(commandName, "!"); ok && localCommand != "" {
			fullCommand := append([]string{localCommand}, args[1:]...)
			s.notConsoleCommand(fullCommand)
			continue
		}

		command := strings.ToLower(args[0])
		ctx := &ExecutionContext{
			server:  s,
			session: nil,
			ui:      &s.console,
		}
		err = s.commandRegistry.Execute(ctx, command, args[1:])
		if err != nil {
			if errors.Is(err, ErrExitConsole) {
				out = exitCmd
				consoleInput = false
			} else if errors.Is(err, ErrBackgroundConsole) {
				out = bgCmd
				consoleInput = false
			} else {
				s.console.PrintError("Error: %v", err)
			}
		} else {
			s.console.Term.SetPrompt(getPrompt())
		}
	}

	return out
}

func (s *server) notConsoleCommand(command []string) {
	if s.serverInterpreter.Shell == "" {
		s.console.PrintError("No Shell set")
		return
	}

	s.console.PrintWarn("Executing local Command: %s", command)
	command = append(s.serverInterpreter.ShellExecArgs, strings.Join(command, " "))

	ctx, cancel := context.WithTimeout(context.Background(), conf.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.serverInterpreter.Shell, command...)
	cmd.Stdout = s.console.Term
	cmd.Stderr = s.console.Term
	if err := cmd.Run(); err != nil {
		s.console.PrintError("%v", err)
	}
	s.console.Println("")
}
