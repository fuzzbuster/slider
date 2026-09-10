package server

import (
	"encoding/json"
	"errors"
	"fmt"

	"slider/pkg/conf"
	"slider/pkg/types"

	"github.com/spf13/pflag"
)

const (
	sessionsCmd   = "sessions"
	sessionsDesc  = "Interacts with Client Sessions"
	sessionsUsage = "Usage: sessions [flags]"
)

// SessionsCommand implements the 'sessions' command.
type SessionsCommand struct{ BaseCommand }

func (c *SessionsCommand) Name() string        { return sessionsCmd }
func (c *SessionsCommand) Description() string { return sessionsDesc }
func (c *SessionsCommand) Usage() string       { return sessionsUsage }

func (c *SessionsCommand) Run(ctx *ExecutionContext, args []string) error {
	svr := ctx.server
	ui := ctx.UI()

	flags := pflag.NewFlagSet(sessionsCmd, pflag.ContinueOnError)
	flags.SetOutput(ui.Writer())
	interactiveID := flags.IntP("interactive", "i", 0, "Start Interactive Slider Shell on a Session ID")
	disconnectID := flags.IntP("disconnect", "d", 0, "Disconnect Session ID")
	killID := flags.IntP("kill", "k", 0, "Kill Session ID")
	flags.Usage = func() {
		_, _ = fmt.Fprintf(ui.Writer(), "Usage: %s\n\n", sessionsUsage)
		_, _ = fmt.Fprintf(ui.Writer(), "%s\n\n", sessionsDesc)
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("too many arguments")
	}

	changedCount := 0
	for _, name := range []string{"interactive", "disconnect", "kill"} {
		if flags.Changed(name) {
			changedCount++
		}
	}
	if changedCount > 1 {
		return fmt.Errorf("flags --interactive, --disconnect and --kill cannot be used together")
	}

	if len(args) == 0 {
		svr.listSessions(ui)
		return nil
	}
	if *disconnectID != 0 {
		return svr.disconnectSession(ui, *disconnectID)
	}
	if *killID != 0 {
		return svr.killSession(ui, *killID)
	}
	if *interactiveID != 0 {
		return svr.interactWithSession(ui, *interactiveID)
	}
	return nil
}

func (s *server) disconnectSession(ui UserInterface, sessionID int) error {
	unified, ok := s.ResolveUnifiedSessions()[int64(sessionID)]
	if !ok {
		return fmt.Errorf("unknown session ID %d", sessionID)
	}
	if unified.GatewayID != 0 {
		return fmt.Errorf("remote sessions cannot be disconnected; use --kill to terminate session %d", sessionID)
	}
	sess, err := s.GetSession(int(unified.ActualID))
	if err != nil {
		return fmt.Errorf("unknown session ID %d", sessionID)
	}
	if err := sess.Close(); err != nil {
		return fmt.Errorf("failed to close connection to session ID %d: %w", sess.GetID(), err)
	}
	ui.PrintSuccess("Closed connection to Session ID %d", sess.GetID())
	return nil
}

func (s *server) killSession(ui UserInterface, sessionID int) error {
	unified, ok := s.ResolveUnifiedSessions()[int64(sessionID)]
	if !ok {
		return fmt.Errorf("unknown session ID %d", sessionID)
	}
	if unified.GatewayID == 0 {
		sess, err := s.GetSession(int(unified.ActualID))
		if err != nil {
			return fmt.Errorf("unknown session ID %d", sessionID)
		}
		if _, _, err := sess.SendRequest(conf.SSHRequestShutdown, true, nil); err != nil {
			return fmt.Errorf("client did not answer properly to the request: %w", err)
		}
	} else if err := s.killRemoteSession(unified); err != nil {
		return fmt.Errorf("client did not answer properly to the request: %w", err)
	}
	ui.PrintSuccess("SessionID %d terminated gracefully", sessionID)
	return nil
}

func (s *server) killRemoteSession(unified UnifiedSession) error {
	gatewaySession, err := s.GetSession(int(unified.GatewayID))
	if err != nil {
		return fmt.Errorf("gateway session %d not found", unified.GatewayID)
	}
	target := append([]int64(nil), unified.Path...)
	target = append(target, unified.ActualID)
	payload, err := json.Marshal(types.ForwardRequestPayload{
		Target:  target,
		ReqType: conf.SSHRequestShutdown,
	})
	if err != nil {
		return err
	}
	ok, _, err := gatewaySession.SendRequest(
		conf.SSHRequestSliderTCPIPForward,
		true,
		payload,
	)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("remote session rejected shutdown request")
	}
	return nil
}
