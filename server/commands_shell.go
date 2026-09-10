package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/pflag"
)

const (
	shellCmd   = "shell"
	shellDesc  = "Binds to a client Shell"
	shellUsage = "Usage: shell [flags]"
)

// ShellCommand implements the 'shell' command.
type ShellCommand struct{ BaseCommand }

type shellCommandOptions struct {
	sessionID   int
	port        int
	killID      int
	interactive bool
	tlsOn       bool
	expose      bool
	useAltShell bool
}

func (c *ShellCommand) Name() string        { return shellCmd }
func (c *ShellCommand) Description() string { return shellDesc }
func (c *ShellCommand) Usage() string       { return shellUsage }

func (c *ShellCommand) Run(ctx *ExecutionContext, args []string) error {
	svr := ctx.server
	ui := ctx.UI()

	flags := pflag.NewFlagSet(shellCmd, pflag.ContinueOnError)
	flags.SetOutput(ui.Writer())
	sessionID := flags.IntP("session", "s", 0, "Target Session ID for the shell")
	port := flags.IntP("port", "p", 0, "Use this port number as local Listener, otherwise randomly selected")
	killID := flags.IntP("kill", "k", 0, "Kill Shell Listener and Server on a Session ID")
	interactive := flags.BoolP("interactive", "i", false, "Interactive mode, enters shell directly. Always TLS")
	tlsOn := flags.BoolP("tls", "t", false, "Enable TLS for the Shell")
	expose := flags.BoolP("expose", "e", false, "Expose port to all interfaces")
	useAltShell := flags.BoolP("alt-shell", "a", false, "Use the alternate shell")
	flags.Usage = func() {
		_, _ = fmt.Fprintf(ui.Writer(), "%s\n", shellDesc)
		_, _ = fmt.Fprintf(ui.Writer(), "Usage: %s\n\n", shellUsage)
		flags.PrintDefaults()
		_, _ = fmt.Fprintf(ui.Writer(), "\n")
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("shell command does not accept positional arguments")
	}
	if !flags.Changed("session") && !flags.Changed("kill") {
		return fmt.Errorf("one of the flags --session or --kill must be set")
	}
	if flags.Changed("session") && flags.Changed("kill") {
		return fmt.Errorf("flags --session and --kill cannot be used together")
	}
	for _, name := range []string{"port", "interactive", "tls", "expose"} {
		if flags.Changed("kill") && flags.Changed(name) {
			return fmt.Errorf("flags --kill and --%s cannot be used together", name)
		}
	}
	if flags.Changed("interactive") && flags.Changed("expose") {
		return fmt.Errorf("flags --interactive and --expose cannot be used together")
	}
	if flags.Changed("interactive") && flags.Changed("tls") {
		return fmt.Errorf("flags --interactive and --tls cannot be used together")
	}

	return c.runShellCommand(svr, ui, shellCommandOptions{
		sessionID:   *sessionID,
		port:        *port,
		killID:      *killID,
		interactive: *interactive,
		tlsOn:       *tlsOn,
		expose:      *expose,
		useAltShell: *useAltShell,
	})
}

func (c *ShellCommand) runShellCommand(
	svr *server,
	ui UserInterface,
	options shellCommandOptions,
) error {
	sessionID := options.sessionID + options.killID
	unified, ok := svr.ResolveUnifiedSessions()[int64(sessionID)]
	if !ok {
		return fmt.Errorf("session %d not found", sessionID)
	}
	if strings.HasPrefix(unified.Role, "operator") {
		return fmt.Errorf("shell command not allowed against operator roles")
	}

	if unified.GatewayID != 0 {
		if options.killID > 0 {
			return c.handleRemoteShellKill(svr, unified, ui, options.killID)
		}
		if options.sessionID > 0 {
			return c.handleRemoteShell(
				svr,
				unified,
				ui,
				options.port,
				options.interactive,
				options.tlsOn,
				options.expose,
				options.useAltShell,
			)
		}
	}

	sessionID = int(unified.ActualID)
	sess, err := svr.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("unknown session ID %d", sessionID)
	}
	if options.killID > 0 {
		return c.handleLocalShellKill(sess, ui, options.killID)
	}
	if options.sessionID > 0 {
		return c.handleLocalShell(svr, sess, unified, ui, options)
	}
	return nil
}
