package server

import (
	"errors"
	"fmt"

	"github.com/spf13/pflag"
)

const (
	// Console Socks Command
	socksCmd   = "socks"
	socksDesc  = "Manages SOCKS5 servers (local and session-based)"
	socksUsage = "Usage: socks [flags]"
)

// SocksCommand implements the 'socks' command
type SocksCommand struct{ BaseCommand }

func (c *SocksCommand) Name() string        { return socksCmd }
func (c *SocksCommand) Description() string { return socksDesc }
func (c *SocksCommand) Usage() string       { return socksUsage }

func (c *SocksCommand) Run(ctx *ExecutionContext, args []string) error {
	svr := ctx.server
	ui := ctx.UI()

	socksFlags := pflag.NewFlagSet(socksCmd, pflag.ContinueOnError)
	socksFlags.SetOutput(ui.Writer())

	sSession := socksFlags.IntP("session", "s", 0, "Target session-based SOCKS5 server")
	sPort := socksFlags.IntP("port", "p", 0, "Port number (for server creation)")
	sKill := socksFlags.BoolP("kill", "k", false, "Kill SOCKS5 server (requires -l or -s)")
	sExpose := socksFlags.BoolP("expose", "e", false, "Expose port to all interfaces")
	sLocal := socksFlags.BoolP("local", "l", false, "Target local SOCKS5 server")

	socksFlags.Usage = func() {
		_, _ = fmt.Fprintf(ui.Writer(), "%s\n", socksDesc)
		_, _ = fmt.Fprintf(ui.Writer(), "Usage: %s\n\n", socksUsage)
		socksFlags.PrintDefaults()
		_, _ = fmt.Fprintf(ui.Writer(), "\n")
	}

	if pErr := socksFlags.Parse(args); pErr != nil {
		if errors.Is(pErr, pflag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("flag error: %w", pErr)
	}

	if len(socksFlags.Args()) > 0 {
		return fmt.Errorf("socks command does not accept positional arguments")
	}

	if !socksFlags.Changed("session") && !socksFlags.Changed("kill") && !socksFlags.Changed("local") {
		return listSocksSessions(svr, ui)
	}

	if socksFlags.Changed("kill") {
		if !socksFlags.Changed("local") && !socksFlags.Changed("session") {
			return fmt.Errorf("--kill requires either --local or --session to specify target")
		}
		if socksFlags.Changed("local") && socksFlags.Changed("session") {
			return fmt.Errorf("--kill cannot target both --local and --session")
		}
		if socksFlags.Changed("expose") {
			return fmt.Errorf("--kill and --expose cannot be used together")
		}
	}

	if socksFlags.Changed("session") && socksFlags.Changed("local") {
		return fmt.Errorf("flags --session and --local cannot be used together")
	}

	if *sKill {
		if *sLocal {
			return killLocalSocksServer(svr, ui)
		}
		if *sSession > 0 {
			return killSessionSocksServer(svr, ui, *sSession)
		}
	}

	if *sLocal {
		return createLocalSocksServer(svr, ui, *sPort, *sExpose)
	}

	if *sSession > 0 {
		return createSessionSocksServer(svr, ui, *sSession, *sPort, *sExpose)
	}

	return fmt.Errorf("one of the flags --session, --local, or --kill must be set")
}
